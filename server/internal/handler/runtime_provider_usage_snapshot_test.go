package handler

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProviderUsageTargetForOfflineRuntimeSupportsCachedReads(t *testing.T) {
	runtimeID, workspaceID := pgtype.UUID{}, pgtype.UUID{}
	if err := runtimeID.Scan("11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatal(err)
	}
	if err := workspaceID.Scan("22222222-2222-2222-2222-222222222222"); err != nil {
		t.Fatal(err)
	}
	target, ok := providerUsageTargetForRuntime(db.AgentRuntime{
		ID: runtimeID, WorkspaceID: workspaceID,
		DaemonID:    pgtype.Text{String: "daemon-a", Valid: true},
		RuntimeMode: "local", Provider: "Codex", Status: "offline",
	})
	if !ok {
		t.Fatal("offline local runtime should still resolve its cache identity")
	}
	if target.ProbeTarget != "builtin:daemon-a:codex" {
		t.Fatalf("probe target = %q", target.ProbeTarget)
	}
}

func TestProviderUsageManualRefreshSharesRollingCooldown(t *testing.T) {
	ctx := context.Background()
	h := *testHandler
	h.ModelListStore = NewInMemoryModelListStore()
	now := time.Date(2026, 9, 10, 10, 4, 59, 0, time.UTC)
	target := providerUsageCooldownFixture(t, "rolling", now.Add(-time.Minute))
	// A manual refresh is admitted within the scheduler's existing bucket.
	if _, admitted, err := h.EnqueueProviderUsageTarget(ctx, target, now, now); err != nil || !admitted {
		t.Fatalf("manual refresh within bucket: admitted=%v err=%v", admitted, err)
	}
	// Crossing the five-minute boundary does not bypass the shared cooldown.
	nextBucket := now.Add(time.Second)
	if _, admitted, err := h.EnqueueProviderUsageTarget(ctx, target, nextBucket.Truncate(providerUsageCadence), nextBucket); err != nil || admitted {
		t.Fatalf("scheduler bypassed rolling limit: admitted=%v err=%v", admitted, err)
	}
	snapshot, err := h.readProviderUsageSnapshot(ctx, target, nextBucket)
	if err != nil || snapshot.RefreshAvailableAt == nil || !snapshot.RefreshAvailableAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("missing shared deadline: snapshot=%+v err=%v", snapshot, err)
	}
	// Competing runtime representatives for the same target get one reservation.
	otherRuntimeID := dbfx.Runtime(t, "Shared probe representative")
	var admittedCount atomic.Int32
	var wg sync.WaitGroup
	for index := range 12 {
		wg.Go(func() {
			at := now.Add(time.Minute)
			representative := target
			if index%2 == 0 {
				representative.RuntimeID = otherRuntimeID
			}
			_, admitted, err := h.EnqueueProviderUsageTarget(ctx, representative, at, at)
			if err != nil {
				t.Errorf("concurrent refresh: %v", err)
			}
			if admitted {
				admittedCount.Add(1)
			}
		})
	}
	wg.Wait()
	if got := admittedCount.Load(); got != 1 {
		t.Fatalf("admitted %d concurrent probes, want 1", got)
	}
}

func TestProviderUsageRetryAfterPreservesQuotaAndAdmission(t *testing.T) {
	ctx := context.Background()
	h := *testHandler
	h.ModelListStore = NewInMemoryModelListStore()
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	for _, withGood := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "last-known-good"}[withGood], func(t *testing.T) {
			target := providerUsageCooldownFixture(t, t.Name(), now)
			if withGood {
				used := 42.0
				good := &ProviderUsageSnapshot{Provider: "codex", Status: "available", Source: "official", ObservedAt: now,
					Windows: []ProviderUsageWindow{{ID: "five_hour", Label: "5-hour", UsedPercent: &used, Unit: "percent"}}}
				if err := h.recordProviderUsageResult(ctx, target, good, "completed", now); err != nil {
					t.Fatal(err)
				}
			}
			retry := int64(120)
			limited := &ProviderUsageSnapshot{Provider: "codex", Status: "rate_limited", Source: "unavailable", ObservedAt: now, RetryAfterSeconds: &retry}
			if err := h.recordProviderUsageResult(ctx, target, limited, "completed", now); err != nil {
				t.Fatal(err)
			}
			got, err := h.readProviderUsageSnapshot(ctx, target, now.Add(time.Minute))
			if err != nil || got.RefreshAvailableAt == nil || !got.RefreshAvailableAt.Equal(now.Add(2*time.Minute)) || got.LastErrorCode != "rate_limited" {
				t.Fatalf("retry deadline not retained: got=%+v err=%v", got, err)
			}
			if withGood && (got.Status != "available" || len(got.Windows) != 1 || *got.Windows[0].UsedPercent != 42) {
				t.Fatalf("rate limit replaced quota: %+v", got)
			}
			if !withGood && got.Status != "rate_limited" {
				t.Fatalf("cold rate limit status=%q", got.Status)
			}
			for _, delay := range []time.Duration{time.Minute, 2 * time.Minute} {
				at := now.Add(delay)
				if _, admitted, err := h.EnqueueProviderUsageTarget(ctx, target, at, at); err != nil || admitted != (delay == 2*time.Minute) {
					t.Fatalf("retry admission at %s: admitted=%v err=%v", delay, admitted, err)
				}
			}
		})
	}
}

func providerUsageCooldownFixture(t *testing.T, name string, lastAttempt time.Time) ProviderUsageTarget {
	t.Helper()
	target := ProviderUsageTarget{ProbeTarget: "builtin:cooldown-" + name + ":codex", RuntimeID: testRuntimeID,
		WorkspaceID: testWorkspaceID, DaemonID: "cooldown-" + name, Provider: "codex"}
	dbfx.InsertNoID(t, "runtime_provider_usage_snapshot", testutil.Cols{
		"probe_target": target.ProbeTarget, "runtime_id": target.RuntimeID, "workspace_id": target.WorkspaceID,
		"daemon_id": target.DaemonID, "provider": target.Provider, "last_attempt_at": lastAttempt,
	}, "probe_target = $1", target.ProbeTarget)
	return target
}

func TestInitiateProviderUsageRefreshesAfterCooldown(t *testing.T) {
	h := *testHandler
	h.ModelListStore = NewInMemoryModelListStore()
	// Exercise the HTTP contract after a one-minute cooldown; the fixed-clock
	// reservation test above covers admission within a scheduled bucket.
	now := time.Now().UTC()
	target := providerUsageCooldownFixture(t, "manual-endpoint", now.Add(-61*time.Second))
	runtimeID := dbfx.Runtime(t, "Manual usage refresh", testutil.Cols{
		"daemon_id": target.DaemonID, "provider": "codex", "runtime_mode": "local",
	})
	request := func() *http.Request {
		return withURLParam(newRequest(http.MethodPost, "/api/runtimes/"+runtimeID+"/provider-usage", nil), "runtimeId", runtimeID)
	}
	var first, second ModelListRequest
	testutil.Call(t, h.InitiateProviderUsage, request()).Want(http.StatusOK).JSON(&first)
	if first.Status != ModelListPending {
		t.Fatalf("manual refresh did not enqueue: %+v", first)
	}
	testutil.Call(t, h.InitiateProviderUsage, request()).Want(http.StatusOK).JSON(&second)
	if second.Status != ModelListCompleted || second.ProviderUsage == nil || second.ProviderUsage.RefreshAvailableAt == nil {
		t.Fatalf("limited refresh missing retained snapshot/deadline: %+v", second)
	}
}

func TestProviderUsageResultPreservesLastKnownGood(t *testing.T) {
	ctx := context.Background()
	target := ProviderUsageTarget{
		ProbeTarget: "builtin:test-last-known-good:codex",
		RuntimeID:   testRuntimeID, WorkspaceID: testWorkspaceID,
		DaemonID: "test-last-known-good", Provider: "codex",
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM runtime_provider_usage_snapshot WHERE probe_target = $1`, target.ProbeTarget)
	})

	used, remaining := 40.0, 60.0
	observed := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	good := &ProviderUsageSnapshot{
		Provider: "codex", Status: "available", Source: "official", ObservedAt: observed,
		Windows: []ProviderUsageWindow{{
			ID: "five_hour", Label: "5-hour", UsedPercent: &used,
			RemainingPercent: &remaining, Unit: "percent",
		}},
	}
	if err := testHandler.recordProviderUsageResult(ctx, target, good, "completed", observed); err != nil {
		t.Fatalf("store good snapshot: %v", err)
	}
	if err := testHandler.recordProviderUsageResult(ctx, target, nil, "failed", observed.Add(time.Minute)); err != nil {
		t.Fatalf("store failed attempt: %v", err)
	}

	got, err := testHandler.readProviderUsageSnapshot(ctx, target, observed.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if got.Status != "available" || len(got.Windows) != 1 || got.LastErrorCode != "probe_failed" {
		t.Fatalf("last-known-good not preserved: %+v", got)
	}
	stale, err := testHandler.readProviderUsageSnapshot(ctx, target, observed.Add(16*time.Minute))
	if err != nil {
		t.Fatalf("read stale snapshot: %v", err)
	}
	if !stale.Stale || stale.Status != "partial" {
		t.Fatalf("old provider observation should be stale/partial: %+v", stale)
	}
}

func TestProviderUsageEnqueueDeduplicatesFiveMinuteBucket(t *testing.T) {
	ctx := context.Background()
	target := ProviderUsageTarget{
		ProbeTarget: "builtin:test-dedupe:codex",
		RuntimeID:   testRuntimeID, WorkspaceID: testWorkspaceID,
		DaemonID: "test-dedupe", Provider: "codex",
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM runtime_provider_usage_snapshot WHERE probe_target = $1`, target.ProbeTarget)
	})
	original := testHandler.ModelListStore
	testHandler.ModelListStore = NewInMemoryModelListStore()
	t.Cleanup(func() { testHandler.ModelListStore = original })

	now := time.Date(2026, 9, 5, 10, 2, 0, 0, time.UTC)
	if _, admitted, err := testHandler.EnqueueProviderUsageTarget(ctx, target, now.Truncate(providerUsageCadence), now); err != nil || !admitted {
		t.Fatalf("first enqueue: admitted=%v err=%v", admitted, err)
	}
	if _, admitted, err := testHandler.EnqueueProviderUsageTarget(ctx, target, now.Truncate(providerUsageCadence), now.Add(time.Second)); err != nil || admitted {
		t.Fatalf("duplicate enqueue: admitted=%v err=%v", admitted, err)
	}
}
