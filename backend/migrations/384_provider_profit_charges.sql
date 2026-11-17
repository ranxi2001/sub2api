-- Read-only reconciliation facts, independent of billing and of the mutable
-- catalog. Keep event-time ownership and evidence even if a provider is
-- removed. No credential, URL, raw response or upstream request text retained.
CREATE TABLE provider_profit_charges (
    usage_log_id BIGINT PRIMARY KEY,
    provider_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    channel_id BIGINT NOT NULL,
    identity_from TIMESTAMPTZ NOT NULL,
    origin_hash TEXT NOT NULL CHECK (length(origin_hash) = 64),
    upstream_usage_id BIGINT NOT NULL CHECK (upstream_usage_id > 0),
    upstream_user_id BIGINT NOT NULL CHECK (upstream_user_id > 0),
    key_id BIGINT NOT NULL CHECK (key_id > 0),
    upstream_request_id_hash TEXT NOT NULL CHECK (length(upstream_request_id_hash) = 64),
    local_created_at TIMESTAMPTZ NOT NULL,
    upstream_created_at TIMESTAMPTZ NOT NULL,
    amount NUMERIC NOT NULL CHECK (amount >= 0 AND amount < 'Infinity'::numeric),
    settled_amount NUMERIC CHECK (settled_amount > 0 AND settled_amount < 'Infinity'::numeric),
    evidence_kind TEXT GENERATED ALWAYS AS (CASE WHEN settled_amount IS NULL THEN 'reported_usage' ELSE 'settled_debit' END) STORED,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(origin_hash, upstream_user_id, upstream_usage_id),
    UNIQUE(origin_hash, upstream_user_id, key_id, upstream_request_id_hash)
);
CREATE INDEX provider_profit_charge_provider_time ON provider_profit_charges(provider_id,local_created_at);

-- A later proof may fill a previously unknown debit on exactly the same
-- evidence. Do not allow it to rewrite the reported charge or its owner.
CREATE FUNCTION provider_profit_guard_charge_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'provider profit charge evidence is immutable';
    END IF;
    IF (to_jsonb(NEW) - ARRAY['settled_amount','evidence_kind']) IS DISTINCT FROM
       (to_jsonb(OLD) - ARRAY['settled_amount','evidence_kind'])
       OR (OLD.settled_amount IS NOT NULL AND OLD.settled_amount IS DISTINCT FROM NEW.settled_amount) THEN
        RAISE EXCEPTION 'provider profit charge evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- AFTER: the generated evidence_kind is defined when serializing NEW.
CREATE TRIGGER provider_profit_charge_immutable AFTER UPDATE OR DELETE ON provider_profit_charges
FOR EACH ROW EXECUTE FUNCTION provider_profit_guard_charge_update();
