#!/usr/bin/env python3

import datetime as dt
import importlib.util
import json
import os
import pathlib
import tempfile
import unittest
from unittest import mock


SCRIPT_PATH = pathlib.Path(__file__).with_name("provider_usage_probe.py")
SPEC = importlib.util.spec_from_file_location("provider_usage_probe", SCRIPT_PATH)
assert SPEC is not None and SPEC.loader is not None
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


OBSERVED_AT = dt.datetime(2026, 8, 31, 10, 0, tzinfo=dt.timezone.utc)


class ProviderUsageNormalizerTests(unittest.TestCase):
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

    def test_antigravity_preserves_model_identity_and_deduplicates(self) -> None:
        result = probe.normalize_antigravity(
            {
                "models": {
                    "gemini-a": {
                        "displayName": "Gemini 2.5 Pro",
                        "quotaInfo": {
                            "remainingFraction": 0.75,
                            "resetTime": "2026-09-01T00:00:00Z",
                        },
                    },
                    "gemini-a-alias": {
                        "displayName": "Gemini 2.5 Pro",
                        "quotaInfo": {
                            "remainingFraction": 0.75,
                            "resetTime": "2026-09-01T00:00:00Z",
                        },
                    },
                    "claude-b": {
                        "displayName": "Claude Sonnet",
                        "quotaInfo": {
                            "remainingFraction": 0.4,
                            "resetTime": "2026-09-02T00:00:00Z",
                        },
                    },
                }
            },
            OBSERVED_AT,
        )
        self.assertEqual(result["provider"], "antigravity")
        self.assertEqual(len(result["windows"]), 2)
        groups = {item["group"] for item in result["windows"]}
        self.assertEqual(groups, {"Gemini 2.5 Pro", "Claude Sonnet"})

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

    def test_antigravity_uses_direct_model_catalog_endpoint(self) -> None:
        limiter = mock.Mock()
        with (
            mock.patch.object(probe, "antigravity_token", return_value="test-token"),
            mock.patch.object(
                probe,
                "http_json",
                return_value={
                    "models": {
                        "gemini": {
                            "displayName": "Gemini",
                            "quotaInfo": {"remainingFraction": 0.8},
                        }
                    }
                },
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
