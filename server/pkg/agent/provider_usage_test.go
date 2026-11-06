package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRefreshAntigravityCredentialUsesExplicitUncachedModelsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	marker := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MULTICA_TEST_RENEW_MARKER", marker)
	path := filepath.Join(t.TempDir(), "fake-agy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = models ] || exit 9\necho call >> \"$MULTICA_TEST_RENEW_MARKER\"\necho fixture-output\necho fixture-error >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if !RefreshAntigravityCredential(context.Background(), NewCommand(path, nil)) {
			t.Fatal("native model command failed")
		}
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "call\ncall\n" {
		t.Fatalf("renewal was cached: %q, %v", data, err)
	}
	for _, cmd := range []Command{{}, NewCommand(filepath.Join(t.TempDir(), "missing"), nil), NewCommand(path, []string{"unexpected"})} {
		if RefreshAntigravityCredential(context.Background(), cmd) {
			t.Fatal("missing or failing command reported success")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if RefreshAntigravityCredential(ctx, NewCommand(path, nil)) {
		t.Fatal("canceled renewal reported success")
	}
}

func TestParseProviderUsageProbeAcceptsRateLimitMetadata(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"provider":"codex",
		"status":"rate_limited",
		"source":"unavailable",
		"observed_at":"2026-08-31T10:00:00Z",
		"message":"The local provider usage refresh is cooling down.",
		"retry_after_seconds":25
	}`)
	got, err := parseProviderUsageProbe(raw, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rate_limited" || got.RetryAfterSeconds == nil || *got.RetryAfterSeconds != 25 {
		t.Fatalf("rate-limited snapshot = %+v", got)
	}
}

func TestParseProviderUsageProbeAcceptsNativeRefreshHint(t *testing.T) {
	raw := []byte(`{"provider":"antigravity","status":"auth_required","source":"unavailable","observed_at":"2026-08-31T10:00:00Z","credential_refresh_needed":true}`)
	usage, err := parseProviderUsageProbe(raw, "antigravity")
	if err != nil || !usage.CredentialRefreshNeeded {
		t.Fatalf("refresh hint was lost: %+v, %v", usage, err)
	}
}

func TestParseProviderUsageProbeAcceptsQuotaWindows(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"provider":"claude",
		"status":"available",
		"source":"official",
		"observed_at":"2026-08-31T10:00:00Z",
		"windows":[{
			"id":"five-hour",
			"group":"Claude Code",
			"label":"5 hour limit",
			"used_percent":23.5,
			"remaining_percent":76.5,
			"window_duration_mins":300,
			"resets_at":"2026-08-31T12:00:00Z",
			"unit":"percent"
		}]
	}`)
	got, err := parseProviderUsageProbe(raw, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 1 || got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 23.5 {
		t.Fatalf("quota snapshot = %+v", got)
	}
}

func TestParseProviderUsageProbeRejectsMismatchedProvider(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"provider":"claude","status":"available","source":"official","observed_at":"2026-08-31T10:00:00Z"}`)
	if _, err := parseProviderUsageProbe(raw, "codex"); err == nil {
		t.Fatal("mismatched provider was accepted")
	}
}

func TestParseProviderUsageProbeRejectsInvalidPercentage(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"provider":"antigravity",
		"status":"available",
		"source":"official",
		"observed_at":"2026-08-31T10:00:00Z",
		"windows":[{"id":"model","label":"Model quota","used_percent":101,"unit":"percent"}]
	}`)
	if _, err := parseProviderUsageProbe(raw, "antigravity"); err == nil {
		t.Fatal("invalid percentage was accepted")
	}
}

func TestParseProviderUsageProbeRejectsInvalidRetryInterval(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"provider":"codex",
		"status":"rate_limited",
		"source":"unavailable",
		"observed_at":"2026-08-31T10:00:00Z",
		"retry_after_seconds":0
	}`)
	if _, err := parseProviderUsageProbe(raw, "codex"); err == nil {
		t.Fatal("invalid retry interval was accepted")
	}
}
