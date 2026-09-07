CREATE INDEX CONCURRENTLY task_quota_checkpoint_workspace_time_idx ON task_quota_checkpoint (workspace_id, boundary_at DESC);
