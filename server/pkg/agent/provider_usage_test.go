package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalesceProviderUsageSharesConcurrentProbe(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	probe := func() (ProviderUsage, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return ProviderUsage{Provider: "codex", Status: "available"}, nil
	}

	const workers = 8
	results := make(chan ProviderUsage, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			usage, err := coalesceProviderUsage("test-coalesce", probe)
			if err != nil {
				t.Errorf("coalesced probe: %v", err)
				return
			}
			results <- usage
		}()
	}
	ready.Wait()
	<-started
	time.Sleep(10 * time.Millisecond)
	close(release)
	for range workers {
		if got := <-results; got.Provider != "codex" {
			t.Fatalf("provider = %q", got.Provider)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
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
