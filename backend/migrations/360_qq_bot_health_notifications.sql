CREATE TABLE IF NOT EXISTS qq_bot_health_snapshots (
    app_id TEXT PRIMARY KEY,
    snapshot JSONB NOT NULL,
    next_check_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_health_notification_queue ON qq_bot_jobs(app_id,created_at)
    WHERE event_type='HEALTH_CHANGE_NOTIFICATION' AND status IN ('queued','running');
