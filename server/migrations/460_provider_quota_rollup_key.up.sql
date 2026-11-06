CREATE UNIQUE INDEX CONCURRENTLY provider_quota_rollup_key
    ON provider_quota_rollup (workspace_id, probe_target, window_id, granularity, bucket_start);
