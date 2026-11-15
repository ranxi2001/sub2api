-- Zero-value check-ins and losing lottery entries still consume their entitlement.
ALTER TABLE qq_bot_rewards DROP CONSTRAINT IF EXISTS qq_bot_rewards_kind_check;
ALTER TABLE qq_bot_rewards ADD CONSTRAINT qq_bot_rewards_kind_check CHECK (kind IN ('checkin','invite','lottery'));
ALTER TABLE qq_bot_rewards DROP CONSTRAINT IF EXISTS qq_bot_rewards_amount_check;
ALTER TABLE qq_bot_rewards ADD CONSTRAINT qq_bot_rewards_amount_check CHECK (amount >= 0);

CREATE TABLE IF NOT EXISTS qq_bot_lottery_draws (
    id BIGSERIAL PRIMARY KEY,
    request_key TEXT NOT NULL UNIQUE,
    app_id TEXT NOT NULL, member_id TEXT NOT NULL, group_id TEXT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id),
    prize_name TEXT NOT NULL,
    amount NUMERIC(20,8) NOT NULL CHECK (amount >= 0),
    config_snapshot JSONB NOT NULL,
    recharge_total NUMERIC(20,8) NOT NULL CHECK (recharge_total >= 0),
    earned BIGINT NOT NULL CHECK (earned >= 0),
    remaining BIGINT NOT NULL CHECK (remaining >= 0),
    result_text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_lottery_draws_user_time ON qq_bot_lottery_draws(user_id, created_at DESC);
