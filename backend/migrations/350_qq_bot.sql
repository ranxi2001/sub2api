-- QQ group assistant. Rewards and usage are deliberately separate: model calls
-- use the operator's API key; member balances are only credited by rewards.
CREATE TABLE IF NOT EXISTS qq_bot_groups (
    app_id TEXT NOT NULL, group_id TEXT NOT NULL,
    last_event TEXT NOT NULL DEFAULT '', last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (app_id, group_id)
);
CREATE TABLE IF NOT EXISTS qq_bot_bindings (
    app_id TEXT NOT NULL, member_id TEXT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id),
    nickname TEXT NOT NULL DEFAULT '', group_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (app_id, member_id), UNIQUE (app_id, user_id)
);
CREATE TABLE IF NOT EXISTS qq_bot_bind_requests (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL, code_hash TEXT NOT NULL UNIQUE,
    member_id TEXT NOT NULL DEFAULT '', nickname TEXT NOT NULL DEFAULT '', group_id TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS qq_bot_members (
    app_id TEXT NOT NULL, group_id TEXT NOT NULL, member_id TEXT NOT NULL,
    inviter_id TEXT NOT NULL DEFAULT '', invited_at TIMESTAMPTZ,
    joined_at TIMESTAMPTZ, left_at TIMESTAMPTZ, last_membership_at TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (app_id, group_id, member_id)
);
CREATE TABLE IF NOT EXISTS qq_bot_rewards (
    id BIGSERIAL PRIMARY KEY, reward_key TEXT NOT NULL UNIQUE,
    app_id TEXT NOT NULL, group_id TEXT NOT NULL, member_id TEXT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL CHECK (kind IN ('checkin','invite')),
    source_user_id BIGINT REFERENCES users(id), source_member_id TEXT NOT NULL DEFAULT '',
    amount NUMERIC(20,8) NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_rewards_user_time ON qq_bot_rewards(user_id, created_at DESC);
CREATE TABLE IF NOT EXISTS qq_bot_jobs (
    id TEXT PRIMARY KEY, app_id TEXT NOT NULL, event_key TEXT NOT NULL,
    event_type TEXT NOT NULL, group_id TEXT NOT NULL, member_id TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '', payload JSONB NOT NULL,
    kind TEXT NOT NULL DEFAULT 'event', status TEXT NOT NULL DEFAULT 'queued',
    result_text TEXT NOT NULL DEFAULT '', result_image BYTEA, image_type TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '', input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), started_at TIMESTAMPTZ, finished_at TIMESTAMPTZ,
    UNIQUE (app_id, event_key)
);
CREATE INDEX IF NOT EXISTS qq_bot_jobs_queue ON qq_bot_jobs(created_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS qq_bot_jobs_member ON qq_bot_jobs(app_id, group_id, member_id, created_at DESC);
CREATE TABLE IF NOT EXISTS qq_bot_daily_usage (
    app_id TEXT NOT NULL, scope TEXT NOT NULL, day DATE NOT NULL, kind TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (app_id, scope, day, kind)
);
CREATE TABLE IF NOT EXISTS qq_bot_conversations (
    app_id TEXT NOT NULL, group_id TEXT NOT NULL, member_id TEXT NOT NULL,
    messages JSONB NOT NULL DEFAULT '[]', updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (app_id, group_id, member_id)
);
