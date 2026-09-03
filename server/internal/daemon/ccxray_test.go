package daemon

import (
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
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

func TestShouldFailOpenCCXRayOnlyBeforeSessionOrTools(t *testing.T) {
	handshakeFailure := agent.Result{
		Status: "failed",
		Error:  agent.CodexHandshakeTimeoutMarker + ": initialize did not respond",
	}
	processFailure := agent.Result{Status: "failed", Error: "codex initialize failed: codex process exited: exit status 1"}

	for _, tc := range []struct {
		name       string
		enabled    bool
		provider   string
		result     agent.Result
		tools      int32
		executeErr error
		want       bool
	}{
		{name: "handshake timeout", enabled: true, provider: "codex", result: handshakeFailure, want: true},
		{name: "wrapper process exit", enabled: true, provider: "codex", result: processFailure, want: true},
		{name: "wrapper launch error", enabled: true, provider: "codex", executeErr: errors.New("exec ccxray"), want: true},
		{name: "session already established", enabled: true, provider: "codex", result: agent.Result{Status: "failed", Error: handshakeFailure.Error, SessionID: "thread-1"}},
		{name: "tool already ran", enabled: true, provider: "codex", result: handshakeFailure, tools: 1},
		{name: "provider auth failure", enabled: true, provider: "codex", result: agent.Result{Status: "failed", Error: "HTTP 401 unauthorized"}},
		{name: "claude remains unchanged", enabled: true, provider: "claude", result: handshakeFailure},
		{name: "ccxray disabled", provider: "codex", result: handshakeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFailOpenCCXRay(tc.enabled, tc.provider, tc.result, tc.tools, tc.executeErr); got != tc.want {
				t.Fatalf("shouldFailOpenCCXRay() = %v, want %v", got, tc.want)
			}
		})
	}
}
