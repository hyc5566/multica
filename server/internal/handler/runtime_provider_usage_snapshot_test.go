package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
