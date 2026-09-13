#!/usr/bin/env python3

import datetime as dt
import importlib.util
import json
import os
import pathlib
import ssl
import tempfile
import unittest
from unittest import mock


SCRIPT_PATH = pathlib.Path(__file__).with_name("provider_usage_probe.py")
SPEC = importlib.util.spec_from_file_location("provider_usage_probe", SCRIPT_PATH)
assert SPEC is not None and SPEC.loader is not None
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


OBSERVED_AT = dt.datetime(2026, 8, 31, 10, 0, tzinfo=dt.timezone.utc)


class AntigravityCredentialTests(unittest.TestCase):
    def test_expired_file_does_not_hide_live_hud_token(self):
        with tempfile.TemporaryDirectory() as directory:
            home = pathlib.Path(directory)
            folder = home / ".gemini" / "antigravity-cli"
            folder.mkdir(parents=True)
            (folder / "antigravity-oauth-token").write_text(json.dumps({
                "token": {"access_token": "old", "expiry": "2026-08-31T09:00:00Z"}
            }))
            (folder / "agy-hud-token.json").write_text(json.dumps({"tokens": [
                {"accessToken": "expired", "expiry": "2026-08-31T09:00:00Z"},
                {"accessToken": "live", "expiry": "2026-08-31T11:00:00Z"},
            ]}))
            with mock.patch.object(probe.pathlib.Path, "home", return_value=home), mock.patch.object(
                probe.sys, "platform", "linux"
            ), mock.patch.object(probe, "utc_now", return_value=OBSERVED_AT):
                self.assertEqual(probe.antigravity_token(), "live")

    def test_expired_keychain_falls_back_to_live_agy_file(self):
        import base64
        value = {"token": {"access_token": "old", "expiry": "2026-08-31T09:00:00Z"}}
        raw = "go-keyring-base64:" + base64.b64encode(json.dumps(value).encode()).decode()
        with mock.patch.object(probe.sys, "platform", "darwin"), mock.patch.object(
            probe.subprocess, "run", return_value=mock.Mock(stdout=raw)
        ), mock.patch.object(probe, "token_from_antigravity_file", return_value="live"), mock.patch.object(
            probe, "utc_now", return_value=OBSERVED_AT
        ):
            self.assertEqual(probe.antigravity_token(), "live")

    def test_expiry_and_legacy_formats(self):
        with mock.patch.object(probe, "utc_now", return_value=OBSERVED_AT):
            for expiry in ["2026-08-31T10:00:00Z", "invalid", "2026-08-31T17:59:59+08:00"]:
                with self.subTest(expiry=expiry):
                    self.assertEqual(probe.usable_antigravity_token(
                        {"access_token": "old", "expiry": expiry}), "")
            self.assertEqual(probe.usable_antigravity_token(
                {"token": {"access_token": "live", "expiry": "2026-08-31T18:00:01+08:00"}}), "live")
            self.assertEqual(probe.usable_antigravity_token({"access_token": "legacy"}), "legacy")

    def test_does_not_use_unrelated_gemini_login(self):
        with tempfile.TemporaryDirectory() as directory:
            home = pathlib.Path(directory)
            (home / ".gemini").mkdir()
            (home / ".gemini" / "oauth_creds.json").write_text('{"access_token":"other-account"}')
            with mock.patch.object(probe.pathlib.Path, "home", return_value=home), mock.patch.object(
                probe.sys, "platform", "linux"
            ), self.assertRaises(probe.ProbeFailure):
                probe.antigravity_token()


class ProviderUsageNormalizerTests(unittest.TestCase):
    def test_codex_rejects_missing_or_unusable_windows(self) -> None:
        for payload in (
            {},
            {"rate_limit": {}},
            {"rate_limit": {"primary_window": {}}},
            {"rate_limit": {"primary_window": {"used_percent": "unknown"}}},
        ):
            with self.subTest(payload=payload):
                with self.assertRaises(probe.ProbeFailure) as failure:
                    probe.normalize_codex(payload, OBSERVED_AT)
                self.assertEqual(failure.exception.status, "error")

    def test_claude_normalizes_percent_windows(self) -> None:
        result = probe.normalize_claude(
            {
                "five_hour": {
                    "utilization": 23.5,
                    "resets_at": "2026-08-31T12:00:00Z",
                },
                "seven_day": {
                    "utilization": 41.2,
                    "resets_at": "2026-09-05T00:00:00Z",
                },
            },
            OBSERVED_AT,
        )
        self.assertEqual(result["provider"], "claude")
        self.assertEqual(result["status"], "available")
        self.assertEqual(len(result["windows"]), 2)
        self.assertEqual(result["windows"][0]["used_percent"], 23.5)
        self.assertEqual(result["windows"][0]["remaining_percent"], 76.5)
        self.assertEqual(result["windows"][1]["window_duration_mins"], 10080)

    def test_antigravity_maps_explicit_shared_pools_and_windows(self) -> None:
        result = probe.normalize_antigravity({"groups": [
            {"displayName": "Gemini Models", "buckets": [
                {"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.75},
                {"bucketId": "gemini-weekly", "window": "weekly", "remainingFraction": 0.4,
                 "resetTime": "2026-08-31T11:00:00Z"},
            ]},
            {"displayName": "Claude and GPT models", "buckets": [
                {"bucketId": "3p-5h", "window": "5h", "remainingFraction": 1},
                {"bucketId": "3p-weekly", "window": "weekly", "remainingFraction": 0},
            ]},
        ]}, OBSERVED_AT)
        self.assertEqual(result["status"], "available")
        rows = result["windows"]
        self.assertEqual([r["used_percent"] for r in rows], [25, 60, 0, 100])
        self.assertEqual([r["window_duration_mins"] for r in rows], [300, 10080, 300, 10080])
        self.assertEqual(rows[2]["group"], "Claude and GPT models")
        # A weekly reset one hour away is still weekly, not a five-hour guess.
        self.assertEqual(rows[1]["resets_at"], "2026-08-31T11:00:00Z")

    def test_antigravity_missing_invalid_disabled_or_amount_never_becomes_percentage(self) -> None:
        invalid = [{"remainingFraction": v} for v in
                   (None, "0.5", True, -0.1, 1.1, float("nan"), float("inf"))]
        invalid += [{}, {"remainingFraction": 0.5, "disabled": True},
                    {"remainingAmount": 12}, {"remainingAmount": 12, "remainingFraction": 0.5}]
        for fields in invalid:
            with self.subTest(fields=fields):
                result = probe.normalize_antigravity({"buckets": [{
                    "bucketId": "gemini-5h", "window": "5h", **fields,
                }]}, OBSERVED_AT)
                row = result["windows"][0]
                self.assertEqual(result["status"], "partial")
                self.assertNotIn("used_percent", row)
                self.assertNotIn("remaining_percent", row)
                self.assertEqual(row["window_duration_mins"], 300)
                self.assertEqual(len(result["windows"]), 1)

    def test_antigravity_keeps_independent_and_unknown_buckets_without_inventing_windows(self) -> None:
        result = probe.normalize_antigravity({"groups": [{"displayName": "New provider", "buckets": [
            {"bucketId": "independent-a", "window": "daily", "remainingFraction": 0.8},
            {"bucketId": "independent-b", "remainingFraction": 0.8, "resetTime": "bad-date"},
        ]}]}, OBSERVED_AT)
        self.assertEqual(result["status"], "partial")
        self.assertEqual(len(result["windows"]), 2)
        for row in result["windows"]:
            self.assertNotIn("window_duration_mins", row)
            self.assertNotIn("resets_at", row)
            self.assertEqual(row["remaining_percent"], 80)

    def test_antigravity_rejects_unusable_or_ambiguous_summary(self) -> None:
        for payload in ({}, {"models": {}}, {"groups": {}}, {"groups": [None]},
                        {"buckets": [None]}, {"buckets": [{}]}, {"buckets": [
                            {"bucketId": "same"}, {"bucketId": "same"}]}):
            with self.subTest(payload=payload), self.assertRaises(probe.ProbeFailure):
                probe.normalize_antigravity(payload, OBSERVED_AT)

    def test_codex_keeps_primary_and_model_specific_limits(self) -> None:
        result = probe.normalize_codex(
            {
                "plan_type": "pro",
                "rate_limit": {
                    "primary_window": {
                        "used_percent": 19,
                        "reset_at": 1788272549,
                        "limit_window_seconds": 604800,
                    }
                },
                "additional_rate_limits": [
                    {
                        "limit_name": "Spark",
                        "rate_limit": {
                            "primary_window": {
                                "used_percent": 25.5,
                                "reset_at": 1787859306,
                                "limit_window_seconds": 18000,
                            },
                            "secondary_window": {
                                "used_percent": 40,
                                "reset_at": 1788446106,
                                "limit_window_seconds": 604800,
                            },
                        },
                    }
                ],
            },
            OBSERVED_AT,
        )
        self.assertEqual(result["provider"], "codex")
        self.assertEqual(result["account_scope"], "pro")
        self.assertEqual(len(result["windows"]), 3)
        spark = [item for item in result["windows"] if item["group"] == "Spark"]
        self.assertEqual(len(spark), 2)
        self.assertEqual(spark[0]["remaining_percent"], 74.5)


class ProviderFetchTests(unittest.TestCase):
    def test_provider_https_adds_public_roots_to_existing_private_ca_context(self) -> None:
        for defaults_exist in (True, False):
            with self.subTest(defaults_exist=defaults_exist):
                context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
                context.load_verify_locations = mock.Mock()
                paths = mock.Mock(
                    openssl_cafile="/test/public-ca.pem", openssl_capath="/test/certs"
                )

                def default_context():
                    # The default loader must still see the daemon's private CA.
                    self.assertEqual(os.environ["SSL_CERT_FILE"], "/test/private-ca.pem")
                    return context

                with (
                    mock.patch.dict(os.environ, {"SSL_CERT_FILE": "/test/private-ca.pem"}),
                    mock.patch.object(probe.ssl, "create_default_context", side_effect=default_context),
                    mock.patch.object(probe.ssl, "get_default_verify_paths", return_value=paths),
                    mock.patch.object(probe.os.path, "isfile", return_value=defaults_exist),
                    mock.patch.object(probe.os.path, "isdir", return_value=defaults_exist),
                    mock.patch.object(probe.urllib.request, "urlopen") as request,
                ):
                    request.return_value.__enter__.return_value.read.return_value = b'{"ok":true}'
                    self.assertEqual(probe.http_json(probe.CODEX_USAGE_URL, method="GET", headers={}), {"ok": True})

                self.assertIs(request.call_args.kwargs["context"], context)
                self.assertTrue(context.check_hostname)
                self.assertEqual(context.verify_mode, ssl.CERT_REQUIRED)
                if defaults_exist:
                    context.load_verify_locations.assert_called_once_with(
                        cafile="/test/public-ca.pem", capath="/test/certs"
                    )
                else:
                    context.load_verify_locations.assert_not_called()

    def test_claude_uses_local_oauth_token_and_direct_usage_endpoint(self) -> None:
        limiter = mock.Mock()
        with (
            mock.patch.object(
                probe,
                "read_json",
                return_value={
                    "claudeAiOauth": {
                        "accessToken": "test-access-token",
                        "refreshToken": "test-refresh-token",
                        "expiresAt": 9_999_999_999_999,
                    }
                },
            ),
            mock.patch.object(
                probe,
                "http_json",
                return_value={"five_hour": {"utilization": 12}},
            ) as request,
        ):
            result = probe.fetch_claude(limiter, OBSERVED_AT)

        limiter.admit.assert_called_once_with("claude", request_count=1)
        request.assert_called_once_with(
            probe.CLAUDE_USAGE_URL,
            method="GET",
            headers={
                "Authorization": "Bearer test-access-token",
                "Accept": "application/json",
                "Content-Type": "application/json",
                "User-Agent": "claude-code/2.1.251",
            },
        )
        self.assertEqual(result["windows"][0]["used_percent"], 12)

    def test_claude_expiry_reserves_refresh_and_usage_requests(self) -> None:
        limiter = mock.Mock()
        with (
            mock.patch.object(
                probe,
                "read_json",
                return_value={
                    "claudeAiOauth": {
                        "accessToken": "expired-access-token",
                        "refreshToken": "test-refresh-token",
                        "expiresAt": 1,
                    }
                },
            ),
            mock.patch.object(
                probe,
                "http_json",
                side_effect=[
                    {"access_token": "refreshed-access-token"},
                    {"five_hour": {"utilization": 7}},
                ],
            ) as request,
        ):
            result = probe.fetch_claude(limiter, OBSERVED_AT)

        limiter.admit.assert_called_once_with("claude", request_count=2)
        self.assertEqual(request.call_count, 2)
        self.assertEqual(request.call_args_list[0].args, (probe.CLAUDE_REFRESH_URL,))
        self.assertEqual(request.call_args_list[1].args, (probe.CLAUDE_USAGE_URL,))
        self.assertEqual(
            request.call_args_list[1].kwargs["headers"]["Authorization"],
            "Bearer refreshed-access-token",
        )
        self.assertEqual(result["windows"][0]["used_percent"], 7)

    def test_antigravity_uses_direct_quota_summary_endpoint(self) -> None:
        limiter = mock.Mock()
        with (
            mock.patch.object(probe, "antigravity_token", return_value="test-token"),
            mock.patch.object(
                probe,
                "http_json",
                return_value={"buckets": [{"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.8}]},
            ) as request,
        ):
            result = probe.fetch_antigravity(limiter, OBSERVED_AT)

        limiter.admit.assert_called_once_with("antigravity")
        args, kwargs = request.call_args
        self.assertEqual(args, (probe.ANTIGRAVITY_USAGE_URL,))
        self.assertEqual(kwargs["method"], "POST")
        self.assertEqual(kwargs["headers"]["Authorization"], "Bearer test-token")
        self.assertEqual(kwargs["body"], {})
        self.assertEqual(result["windows"][0]["remaining_percent"], 80)
        self.assertEqual(result["status"], "partial")

    def test_codex_uses_account_scoped_direct_usage_endpoint(self) -> None:
        limiter = mock.Mock()
        with (
            mock.patch.object(
                probe,
                "read_json",
                return_value={
                    "tokens": {
                        "access_token": "test-access-token",
                        "account_id": "test-account",
                    }
                },
            ),
            mock.patch.object(
                probe,
                "http_json",
                return_value={
                    "rate_limit": {
                        "primary_window": {"used_percent": 18},
                    }
                },
            ) as request,
        ):
            result = probe.fetch_codex(limiter, OBSERVED_AT)

        limiter.admit.assert_called_once_with("codex")
        request.assert_called_once_with(
            probe.CODEX_USAGE_URL,
            method="GET",
            headers={
                "Authorization": "Bearer test-access-token",
                "ChatGPT-Account-ID": "test-account",
                "Accept": "application/json",
                "User-Agent": "Codex/1.0",
            },
        )
        self.assertEqual(result["windows"][0]["used_percent"], 18)


class LocalRateLimiterTests(unittest.TestCase):
    def test_minimum_interval_and_hourly_limit_are_persisted(self) -> None:
        with tempfile.TemporaryDirectory() as state_dir:
            env = {
                "MULTICA_PROVIDER_USAGE_STATE_DIR": state_dir,
                "MULTICA_PROVIDER_USAGE_MIN_INTERVAL_SECONDS": "30",
                "MULTICA_PROVIDER_USAGE_MAX_REQUESTS_PER_HOUR": "2",
            }
            with mock.patch.dict(os.environ, env, clear=False):
                limiter = probe.LocalRateLimiter()
                limiter.admit("codex", now=100)
                with self.assertRaises(probe.ProbeFailure) as cooling_down:
                    limiter.admit("codex", now=105)
                self.assertEqual(cooling_down.exception.status, "rate_limited")
                self.assertEqual(cooling_down.exception.retry_after_seconds, 25)

                limiter.admit("codex", now=131)
                with self.assertRaises(probe.ProbeFailure) as hourly:
                    limiter.admit("codex", now=162)
                self.assertEqual(hourly.exception.status, "rate_limited")
                self.assertGreater(hourly.exception.retry_after_seconds, 3500)

                state_path = pathlib.Path(state_dir) / "request-history.json"
                state = json.loads(state_path.read_text(encoding="utf-8"))
                self.assertEqual(state["requests"]["codex"], [100.0, 131.0])
                if os.name != "nt":
                    self.assertEqual(state_path.stat().st_mode & 0o777, 0o600)

    def test_multiple_external_requests_reserve_multiple_hourly_slots(self) -> None:
        with tempfile.TemporaryDirectory() as state_dir:
            env = {
                "MULTICA_PROVIDER_USAGE_STATE_DIR": state_dir,
                "MULTICA_PROVIDER_USAGE_MIN_INTERVAL_SECONDS": "1",
                "MULTICA_PROVIDER_USAGE_MAX_REQUESTS_PER_HOUR": "2",
            }
            with mock.patch.dict(os.environ, env, clear=False):
                limiter = probe.LocalRateLimiter()
                limiter.admit("claude", now=100, request_count=2)
                with self.assertRaises(probe.ProbeFailure) as hourly:
                    limiter.admit("claude", now=102)
                self.assertEqual(hourly.exception.status, "rate_limited")

                state_path = pathlib.Path(state_dir) / "request-history.json"
                state = json.loads(state_path.read_text(encoding="utf-8"))
                self.assertEqual(state["requests"]["claude"], [100.0, 100.0])


if __name__ == "__main__":
    unittest.main()
