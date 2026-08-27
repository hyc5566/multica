package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageAuthErrorClassification(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		"Not logged in. Run login",
		"authentication required",
		"401 Unauthorized",
	} {
		if !isUsageAuthError(errors.New(message)) {
			t.Fatalf("expected auth classification for %q", message)
		}
	}
	if isUsageAuthError(errors.New("binary exited unexpectedly")) {
		t.Fatal("generic process failure must not be classified as auth")
	}
}

func TestParseCodexUsagePreservesWindowsAndReset(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"rateLimits": {"limitId":"codex","limitName":null,"primary":{"usedPercent":19,"windowDurationMins":10080,"resetsAt":1788272549},"secondary":null,"planType":"pro"},
		"rateLimitsByLimitId": {
			"spark": {"limitId":"spark","limitName":"Spark","primary":{"usedPercent":25.5,"windowDurationMins":300,"resetsAt":1787859306},"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1788446106},"planType":"pro"}
		}
	}`)
	got, err := parseCodexUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" || got.Source != "official" || got.AccountScope != "pro" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("windows = %d, want 3: %+v", len(got.Windows), got.Windows)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 25.5 {
		t.Fatalf("used percent not preserved: %+v", got.Windows[0])
	}
	if got.Windows[0].RemainingPercent == nil || *got.Windows[0].RemainingPercent != 74.5 {
		t.Fatalf("remaining percent not derived correctly: %+v", got.Windows[0])
	}
	wantReset := time.Unix(1787859306, 0).UTC()
	if got.Windows[0].ResetsAt == nil || !got.Windows[0].ResetsAt.Equal(wantReset) {
		t.Fatalf("reset = %v, want %v", got.Windows[0].ResetsAt, wantReset)
	}
}

func TestParseAntigravityUsageUsesStructuredCommandData(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"status":"SUCCESS",
		"response":"human-readable text deliberately ignored",
		"usage":{"total_tokens":0},
		"command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[
			{"id":"gemini-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0.928599,"reset_time":"2026-09-01T15:18:49Z"},
			{"id":"gemini-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"2026-08-27T19:34:50Z"}
		]}]}}
	}`)
	got, err := parseAntigravityUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" || got.Source != "official" || len(got.Windows) != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	weekly := got.Windows[0]
	if weekly.WindowDurationMins == nil || *weekly.WindowDurationMins != 10080 {
		t.Fatalf("weekly duration missing: %+v", weekly)
	}
	if weekly.RemainingPercent == nil || *weekly.RemainingPercent < 92.85 || *weekly.RemainingPercent > 92.87 {
		t.Fatalf("remaining percent = %v", weekly.RemainingPercent)
	}
	if weekly.UsedPercent == nil || *weekly.UsedPercent < 7.13 || *weekly.UsedPercent > 7.15 {
		t.Fatalf("used percent = %v", weekly.UsedPercent)
	}
}

func TestParseClaudeUsageUsesOfficialStatusLineWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 15, 20, 0, 0, time.UTC)
	raw := []byte(`{
		"schema_version":1,
		"observed_at":"2026-08-27T15:19:00Z",
		"rate_limits":{
			"five_hour":{"used_percentage":23.5,"resets_at":1787859306},
			"seven_day":{"used_percentage":41.2,"resets_at":1788446106}
		}
	}`)
	got, err := parseClaudeUsage(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" || got.Source != "official" || len(got.Windows) != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got.Windows[0].UsedPercent == nil || *got.Windows[0].UsedPercent != 23.5 {
		t.Fatalf("five-hour percentage missing: %+v", got.Windows[0])
	}
	if got.Windows[1].RemainingPercent == nil || *got.Windows[1].RemainingPercent < 58.79 || *got.Windows[1].RemainingPercent > 58.81 {
		t.Fatalf("weekly remaining percentage = %v", got.Windows[1].RemainingPercent)
	}
}

func TestParseClaudeUsageMarksStaleSnapshotPartial(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)
	raw := []byte(`{"observed_at":"2026-08-27T14:00:00Z","rate_limits":{"five_hour":{"used_percentage":20}}}`)
	got, err := parseClaudeUsage(raw, observed.Add(claudeUsageMaxAge+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "partial" || len(got.Windows) != 1 || got.Message == "" {
		t.Fatalf("stale snapshot = %+v", got)
	}
}

func TestProbeClaudeUsageMissingSnapshotIsUnavailable(t *testing.T) {
	t.Setenv(claudeUsageSnapshotEnv, filepath.Join(t.TempDir(), "missing.json"))
	got := ProbeProviderUsage(t.Context(), "claude", Command{})
	if got.Status != "unavailable" || got.Source != "unavailable" || len(got.Windows) != 0 {
		t.Fatalf("missing Claude snapshot = %+v", got)
	}
}

func TestProbeClaudeUsageReadsCredentialFreeCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	if err := os.WriteFile(path, []byte(`{"observed_at":"2099-01-01T00:00:00Z","rate_limits":{"seven_day":{"used_percentage":12}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeUsageSnapshotEnv, path)
	got := ProbeProviderUsage(t.Context(), "claude", Command{})
	if got.Status != "available" || got.Source != "official" || len(got.Windows) != 1 {
		t.Fatalf("cached Claude snapshot = %+v", got)
	}
}
