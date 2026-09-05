CREATE TABLE runtime_provider_usage_snapshot (
    probe_target TEXT NOT NULL,
    runtime_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    daemon_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    profile_id UUID,
    snapshot JSONB,
    last_attempt_at TIMESTAMPTZ NOT NULL,
    last_success_at TIMESTAMPTZ,
    last_error_code TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
