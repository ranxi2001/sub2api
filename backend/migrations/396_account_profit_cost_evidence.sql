-- NULL source distinguishes old five-minute statistics. The account usage
-- dialog can still sum its existing columns across the new cost categories.
ALTER TABLE account_usage_financial_rollups
    ADD COLUMN procurement_source text,
    ADD COLUMN procurement_amount numeric,
    ADD COLUMN image_quantities jsonb;

CREATE FUNCTION profit_image_quantities(tier text, n integer, breakdown jsonb)
RETURNS jsonb LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN n > 0 AND tier IN ('1K','2K','4K') THEN jsonb_build_object(tier,n)
        WHEN n > 0 AND tier='mixed' THEN (
            SELECT jsonb_object_agg(key,value) FROM jsonb_each(
                CASE WHEN jsonb_typeof(breakdown)='object' THEN breakdown ELSE '{}'::jsonb END)
            WHERE key IN ('1K','2K','4K') AND jsonb_typeof(value)='number'
              AND value::text ~ '^[1-9][0-9]*$'
            HAVING SUM((value::text)::numeric)=n
        ) END
$$;

CREATE FUNCTION profit_image_amount(quantities jsonb, prices jsonb)
RETURNS numeric LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT CASE WHEN count(*)=count(price) THEN sum(quantity*price) END
    FROM (SELECT (value::text)::numeric AS quantity,
        CASE WHEN jsonb_typeof(prices->key)='number' AND (prices->>key)::numeric BETWEEN 0 AND 1000000
             THEN (prices->>key)::numeric END AS price FROM jsonb_each(quantities)) q
$$;

CREATE VIEW account_profit_events AS
SELECT u.created_at,u.account_id,u.group_id,u.model,COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,u.upstream_model,
    1::bigint AS requests,(u.input_tokens::bigint+u.output_tokens+u.cache_creation_tokens+u.cache_read_tokens)::numeric AS tokens,
    u.total_cost AS standard_charge,u.actual_cost AS user_charge,
    base.amount*base.rate AS account_charge,COALESCE(u.duration_ms,0)::numeric AS duration_sum,(u.duration_ms IS NOT NULL)::integer AS duration_count,
    CASE WHEN base.rate=1 THEN base.amount ELSE 0 END AS unit_rate_charge,
    kind.source AS procurement_source,
    CASE kind.source WHEN 'settled' THEN c.settled_amount
        WHEN 'gateway' THEN u.total_cost * factor.rate
        WHEN 'snapshot' THEN base.amount * factor.rate
        WHEN 'snapshot_image' THEN profit_image_amount(img.quantities,snap.data->'image_prices')
        WHEN 'account' THEN base.amount*base.rate
        WHEN 'legacy_rate' THEN base.amount END AS procurement_amount,
    CASE WHEN kind.source IN ('legacy_image','legacy_image_account') THEN img.quantities END AS image_quantities
FROM usage_logs u
LEFT JOIN provider_profit_charges c ON c.usage_log_id=u.id AND c.account_id=u.account_id AND c.local_created_at=u.created_at
CROSS JOIN LATERAL (SELECT COALESCE(u.account_stats_cost,u.total_cost) AS amount,COALESCE(u.account_rate_multiplier,1) AS rate) base
CROSS JOIN LATERAL (SELECT u.procurement_cost_snapshot AS data) snap
CROSS JOIN LATERAL (SELECT CASE WHEN jsonb_typeof(snap.data->'cost_multiplier')='number' THEN
    CASE WHEN (snap.data->>'cost_multiplier')::numeric BETWEEN 0 AND 1000000 THEN (snap.data->>'cost_multiplier')::numeric END END AS rate) factor
CROSS JOIN LATERAL (SELECT profit_image_quantities(u.image_size,u.image_count,u.image_size_breakdown) AS quantities,
    COALESCE(u.billing_mode='image',false) OR (u.image_count>0 AND u.image_size IS NOT NULL AND COALESCE(u.billing_mode,'')<>'video') AS is_image) img
CROSS JOIN LATERAL (SELECT CASE
    WHEN c.settled_amount IS NOT NULL THEN 'settled'
    WHEN snap.data->>'version'='1' AND snap.data->>'basis'='total_cost' AND snap.data->>'account_type'='gateway'
      AND snap.data->>'provider' IN ('sub2api','new_api') AND snap.data->>'target_platform'=u.platform
      AND u.platform NOT IN ('','composite') THEN 'gateway'
    WHEN snap.data IS NOT NULL AND (snap.data->>'version' IS DISTINCT FROM '2' OR snap.data->>'basis' IS DISTINCT FROM 'account_usage') THEN 'unknown'
    WHEN img.is_image AND snap.data->'image_prices' IS NOT NULL THEN 'snapshot_image'
    WHEN img.is_image AND snap.data IS NULL AND base.rate<>1 THEN 'legacy_image_account'
    WHEN img.is_image AND snap.data IS NULL THEN 'legacy_image'
    WHEN base.rate<>1 THEN 'account'
    WHEN img.is_image THEN 'unknown'
    WHEN snap.data IS NOT NULL THEN 'snapshot'
    ELSE 'legacy_rate' END AS source) kind;

-- A reconciliation can arrive after the five-minute bucket was closed.
CREATE FUNCTION queue_settled_profit_bucket() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b timestamptz;
BEGIN
    IF TG_OP='UPDATE' AND OLD.settled_amount IS NOT DISTINCT FROM NEW.settled_amount THEN RETURN NULL; END IF;
    b := account_usage_bucket(NEW.local_created_at);
    PERFORM pg_advisory_xact_lock_shared(825393,(extract(epoch FROM b)/300)::integer);
    INSERT INTO account_usage_financial_pending VALUES(b) ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
CREATE TRIGGER profit_settlement_queued AFTER INSERT OR UPDATE OF settled_amount ON provider_profit_charges
FOR EACH ROW WHEN (NEW.settled_amount IS NOT NULL) EXECUTE FUNCTION queue_settled_profit_bucket();

-- Upgrade old aggregates in spare worker cycles, without marking correct
-- account-usage history unavailable while procurement metadata is rebuilt.
CREATE TABLE account_profit_legacy_buckets (bucket timestamptz PRIMARY KEY);
INSERT INTO account_profit_legacy_buckets SELECT DISTINCT bucket FROM account_usage_financial_rollups;
