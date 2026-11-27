package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/lib/pq"
)

// GetAccountProcurementWindows returns the covered procurement amount for the
// administrator's selected accounts. One batched read serves today and lifetime.
func (r *usageLogRepository) GetAccountProcurementWindows(ctx context.Context, ids []int64, start, end time.Time, lifetime bool) (map[int64]usagestats.AccountProcurementWindows, error) {
	from := start
	if lifetime {
		from = time.Time{}
	}
	rows, err := r.sql.QueryContext(ctx, accountProcurementWindowsSQL, from, end, pq.Array(ids), start)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[int64]usagestats.AccountProcurementWindows)
	for rows.Next() {
		var id int64
		var w usagestats.AccountProcurementWindows
		if err := rows.Scan(&id, &w.Window.Amount, &w.Window.Known, &w.Window.Requests, &w.Lifetime.Amount, &w.Lifetime.Known, &w.Lifetime.Requests); err != nil {
			return nil, err
		}
		result[id] = w
	}
	return result, rows.Err()
}

// Read published five-minute buckets. If a bucket has a pending correction,
// old projection or crosses the requested split, read the exact events once.
// Bounds, configurations, reconciliations and both totals share one snapshot.
const accountProcurementWindowsSQL = financialWindowBounds + `, cfg AS MATERIALIZED (
 SELECT id,type AS account_type,extra->'image_purchase_prices' AS image_prices,
        true AS standard_tier_tokens,false AS standard_tokens,
        CASE WHEN jsonb_typeof(extra->'cost_multiplier')='number' THEN
          CASE WHEN (extra->>'cost_multiplier')::numeric BETWEEN 0 AND 1000000 THEN (extra->>'cost_multiplier')::numeric END END AS multiplier,
        'current_saved_multiplier' AS cost_basis
 FROM accounts WHERE id=ANY($3::bigint[]) AND type<>'gateway' AND extra->>'token_procurement_basis'='standard_tier'
)
SELECT c.id, x.window_amount,x.window_known,x.window_requests,x.lifetime_amount,x.lifetime_known,x.lifetime_requests
FROM cfg c CROSS JOIN LATERAL (
 WITH raw_buckets AS MATERIALIZED (
   SELECT bucket FROM account_usage_financial_pending,bounds WHERE bucket>=lo AND bucket<hi
   UNION SELECT bucket FROM account_usage_financial_rollups,bounds WHERE account_id=c.id AND bucket>=lo AND bucket<hi
     AND (standard_tier_projected IS NOT TRUE OR procurement_source IS NULL OR procurement_source='account')
   UNION SELECT account_usage_bucket($4) FROM bounds WHERE account_usage_bucket($4)<>$4 AND account_usage_bucket($4)>=lo AND account_usage_bucket($4)<hi
 ), facts AS (
   SELECT bucket AS at,requests,account_charge,procurement_source,procurement_amount,image_quantities,standard_token_amount,standard_tier_amount,standard_tier_eligible
   FROM account_usage_financial_rollups r,bounds WHERE r.account_id=c.id AND bucket>=lo AND bucket<hi AND NOT EXISTS(SELECT 1 FROM raw_buckets b WHERE b.bucket=r.bucket)
   UNION ALL
   SELECT e.created_at,e.requests,e.account_charge,e.procurement_source,e.procurement_amount,e.image_quantities,e.standard_token_amount,e.standard_tier_amount,e.standard_tier_eligible
   FROM (SELECT $1::timestamptz AS begin_at,lo AS end_at FROM bounds UNION ALL
         SELECT GREATEST(lo,hi),$2::timestamptz FROM bounds UNION ALL
         SELECT bucket,bucket+interval '5 minutes' FROM raw_buckets) ranges
   JOIN account_profit_events e ON e.account_id=c.id AND e.created_at>=begin_at AND e.created_at<end_at
 ), priced AS (
   SELECT at,requests,` + accountProcurementCostSQL + ` AS amount FROM facts f
 )
 SELECT CASE WHEN count(*) FILTER(WHERE at>=$4)=0 THEN 0 ELSE sum(amount) FILTER(WHERE at>=$4) END AS window_amount,
   COALESCE(sum(requests) FILTER(WHERE at>=$4 AND amount IS NOT NULL),0) AS window_known,
   COALESCE(sum(requests) FILTER(WHERE at>=$4),0) AS window_requests,
   CASE WHEN count(*)=0 THEN 0 ELSE sum(amount) END AS lifetime_amount,
   COALESCE(sum(requests) FILTER(WHERE amount IS NOT NULL),0) AS lifetime_known,
   COALESCE(sum(requests),0) AS lifetime_requests
 FROM priced
) x`
