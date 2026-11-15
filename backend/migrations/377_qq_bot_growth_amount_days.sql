-- Date cursors prevent qualified days from funding more than one recurring
-- reward, including across gaps, threshold changes and account rebindings.
CREATE TABLE IF NOT EXISTS qq_bot_growth_amount_day_progress (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('usage_amount_days','usage_amount_streak','recharge_days','recharge_streak')),
    last_reward_day DATE NOT NULL,
    cycles BIGINT NOT NULL DEFAULT 0 CHECK (cycles >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, task_id, kind)
);
