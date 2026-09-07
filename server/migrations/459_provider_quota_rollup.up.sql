CREATE TABLE provider_quota_rollup (
    workspace_id UUID NOT NULL,
    probe_target TEXT NOT NULL,
    provider TEXT NOT NULL,
    window_id TEXT NOT NULL,
    granularity TEXT NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    sample_count BIGINT NOT NULL,
    success_count BIGINT NOT NULL,
    failure_count BIGINT NOT NULL,
    min_used_percent DOUBLE PRECISION,
    max_used_percent DOUBLE PRECISION,
    avg_used_percent DOUBLE PRECISION,
    last_used_percent DOUBLE PRECISION,
    reset_crossing_count BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
