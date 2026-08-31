#!/usr/bin/env python3
"""Fetch normalized provider quota snapshots without invoking an agent CLI."""

from __future__ import annotations

import base64
import contextlib
import datetime as dt
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from typing import Any, Dict, Iterator, List, Mapping, Optional


CLAUDE_USAGE_URL = "https://api.anthropic.com/api/oauth/usage"
CLAUDE_REFRESH_URL = "https://platform.claude.com/v1/oauth/token"
CLAUDE_CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
ANTIGRAVITY_USAGE_URL = (
    "https://daily-cloudcode-pa.googleapis.com/"
    "v1internal:fetchAvailableModels"
)
CODEX_USAGE_URL = "https://chatgpt.com/backend-api/wham/usage"

MAX_RESPONSE_BYTES = 2 * 1024 * 1024
DEFAULT_MIN_INTERVAL_SECONDS = 30
DEFAULT_MAX_REQUESTS_PER_HOUR = 60


class ProbeFailure(Exception):
    def __init__(
        self,
        status: str,
        message: str,
        retry_after_seconds: Optional[int] = None,
    ) -> None:
        super().__init__(message)
        self.status = status
        self.message = message
        self.retry_after_seconds = retry_after_seconds


def utc_now() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def iso_utc(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def clamp_percent(value: float) -> float:
    return max(0.0, min(100.0, float(value)))


def number(value: Any) -> Optional[float]:
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None


def parse_reset(value: Any) -> Optional[str]:
    numeric = number(value)
    if numeric is not None:
        if numeric > 10_000_000_000:
            numeric /= 1000
        try:
            return iso_utc(dt.datetime.fromtimestamp(numeric, tz=dt.timezone.utc))
        except (OverflowError, OSError, ValueError):
            return None
    if not isinstance(value, str) or not value.strip():
        return None
    raw = value.strip()
    try:
        parsed = dt.datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return iso_utc(parsed)


def window(
    window_id: str,
    group: str,
    label: str,
    *,
    used_percent: Optional[float] = None,
    remaining_percent: Optional[float] = None,
    duration_minutes: Optional[int] = None,
    resets_at: Any = None,
) -> Dict[str, Any]:
    result: Dict[str, Any] = {
        "id": window_id,
        "group": group,
        "label": label,
        "unit": "percent",
    }
    if used_percent is not None:
        result["used_percent"] = clamp_percent(used_percent)
    if remaining_percent is not None:
        result["remaining_percent"] = clamp_percent(remaining_percent)
    if duration_minutes is not None:
        result["window_duration_mins"] = int(duration_minutes)
    reset = parse_reset(resets_at)
    if reset is not None:
        result["resets_at"] = reset
    return result


def snapshot(
    provider: str,
    windows: List[Dict[str, Any]],
    observed_at: dt.datetime,
    *,
    account_scope: str = "",
    status: str = "available",
    source: str = "official",
    message: str = "",
    retry_after_seconds: Optional[int] = None,
) -> Dict[str, Any]:
    result: Dict[str, Any] = {
        "provider": provider,
        "status": status,
        "source": source,
        "observed_at": iso_utc(observed_at),
    }
    if account_scope:
        result["account_scope"] = account_scope
    if windows:
        result["windows"] = windows
    if message:
        result["message"] = message
    if retry_after_seconds is not None:
        result["retry_after_seconds"] = max(1, int(retry_after_seconds))
    return result


def normalize_claude(payload: Mapping[str, Any], observed_at: dt.datetime) -> Dict[str, Any]:
    windows: List[Dict[str, Any]] = []
    definitions = (
        ("five_hour", "five-hour", "5 hour limit", 300),
        ("seven_day", "seven-day", "Weekly limit", 10080),
    )
    for key, window_id, label, duration in definitions:
        value = payload.get(key)
        if not isinstance(value, Mapping):
            continue
        used = number(value.get("utilization"))
        reset = value.get("resets_at")
        if used is None and reset is None:
            continue
        used = clamp_percent(used) if used is not None else None
        remaining = clamp_percent(100 - used) if used is not None else None
        windows.append(
            window(
                window_id,
                "Claude Code",
                label,
                used_percent=used,
                remaining_percent=remaining,
                duration_minutes=duration,
                resets_at=reset,
            )
        )
    status = "available" if windows else "partial"
    message = "" if windows else "Anthropic returned no subscriber quota windows."
    return snapshot("claude", windows, observed_at, status=status, message=message)


def normalize_antigravity(
    payload: Mapping[str, Any], observed_at: dt.datetime
) -> Dict[str, Any]:
    models = payload.get("models")
    if not isinstance(models, Mapping):
        raise ProbeFailure("error", "Google returned no model quota catalog.")
    windows: List[Dict[str, Any]] = []
    seen = set()
    for model_id in sorted(models):
        value = models.get(model_id)
        if not isinstance(value, Mapping):
            continue
        quota = value.get("quotaInfo")
        if not isinstance(quota, Mapping):
            continue
        display_name = value.get("displayName")
        if not isinstance(display_name, str) or not display_name.strip():
            display_name = str(model_id)
        remaining_fraction = number(quota.get("remainingFraction"))
        remaining = (
            clamp_percent(remaining_fraction * 100)
            if remaining_fraction is not None
            else None
        )
        used = clamp_percent(100 - remaining) if remaining is not None else None
        reset = parse_reset(quota.get("resetTime"))
        dedupe_key = (display_name, remaining, reset)
        if dedupe_key in seen:
            continue
        seen.add(dedupe_key)
        windows.append(
            window(
                str(model_id),
                display_name.strip(),
                "Model quota",
                used_percent=used,
                remaining_percent=remaining,
                resets_at=reset,
            )
        )
    status = "available" if windows else "partial"
    message = "" if windows else "Google returned no model quota buckets."
    return snapshot("antigravity", windows, observed_at, status=status, message=message)


def append_codex_windows(
    target: List[Dict[str, Any]],
    rate_limit: Any,
    *,
    limit_id: str,
    group: str,
) -> None:
    if not isinstance(rate_limit, Mapping):
        return
    for suffix, label, value in (
        ("primary", "Primary limit", rate_limit.get("primary_window")),
        ("secondary", "Secondary limit", rate_limit.get("secondary_window")),
    ):
        if not isinstance(value, Mapping):
            continue
        used = number(value.get("used_percent"))
        duration_seconds = number(value.get("limit_window_seconds"))
        duration_minutes = (
            round(duration_seconds / 60) if duration_seconds is not None else None
        )
        if duration_minutes == 300:
            label = "5 hour limit"
        elif duration_minutes == 10080:
            label = "Weekly limit"
        used = clamp_percent(used) if used is not None else None
        remaining = clamp_percent(100 - used) if used is not None else None
        target.append(
            window(
                f"{limit_id}-{suffix}",
                group,
                label,
                used_percent=used,
                remaining_percent=remaining,
                duration_minutes=duration_minutes,
                resets_at=value.get("reset_at"),
            )
        )


def normalize_codex(payload: Mapping[str, Any], observed_at: dt.datetime) -> Dict[str, Any]:
    windows: List[Dict[str, Any]] = []
    append_codex_windows(
        windows,
        payload.get("rate_limit"),
        limit_id="codex",
        group="Codex",
    )
    additional = payload.get("additional_rate_limits")
    if isinstance(additional, list):
        for index, value in enumerate(additional):
            if not isinstance(value, Mapping):
                continue
            name = value.get("limit_name")
            group = name.strip() if isinstance(name, str) and name.strip() else "Codex model"
            limit_id = "model-" + str(index)
            append_codex_windows(
                windows,
                value.get("rate_limit"),
                limit_id=limit_id,
                group=group,
            )
    append_codex_windows(
        windows,
        payload.get("code_review_rate_limit"),
        limit_id="code-review",
        group="Code Review",
    )
    plan = payload.get("plan_type")
    account_scope = plan.strip() if isinstance(plan, str) else ""
    status = "available" if windows else "partial"
    message = "" if windows else "OpenAI returned no account quota windows."
    return snapshot(
        "codex",
        windows,
        observed_at,
        account_scope=account_scope,
        status=status,
        message=message,
    )


def positive_int_env(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default


@contextlib.contextmanager
def locked_file(path: pathlib.Path) -> Iterator[None]:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(path.parent, 0o700)
    handle = path.open("a+b")
    os.chmod(path, 0o600)
    try:
        if os.name == "nt":
            import msvcrt

            if path.stat().st_size == 0:
                handle.write(b"0")
                handle.flush()
            handle.seek(0)
            msvcrt.locking(handle.fileno(), msvcrt.LK_LOCK, 1)
        else:
            import fcntl

            fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        yield
    finally:
        if os.name == "nt":
            import msvcrt

            handle.seek(0)
            msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
        else:
            import fcntl

            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        handle.close()


class LocalRateLimiter:
    def __init__(self) -> None:
        state_root = os.environ.get("MULTICA_PROVIDER_USAGE_STATE_DIR", "").strip()
        if state_root:
            self.root = pathlib.Path(state_root).expanduser()
        else:
            self.root = pathlib.Path.home() / ".multica" / "provider-usage"
        self.state_path = self.root / "request-history.json"
        self.lock_path = self.root / "request-history.lock"
        self.min_interval = positive_int_env(
            "MULTICA_PROVIDER_USAGE_MIN_INTERVAL_SECONDS",
            DEFAULT_MIN_INTERVAL_SECONDS,
        )
        self.max_per_hour = positive_int_env(
            "MULTICA_PROVIDER_USAGE_MAX_REQUESTS_PER_HOUR",
            DEFAULT_MAX_REQUESTS_PER_HOUR,
        )

    def admit(
        self,
        provider: str,
        now: Optional[float] = None,
        request_count: int = 1,
    ) -> None:
        current = time.time() if now is None else float(now)
        request_count = max(1, int(request_count))
        with locked_file(self.lock_path):
            state: Dict[str, Any] = {"version": 1, "requests": {}}
            try:
                loaded = json.loads(self.state_path.read_text(encoding="utf-8"))
                if isinstance(loaded, dict) and isinstance(loaded.get("requests"), dict):
                    state = loaded
            except (FileNotFoundError, json.JSONDecodeError, OSError):
                pass
            requests = state.setdefault("requests", {})
            raw_history = requests.get(provider, [])
            if not isinstance(raw_history, list):
                raw_history = []
            history = [
                float(value)
                for value in raw_history
                if number(value) is not None and current - float(value) < 3600
            ]
            if history:
                retry = int(max(1, self.min_interval - (current - history[-1]) + 0.999))
                if current - history[-1] < self.min_interval:
                    raise ProbeFailure(
                        "rate_limited",
                        "The local provider usage refresh is cooling down.",
                        retry,
                    )
            if len(history) + request_count > self.max_per_hour:
                retry = (
                    int(max(1, 3600 - (current - history[0]) + 0.999))
                    if history
                    else 3600
                )
                raise ProbeFailure(
                    "rate_limited",
                    "The local hourly provider usage request limit was reached.",
                    retry,
                )
            history.extend([current] * request_count)
            requests[provider] = history
            self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
            os.chmod(self.root, 0o700)
            with tempfile.NamedTemporaryFile(
                mode="w",
                encoding="utf-8",
                dir=self.root,
                prefix="request-history.",
                delete=False,
            ) as temporary:
                json.dump(state, temporary, separators=(",", ":"), sort_keys=True)
                temporary.flush()
                os.fsync(temporary.fileno())
                temporary_path = pathlib.Path(temporary.name)
            os.chmod(temporary_path, 0o600)
            os.replace(temporary_path, self.state_path)
            os.chmod(self.state_path, 0o600)


def read_json(path: pathlib.Path, missing_message: str) -> Mapping[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ProbeFailure("auth_required", missing_message) from exc
    except (json.JSONDecodeError, OSError) as exc:
        raise ProbeFailure("auth_required", "The local provider credential is unreadable.") from exc
    if not isinstance(value, Mapping):
        raise ProbeFailure("auth_required", "The local provider credential is invalid.")
    return value


def http_json(
    url: str,
    *,
    method: str,
    headers: Mapping[str, str],
    body: Optional[Mapping[str, Any]] = None,
) -> Mapping[str, Any]:
    encoded = None
    if body is not None:
        encoded = json.dumps(body, separators=(",", ":")).encode("utf-8")
    request = urllib.request.Request(url, data=encoded, method=method)
    for name, value in headers.items():
        request.add_header(name, value)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            raw = response.read(MAX_RESPONSE_BYTES + 1)
    except urllib.error.HTTPError as exc:
        if exc.code in (401, 403):
            raise ProbeFailure(
                "auth_required", "The provider rejected the local OAuth credential."
            ) from exc
        if exc.code == 429:
            retry_raw = exc.headers.get("Retry-After", "")
            try:
                retry = max(1, int(retry_raw))
            except ValueError:
                retry = 60
            raise ProbeFailure(
                "rate_limited", "The provider rate-limited the usage request.", retry
            ) from exc
        raise ProbeFailure("error", "The provider usage endpoint returned an error.") from exc
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise ProbeFailure("error", "The provider usage endpoint is unavailable.") from exc
    if len(raw) > MAX_RESPONSE_BYTES:
        raise ProbeFailure("error", "The provider usage response was too large.")
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ProbeFailure("error", "The provider usage response was invalid JSON.") from exc
    if not isinstance(value, Mapping):
        raise ProbeFailure("error", "The provider usage response had an invalid shape.")
    if isinstance(value.get("error"), Mapping):
        raise ProbeFailure("error", "The provider usage endpoint returned an error.")
    return value


def claude_credential_path() -> pathlib.Path:
    config_dir = os.environ.get("CLAUDE_CONFIG_DIR", "").strip()
    if config_dir:
        return pathlib.Path(config_dir).expanduser() / ".credentials.json"
    return pathlib.Path.home() / ".claude" / ".credentials.json"


def fetch_claude(limiter: LocalRateLimiter, observed_at: dt.datetime) -> Dict[str, Any]:
    credential = read_json(
        claude_credential_path(), "Claude Code is not signed in on this machine."
    )
    oauth = credential.get("claudeAiOauth")
    if not isinstance(oauth, Mapping):
        raise ProbeFailure("auth_required", "Claude Code OAuth credentials are missing.")
    access_token = oauth.get("accessToken")
    refresh_token = oauth.get("refreshToken")
    expires_at = number(oauth.get("expiresAt")) or 0
    if not isinstance(access_token, str) or not access_token:
        raise ProbeFailure("auth_required", "Claude Code OAuth credentials are missing.")
    now_ms = observed_at.timestamp() * 1000
    limiter.admit(
        "claude",
        request_count=2 if expires_at <= now_ms else 1,
    )
    if expires_at <= now_ms:
        if not isinstance(refresh_token, str) or not refresh_token:
            raise ProbeFailure("auth_required", "Claude Code OAuth credentials have expired.")
        refreshed = http_json(
            CLAUDE_REFRESH_URL,
            method="POST",
            headers={"Content-Type": "application/json"},
            body={
                "grant_type": "refresh_token",
                "refresh_token": refresh_token,
                "client_id": CLAUDE_CLIENT_ID,
            },
        )
        refreshed_token = refreshed.get("access_token")
        if not isinstance(refreshed_token, str) or not refreshed_token:
            raise ProbeFailure("auth_required", "Claude Code OAuth refresh failed.")
        access_token = refreshed_token
    payload = http_json(
        CLAUDE_USAGE_URL,
        method="GET",
        headers={
            "Authorization": "Bearer " + access_token,
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": "claude-code/2.1.251",
        },
    )
    return normalize_claude(payload, observed_at)


def token_from_antigravity_file(path: pathlib.Path) -> str:
    if not path.is_file():
        return ""
    value = read_json(path, "Antigravity is not signed in on this machine.")
    token = value.get("access_token")
    nested = value.get("token")
    if not isinstance(token, str) and isinstance(nested, Mapping):
        token = nested.get("access_token")
    tokens = value.get("tokens")
    if not isinstance(token, str) and isinstance(tokens, list) and tokens:
        first = tokens[0]
        if isinstance(first, Mapping):
            token = first.get("accessToken")
    return token if isinstance(token, str) else ""


def antigravity_token() -> str:
    if sys.platform == "darwin":
        try:
            result = subprocess.run(
                [
                    "security",
                    "find-generic-password",
                    "-s",
                    "gemini",
                    "-a",
                    "antigravity",
                    "-w",
                ],
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                timeout=5,
            )
            raw = result.stdout.strip()
            if raw.startswith("go-keyring-base64:"):
                decoded = base64.b64decode(raw.removeprefix("go-keyring-base64:"))
                value = json.loads(decoded)
                if isinstance(value, Mapping):
                    nested = value.get("token")
                    token = nested.get("access_token") if isinstance(nested, Mapping) else None
                    if not isinstance(token, str):
                        token = value.get("access_token")
                    if isinstance(token, str) and token:
                        return token
        except (OSError, subprocess.SubprocessError, ValueError, json.JSONDecodeError):
            pass
    candidates = (
        pathlib.Path.home() / ".gemini" / "antigravity-cli" / "antigravity-oauth-token",
        pathlib.Path.home() / ".gemini" / "antigravity-cli" / "agy-hud-token.json",
        pathlib.Path.home() / ".gemini" / "oauth_creds.json",
    )
    for candidate in candidates:
        try:
            token = token_from_antigravity_file(candidate)
        except ProbeFailure:
            continue
        if token:
            return token
    raise ProbeFailure("auth_required", "Antigravity is not signed in on this machine.")


def fetch_antigravity(
    limiter: LocalRateLimiter, observed_at: dt.datetime
) -> Dict[str, Any]:
    token = antigravity_token()
    limiter.admit("antigravity")
    payload = http_json(
        ANTIGRAVITY_USAGE_URL,
        method="POST",
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
            "User-Agent": "antigravity/0.0.0 darwin/arm64",
            "Client-Metadata": json.dumps(
                {
                    "ideType": "IDE_UNSPECIFIED",
                    "platform": "PLATFORM_UNSPECIFIED",
                    "pluginType": "GEMINI",
                },
                separators=(",", ":"),
            ),
        },
        body={},
    )
    return normalize_antigravity(payload, observed_at)


def codex_credential_path() -> pathlib.Path:
    codex_home = os.environ.get("CODEX_HOME", "").strip()
    if codex_home:
        return pathlib.Path(codex_home).expanduser() / "auth.json"
    return pathlib.Path.home() / ".codex" / "auth.json"


def fetch_codex(limiter: LocalRateLimiter, observed_at: dt.datetime) -> Dict[str, Any]:
    credential = read_json(
        codex_credential_path(), "Codex is not signed in on this machine."
    )
    tokens = credential.get("tokens")
    if not isinstance(tokens, Mapping):
        raise ProbeFailure("auth_required", "Codex OAuth credentials are missing.")
    access_token = tokens.get("access_token")
    account_id = tokens.get("account_id")
    if not isinstance(access_token, str) or not access_token:
        raise ProbeFailure("auth_required", "Codex OAuth credentials are missing.")
    if not isinstance(account_id, str) or not account_id:
        raise ProbeFailure("auth_required", "Codex account scope is missing.")
    limiter.admit("codex")
    payload = http_json(
        CODEX_USAGE_URL,
        method="GET",
        headers={
            "Authorization": "Bearer " + access_token,
            "ChatGPT-Account-ID": account_id,
            "Accept": "application/json",
            "User-Agent": "Codex/1.0",
        },
    )
    return normalize_codex(payload, observed_at)


def fetch_provider(provider: str) -> Dict[str, Any]:
    observed_at = utc_now()
    limiter = LocalRateLimiter()
    try:
        if provider == "claude":
            return fetch_claude(limiter, observed_at)
        if provider == "antigravity":
            return fetch_antigravity(limiter, observed_at)
        if provider == "codex":
            return fetch_codex(limiter, observed_at)
        raise ProbeFailure("unavailable", "This provider has no direct usage probe.")
    except ProbeFailure as exc:
        return snapshot(
            provider,
            [],
            observed_at,
            status=exc.status,
            source="unavailable",
            message=exc.message,
            retry_after_seconds=exc.retry_after_seconds,
        )
    except Exception:
        return snapshot(
            provider,
            [],
            observed_at,
            status="error",
            source="unavailable",
            message="The direct provider usage probe failed.",
        )


def normalize_stdin(provider: str) -> Dict[str, Any]:
    try:
        value = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        raise ProbeFailure("error", "Fixture input was invalid JSON.") from exc
    if not isinstance(value, Mapping):
        raise ProbeFailure("error", "Fixture input had an invalid shape.")
    observed_at = dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)
    if provider == "claude":
        return normalize_claude(value, observed_at)
    if provider == "antigravity":
        return normalize_antigravity(value, observed_at)
    if provider == "codex":
        return normalize_codex(value, observed_at)
    raise ProbeFailure("unavailable", "This provider has no direct usage probe.")


def main(argv: List[str]) -> int:
    if len(argv) == 3 and argv[1] == "--normalize":
        result = normalize_stdin(argv[2].strip().lower())
    elif len(argv) == 2:
        provider = argv[1].strip().lower()
        if provider == "all":
            result = {
                "observed_at": iso_utc(utc_now()),
                "providers": [fetch_provider(name) for name in ("claude", "antigravity", "codex")],
            }
        else:
            result = fetch_provider(provider)
    else:
        print(
            "usage: provider_usage_probe.py [claude|antigravity|codex|all]",
            file=sys.stderr,
        )
        return 2
    json.dump(result, sys.stdout, separators=(",", ":"), sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv))
    except ProbeFailure as failure:
        json.dump(
            snapshot(
                "unknown",
                [],
                utc_now(),
                status=failure.status,
                source="unavailable",
                message=failure.message,
                retry_after_seconds=failure.retry_after_seconds,
            ),
            sys.stdout,
            separators=(",", ":"),
            sort_keys=True,
        )
        sys.stdout.write("\n")
        raise SystemExit(1)
