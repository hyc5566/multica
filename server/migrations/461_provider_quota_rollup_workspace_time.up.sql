CREATE INDEX CONCURRENTLY provider_quota_rollup_workspace_time
    ON provider_quota_rollup (workspace_id, granularity, bucket_start DESC);
