-- NULL identifies older rollups whose one-times charge needs the original events.
ALTER TABLE account_usage_financial_rollups ADD COLUMN unit_rate_charge numeric;

CREATE OR REPLACE VIEW account_usage_financial_events AS
SELECT created_at, account_id, group_id, model, COALESCE(NULLIF(requested_model,''),model) AS public_model, upstream_model,
    1::bigint AS requests,(input_tokens::bigint+output_tokens+cache_creation_tokens+cache_read_tokens)::numeric AS tokens,
    total_cost AS standard_charge,actual_cost AS user_charge,
    COALESCE(account_stats_cost,total_cost)*COALESCE(account_rate_multiplier,1) AS account_charge,
    COALESCE(duration_ms,0)::numeric AS duration_sum,(duration_ms IS NOT NULL)::int AS duration_count,
    CASE WHEN COALESCE(account_rate_multiplier,1)=1 THEN COALESCE(account_stats_cost,total_cost) ELSE 0 END AS unit_rate_charge
FROM usage_logs;
