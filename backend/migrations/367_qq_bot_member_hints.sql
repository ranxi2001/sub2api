-- Short-lived display hints let operators identify recent speakers even when
-- QQ omits their nickname. These fields never participate in authorization.
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS member_nickname TEXT NOT NULL DEFAULT '';
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS message_preview TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS qq_bot_jobs_recent_speakers
    ON qq_bot_jobs(app_id, created_at DESC, id DESC)
    WHERE event_type IN ('GROUP_AT_MESSAGE_CREATE','GROUP_MESSAGE_CREATE') AND member_id<>'';
