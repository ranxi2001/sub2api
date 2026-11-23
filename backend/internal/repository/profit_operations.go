package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Current members/capacity and fallback estimates share the safe URL projection.
// Request-level ownership is resolved separately at the time of each usage row.
func profitOperationsAccounts(ctx context.Context, tx *sql.Tx, f service.UpstreamOperationsFilter) ([]byte, error) {
	raw, err := operationsAccounts(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	var accounts []struct {
		operationsAccount
		Multiplier      *float64 `json:"multiplier"`
		AccountType     string   `json:"account_type"`
		CostBasis       string   `json:"cost_basis"`
		URLKey          string   `json:"url_key"`
		URLAttribution  string   `json:"url_attribution"`
		URLChannelID    *int64   `json:"url_channel_id"`
		URLOriginHash   string   `json:"url_origin_hash"`
		ChannelID       int64    `json:"channel_id"`
		ChannelName     string   `json:"channel_name"`
		ChannelCode     string   `json:"channel_code"`
		ChannelRemark   string   `json:"channel_remark"`
		ChannelRevision int64    `json:"channel_revision"`
		Assignment      string   `json:"assignment"`
		OAuthChannelID  *int64   `json:"oauth_channel_id"`
	}
	if err = json.Unmarshal(raw, &accounts); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,type,extra->'cost_multiplier' FROM accounts WHERE id IN (SELECT id FROM jsonb_to_recordset($1::jsonb) c(id bigint))`, string(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	costs := map[int64]any{}
	accountTypes := map[int64]string{}
	for rows.Next() {
		var id int64
		var value []byte
		var accountType string
		if err = rows.Scan(&id, &accountType, &value); err != nil {
			return nil, err
		}
		var cost any
		if len(value) > 0 {
			if err = json.Unmarshal(value, &cost); err != nil {
				return nil, err
			}
		}
		costs[id] = cost
		accountTypes[id] = accountType
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range accounts {
		a := &accounts[i]
		id := a.ID
		v, basis := service.OperationsCostMultiplier(&service.Account{Extra: map[string]any{"cost_multiplier": costs[id]}})
		a.Multiplier = v
		a.CostBasis = basis
		a.AccountType = accountTypes[id]
		if a.AccountType == service.AccountTypeGateway {
			a.Multiplier = nil
			a.CostBasis = "request_procurement_unknown"
		}
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	directory, err := profitURLDirectory(ctx, tx)
	if err != nil {
		return nil, err
	}
	bindings, err := tx.QueryContext(ctx, `SELECT a.id,COALESCE(b.assignment,''),COALESCE(c.id,0),COALESCE(c.name,''),COALESCE(c.code,''),COALESCE(c.remark,''),COALESCE(c.revision,0),COALESCE(src.origin_hash,'')
	 FROM jsonb_to_recordset($1::jsonb) a(id bigint)
	 LEFT JOIN LATERAL provider_profit_account_at(a.id,statement_timestamp()) b ON true
	 LEFT JOIN provider_profit_account_source_history src ON src.account_id=a.id AND src.effective_until IS NULL
	 LEFT JOIN upstream_channels c ON c.id=b.channel_id`, string(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = bindings.Close() }()
	channels := map[int64]service.UpstreamChannel{}
	assignments := map[int64]string{}
	origins := map[int64]string{}
	for bindings.Next() {
		var id int64
		var c service.UpstreamChannel
		var assignment, origin string
		if err = bindings.Scan(&id, &assignment, &c.ID, &c.Name, &c.Code, &c.Remark, &c.Revision, &origin); err != nil {
			return nil, err
		}
		if assignment != "" {
			assignments[id] = assignment
		}
		origins[id] = origin
		if c.ID != 0 {
			channels[id] = c
		}
	}
	if err = bindings.Err(); err != nil {
		return nil, err
	}
	oauthChannels, err := profitOAuthChannels(ctx, tx, string(raw))
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		a := &accounts[i]
		a.URLKey = a.Key
		a.URLAttribution = a.Attribution
		a.Assignment = "unbound_url"
		if strings.HasPrefix(a.Key, "unknown:") {
			a.Assignment = "unbound_unknown"
		}
		recorded := assignments[a.ID]
		// A unique site key may own a URL that has no channel, including after a
		// manual unlink. An explicit or imported channel is left untouched.
		// Tenant paths and other non-root addresses never enter the directory.
		if p, ok := directory[service.ProviderProfitSiteKey(a.URLKey)]; ok {
			a.URLChannelID, a.URLOriginHash = &p.ChannelID, p.OriginHash
			if service.ProfitURLMayClaim(recorded, origins[a.ID], p.OriginHash) {
				recorded = "provider_url"
				assignments[a.ID] = recorded
				channels[a.ID] = service.UpstreamChannel{ID: p.ChannelID, Name: p.Name, Code: p.Code, Remark: p.Remark, Revision: p.Revision}
			}
		}
		if _, linked := channels[a.ID]; !linked && service.ProfitOAuthMayClaim(a.Key, recorded) {
			if c, ok := oauthChannels[a.ID]; ok {
				recorded = "provider_import"
				assignments[a.ID] = recorded
				channels[a.ID] = c
				a.OAuthChannelID = &c.ID
			}
		}
		if assignment, ok := assignments[a.ID]; ok {
			a.Assignment = assignment
		}
		if c, ok := channels[a.ID]; ok {
			a.Key = fmt.Sprintf("channel:%d", c.ID)
			a.ChannelID = c.ID
			a.ChannelName = c.Name
			a.ChannelCode = c.Code
			a.ChannelRemark = c.Remark
			a.ChannelRevision = c.Revision
			a.Attribution = "current_" + a.Assignment + "_channel"
		}
	}
	return json.Marshal(accounts)
}

// profitOAuthChannels resolves provider_binding for accounts that have no
// stable URL. It does not parse a host out of a dynamic or tenant address.
func profitOAuthChannels(ctx context.Context, tx *sql.Tx, raw string) (map[int64]service.UpstreamChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,c.id,c.name,c.code,c.remark,c.revision
		FROM jsonb_to_recordset($1::jsonb) ids(id bigint)
		JOIN accounts a ON a.id=ids.id
		JOIN provider_profit_links l ON l.provider_id=provider_profit_positive_id(a.extra#>>'{provider_binding,provider_id}')
		JOIN upstream_channels c ON c.id=l.channel_id
		WHERE a.extra ? 'provider_binding'`, raw)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]service.UpstreamChannel{}
	for rows.Next() {
		var id int64
		var c service.UpstreamChannel
		if err = rows.Scan(&id, &c.ID, &c.Name, &c.Code, &c.Remark, &c.Revision); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

const profitOperationsCohort = `accounts_config AS (
 SELECT * FROM jsonb_to_recordset($3::jsonb) AS c(id bigint,name text,key text,attribution text,updated_at timestamptz,deleted boolean,multiplier numeric,cost_basis text,schedulable boolean,concurrency integer,status text,url_key text,url_attribution text,url_channel_id bigint,url_origin_hash text,channel_id bigint,channel_name text,channel_code text,channel_remark text,channel_revision bigint,assignment text,oauth_channel_id bigint,account_type text)
), cohort AS (
 SELECT * FROM accounts_config WHERE ($4='' OR key=$4) AND ($5::bigint=0 OR id=$5) AND ($11::bigint=0 OR channel_id=$11)
)`

// A captured unknown/unsupported snapshot never falls back to today's account
// settings. The multiplier already includes New API's request-target x1/x7.
const profitRequestCostMultiplierSQL = `CASE WHEN u.procurement_cost_snapshot->>'version'='2' AND u.procurement_cost_snapshot->>'basis'='account_usage' THEN
 CASE WHEN jsonb_typeof(u.procurement_cost_snapshot->'cost_multiplier')='number' THEN (u.procurement_cost_snapshot->>'cost_multiplier')::numeric END
 WHEN u.procurement_cost_snapshot IS NOT NULL THEN
 CASE WHEN u.procurement_cost_snapshot->>'version'='1'
  AND u.procurement_cost_snapshot->>'basis'='total_cost'
  AND u.procurement_cost_snapshot->>'account_type'='gateway'
  AND u.procurement_cost_snapshot->>'provider' IN ('new_api','sub2api')
  AND u.procurement_cost_snapshot->>'target_platform'=u.platform
  AND u.platform<>'' AND u.platform<>'composite'
  AND jsonb_typeof(u.procurement_cost_snapshot->'cost_multiplier')='number'
 THEN CASE WHEN (u.procurement_cost_snapshot->>'cost_multiplier')::numeric BETWEEN 0 AND 1000000
  THEN (u.procurement_cost_snapshot->>'cost_multiplier')::numeric END END
 WHEN c.account_type='gateway' THEN NULL ELSE c.multiplier END`

// Join the complete projection before applying the selected cohort. Joining only
// cohort would turn every configured account outside it into account_missing.
const profitOperationsBase = `WITH ` + profitOperationsCohort + `, event_rows AS (
 SELECT u.account_id,u.requested_model,u.model,u.upstream_model,u.total_cost,u.actual_cost,u.stream,u.first_token_ms,
 u.account_stats_cost,u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_creation_tokens,u.duration_ms,u.billing_type,u.created_at,u.queue_timing,u.queue_wait_ms,
 COALESCE('channel:'||owner.channel_id::text,c.url_key,'unknown:account_missing') AS upstream_key,
 CASE WHEN e.channel_id IS NOT NULL OR h.channel_id IS NOT NULL THEN 'historical_channel' WHEN legacy.channel_id IS NOT NULL THEN 'legacy_channel_estimate' WHEN owner.channel_id IS NOT NULL THEN 'provider_url' ELSE COALESCE(c.url_attribution,'account_missing') END AS attribution,
 c.name AS account_name,c.updated_at AS config_updated_at,c.deleted,` + profitRequestCostMultiplierSQL + ` AS multiplier,
 u.procurement_cost_snapshot IS NOT NULL AS has_procurement_snapshot,
 owner.channel_id AS explicit_channel_id,ch.name AS channel_name,ch.code AS channel_code,ch.remark AS channel_remark,ch.revision AS channel_revision,p.id AS provider_id,p.site_type AS provider_site_type,
 CASE WHEN snap.amount IS NOT NULL THEN snap.amount
      WHEN $18::bool AND jsonb_typeof(p.metrics->'today_spend')='number' AND p.observed_at>=$12::timestamptz AND p.observed_at<$13::timestamptz THEN (p.metrics->>'today_spend')::numeric END AS wallet_spend,
 CASE WHEN snap.amount IS NOT NULL AND snap.currency IN ('USD','CNY','quota_units') THEN snap.currency
      WHEN $18::bool AND p.observed_at>=$12::timestamptz AND p.observed_at<$13::timestamptz AND p.metrics->>'currency' IN ('USD','CNY','quota_units') THEN p.metrics->>'currency' ELSE '' END AS wallet_currency,
 CASE WHEN snap.amount IS NOT NULL THEN snap.observed_at
      WHEN $18::bool AND jsonb_typeof(p.metrics->'today_spend')='number' AND p.observed_at>=$12::timestamptz AND p.observed_at<$13::timestamptz THEN p.observed_at END AS wallet_observed_at,
 CASE WHEN snap.amount IS NULL THEN NULLIF(snap.last_error,'') END AS wallet_error,
 CASE WHEN snap.amount IS NULL THEN snap.last_error_at END AS wallet_error_at,
 COALESCE(CASE WHEN h.channel_id IS NOT NULL THEN h.assignment END,
  CASE WHEN h.assignment='manual_unlinked' AND owner.channel_id IS NOT NULL THEN CASE WHEN c.url_key='unknown:oauth_dynamic_endpoint' THEN 'provider_import' ELSE 'provider_url' END END,
  h.assignment,
  CASE WHEN legacy.channel_id IS NOT NULL THEN 'explicit' WHEN owner.channel_id IS NOT NULL AND c.url_key='unknown:oauth_dynamic_endpoint' THEN 'provider_import' WHEN owner.channel_id IS NOT NULL THEN 'provider_url' WHEN c.url_key IS NULL OR c.url_key LIKE 'unknown:%' THEN 'unbound_unknown' ELSE 'unbound_url' END) AS assignment,
 e.amount AS upstream_reported,e.settled_amount AS upstream_debit,
 CASE WHEN u.procurement_cost_snapshot IS NOT NULL THEN 'request_procurement_snapshot' ELSE COALESCE(c.cost_basis,'account_missing') END AS cost_basis,
 COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,
 u.total_cost AS cost_base
 FROM usage_logs u LEFT JOIN accounts_config c ON c.id=u.account_id
 LEFT JOIN provider_profit_charges e ON e.usage_log_id=u.id AND e.account_id=u.account_id AND e.local_created_at=u.created_at
 -- Resolve only channel and assignment here. Joining the timelines directly
 -- allows PostgreSQL to hash them once for the report window. A manual unlink
 -- suppresses imported history but can still fall back to the current URL below.
 -- Preserve the ownership lookup's multiplicity: manual rows retain all source
 -- and channel matches; imported rows retain only matching source/channel pairs.
 LEFT JOIN provider_profit_manual_history manual ON manual.account_id=u.account_id
  AND manual.effective_from<=u.created_at AND (manual.effective_until IS NULL OR u.created_at<manual.effective_until)
 LEFT JOIN provider_profit_account_source_history manual_src ON manual.account_id IS NOT NULL AND manual_src.account_id=u.account_id
  AND manual_src.effective_from<=u.created_at AND (manual_src.effective_until IS NULL OR u.created_at<manual_src.effective_until)
 LEFT JOIN provider_profit_channel_history manual_owner ON manual.account_id IS NOT NULL AND manual_owner.channel_id=manual.channel_id
  AND manual_owner.effective_from<=u.created_at AND (manual_owner.effective_until IS NULL OR u.created_at<manual_owner.effective_until)
 LEFT JOIN (provider_profit_account_source_history imported_src
  JOIN provider_profit_channel_history imported ON imported.provider_id=imported_src.provider_id AND imported.identity_from=imported_src.identity_from)
  ON manual.account_id IS NULL AND imported_src.account_id=u.account_id
  AND imported_src.effective_from<=u.created_at AND (imported_src.effective_until IS NULL OR u.created_at<imported_src.effective_until)
  AND imported.effective_from<=u.created_at AND (imported.effective_until IS NULL OR u.created_at<imported.effective_until)
 CROSS JOIN LATERAL (SELECT COALESCE(manual.channel_id,imported.channel_id) AS channel_id,
  CASE WHEN manual.channel_id IS NOT NULL THEN 'explicit' WHEN manual.account_id IS NOT NULL THEN 'manual_unlinked'
       WHEN imported.channel_id IS NOT NULL THEN 'provider_import' END AS assignment) h
 LEFT JOIN provider_profit_manual_history legacy ON legacy.account_id=u.account_id AND legacy.legacy_at_migration AND u.created_at<legacy.effective_from
 LEFT JOIN provider_profit_account_source_history src ON c.url_channel_id IS NOT NULL AND src.account_id=u.account_id AND src.effective_from<=u.created_at AND (src.effective_until IS NULL OR src.effective_until>u.created_at)
 CROSS JOIN LATERAL (SELECT COALESCE(e.channel_id,h.channel_id,legacy.channel_id,
   CASE
     WHEN COALESCE(h.assignment,'') IN ('','manual_unlinked') AND c.url_key='unknown:oauth_dynamic_endpoint' THEN c.oauth_channel_id
     WHEN h.assignment IS NULL AND (src.origin_hash IS NULL OR src.origin_hash=c.url_origin_hash) THEN c.url_channel_id
     WHEN h.assignment='manual_unlinked' THEN c.url_channel_id
   END) AS channel_id) owner
 LEFT JOIN upstream_channels ch ON ch.id=owner.channel_id
 LEFT JOIN provider_profit_links pl ON pl.channel_id=owner.channel_id
 LEFT JOIN mature_providers p ON p.id=pl.provider_id AND pl.cost_mode='wallet'
 LEFT JOIN provider_spend_snapshots snap ON snap.provider_id=p.id AND snap.window_from=$1 AND snap.window_until=$2
 WHERE u.created_at >= $1 AND u.created_at < $2 AND ($5::bigint=0 OR u.account_id=$5) AND ($6::bigint=0 OR u.group_id=$6)
 AND ($7='' OR CASE $8 WHEN 'public' THEN COALESCE(NULLIF(u.requested_model,''),u.model) WHEN 'recorded' THEN u.model WHEN 'upstream' THEN u.upstream_model END=$7)
), scoped AS (
 SELECT * FROM event_rows WHERE ($11::bigint=0 OR explicit_channel_id=$11) AND ($4='' OR upstream_key=$4)
), bases AS (
 SELECT *,CASE WHEN cost_base>=0 AND cost_base<'Infinity'::numeric AND multiplier>=0 AND multiplier<'Infinity'::numeric THEN cost_base*multiplier END AS estimated,
 CASE WHEN total_cost>=0 AND total_cost<'Infinity'::numeric THEN total_cost END AS standard,
 CASE WHEN actual_cost>=0 AND actual_cost<'Infinity'::numeric THEN actual_cost END AS income,
 CASE WHEN stream AND first_token_ms>0 AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0 OR input_tokens>0 OR output_tokens>0 OR cache_read_tokens>0 OR cache_creation_tokens>0) THEN first_token_ms END AS ttft,
 CASE WHEN duration_ms>0 AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0 OR input_tokens>0 OR output_tokens>0 OR cache_read_tokens>0 OR cache_creation_tokens>0) THEN duration_ms END AS measured_duration
 FROM scoped
), priced AS (
 -- Bound channels do not take a per-request bill. Their report cost is the
 -- wallet snapshot stored for this exact window. Today's card is only a
 -- fallback when that window is the Asia/Shanghai consumption day. Unbound
 -- channels keep the multiplier estimate. A missing estimate stays NULL.
 SELECT *,CASE WHEN provider_id IS NULL THEN estimated END AS effective_cost FROM bases
)`

// One stored window amount per provider, never a sum across that provider's requests.
const profitWalletColumns = `,
 COALESCE(count(provider_id)=count(*) AND count(*)>0 AND count(DISTINCT provider_id)=1 AND max(wallet_spend) IS NOT NULL AND max(NULLIF(wallet_currency,''))='USD', false) AS wallet_usd_ok,
 COALESCE(count(provider_id)=count(*) AND count(*)>0 AND count(DISTINCT provider_id)=1 AND max(wallet_spend) IS NOT NULL AND COALESCE(max(NULLIF(wallet_currency,'')),'')<>'' AND max(NULLIF(wallet_currency,''))<>'USD', false) AS wallet_other_currency,
 CASE WHEN count(DISTINCT provider_id)=1 AND count(provider_id)=count(*) THEN max(wallet_spend) END AS wallet_spend,
 CASE WHEN count(DISTINCT provider_id)=1 AND count(provider_id)=count(*) THEN NULLIF(max(NULLIF(wallet_currency,'')),'') END AS wallet_currency,
 CASE WHEN count(DISTINCT provider_id)=1 AND count(provider_id)=count(*) THEN max(wallet_observed_at) END AS wallet_observed_at,
 CASE WHEN count(DISTINCT provider_id)=1 AND count(provider_id)=count(*) THEN max(wallet_error) END AS wallet_error,
 CASE WHEN count(DISTINCT provider_id)=1 AND count(provider_id)=count(*) THEN max(wallet_error_at) END AS wallet_error_at`

var profitOperationsAggregate = `count(*) AS requests,
 CASE WHEN count(*)=0 THEN 0 WHEN count(standard)=count(*) THEN sum(standard) END AS standard_charge,
 CASE WHEN count(*)=0 THEN 0 WHEN count(income)=count(*) THEN sum(income) END AS request_income,
 count(*) FILTER(WHERE standard IS NULL) AS unknown_standard_requests,
 count(*) FILTER(WHERE income IS NULL) AS unknown_income_requests,
 CASE WHEN count(*)=0 THEN 0 WHEN count(estimated)=count(*) THEN sum(estimated) END AS estimated_cost,
 CASE WHEN count(*)=0 THEN 0 WHEN count(estimated)=count(*) AND count(income)=count(*) THEN sum(income)-sum(estimated) END AS estimated_profit,
 CASE WHEN count(estimated)=count(*) AND count(income)=count(*) AND sum(income)>0 THEN (sum(income)-sum(estimated))/sum(income) END AS margin,
 sum(estimated) AS covered_estimated_cost,
 sum(income) FILTER(WHERE estimated IS NOT NULL) AS covered_income,
 count(estimated) AS cost_covered_requests,count(*) FILTER(WHERE estimated IS NULL) AS unknown_cost_requests,
 count(provider_id) AS supplier_requests,
 count(upstream_reported) FILTER (WHERE provider_id IS NOT NULL) AS bill_requests,
 count(estimated) FILTER (WHERE provider_id IS NULL) AS estimated_requests,
 CASE WHEN count(*)=0 THEN 0 WHEN count(effective_cost)=count(*) THEN sum(effective_cost) END AS upstream_cost,
 CASE WHEN count(*)=0 THEN 0 WHEN count(effective_cost)=count(*) AND count(income)=count(*) THEN sum(income)-sum(effective_cost) END AS gross_profit,
 CASE WHEN count(effective_cost)=count(*) AND count(income)=count(*) AND sum(income)>0 THEN (sum(income)-sum(effective_cost))/sum(income) END AS gross_margin,
 count(upstream_reported) AS reported_usage_requests,sum(upstream_reported) AS reported_usage_cost,
 count(upstream_debit) AS reconciled_requests,sum(upstream_debit) AS covered_reconciled_cost,
 CASE WHEN count(upstream_debit)=count(*) THEN sum(upstream_debit) END AS reconciled_cost,
 CASE WHEN count(upstream_debit)=count(*) AND count(income)=count(*) THEN sum(income)-sum(upstream_debit) END AS reconciled_profit,
 count(*) FILTER(WHERE attribution IN ('current_config','historical_channel','legacy_channel_estimate','provider_url')) AS attributed_requests,
 count(*) FILTER(WHERE attribution NOT IN ('current_config','historical_channel','legacy_channel_estimate','provider_url')) AS unknown_upstream_requests,
 count(*) FILTER(WHERE cost_basis='current_saved_multiplier') AS saved_multiplier_requests,
 count(*) FILTER(WHERE cost_basis='current_default_0.1') AS default_multiplier_requests,
 count(*) FILTER(WHERE account_stats_cost IS NOT NULL) AS saved_stats_base_requests,
 count(*) FILTER(WHERE account_stats_cost IS NULL) AS standard_base_fallback_requests,
 count(*) FILTER(WHERE billing_type=1) AS subscription_requests,
 count(*) FILTER(WHERE deleted) AS deleted_account_requests,
 count(*) FILTER(WHERE ttft IS NOT NULL) AS samples,
 count(*) FILTER(WHERE stream AND ttft IS NULL) AS unknown_samples,
 count(*) FILTER(WHERE actual_cost=0 AND total_cost=0 AND COALESCE(account_stats_cost,0)=0 AND input_tokens=0 AND output_tokens=0 AND cache_read_tokens=0 AND cache_creation_tokens=0) AS unmetered_zero_records,
 percentile_cont(.5) WITHIN GROUP(ORDER BY ttft) AS p50_ms,
 percentile_cont(.9) WITHIN GROUP(ORDER BY ttft) AS p90_ms,
 percentile_cont(.95) WITHIN GROUP(ORDER BY ttft) AS p95_ms,
 percentile_cont(.9) WITHIN GROUP(ORDER BY measured_duration) AS duration_p90_ms,
 max(created_at) AS latest_sample_at,min(config_updated_at) AS oldest_config_updated_at,max(config_updated_at) AS latest_config_updated_at,
 jsonb_agg(DISTINCT cost_basis) AS cost_bases` + queueInclusiveOperationsAggregate("measured_duration")

func (r *channelRepository) ReadProfitOperations(ctx context.Context, f service.UpstreamOperationsFilter) (*service.ProfitOperationsSnapshot, error) {
	if err := service.NormalizeProfitOperationsFilter(&f); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if f.AccountUsage {
		return readAccountUsageProfit(ctx, tx, f)
	}
	raw, err := profitOperationsAccounts(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	size, offset := f.PageSize, (f.Page-1)*f.PageSize
	if service.CollectProfitRows(ctx) {
		size, offset = 10001, 0
	}
	args := []any{f.From, f.Until, string(raw), f.Upstream, f.AccountID, f.GroupID, f.Model, f.ModelScope, size, offset, f.ChannelID}
	dimension, name := `upstream_key`, `COALESCE(NULLIF(max(channel_name),''),upstream_key)`
	channelFields := `,COALESCE(max(explicit_channel_id),0) AS channel_id,max(provider_id) AS provider_id,max(provider_site_type) AS provider_site_type,max(channel_code) AS channel_code,max(channel_remark) AS channel_remark,max(channel_revision) AS channel_revision,COALESCE(max(assignment),'unbound_unknown') AS assignment`
	switch f.Dimension {
	case "account":
		channelFields = ""
		dimension = `account_id::text`
		name = `COALESCE(max(account_name),'Deleted account') || ' #' || account_id::text`
	case "model":
		channelFields = ""
		dimension = `CASE $8 WHEN 'public' THEN public_model WHEN 'recorded' THEN model WHEN 'upstream' THEN COALESCE(upstream_model,'(unknown upstream model)') END`
		name = dimension
	}
	// Saved-bill freshness stays on the row for diagnostics. It does not price the report.
	now := time.Now()
	dayStart, dayEnd := service.ProfitConsumptionDay(now)
	windowIsToday := service.ProfitWindowUsesWalletToday(f.From, f.Until, now)
	subset := f.AccountID != 0 || f.GroupID != 0 || strings.TrimSpace(f.Model) != ""
	upstreamDimension := f.Dimension == "upstream"
	// $14 summary may use the window wallet, $15 subset, $16 rows may show it,
	// $17 upstream dimension, $18 this window is the consumption day (today card fallback and wallet_today label).
	queryArgs := append(append([]any{}, args...), dayStart, dayEnd, !subset, subset, !subset && upstreamDimension, upstreamDimension, windowIsToday)
	syncFrom := ""
	billJoin := ""
	billField := ""
	order := f.Sort + " " + f.Direction + " NULLS LAST,key ASC"
	if channelFields != "" {
		syncFrom = `, bill_sync AS (
 SELECT provider_id, max(mark_at) AS bills_synced_at FROM (
  SELECT provider_id, synced_at AS mark_at FROM provider_profit_sync_runs
  WHERE window_from < $2 AND window_until > $1
  UNION ALL
  SELECT provider_id, captured_at FROM provider_profit_charges
  WHERE local_created_at >= $1 AND local_created_at < $2
 ) marks GROUP BY provider_id)`
		billJoin = ` LEFT JOIN bill_sync ON bill_sync.provider_id = g.provider_id`
		billField = `,'bills_synced_at',to_jsonb(bill_sync.bills_synced_at)`
	}
	// A missing wallet stays unavailable. wallet_not_split is only for a usable
	// window amount that must not be divided across account or model rows.
	costSource := `CASE WHEN g.supplier_requests=0 THEN 'estimate' WHEN NOT g.wallet_usd_ok AND NOT g.wallet_other_currency THEN 'wallet_unavailable' WHEN $16::bool AND g.wallet_usd_ok AND $18::bool THEN 'wallet_today' WHEN $16::bool AND g.wallet_usd_ok THEN 'wallet_window' WHEN $16::bool AND g.wallet_other_currency THEN 'wallet_currency' WHEN NOT $16::bool AND ($15::bool OR NOT $17::bool) THEN 'wallet_not_split' ELSE 'wallet_unavailable' END`
	// Sort and direction are strictly validated before SQL construction.
	// Rank after the wallet override so upstream_cost sorts by the displayed amount.
	rowInner := `SELECT (to_jsonb(g) || jsonb_build_object(
 'upstream_cost',CASE WHEN $16::bool AND g.wallet_usd_ok THEN to_jsonb(g.wallet_spend) ELSE to_jsonb(g.upstream_cost) END,
 'gross_profit',CASE WHEN $16::bool AND g.wallet_usd_ok AND g.request_income IS NOT NULL THEN to_jsonb(g.request_income-g.wallet_spend) ELSE to_jsonb(g.gross_profit) END,
 'gross_margin',CASE WHEN $16::bool AND g.wallet_usd_ok AND g.request_income>0 THEN to_jsonb((g.request_income-g.wallet_spend)/g.request_income) ELSE to_jsonb(g.gross_margin) END,
 'cost_source',to_jsonb(` + costSource + `),'wallet_currency',to_jsonb(g.wallet_currency),'wallet_observed_at',to_jsonb(g.wallet_observed_at),'wallet_error',to_jsonb(g.wallet_error),'wallet_error_at',to_jsonb(g.wallet_error_at)` + billField + `)) AS merged,
 g.requests,g.standard_charge,g.request_income,g.estimated_cost,g.estimated_profit,g.margin,
 CASE WHEN $16::bool AND g.wallet_usd_ok THEN g.wallet_spend ELSE g.upstream_cost END AS upstream_cost,
 CASE WHEN $16::bool AND g.wallet_usd_ok AND g.request_income IS NOT NULL THEN g.request_income-g.wallet_spend ELSE g.gross_profit END AS gross_profit,
 CASE WHEN $16::bool AND g.wallet_usd_ok AND g.request_income>0 THEN (g.request_income-g.wallet_spend)/g.request_income ELSE g.gross_margin END AS gross_margin,
 g.name,g.key FROM grouped g` + billJoin
	rowQuery := `SELECT merged, row_number() OVER (ORDER BY ` + order + `) AS ord FROM (` + rowInner + `) ordered ORDER BY ord`
	query := profitOperationsBase + syncFrom + `, grouped AS (SELECT ` + dimension + ` AS key,` + name + ` AS name,` + profitOperationsAggregate + channelFields + profitWalletColumns + ` FROM priced GROUP BY ` + dimension + `), summary AS (SELECT ` + profitOperationsAggregate + ` FROM priced), channel_roll AS (
 SELECT COALESCE(explicit_channel_id,0) AS roll_channel,count(*) AS requests,count(provider_id) AS supplier_requests,count(effective_cost) AS estimate_rows,sum(effective_cost) AS estimate_sum` + profitWalletColumns + ` FROM priced GROUP BY COALESCE(explicit_channel_id,0)), wallet_total AS (
 SELECT COALESCE(sum(requests),0) AS requests,COALESCE(bool_or(supplier_requests>0 AND NOT wallet_usd_ok),false) AS blocked,COALESCE(bool_or(supplier_requests>0 AND wallet_other_currency),false) AS other_currency,COALESCE(bool_or(supplier_requests=0 AND estimate_rows IS DISTINCT FROM requests),false) AS estimate_incomplete,COALESCE(sum(wallet_spend) FILTER (WHERE supplier_requests>0 AND wallet_usd_ok),0) AS wallet_sum,COALESCE(sum(estimate_sum) FILTER (WHERE supplier_requests=0 AND estimate_rows=requests),0) AS estimate_sum FROM channel_roll)
 SELECT jsonb_build_object('summary',(SELECT to_jsonb(s) || jsonb_build_object(
 'cost_source',to_jsonb(CASE WHEN COALESCE(s.supplier_requests,0)=0 THEN 'estimate' WHEN w.blocked AND w.other_currency THEN 'wallet_currency' WHEN w.blocked OR w.estimate_incomplete THEN 'wallet_unavailable' WHEN $15::bool THEN 'wallet_not_split' WHEN s.supplier_requests<s.requests THEN 'mixed' WHEN $18::bool THEN 'wallet_today' ELSE 'wallet_window' END),
 'wallet_basis',to_jsonb(CASE WHEN $18::bool THEN 'wallet_today' ELSE 'wallet_window' END),
 'upstream_cost',CASE WHEN $14::bool AND NOT $15::bool THEN CASE WHEN w.requests=0 THEN to_jsonb(0::numeric) WHEN w.blocked OR w.estimate_incomplete THEN 'null'::jsonb ELSE to_jsonb(w.wallet_sum+w.estimate_sum) END ELSE to_jsonb(s.upstream_cost) END,
 'gross_profit',CASE WHEN $14::bool AND NOT $15::bool THEN CASE WHEN w.requests=0 THEN to_jsonb(0::numeric) WHEN NOT (w.blocked OR w.estimate_incomplete) AND s.request_income IS NOT NULL THEN to_jsonb(s.request_income-(w.wallet_sum+w.estimate_sum)) ELSE 'null'::jsonb END ELSE to_jsonb(s.gross_profit) END,
 'gross_margin',CASE WHEN $14::bool AND NOT $15::bool THEN CASE WHEN NOT (w.blocked OR w.estimate_incomplete) AND w.requests>0 AND s.request_income>0 THEN to_jsonb((s.request_income-(w.wallet_sum+w.estimate_sum))/s.request_income) ELSE 'null'::jsonb END ELSE to_jsonb(s.gross_margin) END
 ) FROM summary s CROSS JOIN wallet_total w),'total',(SELECT count(*) FROM grouped),
 'rows',COALESCE((SELECT jsonb_agg(merged ORDER BY ord) FROM (` + rowQuery + ` LIMIT $9 OFFSET $10) page),'[]'::jsonb),
 'accounts',COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis,url_key,channel_id,channel_name,channel_code,channel_remark,channel_revision,assignment FROM accounts_config JOIN (
   SELECT DISTINCT account_id AS id FROM priced
   UNION
   SELECT id FROM cohort WHERE $5::bigint>0
 ) report_accounts USING (id) ORDER BY id LIMIT 100)c),'[]'::jsonb),
 'members',COALESCE((SELECT jsonb_agg(to_jsonb(m)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis,url_key,channel_id,channel_name,channel_code,channel_remark,channel_revision,assignment,schedulable,concurrency,status FROM cohort c WHERE ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) ORDER BY id LIMIT $9 OFFSET $10)m),'[]'::jsonb),
 'member_total',(SELECT count(*) FROM cohort c WHERE ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog',COALESCE((SELECT jsonb_agg(to_jsonb(g)) FROM (SELECT key AS key,COALESCE(NULLIF(max(channel_name),''),key) AS name,max(channel_id) AS channel_id,max(channel_code) AS channel_code,max(channel_remark) AS channel_remark,max(channel_revision) AS channel_revision,max(assignment) AS assignment,count(*) AS accounts FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) GROUP BY key ORDER BY key LIMIT $9 OFFSET $10)g),'[]'::jsonb),
 'capacity',(SELECT jsonb_build_object('accounts',count(*),'schedulable',count(*) FILTER(WHERE schedulable AND status='active'),'configured_concurrency',COALESCE(sum(concurrency),0)) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog_total',(SELECT count(DISTINCT key) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'account_details_limit',100,'historical_upstream_snapshot_requests',(SELECT count(*) FROM priced WHERE attribution='historical_channel'),'historical_cost_multiplier_snapshot_requests',(SELECT count(*) FROM priced WHERE has_procurement_snapshot AND multiplier IS NOT NULL),
 'wallet_synced_at',(SELECT to_jsonb(max(observed_at)) FROM provider_spend_snapshots WHERE window_from=$1 AND window_until=$2 AND amount IS NOT NULL),
 'cost_formula','request procurement estimate: persisted usage_logs.total_cost * captured procurement multiplier once; unknown gateway snapshots stay unknown; legacy estimate: total_cost * current extra.cost_multiplier (default 0.1); bound channel wallet accounting is unchanged and never split across requests',
 'income_source','persisted usage_logs.actual_cost; subscription usage valuation, not cash receipts; no guarantee of debit in simple mode',
 'standard_source','persisted usage_logs.total_cost; historical model/cache/per-request pricing is not recalculated',
 'account_rate_multiplier_used',false,'error_rate',NULL)`
	out := &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Scope: f, QueriedAt: time.Now().UTC(), Attribution: "historical_channel_then_current_provider_url; manual_unlink_falls_back_to_unique_site; oauth_uses_provider_binding; supplier_uses_window_spend; unbound_uses_estimates"}, Currency: "USD billing units; bound cost uses the window wallet currency and only USD is subtracted"}
	if err = tx.QueryRowContext(ctx, query, queryArgs...).Scan(&out.Usage); err != nil {
		return nil, fmt.Errorf("read upstream usage: %w", err)
	}
	profitErrors := strings.Replace(upstreamOperationsErrors, upstreamOperationsCohort, profitOperationsCohort, 1)
	profitErrors = strings.Replace(profitErrors, "params AS (SELECT $8::text AS model_scope)", "params AS (SELECT $8::text AS model_scope,$9::int AS page_size,$10::int AS row_offset,$11::bigint AS selected_channel_id)", 1)
	profitErrors = strings.Replace(profitErrors, "WHERE e.created_at >= $1", "WHERE ($11::bigint=0 OR e.account_id IN (SELECT id FROM cohort)) AND e.created_at >= $1", 1)
	out.Errors = readUpstreamSection(ctx, tx, profitErrors, args...)
	if f.Model != "" && f.ModelScope != "upstream" {
		out.Quality = service.ChannelOperationsSection{Status: "unsupported_scope", Data: json.RawMessage("null"), Error: "Quality uses probe model identity; public/recorded aliases cannot be inferred", QueriedAt: out.QueriedAt}
	} else {
		// Reuse canonical complete frozen plan×round×model classification. Only the
		// account cohort changes; filtering by model still follows round integrity.
		classifier := channelOperationsQualityBase[strings.Index(channelOperationsQualityBase, "), model_sets AS ("):]
		quality := `WITH ` + profitOperationsCohort + `, scoped AS (
          SELECT p.account_id,r.id,r.plan_id,r.status,r.created_at,r.pelican_config,r.quality_round_id,r.quality_action,r.quality_judgment,r.error_message,NULLIF(r.pelican_config->>'model_id','') AS measured_model
          FROM scheduled_test_results r JOIN scheduled_test_plans p ON p.id=r.plan_id
          WHERE p.account_id IN (SELECT id FROM cohort) AND r.created_at >= $1 AND r.created_at < $2
          AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=p.account_id AND ag.group_id=$6))
          AND jsonb_typeof(r.pelican_config->'quality')='object' AND $8::text IS NOT NULL
        ` + classifier
		quality += ` SELECT jsonb_build_object('source','scheduled_test_results complete frozen plan+round+model; current account cohort', 'summary',(SELECT jsonb_build_object('rounds',count(*),'passed',count(*) FILTER(WHERE outcome='passed'),'degraded',count(*) FILTER(WHERE outcome='degraded'),'inconclusive',count(*) FILTER(WHERE outcome='inconclusive')) FROM classified),'rows',COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT * FROM classified ORDER BY id DESC LIMIT $9 OFFSET $10)c),'[]'::jsonb),'total',(SELECT count(*) FROM classified))`
		out.Quality = readUpstreamSection(ctx, tx, quality, args...)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

var _ service.ProfitOperationsRepository = (*channelRepository)(nil)
