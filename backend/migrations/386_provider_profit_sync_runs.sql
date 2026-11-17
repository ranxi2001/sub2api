-- Durable record of a completed provider-bill sync. Charges stay in
-- provider_profit_charges; this row only says when a window was fully read.
-- Rows are not expired or deleted on a timer. No credentials, URLs, or
-- request text are stored.
CREATE TABLE provider_profit_sync_runs (
    provider_id BIGINT NOT NULL REFERENCES mature_providers(id) ON DELETE CASCADE,
    window_from TIMESTAMPTZ NOT NULL,
    window_until TIMESTAMPTZ NOT NULL,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    matched BIGINT NOT NULL CHECK (matched >= 0),
    unmatched BIGINT NOT NULL CHECK (unmatched >= 0),
    PRIMARY KEY (provider_id, window_from, window_until)
);
-- No window_from < window_until check. PostgreSQL timestamptz is microsecond
-- precision, so a valid sub-microsecond Go window can round to equal bounds.
-- Rejecting that insert would roll back an otherwise complete charge reconcile.

-- Supports the report's "charges captured inside this window" freshness lookup.
CREATE INDEX provider_profit_charge_local_captured
    ON provider_profit_charges (local_created_at, provider_id, captured_at);
