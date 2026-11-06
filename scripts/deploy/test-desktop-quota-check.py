"""Isolated acceptance checks; never reads accounts or executes installed CLIs."""
import importlib.util
import pathlib
import json
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("check", pathlib.Path(__file__).with_name("desktop-quota-check.py"))
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


class QuotaCheckTest(unittest.TestCase):
    def test_failed_installer_stops_before_account_or_daemon_access(self):
        args = ["check", "--profile", "desktop-10.1.24.90-45671",
                "--workspace", "76194abc-bda7-4995-8b18-a90fe46d9dce",
                "--runtime", "0699adbe-b52d-4f5f-a982-46f11a7df5c4",
                "--expected-version", "0.4.41-zh-tw.15",
                "--update-command", "/test/installer", "--approved-version"]
        with patch.object(check.sys, "argv", args), patch.object(
                check.subprocess, "run", side_effect=subprocess.CalledProcessError(7, "installer")) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                check.main()
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0], ["/test/installer", "--approved-version"])

    def test_freshness_cutoff_follows_installer_and_daemon_validation(self):
        ws = "76194abc-bda7-4995-8b18-a90fe46d9dce"
        rt = "0699adbe-b52d-4f5f-a982-46f11a7df5c4"
        ready = check.timestamp("2026-09-15T03:01:00Z")
        status = {"status": "running", "cli_version": "test",
                  "workspaces": [{"id": ws, "runtimes": [rt]}]}
        args = ["check", "--profile", "desktop-10.1.24.90-45671", "--workspace", ws,
                "--runtime", rt, "--expected-version", "test", "--update-command", "/test/installer"]
        with patch.object(check.sys, "argv", args), patch.object(
                check.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0),
                subprocess.CompletedProcess([], 0, stdout=json.dumps(status))]) as run, patch.object(
                check.pathlib.Path, "read_text", return_value='{"server_url":"https://test.invalid","token":"fake"}'), patch.object(
                check.ssl, "create_default_context"), patch.object(check.urllib.request, "build_opener"), patch.object(
                check.dt, "datetime", wraps=check.dt.datetime) as clock, patch.object(check, "refresh", return_value={}) as refresh, patch("builtins.print"):
            def after_ready(*_):
                self.assertEqual(run.call_count, 2)
                return ready
            clock.now.side_effect = after_ready
            check.main()
            self.assertEqual(refresh.call_args.args[2], ready)

    def test_refresh_accepts_new_result_but_rejects_failed_attempt_and_old_cache(self):
        cutoff = check.timestamp("2026-09-15T03:00:00Z")
        good = {"status": "available", "observed_at": "2026-09-15T03:00:01Z", "windows": [{"id": "5h"}]}
        pending = {"status": "pending", "id": "0699adbeb52d4f5fa98246f11a7df5c4"}
        cases = [
            ({"status": "completed", "provider_usage": good}, good, True),
            ({"status": "completed", "provider_usage": {"status": "auth_required"}}, good, False),
            ({"status": "completed", "provider_usage": good}, dict(good, observed_at="2026-09-15T02:00:00Z"), False),
            ({"status": "completed", "provider_usage": good}, dict(good, stale=True), False),
            ({"status": "failed"}, good, False),
        ]
        for attempt, stored, succeeds in cases:
            with self.subTest(attempt=attempt, stored=stored):
                replies = iter([pending, attempt, stored])
                calls = []
                def call(route, method):
                    calls.append((route, method))
                    return next(replies)
                with patch.object(check.time, "sleep"), patch.object(check.time, "monotonic", return_value=0):
                    if succeeds:
                        self.assertEqual(check.refresh(call, "/quota", cutoff)["status"], "available")
                    else:
                        with self.assertRaises(ValueError):
                            check.refresh(call, "/quota", cutoff)
                        self.assertEqual(calls[1][0], "/quota/" + pending["id"])
        with patch.object(check.time, "monotonic", side_effect=[0, 51]):
            with self.assertRaises(TimeoutError):
                check.refresh(lambda *_: pending, "/quota", cutoff)


if __name__ == "__main__":
    unittest.main()
