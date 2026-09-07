package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	d := quotaTestDaemon(func(_ context.Context, provider string, _ agent.Command) agent.ProviderUsage {
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
	d := quotaTestDaemon(func(_ context.Context, provider string, _ agent.Command) agent.ProviderUsage {
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
