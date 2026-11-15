-- Email links carry a one-use bearer token. Only its hash is stored.
CREATE TABLE IF NOT EXISTS qq_bot_email_bind_requests (
    token_hash TEXT PRIMARY KEY,
    app_id TEXT NOT NULL, member_id TEXT NOT NULL, group_id TEXT NOT NULL,
    nickname TEXT NOT NULL DEFAULT '', user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (app_id, member_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_email_bind_requests_user ON qq_bot_email_bind_requests(user_id);
CREATE TABLE IF NOT EXISTS qq_bot_email_limits (
    app_id TEXT NOT NULL, scope TEXT NOT NULL, day DATE NOT NULL,
    count INTEGER NOT NULL DEFAULT 0, last_sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (app_id, scope, day)
);
