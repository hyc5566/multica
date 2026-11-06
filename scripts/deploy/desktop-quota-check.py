"""Run the daemon's quota/credential check after an authorized Desktop update."""
import argparse
import datetime as dt
import json
import pathlib
import re
import ssl
import subprocess
import sys
import time
import urllib.request
import uuid


def timestamp(value):
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp requires a timezone")
    return parsed


def verify_snapshot(snapshot, not_before):
    if snapshot.get("status") != "available" or snapshot.get("stale") or snapshot.get("last_error_code"):
        raise ValueError("quota unavailable; inspect the runtime's quota status")
    if timestamp(snapshot.get("observed_at", "")) < not_before:
        raise ValueError("quota snapshot predates this update/check")
    if not snapshot.get("windows"):
        raise ValueError("quota has no windows")
    return {key: snapshot.get(key) for key in ("status", "observed_at", "last_success_at")}


def refresh(call, route, not_before):
    deadline = time.monotonic() + 50
    result = call(route, "POST")
    request_id = result.get("id")
    if result.get("status") in ("pending", "running"):
        uuid.UUID(request_id)  # Validate without changing the opaque cache key.
    while result.get("status") in ("pending", "running"):
        if time.monotonic() >= deadline:
            raise TimeoutError("quota refresh timed out")
        time.sleep(1)
        result = call(route + "/" + request_id, "GET")
    if result.get("status") != "completed":
        raise ValueError("quota refresh request failed")
    # Never accept a retained last-known-good snapshot after a failed attempt.
    verify_snapshot(result.get("provider_usage") or {}, not_before)
    return verify_snapshot(call(route, "GET"), not_before)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True)
    parser.add_argument("--workspace", required=True, type=uuid.UUID)
    parser.add_argument("--runtime", required=True, type=uuid.UUID)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--cli", type=pathlib.Path, default=pathlib.Path(
        "/Applications/Multica 繁中版.app/Contents/Resources/app.asar.unpacked/resources/bin/multica"))
    parser.add_argument("--ca", type=pathlib.Path, default=pathlib.Path.home() /
                        "Library/Application Support/Multica 繁中版/ca-bundle.crt")
    parser.add_argument("--update-command", nargs=argparse.REMAINDER,
                        help="Optional already-authorized installer command; no shell expansion")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", args.profile):
        parser.error("invalid profile name")
    if args.update_command is not None:
        if not args.update_command:
            parser.error("--update-command requires a command")
        subprocess.run(args.update_command, check=True, stdout=sys.stderr)
    status = json.loads(subprocess.run(
        [str(args.cli), "--profile", args.profile, "daemon", "status", "--output", "json"],
        check=True, capture_output=True, text=True, timeout=10).stdout)
    if status.get("status") != "running" or status.get("cli_version") != args.expected_version:
        raise ValueError("expected updated daemon is not running")
    if not any(ws.get("id") == str(args.workspace) and str(args.runtime) in ws.get("runtimes", [])
               for ws in status.get("workspaces", [])):
        raise ValueError("runtime does not belong to this daemon/workspace")
    config = json.loads((pathlib.Path.home() / ".multica/profiles" / args.profile / "config.json").read_text())
    base = config["server_url"].rstrip("/")
    if not base.startswith("https://"):
        raise ValueError("this deployment check requires HTTPS")
    opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(
        context=ssl.create_default_context(cafile=str(args.ca))))
    headers = {"Authorization": "Bearer " + config["token"], "X-Workspace-ID": str(args.workspace)}

    def call(route, method):
        request = urllib.request.Request(base + route, headers=headers, method=method)
        with opener.open(request, timeout=8) as response:
            return json.load(response)

    # The cutoff must follow installer completion and updated-daemon validation.
    not_before = dt.datetime.now(dt.timezone.utc)
    result = refresh(call, "/api/runtimes/" + str(args.runtime) + "/provider-usage", not_before)
    print(json.dumps({"status": "verified", "daemon_version": status["cli_version"],
                      "runtime_id": str(args.runtime), "quota": result}))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Network errors can contain server URLs or response bodies. Do not print them.
        print(json.dumps({"status": "not_verified", "error_type": type(error).__name__}))
        sys.exit(1)
