-- The free-tier receipt stores the Standard quote before customer multipliers.
-- Submitted header-timeout retries multiply the receipt's multiplier and actual
-- charge but leave its component total at one submission.
CREATE FUNCTION usage_free_tier_quote(details jsonb, recorded_rate numeric)
RETURNS numeric LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT CASE WHEN details->'free_fast'='true'::jsonb OR details->'free_ultrafast'='true'::jsonb THEN
   CASE WHEN jsonb_typeof(details->'total_cost')='number' AND jsonb_typeof(details->'multiplier')='number' THEN
     CASE WHEN (details->>'total_cost')::numeric >= 0 THEN
       CASE WHEN recorded_rate=0 AND (details->>'multiplier')::numeric=0 THEN (details->>'total_cost')::numeric
            WHEN recorded_rate>0 AND round((details->>'multiplier')::numeric/recorded_rate)>=1
              AND abs((details->>'multiplier')::numeric-round((details->>'multiplier')::numeric/recorded_rate)*recorded_rate)
                  <=greatest(0.00000001,0.0000005*round((details->>'multiplier')::numeric/recorded_rate))
            THEN (details->>'total_cost')::numeric*round((details->>'multiplier')::numeric/recorded_rate) END
     END
   END
 END
$$;

CREATE OR REPLACE VIEW account_profit_events AS
SELECT u.created_at,u.account_id,u.group_id,u.model,COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,u.upstream_model,
    1::bigint AS requests,(u.input_tokens::bigint+u.output_tokens+u.cache_creation_tokens+u.cache_read_tokens)::numeric AS tokens,
    u.total_cost AS standard_charge,u.actual_cost AS user_charge,
    base.amount*base.rate AS account_charge,COALESCE(u.duration_ms,0)::numeric AS duration_sum,(u.duration_ms IS NOT NULL)::integer AS duration_count,
    CASE WHEN base.rate=1 THEN base.amount ELSE 0 END AS unit_rate_charge,
    kind.source AS procurement_source,
    CASE kind.source WHEN 'settled' THEN c.settled_amount
        WHEN 'gateway' THEN price.standard * factor.rate
        WHEN 'snapshot_standard' THEN price.standard * factor.rate
        WHEN 'snapshot' THEN base.amount * factor.rate
        WHEN 'snapshot_image' THEN profit_image_amount(img.quantities,snap.data->'image_prices')
        WHEN 'account_debit' THEN base.amount*base.rate
        WHEN 'legacy_token_debit' THEN base.amount*base.rate
        WHEN 'legacy_rate' THEN base.amount END AS procurement_amount,
    CASE WHEN kind.source IN ('legacy_image','legacy_image_account') THEN img.quantities END AS image_quantities,
    -- A legacy row needs today's rate. An old v2 snapshot already has a rate.
    CASE WHEN kind.source='legacy_token_debit' THEN
             price.standard * CASE WHEN snap.data IS NULL THEN base.rate ELSE factor.rate END
         WHEN token.eligible AND kind.source='legacy_rate' THEN price.standard
         WHEN token.eligible AND kind.source='snapshot' AND NOT (snap.data ? 'token_basis')
         THEN price.standard * factor.rate END AS standard_token_amount
FROM usage_logs u
LEFT JOIN provider_profit_charges c ON c.usage_log_id=u.id AND c.account_id=u.account_id AND c.local_created_at=u.created_at
CROSS JOIN LATERAL (SELECT COALESCE(usage_free_tier_quote(u.customer_cost_details,u.rate_multiplier),u.total_cost) AS standard) price
CROSS JOIN LATERAL (SELECT COALESCE(u.account_stats_cost,price.standard) AS amount,COALESCE(u.account_rate_multiplier,1) AS rate) base
CROSS JOIN LATERAL (SELECT u.procurement_cost_snapshot AS data) snap
CROSS JOIN LATERAL (SELECT CASE WHEN jsonb_typeof(snap.data->'cost_multiplier')='number' THEN
    CASE WHEN (snap.data->>'cost_multiplier')::numeric BETWEEN 0 AND 1000000 THEN (snap.data->>'cost_multiplier')::numeric END END AS rate) factor
CROSS JOIN LATERAL (SELECT profit_image_quantities(u.image_size,u.image_count,u.image_size_breakdown) AS quantities,
    COALESCE(u.billing_mode='image',false) OR (u.image_count>0 AND u.image_size IS NOT NULL AND COALESCE(u.billing_mode,'')<>'video') AS is_image) img
CROSS JOIN LATERAL (SELECT COALESCE(u.billing_mode,'') IN ('','token') AND COALESCE(u.image_count,0)=0 AS eligible) token
CROSS JOIN LATERAL (SELECT CASE
    WHEN c.settled_amount IS NOT NULL THEN 'settled'
    WHEN snap.data->>'version'='1' AND snap.data->>'basis'='total_cost' AND snap.data->>'account_type'='gateway'
      AND snap.data->>'provider' IN ('sub2api','new_api') AND snap.data->>'target_platform'=u.platform
      AND u.platform NOT IN ('','composite') THEN 'gateway'
    WHEN snap.data IS NOT NULL AND (snap.data->>'version' IS DISTINCT FROM '2' OR snap.data->>'basis' IS DISTINCT FROM 'account_usage') THEN 'unknown'
    WHEN img.is_image AND snap.data->'image_prices' IS NOT NULL THEN 'snapshot_image'
    WHEN img.is_image AND snap.data IS NULL AND base.rate<>1 THEN 'legacy_image_account'
    WHEN img.is_image AND snap.data IS NULL THEN 'legacy_image'
    WHEN token.eligible AND snap.data->>'token_basis'='standard_charge' THEN 'snapshot_standard'
    WHEN token.eligible AND snap.data ? 'token_basis' AND snap.data->>'token_basis' IS DISTINCT FROM 'account_stats' THEN 'unknown'
    -- Without a frozen basis keep both estimates. Never apply both rates.
    WHEN token.eligible AND base.rate<>1 AND (snap.data IS NULL OR NOT (snap.data ? 'token_basis')) THEN 'legacy_token_debit'
    WHEN base.rate<>1 THEN 'account_debit'
    WHEN img.is_image THEN 'unknown'
    WHEN snap.data IS NOT NULL THEN 'snapshot'
    ELSE 'legacy_rate' END AS source) kind;

CREATE OR REPLACE VIEW account_usage_financial_events AS
SELECT created_at, account_id, group_id, model, COALESCE(NULLIF(requested_model,''),model) AS public_model, upstream_model,
    1::bigint AS requests,(input_tokens::bigint+output_tokens+cache_creation_tokens+cache_read_tokens)::numeric AS tokens,
    total_cost AS standard_charge,actual_cost AS user_charge,
    COALESCE(account_stats_cost,usage_free_tier_quote(customer_cost_details,rate_multiplier),total_cost)*COALESCE(account_rate_multiplier,1) AS account_charge,
    COALESCE(duration_ms,0)::numeric AS duration_sum,(duration_ms IS NOT NULL)::int AS duration_count,
    CASE WHEN COALESCE(account_rate_multiplier,1)=1 THEN COALESCE(account_stats_cost,usage_free_tier_quote(customer_cost_details,rate_multiplier),total_cost) ELSE 0 END AS unit_rate_charge
FROM usage_logs;

-- Readers use raw events for pending buckets until the existing five-minute
-- worker replaces the aggregate. No usage rows or customer charges are edited.
INSERT INTO account_profit_legacy_buckets
SELECT DISTINCT bucket FROM account_usage_financial_rollups
ON CONFLICT DO NOTHING;
