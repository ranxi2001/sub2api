CREATE TABLE IF NOT EXISTS priority_lifecycle_inventory_cursor (
    revision TEXT PRIMARY KEY,
    position BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS priority_lifecycle_refresh_jobs (
    job_id UUID PRIMARY KEY,
    identity JSONB NOT NULL,
    statistics_spec TEXT NOT NULL,
    measurement_schema TEXT NOT NULL,
    target_revision BIGINT NOT NULL,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ,
    generation BIGINT
);
CREATE INDEX IF NOT EXISTS priority_lifecycle_refresh_jobs_pending
    ON priority_lifecycle_refresh_jobs(identity) WHERE completed_at IS NULL;
