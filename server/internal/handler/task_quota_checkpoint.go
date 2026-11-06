package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var taskQuotaCaptureStates = map[string]struct{}{
	"fresh": {}, "cached": {}, "stale_cache": {}, "unsupported": {},
	"timeout": {}, "auth_required": {}, "rate_limited": {},
	"provider_error": {}, "no_snapshot": {},
}

var taskQuotaErrorCodes = map[string]struct{}{
	"": {}, "unsupported": {}, "timeout": {}, "auth_required": {},
	"rate_limited": {}, "provider_error": {}, "schema_mismatch": {},
	"report_failed": {},
}

// TaskQuotaCheckpointData is one boundary observation attached to a run.
// Snapshot is normalized provider usage, never a raw provider response.
type TaskQuotaCheckpointData struct {
	TaskID               string                 `json:"task_id,omitempty"`
	Phase                string                 `json:"phase"`
	BoundaryAt           time.Time              `json:"boundary_at"`
	Provider             string                 `json:"provider"`
	RequestedModel       string                 `json:"requested_model,omitempty"`
	CaptureState         string                 `json:"capture_state"`
	ErrorCode            string                 `json:"error_code,omitempty"`
	ObservationAgeMs     *int64                 `json:"observation_age_ms,omitempty"`
	OverlappingTaskCount int32                  `json:"overlapping_task_count"`
	ObservationID        string                 `json:"observation_id,omitempty"`
	Snapshot             *ProviderUsageSnapshot `json:"snapshot,omitempty"`
}

type quotaObservationQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func storeProviderQuotaObservation(ctx context.Context, q quotaObservationQueryer, target ProviderUsageTarget, snapshot *ProviderUsageSnapshot, receivedAt time.Time) (pgtype.UUID, error) {
	if err := validateProviderUsageSnapshot(snapshot, target.Provider); err != nil {
		return pgtype.UUID{}, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return pgtype.UUID{}, err
	}
	var id pgtype.UUID
	err = q.QueryRow(ctx, `
		INSERT INTO provider_quota_observation (
			probe_target, runtime_id, workspace_id, daemon_id, provider,
			status, source, snapshot, observed_at, received_at
		) VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::jsonb, $9, $10)
		ON CONFLICT (probe_target, workspace_id, observed_at) DO UPDATE SET
			received_at = LEAST(provider_quota_observation.received_at, EXCLUDED.received_at)
		RETURNING id
	`, target.ProbeTarget, target.RuntimeID, target.WorkspaceID, target.DaemonID,
		target.Provider, snapshot.Status, snapshot.Source, encoded, snapshot.ObservedAt.UTC(), receivedAt.UTC()).Scan(&id)
	return id, err
}

// ReportTaskQuotaCheckpoint records a fail-open daemon observation at a run
// boundary. It is intentionally separate from terminal and token reports: a
// provider quota outage must never change the task state machine.
func (h *Handler) ReportTaskQuotaCheckpoint(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(chi.URLParam(r, "taskId"))
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}
	var req struct {
		Phase          string                 `json:"phase"`
		BoundaryAt     time.Time              `json:"boundary_at"`
		RequestedModel string                 `json:"requested_model"`
		CaptureState   string                 `json:"capture_state"`
		ErrorCode      string                 `json:"error_code"`
		Snapshot       *ProviderUsageSnapshot `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Phase != "before" && req.Phase != "after" {
		writeError(w, http.StatusBadRequest, "invalid quota checkpoint phase")
		return
	}
	if req.BoundaryAt.IsZero() {
		writeError(w, http.StatusBadRequest, "missing quota checkpoint boundary")
		return
	}
	if _, ok := taskQuotaCaptureStates[req.CaptureState]; !ok {
		writeError(w, http.StatusBadRequest, "invalid quota checkpoint state")
		return
	}
	if _, ok := taskQuotaErrorCodes[req.ErrorCode]; !ok {
		writeError(w, http.StatusBadRequest, "invalid quota checkpoint error code")
		return
	}
	rt, err := h.getAgentRuntime(r.Context(), "task_quota_checkpoint", task.RuntimeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(rt.Provider))
	if req.Snapshot != nil {
		if err := validateProviderUsageSnapshot(req.Snapshot, provider); err != nil {
			writeError(w, http.StatusBadRequest, "invalid quota checkpoint snapshot")
			return
		}
	}
	profileID := (*string)(nil)
	if rt.ProfileID.Valid {
		value := uuidToString(rt.ProfileID)
		profileID = &value
	}
	target := ProviderUsageTarget{
		ProbeTarget: providerUsageProbeTarget(rt.DaemonID.String, provider, profileID),
		RuntimeID:   uuidToString(rt.ID), WorkspaceID: workspaceID,
		DaemonID: rt.DaemonID.String, Provider: provider, ProfileID: profileID,
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store quota checkpoint")
		return
	}
	defer tx.Rollback(r.Context())
	var observationID pgtype.UUID
	var observationAge any
	if req.Snapshot != nil {
		observationID, err = storeProviderQuotaObservation(r.Context(), tx, target, req.Snapshot, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store quota observation")
			return
		}
		age := req.BoundaryAt.Sub(req.Snapshot.ObservedAt).Milliseconds()
		if age < 0 {
			age = 0
		}
		observationAge = age
	}
	var overlap int32
	if err := tx.QueryRow(r.Context(), `
		SELECT COUNT(*)::integer
		  FROM agent_task_queue q
		  JOIN agent_runtime ar ON ar.id = q.runtime_id
		 WHERE q.id <> $1::uuid
		   AND q.status IN ('dispatched', 'waiting_local_directory', 'running')
		   AND CASE
		         WHEN ar.profile_id IS NOT NULL THEN 'profile:' || ar.daemon_id || ':' || ar.profile_id::text
		         ELSE 'builtin:' || ar.daemon_id || ':' || lower(ar.provider)
		       END = $2
	`, taskID, target.ProbeTarget).Scan(&overlap); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count overlapping tasks")
		return
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO task_quota_checkpoint (
			task_id, workspace_id, runtime_id, observation_id, phase, boundary_at,
			provider, requested_model, capture_state, error_code,
			observation_age_ms, overlapping_task_count
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11, $12)
		ON CONFLICT (task_id, phase) DO NOTHING
	`, taskID, workspaceID, task.RuntimeID, observationID, req.Phase, req.BoundaryAt.UTC(),
		provider, strings.TrimSpace(req.RequestedModel), req.CaptureState, req.ErrorCode,
		observationAge, overlap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store quota checkpoint")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store quota checkpoint")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// hydrateTaskQuotaCheckpoints adds quota display metadata to issue execution
// rows with one bounded query. Failure leaves the execution log usable.
func (h *Handler) hydrateTaskQuotaCheckpoints(ctx context.Context, issueID pgtype.UUID, resp []AgentTaskResponse) {
	if len(resp) == 0 || h.DB == nil {
		return
	}
	taskIDs := make([]pgtype.UUID, 0, len(resp))
	for _, task := range resp {
		taskIDs = append(taskIDs, parseUUID(task.ID))
	}
	byTask, err := h.loadTaskQuotaCheckpoints(ctx, taskIDs)
	if err != nil {
		slog.Warn("hydrate task quota checkpoints failed", "issue_id", uuidToString(issueID), "error", err)
		return
	}
	for i := range resp {
		resp[i].QuotaCheckpoints = byTask[resp[i].ID]
	}
}

// loadTaskQuotaCheckpoints is the shared, bounded hydration path for issue and
// Chat projections. Callers authorize the parent resource before passing its
// task ids; an empty or failed result leaves that parent usable.
func (h *Handler) loadTaskQuotaCheckpoints(ctx context.Context, taskIDs []pgtype.UUID) (map[string][]TaskQuotaCheckpointData, error) {
	byTask := make(map[string][]TaskQuotaCheckpointData, len(taskIDs))
	if len(taskIDs) == 0 || h.DB == nil {
		return byTask, nil
	}
	rows, err := h.DB.Query(ctx, `
		SELECT c.task_id, c.phase, c.boundary_at, c.provider, c.requested_model,
		       c.capture_state, c.error_code, c.observation_age_ms,
		       c.overlapping_task_count, c.observation_id, o.snapshot
		  FROM task_quota_checkpoint c
		  LEFT JOIN provider_quota_observation o ON o.id = c.observation_id
		 WHERE c.task_id = ANY($1::uuid[])
		 ORDER BY c.task_id, c.boundary_at
	`, taskIDs)
	if err != nil {
		return byTask, err
	}
	defer rows.Close()
	for rows.Next() {
		var taskUUID pgtype.UUID
		var row TaskQuotaCheckpointData
		var errorCode pgtype.Text
		var age pgtype.Int8
		var observationID pgtype.UUID
		var raw []byte
		if err := rows.Scan(&taskUUID, &row.Phase, &row.BoundaryAt, &row.Provider,
			&row.RequestedModel, &row.CaptureState, &errorCode, &age,
			&row.OverlappingTaskCount, &observationID, &raw); err != nil {
			return byTask, err
		}
		if errorCode.Valid {
			row.ErrorCode = errorCode.String
		}
		if age.Valid {
			value := age.Int64
			row.ObservationAgeMs = &value
		}
		if observationID.Valid {
			row.ObservationID = uuidToString(observationID)
			if len(raw) == 0 {
				row.CaptureState = "expired"
			}
		}
		if len(raw) > 0 {
			var snapshot ProviderUsageSnapshot
			if json.Unmarshal(raw, &snapshot) == nil {
				annotateTaskQuotaWindows(&snapshot, row.Provider, row.RequestedModel)
				selectTaskQuotaWindows(&snapshot, row.Provider, row.RequestedModel)
				row.Snapshot = &snapshot
			}
		}
		key := uuidToString(taskUUID)
		row.TaskID = key
		byTask[key] = append(byTask[key], row)
	}
	return byTask, rows.Err()
}

// MaintainProviderQuotaHistory builds bounded trend data before pruning raw
// observations. Reprocessing recent buckets is intentional and idempotent: a
// late checkpoint can arrive after its observation's original hourly pass.
func (h *Handler) MaintainProviderQuotaHistory(ctx context.Context, now time.Time) (int64, error) {
	if h.DB == nil {
		return 0, errors.New("provider quota history database is unavailable")
	}
	tag, err := h.DB.Exec(ctx, `
		WITH expanded AS (
			SELECT o.workspace_id, o.probe_target, o.provider, o.status, o.observed_at,
			       w->>'id' AS window_id,
			       COALESCE((w->>'used_percent')::double precision,
			                100 - (w->>'remaining_percent')::double precision) AS used_percent,
			       w->>'resets_at' AS resets_at
			  FROM provider_quota_observation o
			 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(o.snapshot->'windows', '[]'::jsonb)) w
			 WHERE o.observed_at >= $1::timestamptz - interval '48 hours'
			UNION ALL
			SELECT o.workspace_id, o.probe_target, o.provider, o.status, o.observed_at,
			       '__probe__' AS window_id, NULL::double precision AS used_percent,
			       NULL::text AS resets_at
			  FROM provider_quota_observation o
			 WHERE o.observed_at >= $1::timestamptz - interval '48 hours'
		), sequenced AS (
			SELECT *, lag(resets_at) OVER (
				PARTITION BY workspace_id, probe_target, window_id ORDER BY observed_at
			) AS previous_reset
			FROM expanded
		), hourly AS (
			SELECT workspace_id, probe_target, provider, window_id,
			       date_trunc('hour', observed_at) AS bucket_start,
			       count(*)::bigint AS sample_count,
			       count(*) FILTER (WHERE status IN ('available', 'partial'))::bigint AS success_count,
			       count(*) FILTER (WHERE status NOT IN ('available', 'partial'))::bigint AS failure_count,
			       min(used_percent) AS min_used_percent,
			       max(used_percent) AS max_used_percent,
			       avg(used_percent) AS avg_used_percent,
			       (array_agg(used_percent ORDER BY observed_at DESC))[1] AS last_used_percent,
			       count(*) FILTER (WHERE previous_reset IS NOT NULL AND resets_at IS DISTINCT FROM previous_reset)::bigint AS reset_crossing_count
			  FROM sequenced
			 GROUP BY workspace_id, probe_target, provider, window_id, date_trunc('hour', observed_at)
		)
		INSERT INTO provider_quota_rollup (
			workspace_id, probe_target, provider, window_id, granularity, bucket_start,
			sample_count, success_count, failure_count, min_used_percent,
			max_used_percent, avg_used_percent, last_used_percent, reset_crossing_count, updated_at
		)
		SELECT workspace_id, probe_target, provider, window_id, 'hour', bucket_start,
		       sample_count, success_count, failure_count, min_used_percent,
		       max_used_percent, avg_used_percent, last_used_percent, reset_crossing_count, $1
		  FROM hourly
		ON CONFLICT (workspace_id, probe_target, window_id, granularity, bucket_start) DO UPDATE SET
			sample_count = EXCLUDED.sample_count,
			success_count = EXCLUDED.success_count,
			failure_count = EXCLUDED.failure_count,
			min_used_percent = EXCLUDED.min_used_percent,
			max_used_percent = EXCLUDED.max_used_percent,
			avg_used_percent = EXCLUDED.avg_used_percent,
			last_used_percent = EXCLUDED.last_used_percent,
			reset_crossing_count = EXCLUDED.reset_crossing_count,
			updated_at = EXCLUDED.updated_at;

		WITH daily AS (
			SELECT workspace_id, probe_target, provider, window_id,
			       date_trunc('day', bucket_start) AS bucket_start,
			       sum(sample_count)::bigint AS sample_count,
			       sum(success_count)::bigint AS success_count,
			       sum(failure_count)::bigint AS failure_count,
			       min(min_used_percent) AS min_used_percent,
			       max(max_used_percent) AS max_used_percent,
			       sum(avg_used_percent * sample_count) / NULLIF(sum(sample_count), 0) AS avg_used_percent,
			       (array_agg(last_used_percent ORDER BY bucket_start DESC))[1] AS last_used_percent,
			       sum(reset_crossing_count)::bigint AS reset_crossing_count
			  FROM provider_quota_rollup
			 WHERE granularity = 'hour' AND bucket_start >= $1::timestamptz - interval '32 days'
			 GROUP BY workspace_id, probe_target, provider, window_id, date_trunc('day', bucket_start)
		)
		INSERT INTO provider_quota_rollup (
			workspace_id, probe_target, provider, window_id, granularity, bucket_start,
			sample_count, success_count, failure_count, min_used_percent,
			max_used_percent, avg_used_percent, last_used_percent, reset_crossing_count, updated_at
		)
		SELECT workspace_id, probe_target, provider, window_id, 'day', bucket_start,
		       sample_count, success_count, failure_count, min_used_percent,
		       max_used_percent, avg_used_percent, last_used_percent, reset_crossing_count, $1
		  FROM daily
		ON CONFLICT (workspace_id, probe_target, window_id, granularity, bucket_start) DO UPDATE SET
			sample_count = EXCLUDED.sample_count,
			success_count = EXCLUDED.success_count,
			failure_count = EXCLUDED.failure_count,
			min_used_percent = EXCLUDED.min_used_percent,
			max_used_percent = EXCLUDED.max_used_percent,
			avg_used_percent = EXCLUDED.avg_used_percent,
			last_used_percent = EXCLUDED.last_used_percent,
			reset_crossing_count = EXCLUDED.reset_crossing_count,
			updated_at = EXCLUDED.updated_at;

		DELETE FROM provider_quota_observation WHERE observed_at < $1::timestamptz - interval '180 days';
		DELETE FROM task_quota_checkpoint WHERE boundary_at < $1::timestamptz - interval '25 months';
		DELETE FROM provider_quota_rollup
		 WHERE (granularity = 'hour' AND bucket_start < $1::timestamptz - interval '13 months')
		    OR (granularity = 'day' AND bucket_start < $1::timestamptz - interval '25 months');
	`, pgx.QueryExecModeSimpleProtocol, now.UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Keep the shared observation intact; project only the run's recorded model
// into task/chat responses (including historical checkpoints and exports).
func selectTaskQuotaWindows(snapshot *ProviderUsageSnapshot, provider, requestedModel string) {
	if !strings.EqualFold(strings.TrimSpace(provider), "antigravity") {
		return
	}
	model := strings.TrimSpace(requestedModel)
	// Quota-summary buckets are provider pools, not model IDs. Keep both
	// advertised windows without attributing the pool's usage to this run.
	pool := antigravityTaskQuotaPool(model)
	selected := []ProviderUsageWindow{}
	for _, w := range snapshot.Windows {
		if pool != "" && (w.ID == pool+"-5h" || w.ID == pool+"-weekly") {
			w.Scope, w.ModelMatch = "provider", "shared"
			selected = append(selected, w)
		}
	}
	if len(selected) > 0 {
		snapshot.Windows = selected
		return
	}
	for _, w := range snapshot.Windows {
		if model != "" && w.ID == model {
			w.Label = w.Group
			if w.Label == "" {
				w.Label = w.ID
			}
			snapshot.Windows = []ProviderUsageWindow{w}
			return
		}
	}
	// Newer Gemini effort variants share a provider-advertised tiered bucket.
	// Never use equal percentages or fuzzy display names as model identity.
	if strings.HasPrefix(model, "gemini-") {
		for _, effort := range []string{"-high", "-medium", "-low", "-minimal"} {
			if !strings.HasSuffix(model, effort) {
				continue
			}
			bucket := strings.TrimSuffix(model, effort) + "-tiered"
			for _, w := range snapshot.Windows {
				if w.ID == bucket {
					w.Label, w.Scope, w.ModelMatch = model, "model", "shared"
					snapshot.Windows = []ProviderUsageWindow{w}
					return
				}
			}
		}
	}
	// Unknown/default model is not permission to display every account window.
	snapshot.Windows = []ProviderUsageWindow{}
}

func antigravityTaskQuotaPool(model string) string {
	switch {
	case strings.HasPrefix(model, "gemini-"):
		return "gemini"
	case strings.HasPrefix(model, "claude-"), strings.HasPrefix(model, "gpt-"):
		return "3p"
	default:
		return ""
	}
}

func annotateTaskQuotaWindows(snapshot *ProviderUsageSnapshot, provider, requestedModel string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	requestedModel = strings.TrimSpace(requestedModel)
	for i := range snapshot.Windows {
		window := &snapshot.Windows[i]
		switch provider {
		case "claude":
			window.Scope, window.ModelMatch = "account", "shared"
		case "antigravity":
			switch window.ID {
			case "gemini-5h", "gemini-weekly", "3p-5h", "3p-weekly":
				window.Scope, window.ModelMatch = "provider", "unknown"
				if pool := antigravityTaskQuotaPool(requestedModel); pool != "" && strings.HasPrefix(window.ID, pool+"-") {
					window.ModelMatch = "shared"
				}
				continue
			}
			window.Scope = "model"
			if requestedModel != "" && window.ID == requestedModel {
				window.ModelMatch = "exact"
			} else {
				window.ModelMatch = "unknown"
			}
		case "codex":
			if strings.HasPrefix(window.ID, "model-") {
				window.Scope, window.ModelMatch = "model", "unknown"
			} else {
				window.Scope, window.ModelMatch = "provider", "shared"
			}
		default:
			window.Scope, window.ModelMatch = "provider", "unknown"
		}
	}
}
