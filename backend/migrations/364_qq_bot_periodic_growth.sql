-- Store the exact progress already exchanged for recurring rewards. Changing
-- an interval applies only to the remainder; switching metrics keeps each
-- metric's ledger, including when switching back. Account keys survive rebinding.
CREATE TABLE IF NOT EXISTS qq_bot_periodic_growth_progress (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('usage_count','usage_tokens','usage_amount','recharge_total','checkin_total')),
    consumed NUMERIC NOT NULL DEFAULT 0 CHECK (consumed >= 0),
    cycles BIGINT NOT NULL DEFAULT 0 CHECK (cycles >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(user_id, task_id, kind)
);
