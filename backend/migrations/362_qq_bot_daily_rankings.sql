-- Version reward rules by Beijing calendar day. Editing today's configuration
-- cannot change an earlier day's winners or replay an already settled day.
CREATE TABLE IF NOT EXISTS qq_bot_ranking_config_versions (
    app_id TEXT NOT NULL,
    effective_day DATE NOT NULL,
    config JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(app_id, effective_day)
);
CREATE TABLE IF NOT EXISTS qq_bot_ranking_settlements (
    app_id TEXT NOT NULL,
    ranking_day DATE NOT NULL,
    rules JSONB NOT NULL,
    reward_count INTEGER NOT NULL DEFAULT 0,
    settled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(app_id, ranking_day)
);
ALTER TABLE qq_bot_reward_codes DROP CONSTRAINT IF EXISTS qq_bot_reward_codes_source_check;
ALTER TABLE qq_bot_reward_codes ADD CONSTRAINT qq_bot_reward_codes_source_check CHECK (
    (source_kind='code' AND source_key IS NULL AND code_hash IS NOT NULL)
    OR (source_kind IN ('growth','streak','ranking') AND source_key IS NOT NULL AND code_hash IS NULL AND status IN ('pending','fulfilled'))
);
ALTER TABLE qq_bot_rewards DROP CONSTRAINT IF EXISTS qq_bot_rewards_kind_check;
ALTER TABLE qq_bot_rewards ADD CONSTRAINT qq_bot_rewards_kind_check CHECK (kind IN ('checkin','invite','lottery','redemption','growth','streak','ranking'));
