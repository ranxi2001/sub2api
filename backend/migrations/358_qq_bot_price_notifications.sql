-- Durable baseline and transactional outbox (qq_bot_jobs) for price notices.
CREATE TABLE IF NOT EXISTS qq_bot_price_snapshots (
    app_id TEXT PRIMARY KEY,
    snapshot JSONB NOT NULL,
    next_check_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_jobs_price_dispatch
    ON qq_bot_jobs(app_id, started_at DESC)
    WHERE event_type = 'PRICE_CHANGE_NOTIFICATION';
