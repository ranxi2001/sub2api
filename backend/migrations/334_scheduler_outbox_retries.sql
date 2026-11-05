-- Copy failed events before advancing the main watermark. No foreign key to
-- scheduler_outbox: ordinary cleanup must be able to remove the original after
-- its complete, immutable event snapshot has been made durable here.
-- A global generation never repeats if an acknowledged event is copied again
-- before the main watermark commits (delete/reinsert ABA).
CREATE SEQUENCE IF NOT EXISTS scheduler_outbox_retry_lease_generation_seq AS BIGINT;

CREATE TABLE IF NOT EXISTS scheduler_outbox_retries (
    event_id BIGINT PRIMARY KEY,
    event_type TEXT NOT NULL,
    account_id BIGINT,
    group_id BIGINT,
    payload JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '5 seconds',
    lease_generation BIGINT NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_scheduler_outbox_retries_due
    ON scheduler_outbox_retries (next_retry_at, event_id);
