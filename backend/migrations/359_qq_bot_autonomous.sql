-- Bounded autonomous chat, isolated by bot and group. No message bodies here.
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS trigger_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS decision_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS model_reserved_at TIMESTAMPTZ;
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS reply_reserved_at TIMESTAMPTZ;
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS reply_sent_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS qq_bot_jobs_autonomous_limits ON qq_bot_jobs(app_id,group_id,reply_reserved_at DESC) WHERE kind='ambient_chat';
CREATE TABLE IF NOT EXISTS qq_bot_autonomous_state (
 app_id TEXT NOT NULL, group_id TEXT NOT NULL, messages BIGINT NOT NULL DEFAULT 0,
 last_message_id TEXT NOT NULL DEFAULT '', last_message_at TIMESTAMPTZ,
 last_text_hash TEXT NOT NULL DEFAULT '', last_candidate_at TIMESTAMPTZ,
 last_reply_at TIMESTAMPTZ, last_reply_member TEXT NOT NULL DEFAULT '',
 last_reply_kind TEXT NOT NULL DEFAULT '', last_reply_trigger TEXT NOT NULL DEFAULT '',
 last_reply_counter BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(app_id,group_id)
);
CREATE TABLE IF NOT EXISTS qq_bot_autonomous_seen (
 app_id TEXT NOT NULL, group_id TEXT NOT NULL, message_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(app_id,group_id,message_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_autonomous_seen_expiry ON qq_bot_autonomous_seen(created_at);
CREATE TABLE IF NOT EXISTS qq_bot_autonomous_decisions (
 app_id TEXT NOT NULL, group_id TEXT NOT NULL, day DATE NOT NULL,
 reason TEXT NOT NULL, count BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(app_id,group_id,day,reason)
);
