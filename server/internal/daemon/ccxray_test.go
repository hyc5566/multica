package daemon

import (
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCCXRayLaunchDisabled(t *testing.T) {
	path, prefix, enabled := ccxrayLaunch(false, false, "codex", "/bin/codex", []string{"exec"})
	if enabled || path != "/bin/codex" || len(prefix) != 1 || prefix[0] != "exec" {
		t.Fatalf("ccxrayLaunch disabled = %q %v %v", path, prefix, enabled)
	}
}

func TestCCXRayHealthSummaryIsBoundedAndFailOpen(t *testing.T) {
	originalLookPath := lookPath
	t.Cleanup(func() { lookPath = originalLookPath })
	now := time.Date(2026, 9, 3, 10, 0, 37, 0, time.UTC)

	lookPath = func(string) (string, error) { return "", errors.New("missing") }
	missing := ccxrayHealthSummary(true, now)
	if missing.Status != protocol.CCXRayStatusNotInstalled || missing.Installed || missing.LastErrorCode != "executable_not_found" {
		t.Fatalf("missing summary = %+v", missing)
	}

	lookPath = func(string) (string, error) { return "/opt/bin/ccxray", nil }
	disabled := ccxrayHealthSummary(false, now)
	if disabled.Status != protocol.CCXRayStatusDisabled || !disabled.Installed || disabled.ObservedAt != "2026-09-03T10:00:00Z" {
		t.Fatalf("disabled summary = %+v", disabled)
	}
	observing := ccxrayHealthSummary(true, now)
	if observing.Status != protocol.CCXRayStatusObserving || !observing.Enabled || !observing.Installed {
		t.Fatalf("observing summary = %+v", observing)
	}
	if len(observing.Version) != 0 {
		t.Fatalf("health probe must not execute or invent a version: %+v", observing)
	}
}

func TestCCXRayProviderAvailabilityIncludesGrokWithoutWrappingIt(t *testing.T) {
	if !ccxraySupportsProvider("grok") {
		t.Fatal("Grok should surface ccxray availability")
	}
	if _, _, wrapped := ccxrayLaunch(true, false, "grok", "/bin/grok", nil); wrapped {
		t.Fatal("Phase A/B must not enable a new Grok proxy launch path")
	}
}

func TestCCXRayLaunchSkipsUnsupportedAndCustomRuntimes(t *testing.T) {
	for _, tc := range []struct {
		provider string
		custom   bool
	}{
		{provider: "cursor"},
		{provider: "claude", custom: true},
	} {
		path, _, enabled := ccxrayLaunch(true, tc.custom, tc.provider, "/bin/provider", nil)
		if enabled || path != "/bin/provider" {
			t.Fatalf("ccxrayLaunch(%q, custom=%v) unexpectedly wrapped", tc.provider, tc.custom)
		}
	}
}
