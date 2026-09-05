package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	providerUsageCadence   = 5 * time.Minute
	providerUsageFreshness = 15 * time.Minute
)

// ProviderUsageTarget is the account-level probe identity. Built-in runtimes
// share one target per daemon/provider even when the daemon joins multiple
// workspaces. A custom profile keeps its own target because fixed arguments or
// environment may select another provider account.
type ProviderUsageTarget struct {
	ProbeTarget string
	RuntimeID   string
	WorkspaceID string
	DaemonID    string
	Provider    string
	ProfileID   *string
}

func providerUsageSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "claude", "antigravity":
		return true
	default:
		return false
	}
}

func providerUsageProbeTarget(daemonID, provider string, profileID *string) string {
	if profileID != nil && strings.TrimSpace(*profileID) != "" {
		return "profile:" + daemonID + ":" + strings.TrimSpace(*profileID)
	}
	return "builtin:" + daemonID + ":" + strings.ToLower(strings.TrimSpace(provider))
}

func providerUsageTargetForRuntime(rt db.AgentRuntime) (ProviderUsageTarget, bool) {
	if rt.RuntimeMode != "local" || !rt.DaemonID.Valid ||
		strings.TrimSpace(rt.DaemonID.String) == "" || !providerUsageSupported(rt.Provider) {
		return ProviderUsageTarget{}, false
	}
	var profileID *string
	if rt.ProfileID.Valid {
		value := uuidToString(rt.ProfileID)
		profileID = &value
	}
	return ProviderUsageTarget{
		ProbeTarget: providerUsageProbeTarget(rt.DaemonID.String, rt.Provider, profileID),
		RuntimeID:   uuidToString(rt.ID),
		WorkspaceID: uuidToString(rt.WorkspaceID),
		DaemonID:    rt.DaemonID.String,
		Provider:    strings.ToLower(strings.TrimSpace(rt.Provider)),
		ProfileID:   profileID,
	}, true
}

// ListProviderUsageTargets returns one currently-online representative for
// each account-level target. It is intentionally a narrow handwritten query so
// this Taiwan-only feature stays outside upstream sqlc-generated files.
func (h *Handler) ListProviderUsageTargets(ctx context.Context) ([]ProviderUsageTarget, error) {
	if h.DB == nil {
		return nil, errors.New("provider usage snapshot database is unavailable")
	}
	rows, err := h.DB.Query(ctx, `
		SELECT DISTINCT ON (probe_target)
		       probe_target, runtime_id, workspace_id, daemon_id, provider, profile_id
		  FROM (
			SELECT CASE
			         WHEN profile_id IS NULL THEN 'builtin:' || daemon_id || ':' || lower(provider)
			         ELSE 'profile:' || daemon_id || ':' || profile_id::text
			       END AS probe_target,
			       id::text AS runtime_id,
			       workspace_id::text AS workspace_id,
			       daemon_id,
			       lower(provider) AS provider,
			       profile_id::text AS profile_id,
			       last_seen_at
			  FROM agent_runtime
			 WHERE status = 'online'
			   AND runtime_mode = 'local'
			   AND daemon_id IS NOT NULL
			   AND lower(provider) IN ('codex', 'claude', 'antigravity')
		  ) eligible
		 ORDER BY probe_target, last_seen_at DESC NULLS LAST, runtime_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list provider usage targets: %w", err)
	}
	defer rows.Close()

	var targets []ProviderUsageTarget
	for rows.Next() {
		var target ProviderUsageTarget
		var profileID pgtype.Text
		if err := rows.Scan(&target.ProbeTarget, &target.RuntimeID, &target.WorkspaceID,
			&target.DaemonID, &target.Provider, &profileID); err != nil {
			return nil, fmt.Errorf("scan provider usage target: %w", err)
		}
		if profileID.Valid {
			target.ProfileID = &profileID.String
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider usage targets: %w", err)
	}
	return targets, nil
}

func (h *Handler) ProviderUsageTargetByKey(ctx context.Context, key string) (ProviderUsageTarget, error) {
	targets, err := h.ListProviderUsageTargets(ctx)
	if err != nil {
		return ProviderUsageTarget{}, err
	}
	for _, target := range targets {
		if target.ProbeTarget == key {
			return target, nil
		}
	}
	return ProviderUsageTarget{}, pgx.ErrNoRows
}

// EnqueueProviderUsageTarget atomically reserves the five-minute bucket before
// adding daemon work. The database reservation is the cross-replica and
// reconnect/manual-refresh idempotency gate.
func (h *Handler) EnqueueProviderUsageTarget(ctx context.Context, target ProviderUsageTarget, bucket, now time.Time) (*ModelListRequest, bool, error) {
	if h.DB == nil || h.ModelListStore == nil {
		return nil, false, errors.New("provider usage refresh is unavailable")
	}
	var profileID any
	if target.ProfileID != nil {
		profileID = *target.ProfileID
	}
	var admitted bool
	err := h.DB.QueryRow(ctx, `
		INSERT INTO runtime_provider_usage_snapshot (
			probe_target, runtime_id, workspace_id, daemon_id, provider, profile_id,
			last_attempt_at, last_error_code, updated_at
		) VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6::uuid, $7, 'pending', $7)
		ON CONFLICT (probe_target) DO UPDATE SET
			runtime_id = EXCLUDED.runtime_id,
			workspace_id = EXCLUDED.workspace_id,
			daemon_id = EXCLUDED.daemon_id,
			provider = EXCLUDED.provider,
			profile_id = EXCLUDED.profile_id,
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_error_code = 'pending',
			updated_at = EXCLUDED.updated_at
		WHERE runtime_provider_usage_snapshot.last_attempt_at < $8
		RETURNING true
	`, target.ProbeTarget, target.RuntimeID, target.WorkspaceID, target.DaemonID,
		target.Provider, profileID, now.UTC(), bucket.UTC()).Scan(&admitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reserve provider usage refresh: %w", err)
	}
	req, err := h.ModelListStore.Create(ctx, target.RuntimeID, "provider_usage")
	if err != nil {
		_ = h.recordProviderUsageDispatchFailure(ctx, target, "enqueue_failed", now)
		return nil, false, fmt.Errorf("enqueue provider usage request: %w", err)
	}
	h.requestDaemonPendingWork(target.RuntimeID, protocol.PendingWorkKindProviderUsage)
	return req, true, nil
}

func (h *Handler) EnqueueProviderUsageRuntime(ctx context.Context, rt db.AgentRuntime, now time.Time) (*ModelListRequest, bool, error) {
	if rt.Status != "online" {
		return nil, false, nil
	}
	target, ok := providerUsageTargetForRuntime(rt)
	if !ok {
		return nil, false, nil
	}
	return h.EnqueueProviderUsageTarget(ctx, target, now.UTC().Truncate(providerUsageCadence), now)
}

func (h *Handler) recordProviderUsageDispatchFailure(ctx context.Context, target ProviderUsageTarget, code string, now time.Time) error {
	_, err := h.DB.Exec(ctx, `
		UPDATE runtime_provider_usage_snapshot
		   SET last_error_code = $2, updated_at = $3
		 WHERE probe_target = $1
	`, target.ProbeTarget, code, now.UTC())
	return err
}

func validateProviderUsageSnapshot(snapshot *ProviderUsageSnapshot, provider string) error {
	if snapshot == nil {
		return errors.New("provider usage snapshot is missing")
	}
	if strings.ToLower(strings.TrimSpace(snapshot.Provider)) != strings.ToLower(strings.TrimSpace(provider)) {
		return errors.New("provider usage snapshot provider mismatch")
	}
	switch snapshot.Status {
	case "available", "partial", "unavailable", "auth_required", "rate_limited", "error":
	default:
		return errors.New("provider usage snapshot status is invalid")
	}
	switch snapshot.Source {
	case "official", "derived", "unavailable":
	default:
		return errors.New("provider usage snapshot source is invalid")
	}
	if snapshot.ObservedAt.IsZero() {
		return errors.New("provider usage snapshot observation time is missing")
	}
	for _, window := range snapshot.Windows {
		if strings.TrimSpace(window.ID) == "" || strings.TrimSpace(window.Label) == "" || window.Unit != "percent" {
			return errors.New("provider usage snapshot window is invalid")
		}
		if window.WindowDurationMins != nil && *window.WindowDurationMins <= 0 {
			return errors.New("provider usage snapshot duration is invalid")
		}
		for _, percent := range []*float64{window.UsedPercent, window.RemainingPercent} {
			if percent != nil && (*percent < 0 || *percent > 100 || math.IsNaN(*percent) || math.IsInf(*percent, 0)) {
				return errors.New("provider usage snapshot percentage is invalid")
			}
		}
	}
	return nil
}

func (h *Handler) recordProviderUsageResult(ctx context.Context, target ProviderUsageTarget, snapshot *ProviderUsageSnapshot, reportStatus string, now time.Time) error {
	good := snapshot != nil && (snapshot.Status == "available" || snapshot.Status == "partial")
	var raw any
	var lastSuccess any
	errorCode := "probe_failed"
	if snapshot != nil {
		errorCode = snapshot.Status
	}
	if good {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return fmt.Errorf("encode provider usage snapshot: %w", err)
		}
		raw = encoded
		lastSuccess = now.UTC()
		errorCode = ""
	}
	if reportStatus != "completed" {
		errorCode = "probe_failed"
	}
	var profileID any
	if target.ProfileID != nil {
		profileID = *target.ProfileID
	}
	_, err := h.DB.Exec(ctx, `
		INSERT INTO runtime_provider_usage_snapshot (
			probe_target, runtime_id, workspace_id, daemon_id, provider, profile_id,
			snapshot, last_attempt_at, last_success_at, last_error_code, updated_at
		) VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6::uuid, $7::jsonb, $8, $9, NULLIF($10, ''), $8)
		ON CONFLICT (probe_target) DO UPDATE SET
			runtime_id = EXCLUDED.runtime_id,
			workspace_id = EXCLUDED.workspace_id,
			daemon_id = EXCLUDED.daemon_id,
			provider = EXCLUDED.provider,
			profile_id = EXCLUDED.profile_id,
			snapshot = CASE WHEN $11 THEN EXCLUDED.snapshot ELSE runtime_provider_usage_snapshot.snapshot END,
			last_success_at = CASE WHEN $11 THEN EXCLUDED.last_success_at ELSE runtime_provider_usage_snapshot.last_success_at END,
			last_error_code = EXCLUDED.last_error_code,
			updated_at = EXCLUDED.updated_at
	`, target.ProbeTarget, target.RuntimeID, target.WorkspaceID, target.DaemonID,
		target.Provider, profileID, raw, now.UTC(), lastSuccess, errorCode, good)
	if err != nil {
		return fmt.Errorf("store provider usage result: %w", err)
	}
	return nil
}

func (h *Handler) readProviderUsageSnapshot(ctx context.Context, target ProviderUsageTarget, now time.Time) (*ProviderUsageSnapshot, error) {
	var raw []byte
	var lastAttempt time.Time
	var lastSuccess pgtype.Timestamptz
	var lastError pgtype.Text
	err := h.DB.QueryRow(ctx, `
		SELECT snapshot, last_attempt_at, last_success_at, last_error_code
		  FROM runtime_provider_usage_snapshot
		 WHERE probe_target = $1
	`, target.ProbeTarget).Scan(&raw, &lastAttempt, &lastSuccess, &lastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return &ProviderUsageSnapshot{
			Provider: target.Provider, Status: "unavailable", Source: "unavailable",
			ObservedAt: now.UTC(), Message: "No provider usage snapshot has been collected yet.",
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read provider usage snapshot: %w", err)
	}
	var snapshot ProviderUsageSnapshot
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return nil, fmt.Errorf("decode provider usage snapshot: %w", err)
		}
		// A custom profile keeps the same target when its provider changes.
		// Never expose the previous provider's quota under the new identity;
		// the next successful probe will replace it.
		if !strings.EqualFold(snapshot.Provider, target.Provider) {
			raw = nil
			snapshot = ProviderUsageSnapshot{
				Provider: target.Provider, Status: "unavailable", Source: "unavailable",
				ObservedAt: lastAttempt.UTC(), Message: "Provider usage refresh has not completed successfully for this provider yet.",
			}
		}
	} else {
		snapshot = ProviderUsageSnapshot{
			Provider: target.Provider, Status: "unavailable", Source: "unavailable",
			ObservedAt: lastAttempt.UTC(), Message: "Provider usage refresh has not completed successfully yet.",
		}
	}
	attempt := lastAttempt.UTC()
	snapshot.LastAttemptAt = &attempt
	if lastSuccess.Valid {
		success := lastSuccess.Time.UTC()
		snapshot.LastSuccessAt = &success
		if !snapshot.ObservedAt.IsZero() && now.Sub(snapshot.ObservedAt) > providerUsageFreshness {
			snapshot.Stale = true
			if snapshot.Status == "available" {
				snapshot.Status = "partial"
			}
		}
	}
	if lastError.Valid && lastError.String != "" && lastError.String != "pending" {
		snapshot.LastErrorCode = lastError.String
		if len(raw) == 0 {
			switch lastError.String {
			case "auth_required", "rate_limited", "error":
				snapshot.Status = lastError.String
			default:
				snapshot.Status = "error"
			}
		}
	}
	return &snapshot, nil
}

// GetProviderUsageSnapshot is cache-only. Opening the agent page never queues
// daemon work; scheduled/reconnect refreshes populate this durable record.
func (h *Handler) GetProviderUsageSnapshot(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	target, eligible := providerUsageTargetForRuntime(rt)
	if !eligible {
		writeJSON(w, http.StatusOK, &ProviderUsageSnapshot{
			Provider: rt.Provider, Status: "unavailable", Source: "unavailable",
			ObservedAt: time.Now().UTC(), Message: "This runtime does not support provider usage refresh.",
		})
		return
	}
	snapshot, err := h.readProviderUsageSnapshot(r.Context(), target, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load provider usage snapshot")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
