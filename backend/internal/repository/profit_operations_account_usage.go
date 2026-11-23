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
	q := accountUsageWindow + `, cfg AS (
 SELECT c.*,CASE WHEN cost_basis='current_saved_multiplier' AND key IN (
   'https://chatgpt.com/backend-api/codex','https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1')
   THEN multiplier END AS purchase_rate
 FROM jsonb_to_recordset($4::jsonb) AS c(id bigint,name text,key text,channel_id bigint,channel_name text,channel_code text,
 channel_remark text,assignment text,schedulable boolean,concurrency integer,status text,deleted boolean,cost_basis text,multiplier numeric)
), selected AS (
 SELECT f.*,c.purchase_rate,COALESCE(c.key,'unknown:account_missing') AS upstream_key,c.name AS account_name,c.channel_id,c.channel_name,c.channel_code,
 c.channel_remark,c.assignment,CASE $5 WHEN 'public' THEN public_model WHEN 'recorded' THEN model ELSE COALESCE(upstream_model,'(unknown upstream model)') END AS model_key
 FROM facts f LEFT JOIN cfg c ON c.id=f.account_id
 WHERE ($6::bigint=0 OR f.group_id=$6) AND ($7::bigint=0 OR c.channel_id=$7) AND ($8='' OR COALESCE(c.key,'unknown:account_missing')=$8)
 AND ($9='' OR CASE $5 WHEN 'public' THEN public_model WHEN 'recorded' THEN model ELSE upstream_model END=$9)
), legacy_buckets AS (
 SELECT DISTINCT account_id,at FROM selected WHERE purchase_rate IS NOT NULL AND unit_rate_charge IS NULL
), priced AS (
 SELECT selected.*,account_charge + COALESCE(unit_rate_charge*(purchase_rate-1),0) AS procurement_cost,
        purchase_rate IS NOT NULL AND unit_rate_charge<>0 AS uses_purchase_rate
 FROM selected WHERE purchase_rate IS NULL OR unit_rate_charge IS NOT NULL
 UNION ALL
 -- Pre-upgrade rollups lack the one-times subtotal. Read each selected account/bucket
 -- once and apply the same group/model filters to its individual events.
 SELECT e.created_at,e.account_id,e.group_id,e.model,e.public_model,e.upstream_model,e.requests,e.tokens,
        e.standard_charge,e.user_charge,e.account_charge,e.duration_sum,e.duration_count,e.unit_rate_charge,
        c.purchase_rate,c.key,c.name,c.channel_id,c.channel_name,c.channel_code,c.channel_remark,c.assignment,
        CASE $5 WHEN 'public' THEN e.public_model WHEN 'recorded' THEN e.model ELSE COALESCE(e.upstream_model,'(unknown upstream model)') END,
        e.account_charge+e.unit_rate_charge*(c.purchase_rate-1),e.unit_rate_charge<>0
 FROM legacy_buckets b JOIN cfg c ON c.id=b.account_id
 JOIN account_usage_financial_events e ON e.account_id=b.account_id AND e.created_at>=b.at AND e.created_at<b.at+interval '5 minutes'
 WHERE ($6::bigint=0 OR e.group_id=$6)
 AND ($9='' OR CASE $5 WHEN 'public' THEN e.public_model WHEN 'recorded' THEN e.model ELSE e.upstream_model END=$9)
), named AS (
 SELECT *, CASE $10 WHEN 'upstream' THEN upstream_key WHEN 'account' THEN account_id::text ELSE model_key END AS key,
 CASE $10 WHEN 'upstream' THEN COALESCE(NULLIF(channel_name,''),upstream_key)
 WHEN 'account' THEN COALESCE(account_name,'Deleted account')||' #'||account_id::text ELSE model_key END AS name FROM priced
), totals AS (
 SELECT grouping(key) AS is_total,key,max(name) AS name,COALESCE(sum(requests),0) AS requests,
 COALESCE(sum(standard_charge),0) AS standard_charge,COALESCE(sum(user_charge),0) AS request_income,COALESCE(sum(procurement_cost),0) AS upstream_cost,
 COALESCE(sum(user_charge),0)-COALESCE(sum(procurement_cost),0) AS gross_profit,(sum(user_charge)-COALESCE(sum(procurement_cost),0))/NULLIF(sum(user_charge),0) AS gross_margin,
 CASE WHEN bool_or(uses_purchase_rate) THEN 'account_procurement' ELSE 'account_usage' END AS cost_source,CASE WHEN $10='upstream' THEN max(channel_id) END AS channel_id,
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
