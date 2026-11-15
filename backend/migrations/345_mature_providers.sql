-- Independent operational catalog. No changes to canonical channels, accounts,
-- pricing, balances, or existing account-ops notification settings.
-- Older binaries ignore this table; application rollback does not drop data.
CREATE TABLE IF NOT EXISTS mature_providers (
    id BIGSERIAL PRIMARY KEY,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    name VARCHAR(120) NOT NULL,
    site_type TEXT NOT NULL CHECK (site_type IN ('sub2api', 'newapi')),
    base_url TEXT NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    cost_multiplier NUMERIC(20,8) CHECK (cost_multiplier >= 0 AND cost_multiplier <= 1000000000),
    low_balance_threshold NUMERIC(20,8) CHECK (low_balance_threshold >= 0 AND low_balance_threshold <= 1000000000),
    suppress_low_balance_alert BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown','connected','disconnected')),
    metrics JSONB NOT NULL DEFAULT '{"balance":null,"today_spend":null,"total_spend":null,"total_recharge":null,"currency":""}',
    observed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
