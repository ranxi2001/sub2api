-- Extracted QQ dependency from DeanZFC/sub2api-custom
-- 5876d7d864dae405c2a3b65074fb556f6985adb8:279_gpt_direct_lottery_rewards.sql.
-- LGPL v3. The unrelated direct-recharge service is not required by QQ rewards.
CREATE TABLE IF NOT EXISTS qq_bot_lottery_chance_grants (
    source_key TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    chances INTEGER NOT NULL CHECK (chances BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS qq_bot_lottery_chance_grants_user ON qq_bot_lottery_chance_grants(user_id);
