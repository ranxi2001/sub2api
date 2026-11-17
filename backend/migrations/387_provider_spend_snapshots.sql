-- Window-keyed upstream wallet spend. Profit operations prices a channel from the
-- snapshot whose [from, until) equals the selected report window. Channel
-- operations keeps mature_providers.metrics.today_spend as the today card only;
-- a historical window must never be written into that field.
-- amount is the upstream wallet total for the window (it may include other
-- sites). NULL amount means this window has no successful reading; last_error
-- explains the latest failed probe without erasing an older amount.
CREATE TABLE provider_spend_snapshots (
    provider_id BIGINT NOT NULL REFERENCES mature_providers(id) ON DELETE CASCADE,
    window_from TIMESTAMPTZ NOT NULL,
    window_until TIMESTAMPTZ NOT NULL,
    amount NUMERIC,
    currency TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ,
    source TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    last_error_at TIMESTAMPTZ,
    PRIMARY KEY (provider_id, window_from, window_until),
    CHECK (window_until > window_from),
    CHECK (amount IS NULL OR (amount >= 0 AND amount < 1000000000000))
);

COMMENT ON TABLE provider_spend_snapshots IS 'Upstream wallet spend for one profit window. Not a per-request bill. Historical rows do not replace mature_providers.metrics.today_spend.';

-- Report grouping, not this function, may fall back to a unique site key after
-- a manual unlink. An explicit channel_id still wins inside the function.
COMMENT ON FUNCTION provider_profit_account_at(bigint, timestamptz) IS 'Point-in-time ownership. Explicit channel wins. A manual unlink returns channel_id NULL and assignment manual_unlinked; the profit report may then claim a unique site key. OAuth dynamic endpoints are not matched by host.';
