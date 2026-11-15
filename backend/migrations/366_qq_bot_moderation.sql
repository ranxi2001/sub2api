-- Persist only message identities and keyed content hashes, never message bodies.
CREATE TABLE IF NOT EXISTS qq_bot_moderation_events (
 app_id TEXT NOT NULL, sandbox BOOLEAN NOT NULL, group_id TEXT NOT NULL,
 message_id TEXT NOT NULL, member_id TEXT NOT NULL, policy_hash TEXT NOT NULL,
 content_hash TEXT NOT NULL DEFAULT '', sent_at TIMESTAMPTZ NOT NULL,
 blocked BOOLEAN NOT NULL DEFAULT FALSE, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(app_id,sandbox,group_id,message_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_moderation_events_window ON qq_bot_moderation_events(app_id,sandbox,group_id,member_id,sent_at DESC);
CREATE INDEX IF NOT EXISTS qq_bot_moderation_events_expiry ON qq_bot_moderation_events(created_at);
CREATE TABLE IF NOT EXISTS qq_bot_moderation_members (
 app_id TEXT NOT NULL, sandbox BOOLEAN NOT NULL, group_id TEXT NOT NULL, member_id TEXT NOT NULL,
 cooldown_until TIMESTAMPTZ NOT NULL DEFAULT 'epoch', last_muted_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
 active_incident_id TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(app_id,sandbox,group_id,member_id)
);
CREATE TABLE IF NOT EXISTS qq_bot_moderation_incidents (
 id TEXT PRIMARY KEY, app_id TEXT NOT NULL, sandbox BOOLEAN NOT NULL, group_id TEXT NOT NULL,
 member_id TEXT NOT NULL, policy_hash TEXT NOT NULL, reason TEXT NOT NULL,
 mute_seconds INTEGER NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_moderation_incidents_recent ON qq_bot_moderation_incidents(app_id,sandbox,created_at DESC);
CREATE TABLE IF NOT EXISTS qq_bot_moderation_operations (
 id BIGSERIAL PRIMARY KEY, incident_id TEXT NOT NULL REFERENCES qq_bot_moderation_incidents(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK (kind IN ('mute','recall')), message_id TEXT NOT NULL DEFAULT '',
 expires_at TIMESTAMPTZ NOT NULL, status TEXT NOT NULL DEFAULT 'queued', error TEXT NOT NULL DEFAULT '',
 started_at TIMESTAMPTZ, finished_at TIMESTAMPTZ,
 UNIQUE(incident_id,kind,message_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_moderation_operations_pending ON qq_bot_moderation_operations(kind,id) WHERE status='queued';
CREATE TABLE IF NOT EXISTS qq_bot_moderation_rates (
 app_id TEXT NOT NULL, sandbox BOOLEAN NOT NULL, kind TEXT NOT NULL,
 next_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch', PRIMARY KEY(app_id,sandbox,kind)
);
