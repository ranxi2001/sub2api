package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func readAccountUsageProfit(ctx context.Context, tx *sql.Tx, f service.UpstreamOperationsFilter) (*service.ProfitOperationsSnapshot, error) {
	if err := accountUsageReady(ctx, tx, f.From, f.Until); err != nil {
		return nil, err
	}
	raw, err := profitOperationsAccounts(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	size, offset := f.PageSize, (f.Page-1)*f.PageSize
	if service.CollectProfitRows(ctx) {
		size, offset = 10001, 0
	}
	// sort and direction are allowlisted by NormalizeProfitOperationsFilter.
	q := financialWindowBounds + `, cfg AS (
 SELECT c.*,a.extra->'image_purchase_prices' AS image_prices,
        c.account_type<>'gateway' AND a.extra->>'token_procurement_basis'='standard_charge' AS standard_tokens
 FROM jsonb_to_recordset($4::jsonb) AS c(id bigint,name text,key text,channel_id bigint,channel_name text,channel_code text,
 channel_remark text,assignment text,schedulable boolean,concurrency integer,status text,deleted boolean,cost_basis text,multiplier numeric,account_type text)
 LEFT JOIN accounts a ON a.id=c.id
), old_buckets AS (
 SELECT bucket FROM account_profit_legacy_buckets,bounds WHERE bucket>=lo AND bucket<hi
 UNION SELECT DISTINCT r.bucket FROM account_usage_financial_rollups r
 LEFT JOIN cfg c ON c.id=r.account_id CROSS JOIN bounds
 WHERE bucket>=lo AND bucket<hi AND ($3::bigint=0 OR r.account_id=$3)
   AND (procurement_source IS NULL OR procurement_source='account' OR
        (c.standard_tokens AND token_basis_projected IS NOT TRUE AND procurement_source IN ('legacy_rate','snapshot','legacy_token_debit')))
), facts AS (
 SELECT account_id,group_id,model,public_model,upstream_model,requests,standard_charge,user_charge,account_charge,procurement_source,procurement_amount,image_quantities,standard_token_amount
 FROM account_usage_financial_rollups,bounds WHERE bucket>=lo AND bucket<hi AND procurement_source IS NOT NULL AND NOT EXISTS (SELECT 1 FROM old_buckets b WHERE b.bucket=account_usage_financial_rollups.bucket) AND ($3::bigint=0 OR account_id=$3)
 UNION ALL
 SELECT e.account_id,e.group_id,e.model,e.public_model,e.upstream_model,e.requests,e.standard_charge,e.user_charge,e.account_charge,e.procurement_source,e.procurement_amount,e.image_quantities,e.standard_token_amount
 FROM (SELECT $1::timestamptz AS start_at,lo AS end_at FROM bounds UNION ALL
       SELECT GREATEST(lo,hi),$2::timestamptz FROM bounds UNION ALL
       SELECT bucket,bucket+interval '5 minutes' FROM old_buckets) ranges
 JOIN account_profit_events e ON e.created_at>=start_at AND e.created_at<end_at WHERE ($3::bigint=0 OR e.account_id=$3)
), priced AS (
 SELECT f.*,c.standard_tokens,COALESCE(c.key,'unknown:account_missing') AS upstream_key,c.name AS account_name,c.channel_id,c.channel_name,c.channel_code,
 c.channel_remark,c.assignment,CASE $5 WHEN 'public' THEN public_model WHEN 'recorded' THEN model ELSE COALESCE(upstream_model,'(unknown upstream model)') END AS model_key,
 CASE WHEN procurement_source='legacy_token_debit' AND c.standard_tokens THEN standard_token_amount
      WHEN procurement_source='snapshot' AND c.standard_tokens AND standard_token_amount IS NOT NULL THEN standard_token_amount
      WHEN procurement_source='legacy_rate' AND c.cost_basis='current_saved_multiplier' THEN
        (CASE WHEN c.standard_tokens THEN COALESCE(standard_token_amount,procurement_amount) ELSE procurement_amount END)*c.multiplier
      WHEN procurement_source='legacy_image_account' AND NULLIF(c.image_prices,'{}'::jsonb) IS NULL THEN account_charge
      WHEN procurement_source IN ('legacy_image','legacy_image_account') AND c.account_type<>'gateway' THEN profit_image_amount(image_quantities,c.image_prices)*requests
      WHEN procurement_source NOT IN ('legacy_rate','legacy_image','legacy_image_account') THEN procurement_amount END AS procurement_cost
 FROM facts f LEFT JOIN cfg c ON c.id=f.account_id
 WHERE ($6::bigint=0 OR f.group_id=$6) AND ($7::bigint=0 OR c.channel_id=$7) AND ($8='' OR COALESCE(c.key,'unknown:account_missing')=$8)
 AND ($9='' OR CASE $5 WHEN 'public' THEN public_model WHEN 'recorded' THEN model ELSE upstream_model END=$9)
), named AS (
 SELECT *, CASE $10 WHEN 'upstream' THEN upstream_key WHEN 'account' THEN account_id::text ELSE model_key END AS key,
 CASE $10 WHEN 'upstream' THEN COALESCE(NULLIF(channel_name,''),upstream_key)
 WHEN 'account' THEN COALESCE(account_name,'Deleted account')||' #'||account_id::text ELSE model_key END AS name FROM priced
), totals AS (
 SELECT grouping(key) AS is_total,key,max(name) AS name,COALESCE(sum(requests),0) AS requests,
 COALESCE(sum(standard_charge),0) AS standard_charge,COALESCE(sum(user_charge),0) AS request_income,CASE WHEN count(*)=0 THEN 0 ELSE sum(procurement_cost) END AS upstream_cost,
 CASE WHEN count(*)=0 THEN 0 ELSE sum(user_charge-procurement_cost) END AS gross_profit,
 sum(user_charge-procurement_cost)/NULLIF(sum(user_charge) FILTER(WHERE procurement_cost IS NOT NULL),0) AS gross_margin,
 COALESCE(sum(requests) FILTER(WHERE procurement_cost IS NOT NULL),0) AS cost_covered_requests,
 COALESCE(sum(requests) FILTER(WHERE procurement_cost IS NULL),0) AS unknown_cost_requests,
 COALESCE(sum(user_charge) FILTER(WHERE procurement_cost IS NULL),0) AS unknown_cost_income,
 COALESCE(sum(procurement_cost) FILTER(WHERE procurement_source='settled'),0) AS covered_reconciled_cost,
 COALESCE(sum(procurement_cost) FILTER(WHERE procurement_source<>'settled'),0) AS covered_estimated_cost,
 COALESCE(sum(requests) FILTER(WHERE (procurement_source IN ('legacy_rate','legacy_image') OR
   (procurement_source IN ('snapshot','legacy_token_debit') AND standard_tokens AND standard_token_amount IS NOT NULL)) AND procurement_cost IS NOT NULL),0) AS current_config_requests,
 'procurement_coverage' AS cost_source,CASE WHEN $10='upstream' THEN max(channel_id) END AS channel_id,
 max(channel_code) AS channel_code,max(channel_remark) AS channel_remark,max(assignment) AS assignment
 FROM named GROUP BY GROUPING SETS ((key),())
), members AS (
 SELECT * FROM cfg c WHERE NOT deleted AND ($7::bigint=0 OR channel_id=$7) AND ($8='' OR key=$8)
 AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))
), catalog AS (SELECT key,max(channel_id) AS channel_id,COALESCE(NULLIF(max(channel_name),''),key) AS name,count(*) AS accounts FROM members GROUP BY key)
 SELECT jsonb_build_object('basis','account_usage','summary',(SELECT to_jsonb(t)-'is_total' FROM totals t WHERE is_total=1),
 'total',(SELECT count(*) FROM totals WHERE is_total=0),
 'rows',COALESCE((SELECT jsonb_agg(to_jsonb(t)-'is_total' ORDER BY ` + f.Sort + ` ` + f.Direction + ` NULLS LAST,key) FROM (
 SELECT * FROM totals WHERE is_total=0 ORDER BY ` + f.Sort + ` ` + f.Direction + ` NULLS LAST,key LIMIT $11 OFFSET $12)t),'[]'),
 'catalog_total',(SELECT count(*) FROM catalog),'catalog',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY key) FROM (SELECT * FROM catalog ORDER BY key LIMIT $11 OFFSET $12)c),'[]'),
 'member_total',(SELECT count(*) FROM members),'members',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM (SELECT id,name FROM members ORDER BY id LIMIT $11 OFFSET $12)m),'[]'),
 'accounts','[]'::jsonb,'account_details_limit',0)`
	out := &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{
		Scope: f, QueriedAt: time.Now().UTC(), Attribution: "current_account_channel",
		Quality: service.ChannelOperationsSection{Status: "not_requested"}, Errors: service.ChannelOperationsSection{Status: "not_requested"},
	}, Currency: "USD"}
	err = tx.QueryRowContext(ctx, q, f.From, f.Until, f.AccountID, string(raw), f.ModelScope, f.GroupID, f.ChannelID, f.Upstream, f.Model, f.Dimension, size, offset).Scan(&out.Usage)
	return out, err
}
