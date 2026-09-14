package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func quotaUsage(provider string, value float64) agent.ProviderUsage {
	return agent.ProviderUsage{
		Provider: provider, Status: "available", Source: "official", ObservedAt: time.Now().UTC(),
		Windows: []agent.ProviderUsageWindow{{ID: "five_hour", Label: "5 hour", UsedPercent: &value, Unit: "percent"}},
	}
}

func quotaTestDaemon(probe func(context.Context, string, agent.Command) agent.ProviderUsage) *Daemon {
	return &Daemon{
		taskQuotaCache:    make(map[string]taskQuotaCachedObservation),
		taskQuotaInflight: make(map[string]*taskQuotaProbeCall),
		taskQuotaProbeFn:  probe,
	}
}

func TestObserveTaskQuotaCachesByProviderAccount(t *testing.T) {
	var calls atomic.Int32
	d := quotaTestDaemon(func(ctx context.Context, provider string, _ agent.Command) agent.ProviderUsage {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > taskQuotaProbeTimeout {
			t.Error("task checkpoint must have a short probe deadline")
		}
		calls.Add(1)
		return quotaUsage(provider, 12)
	})
	rt := Runtime{Provider: "codex"}
	first := d.observeTaskQuota(context.Background(), rt, "codex")
	second := d.observeTaskQuota(context.Background(), rt, "codex")
	if first.state != "fresh" || second.state != "cached" || calls.Load() != 1 {
		t.Fatalf("states=(%q,%q), calls=%d", first.state, second.state, calls.Load())
	}
}

func TestObserveTaskQuotaSingleflight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	d := quotaTestDaemon(func(_ context.Context, provider string, _ agent.Command) agent.ProviderUsage {
		calls.Add(1)
		<-release
		return quotaUsage(provider, 25)
	})
	const count = 8
	start := make(chan struct{})
	results := make(chan taskQuotaProbeResult, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- d.observeTaskQuota(context.Background(), Runtime{ProfileID: "shared"}, "claude")
		}()
	}
	close(start)
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(results)
	if calls.Load() != 1 {
		t.Fatalf("probe calls=%d, want 1", calls.Load())
	}
	for result := range results {
		if result.snapshot.Status != "available" {
			t.Fatalf("result status=%q", result.snapshot.Status)
		}
	}
}

func TestObserveTaskQuotaFallsBackToStaleGoodSnapshot(t *testing.T) {
	d := quotaTestDaemon(func(_ context.Context, provider string, _ agent.Command) agent.ProviderUsage {
		return agent.ProviderUsage{Provider: provider, Status: "rate_limited", Source: "unavailable", ObservedAt: time.Now().UTC()}
	})
	key := taskQuotaProbeKey(Runtime{}, "antigravity")
	d.taskQuotaCache[key] = taskQuotaCachedObservation{
		snapshot: quotaUsage("antigravity", 40),
		cachedAt: time.Now().Add(-time.Minute),
	}
	result := d.observeTaskQuota(context.Background(), Runtime{}, "antigravity")
	if result.state != "stale_cache" || result.snapshot.Status != "available" ||
		result.reportSnapshot.Status != "rate_limited" || result.errorCode != "rate_limited" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestObserveTaskQuotaUnsupportedProviderDoesNotProbe(t *testing.T) {
	d := quotaTestDaemon(func(context.Context, string, agent.Command) agent.ProviderUsage {
		t.Fatal("unsupported provider must not be probed")
		return agent.ProviderUsage{}
	})
	result := d.observeTaskQuota(context.Background(), Runtime{}, "kimi")
	if result.state != "unsupported" || result.errorCode != "unsupported" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestScheduledProviderUsageSharesTaskCoordinator(t *testing.T) {
	var calls atomic.Int32
	reported := make(chan agent.ProviderUsage, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProviderUsage agent.ProviderUsage `json:"provider_usage"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode report: %v", err)
		}
		reported <- body.ProviderUsage
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	d := quotaTestDaemon(func(ctx context.Context, provider string, _ agent.Command) agent.ProviderUsage {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 20*time.Second {
			t.Error("scheduled refresh must allow the full provider probe budget")
		}
		calls.Add(1)
		return quotaUsage(provider, 31)
	})
	d.client = NewClient(srv.URL)
	d.logger = slog.Default()
	rt := Runtime{ID: "runtime-1", Provider: "codex"}
	d.handleProviderUsage(context.Background(), rt, "request-1")
	if got := <-reported; got.Status != "available" {
		t.Fatalf("scheduled report status=%q", got.Status)
	}
	result := d.observeTaskQuota(context.Background(), rt, "codex")
	if calls.Load() != 1 || result.state != "cached" {
		t.Fatalf("calls=%d state=%q, want shared cached observation", calls.Load(), result.state)
	}
}

func TestTaskQuotaJoiningLongRefreshKeepsShortDeadlineAndStaleCache(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan taskQuotaProbeResult, 1)
	d := quotaTestDaemon(func(ctx context.Context, provider string, _ agent.Command) agent.ProviderUsage {
		close(started)
		select {
		case <-release:
			return quotaUsage(provider, 35)
		case <-ctx.Done():
			return agent.ProviderUsage{Provider: provider, Status: "error"}
		}
	})
	rt := Runtime{Provider: "antigravity"}
	key := taskQuotaProbeKey(rt, rt.Provider)
	d.taskQuotaCache[key] = taskQuotaCachedObservation{
		snapshot: quotaUsage(rt.Provider, 20), cachedAt: time.Now().Add(-time.Minute),
	}
	go func() {
		finished <- d.observeProviderQuota(context.Background(), rt, rt.Provider)
	}()
	<-started
	// A larger parent deadline catches a task waiter that accidentally inherits
	// the long refresh budget, while bounding the regression's failure time.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := d.observeTaskQuota(ctx, rt, rt.Provider)
	parentErr := ctx.Err()
	close(release)
	refreshed := <-finished
	if parentErr != nil {
		t.Error("task waited for its parent deadline instead of its three-second budget")
	}
	if result.state != "stale_cache" || result.errorCode != "timeout" ||
		result.snapshot.Status != "available" || result.reportSnapshot.Status != "error" {
		t.Fatalf("unexpected task result: %+v", result)
	}
	if refreshed.state != "fresh" || refreshed.snapshot.Status != "available" {
		t.Fatalf("task timeout canceled the independent refresh: %+v", refreshed)
	}
	if cached := d.observeTaskQuota(context.Background(), rt, rt.Provider); cached.state != "cached" ||
		*cached.snapshot.Windows[0].UsedPercent != 35 {
		t.Fatalf("refresh did not update shared cache: %+v", cached)
	}
}

func TestProviderQuotaNativeRenewalGuardsAndActualResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, tc := range []struct {
		name, provider, profile, status, afterStatus                    string
		missingHint, task, missing, emptyPath, nativeFailure, wantRenew bool
	}{
		{name: "expired native credential", wantRenew: true},
		{name: "renewed but provider still rejects", afterStatus: "auth_required", wantRenew: true},
		{name: "native failure", nativeFailure: true, wantRenew: true},
		{name: "task checkpoint", task: true},
		{name: "custom profile", profile: "profile-1"},
		{name: "other provider", provider: "codex"},
		{name: "missing refresh hint", missingHint: true},
		{name: "rate limited", status: "rate_limited"},
		{name: "no registered executable", missing: true},
		{name: "empty registered path", emptyPath: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, status, afterStatus := tc.provider, tc.status, tc.afterStatus
			if provider == "" {
				provider = "antigravity"
			}
			if status == "" {
				status = "auth_required"
			}
			if afterStatus == "" {
				afterStatus = "available"
			}
			marker := filepath.Join(t.TempDir(), "calls")
			t.Setenv("MULTICA_TEST_RENEW_MARKER", marker)
			path := filepath.Join(t.TempDir(), "fake-agy")
			script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = models ] || exit 9\necho call >> \"$MULTICA_TEST_RENEW_MARKER\"\n"
			if tc.nativeFailure {
				script += "exit 1\n"
			}
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			d := quotaTestDaemon(func(_ context.Context, provider string, _ agent.Command) agent.ProviderUsage {
				calls++
				if calls == 1 {
					return agent.ProviderUsage{Provider: provider, Status: status, Source: "unavailable", ObservedAt: time.Now().UTC(), CredentialRefreshNeeded: !tc.missingHint}
				}
				value := quotaUsage(provider, 38)
				value.Status = afterStatus
				return value
			})
			if !tc.missing {
				if tc.emptyPath {
					path = ""
				}
				d.cfg.Agents = map[string]AgentEntry{provider: {Path: path}}
			}
			rt := Runtime{Provider: provider, ProfileID: tc.profile}
			var result taskQuotaProbeResult
			if tc.task {
				result = d.observeTaskQuota(context.Background(), rt, provider)
			} else {
				result = d.observeProviderQuota(context.Background(), rt, provider)
			}
			_, err := os.Stat(marker)
			if (err == nil) != tc.wantRenew {
				t.Fatalf("native ran=%v, want %v", err == nil, tc.wantRenew)
			}
			wantCalls, wantStatus := 1, status
			if tc.wantRenew && !tc.nativeFailure {
				wantCalls, wantStatus = 2, afterStatus
			}
			if calls != wantCalls || result.reportSnapshot.Status != wantStatus {
				t.Fatalf("probes=%d report=%q, want %d %q", calls, result.reportSnapshot.Status, wantCalls, wantStatus)
			}
			_, cached := d.taskQuotaCache[taskQuotaProbeKey(rt, provider)]
			if cached != (wantStatus == "available") {
				t.Fatalf("cached=%v after %q", cached, wantStatus)
			}
		})
	}
}
