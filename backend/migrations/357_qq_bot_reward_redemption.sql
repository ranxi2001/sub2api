CREATE TABLE IF NOT EXISTS qq_bot_reward_codes (
    id BIGSERIAL PRIMARY KEY,
    app_id TEXT NOT NULL,
    code_hash TEXT NOT NULL UNIQUE,
    code_hint TEXT NOT NULL,
    batch_name TEXT NOT NULL,
    reward_type TEXT NOT NULL CHECK (reward_type IN ('balance','chances','physical','other')),
    amount NUMERIC(20,8) NOT NULL DEFAULT 0,
    quantity INTEGER NOT NULL DEFAULT 0,
    prize_name TEXT NOT NULL DEFAULT '',
    claim_instructions TEXT NOT NULL DEFAULT '',
    target_user_id BIGINT REFERENCES users(id),
    expires_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'unused' CHECK (status IN ('unused','disabled','pending','fulfilled')),
    user_id BIGINT REFERENCES users(id),
    member_id TEXT NOT NULL DEFAULT '',
    group_id TEXT NOT NULL DEFAULT '',
    redeemed_at TIMESTAMPTZ,
    fulfilled_at TIMESTAMPTZ,
    fulfillment_note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((reward_type='balance' AND amount>0 AND amount<=1000 AND quantity=0)
        OR (reward_type<>'balance' AND amount=0 AND quantity BETWEEN 1 AND 1000)),
    CHECK ((status IN ('unused','disabled') AND user_id IS NULL AND redeemed_at IS NULL)
        OR (status IN ('pending','fulfilled') AND user_id IS NOT NULL AND redeemed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS qq_bot_reward_codes_user ON qq_bot_reward_codes(user_id, id DESC);

-- Persistent, shared across processes and groups. User scopes survive QQ rebinding.
CREATE TABLE IF NOT EXISTS qq_bot_reward_limits (
    scope TEXT PRIMARY KEY,
    last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    blocked_until TIMESTAMPTZ NOT NULL DEFAULT 'epoch'
);
CREATE TABLE IF NOT EXISTS qq_bot_reward_attempts (
    request_key TEXT PRIMARY KEY,
    app_id TEXT NOT NULL,
    member_id TEXT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id),
    code_id BIGINT REFERENCES qq_bot_reward_codes(id),
    failed BOOLEAN NOT NULL,
    result_text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_reward_attempts_member ON qq_bot_reward_attempts(app_id,member_id,created_at DESC) WHERE failed;
CREATE INDEX IF NOT EXISTS qq_bot_reward_attempts_user ON qq_bot_reward_attempts(user_id,created_at DESC) WHERE failed;
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS reward_code_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE qq_bot_rewards DROP CONSTRAINT IF EXISTS qq_bot_rewards_kind_check;
ALTER TABLE qq_bot_rewards ADD CONSTRAINT qq_bot_rewards_kind_check CHECK (kind IN ('checkin','invite','lottery','redemption'));
