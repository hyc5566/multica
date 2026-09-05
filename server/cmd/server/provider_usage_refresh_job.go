package main

import (
	"context"
	"errors"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/scheduler"
)

const (
	providerUsageRefreshJobName = "refresh_runtime_provider_usage"
	providerUsageRefreshCadence = 5 * time.Minute
)

// providerUsageRefreshJob is the small platform adapter around the Taiwan-only
// provider usage coordinator. The shared DB-backed scheduler supplies the
// multi-replica lease; the handler owns target selection, request transport and
// durable last-known-good snapshots.
func providerUsageRefreshJob(h *handler.Handler) scheduler.JobSpec {
	return scheduler.JobSpec{
		Name:              providerUsageRefreshJobName,
		MaxPlansPerTick:   1,
		RunTimeout:        20 * time.Second,
		StaleTimeout:      time.Minute,
		HeartbeatInterval: 10 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       1,
		Scopes: func(ctx context.Context, _ time.Time) ([]scheduler.Scope, error) {
			targets, err := h.ListProviderUsageTargets(ctx)
			if err != nil {
				return nil, err
			}
			scopes := make([]scheduler.Scope, 0, len(targets))
			for _, target := range targets {
				scopes = append(scopes, scheduler.Scope{Kind: "provider_usage", ID: target.ProbeTarget})
			}
			return scopes, nil
		},
		PlansForScope: func(_ context.Context, scope scheduler.Scope, now time.Time, _ scheduler.LatestPlanInfo) ([]time.Time, error) {
			return []time.Time{providerUsagePlanTime(scope.ID, now)}, nil
		},
		Handler: func(ctx context.Context, in scheduler.HandlerInput) (scheduler.HandlerResult, error) {
			target, err := h.ProviderUsageTargetByKey(ctx, in.Scope.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return scheduler.HandlerResult{Result: map[string]any{"status": "offline"}}, nil
			}
			if err != nil {
				return scheduler.HandlerResult{}, err
			}
			_, admitted, err := h.EnqueueProviderUsageTarget(
				ctx, target, in.PlanTime.UTC().Truncate(providerUsageRefreshCadence), time.Now().UTC(),
			)
			if err != nil {
				return scheduler.HandlerResult{}, err
			}
			rows := int64(0)
			status := "deduplicated"
			if admitted {
				rows = 1
				status = "queued"
			}
			return scheduler.HandlerResult{
				RowsAffected: rows,
				Result:       map[string]any{"status": status, "provider": target.Provider},
			}, nil
		},
	}
}

func providerUsagePlanTime(target string, now time.Time) time.Time {
	h := fnv.New32a()
	_, _ = h.Write([]byte(target))
	jitter := time.Duration(h.Sum32()%uint32(providerUsageRefreshCadence/time.Second)) * time.Second
	base := now.UTC().Truncate(providerUsageRefreshCadence)
	planned := base.Add(jitter)
	if planned.After(now.UTC()) {
		planned = planned.Add(-providerUsageRefreshCadence)
	}
	return planned
}
