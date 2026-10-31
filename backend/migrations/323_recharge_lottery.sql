CREATE TABLE IF NOT EXISTS lottery_pools (
    id BIGSERIAL PRIMARY KEY, name VARCHAR(100) NOT NULL, mode VARCHAR(20) NOT NULL CHECK (mode IN ('single', 'cumulative')),
    threshold_units BIGINT NOT NULL CHECK (threshold_units > 0), grant_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    draw_enabled BOOLEAN NOT NULL DEFAULT FALSE, rule_locked BOOLEAN NOT NULL DEFAULT FALSE, sort_order INT NOT NULL DEFAULT 0,
    activated_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS lottery_prizes (
    id BIGSERIAL PRIMARY KEY, pool_id BIGINT NOT NULL REFERENCES lottery_pools(id), name VARCHAR(100) NOT NULL,
    kind VARCHAR(20) NOT NULL CHECK (kind IN ('balance', 'subscription', 'text')), weight BIGINT NOT NULL CHECK (weight > 0),
    stock BIGINT NOT NULL DEFAULT -1 CHECK (stock >= -1), enabled BOOLEAN NOT NULL DEFAULT TRUE, sort_order INT NOT NULL DEFAULT 0,
    amount_units BIGINT NOT NULL DEFAULT 0, plan_id BIGINT NOT NULL DEFAULT 0, plan_name TEXT NOT NULL DEFAULT '',
    group_id BIGINT NOT NULL DEFAULT 0, validity_days INT NOT NULL DEFAULT 0, text TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS lotteryprize_pool_id ON lottery_prizes(pool_id);
CREATE TABLE IF NOT EXISTS lottery_accounts (
    id BIGSERIAL PRIMARY KEY, pool_id BIGINT NOT NULL REFERENCES lottery_pools(id), user_id BIGINT NOT NULL REFERENCES users(id),
    total_units BIGINT NOT NULL DEFAULT 0 CHECK (total_units >= 0), earned BIGINT NOT NULL DEFAULT 0 CHECK (earned >= 0),
    used BIGINT NOT NULL DEFAULT 0 CHECK (used >= 0), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS lotteryaccount_pool_id_user_id ON lottery_accounts(pool_id, user_id);
CREATE INDEX IF NOT EXISTS lotteryaccount_user_id ON lottery_accounts(user_id);
CREATE TABLE IF NOT EXISTS lottery_contributions (
    id BIGSERIAL PRIMARY KEY, pool_id BIGINT NOT NULL REFERENCES lottery_pools(id), user_id BIGINT NOT NULL REFERENCES users(id),
    source_type VARCHAR(20) NOT NULL, source_id VARCHAR(128) NOT NULL,
    amount_units BIGINT NOT NULL CHECK (amount_units > 0), net_units BIGINT NOT NULL CHECK (net_units >= 0 AND net_units <= amount_units),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS lotterycontribution_pool_id_source_type_source_id ON lottery_contributions(pool_id, source_type, source_id);
CREATE INDEX IF NOT EXISTS lotterycontribution_user_id_pool_id ON lottery_contributions(user_id, pool_id);
CREATE INDEX IF NOT EXISTS lotterycontribution_source_type_source_id ON lottery_contributions(source_type, source_id);
CREATE TABLE IF NOT EXISTS lottery_ledger (
    id BIGSERIAL PRIMARY KEY, pool_id BIGINT NOT NULL REFERENCES lottery_pools(id), user_id BIGINT NOT NULL REFERENCES users(id),
    action VARCHAR(20) NOT NULL, source_type VARCHAR(20) NOT NULL, source_id VARCHAR(128) NOT NULL,
    amount_units BIGINT NOT NULL, ticket_delta BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS lotteryledger_pool_id_user_id_created_at ON lottery_ledger(pool_id, user_id, created_at);
CREATE INDEX IF NOT EXISTS lotteryledger_user_id ON lottery_ledger(user_id);
CREATE TABLE IF NOT EXISTS lottery_draws (
    id BIGSERIAL PRIMARY KEY, pool_id BIGINT NOT NULL REFERENCES lottery_pools(id), user_id BIGINT NOT NULL REFERENCES users(id),
    prize_id BIGINT NOT NULL REFERENCES lottery_prizes(id), request_key VARCHAR(128) NOT NULL, prize_snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS lotterydraw_user_id_request_key ON lottery_draws(user_id, request_key);
CREATE INDEX IF NOT EXISTS lotterydraw_pool_id_created_at ON lottery_draws(pool_id, created_at);
CREATE INDEX IF NOT EXISTS lotterydraw_user_id_created_at ON lottery_draws(user_id, created_at);
