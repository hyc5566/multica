CREATE TABLE provider_quota_observation (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    probe_target TEXT NOT NULL,
    runtime_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    daemon_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    status TEXT NOT NULL,
    source TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
