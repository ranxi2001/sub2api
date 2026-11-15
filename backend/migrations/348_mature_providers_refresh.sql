-- Unset/zero is disabled. No existing provider begins background networking.
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS refresh_interval_seconds INTEGER NOT NULL DEFAULT 0 CHECK (refresh_interval_seconds=0 OR refresh_interval_seconds BETWEEN 30 AND 86400);
ALTER TABLE mature_providers ADD COLUMN IF NOT EXISTS next_refresh_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE TABLE IF NOT EXISTS mature_provider_alerts (
 id BIGSERIAL PRIMARY KEY,
 provider_id BIGINT NOT NULL REFERENCES mature_providers(id) ON DELETE CASCADE,
 balance NUMERIC(24,8) NOT NULL,
 threshold NUMERIC(24,8) NOT NULL,
 currency TEXT NOT NULL,
 opened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS mature_provider_alerts_open ON mature_provider_alerts(provider_id) WHERE resolved_at IS NULL;
