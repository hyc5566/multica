CREATE TABLE task_quota_checkpoint (
    task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    runtime_id UUID NOT NULL,
    observation_id UUID,
    phase TEXT NOT NULL,
    boundary_at TIMESTAMPTZ NOT NULL,
    provider TEXT NOT NULL,
    requested_model TEXT NOT NULL DEFAULT '',
    capture_state TEXT NOT NULL,
    error_code TEXT,
    observation_age_ms BIGINT,
    overlapping_task_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
