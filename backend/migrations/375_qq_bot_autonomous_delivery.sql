-- Additive queue and observability metadata; existing policies remain opt-in.
ALTER TABLE qq_bot_jobs
    ADD COLUMN IF NOT EXISTS autonomous_ready_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS autonomous_data JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE qq_bot_autonomous_seen
    ADD COLUMN IF NOT EXISTS member_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS job_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ends_conversation BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS qq_bot_autonomous_member_endings
    ON qq_bot_autonomous_seen(app_id,group_id,member_id,created_at DESC)
    WHERE ends_conversation;
CREATE INDEX IF NOT EXISTS qq_bot_autonomous_daily_metrics
    ON qq_bot_jobs(app_id,created_at) WHERE kind='ambient_chat';
