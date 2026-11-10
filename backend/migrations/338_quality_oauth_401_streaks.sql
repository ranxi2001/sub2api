-- Runtime state is independent of pruned test history. Old binaries ignore it;
-- legacy rules have no oauth_401_max_rounds setting and remain opted out.
CREATE TABLE quality_oauth_401_streaks (
    plan_id BIGINT PRIMARY KEY REFERENCES scheduled_test_plans(id) ON DELETE CASCADE,
    failures INTEGER NOT NULL DEFAULT 0 CHECK (failures BETWEEN 0 AND 100),
    config_updated_at TIMESTAMPTZ NOT NULL,
    config_revision TEXT NOT NULL,
    -- A one-way internal equality marker; never exposed through APIs/logs.
    credentials_revision TEXT NOT NULL,
    claimed_until TIMESTAMPTZ NOT NULL,
    last_round_until TIMESTAMPTZ
);
