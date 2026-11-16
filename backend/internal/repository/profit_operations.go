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
	}
	if err = json.Unmarshal(raw, &accounts); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,extra->'cost_multiplier' FROM accounts WHERE id IN (SELECT id FROM jsonb_to_recordset($1::jsonb) c(id bigint))`, string(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	costs := map[int64]any{}
	for rows.Next() {
		var id int64
		var value []byte
		if err = rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		var cost any
		if len(value) > 0 {
			if err = json.Unmarshal(value, &cost); err != nil {
				return nil, err
			}
		}
		costs[id] = cost
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
	for i := range accounts {
		a := &accounts[i]
		a.URLKey = a.Key
		a.URLAttribution = a.Attribution
		a.Assignment = "unbound_url"
		if strings.HasPrefix(a.Key, "unknown:") {
			a.Assignment = "unbound_unknown"
		}
		// Keep URL eligibility separate from today's membership. A later manual
		// edit must not assign earlier events to the new manual channel.
		if p, ok := directory[service.ProviderProfitSiteKey(a.URLKey)]; ok {
			a.URLChannelID, a.URLOriginHash = &p.ChannelID, p.OriginHash
			if assignments[a.ID] == "" && (origins[a.ID] == "" || origins[a.ID] == p.OriginHash) {
				assignments[a.ID] = "provider_url"
				channels[a.ID] = service.UpstreamChannel{ID: p.ChannelID, Name: p.Name, Code: p.Code, Remark: p.Remark, Revision: p.Revision}
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

const profitOperationsCohort = `accounts_config AS (
 SELECT * FROM jsonb_to_recordset($3::jsonb) AS c(id bigint,name text,key text,attribution text,updated_at timestamptz,deleted boolean,multiplier numeric,cost_basis text,schedulable boolean,concurrency integer,status text,url_key text,url_attribution text,url_channel_id bigint,url_origin_hash text,channel_id bigint,channel_name text,channel_code text,channel_remark text,channel_revision bigint,assignment text)
), cohort AS (
 SELECT * FROM accounts_config WHERE ($4='' OR key=$4) AND ($5::bigint=0 OR id=$5) AND ($11::bigint=0 OR channel_id=$11)
)`

// Join the complete projection before applying the selected cohort. Joining only
// cohort would turn every configured account outside it into account_missing.
const profitOperationsBase = `WITH ` + profitOperationsCohort + `, event_rows AS (
 SELECT u.account_id,u.requested_model,u.model,u.upstream_model,u.total_cost,u.actual_cost,u.stream,u.first_token_ms,
 u.account_stats_cost,u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_creation_tokens,u.duration_ms,u.billing_type,u.created_at,u.queue_timing,u.queue_wait_ms,
 COALESCE('channel:'||owner.channel_id::text,c.url_key,'unknown:account_missing') AS upstream_key,
 CASE WHEN e.channel_id IS NOT NULL OR h.channel_id IS NOT NULL THEN 'historical_channel' WHEN legacy.channel_id IS NOT NULL THEN 'legacy_channel_estimate' WHEN owner.channel_id IS NOT NULL THEN 'provider_url' ELSE COALESCE(c.url_attribution,'account_missing') END AS attribution,
 c.name AS account_name,c.updated_at AS config_updated_at,c.deleted,c.multiplier,owner.channel_id AS explicit_channel_id,ch.name AS channel_name,ch.code AS channel_code,ch.remark AS channel_remark,ch.revision AS channel_revision,pl.provider_id,p.site_type AS provider_site_type,
 COALESCE(h.assignment,CASE WHEN legacy.channel_id IS NOT NULL THEN 'explicit' WHEN owner.channel_id IS NOT NULL THEN 'provider_url' WHEN c.url_key IS NULL OR c.url_key LIKE 'unknown:%' THEN 'unbound_unknown' ELSE 'unbound_url' END) AS assignment,
 e.amount AS upstream_reported,e.settled_amount AS upstream_debit,COALESCE(c.cost_basis,'account_missing') AS cost_basis,
 COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,
 u.total_cost AS cost_base
 FROM usage_logs u LEFT JOIN accounts_config c ON c.id=u.account_id
 LEFT JOIN provider_profit_charges e ON e.usage_log_id=u.id AND e.account_id=u.account_id AND e.local_created_at=u.created_at
 LEFT JOIN LATERAL provider_profit_account_at(u.account_id,u.created_at) h ON true
 LEFT JOIN provider_profit_manual_history legacy ON legacy.account_id=u.account_id AND legacy.legacy_at_migration AND u.created_at<legacy.effective_from
 LEFT JOIN provider_profit_account_source_history src ON c.url_channel_id IS NOT NULL AND src.account_id=u.account_id AND src.effective_from<=u.created_at AND (src.effective_until IS NULL OR src.effective_until>u.created_at)
 CROSS JOIN LATERAL (SELECT COALESCE(e.channel_id,h.channel_id,legacy.channel_id,
   CASE WHEN h.assignment IS NULL AND (src.origin_hash IS NULL OR src.origin_hash=c.url_origin_hash) THEN c.url_channel_id END) AS channel_id) owner
 LEFT JOIN upstream_channels ch ON ch.id=owner.channel_id
 LEFT JOIN provider_profit_links pl ON pl.channel_id=owner.channel_id
 LEFT JOIN mature_providers p ON p.id=pl.provider_id
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
 -- Choose the accounting method by channel, never by receipt availability.
 -- A supplier's missing bill is NULL, even if this account has a multiplier.
 SELECT *,CASE WHEN provider_id IS NOT NULL THEN upstream_reported ELSE estimated END AS effective_cost FROM bases
)`

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
	raw, err := profitOperationsAccounts(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	args := []any{f.From, f.Until, string(raw), f.Upstream, f.AccountID, f.GroupID, f.Model, f.ModelScope, f.PageSize, (f.Page - 1) * f.PageSize, f.ChannelID}
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
	order := f.Sort + " " + f.Direction + " NULLS LAST,key ASC"
	// Sort and direction are strictly validated before SQL construction.
	query := profitOperationsBase + `, buckets AS (SELECT ` + dimension + ` AS key,` + name + ` AS name,` + profitOperationsAggregate + channelFields + ` FROM priced GROUP BY ` + dimension + `), summary AS (SELECT ` + profitOperationsAggregate + ` FROM priced)
 SELECT jsonb_build_object('summary',(SELECT to_jsonb(s) FROM summary s),'total',(SELECT count(*) FROM buckets),
 'rows',COALESCE((SELECT jsonb_agg(to_jsonb(b)) FROM (SELECT * FROM buckets ORDER BY ` + order + ` LIMIT $9 OFFSET $10)b),'[]'::jsonb),
 'accounts',COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis,url_key,channel_id,channel_name,channel_code,channel_remark,channel_revision,assignment FROM accounts_config WHERE id IN (SELECT account_id FROM scoped) OR ($5::bigint>0 AND id IN (SELECT id FROM cohort)) ORDER BY id LIMIT 100)c),'[]'::jsonb),
 'members',COALESCE((SELECT jsonb_agg(to_jsonb(m)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis,url_key,channel_id,channel_name,channel_code,channel_remark,channel_revision,assignment,schedulable,concurrency,status FROM cohort c WHERE ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) ORDER BY id LIMIT $9 OFFSET $10)m),'[]'::jsonb),
 'member_total',(SELECT count(*) FROM cohort c WHERE ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog',COALESCE((SELECT jsonb_agg(to_jsonb(g)) FROM (SELECT key AS key,COALESCE(NULLIF(max(channel_name),''),key) AS name,max(channel_id) AS channel_id,max(channel_code) AS channel_code,max(channel_remark) AS channel_remark,max(channel_revision) AS channel_revision,max(assignment) AS assignment,count(*) AS accounts FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) GROUP BY key ORDER BY key LIMIT $9 OFFSET $10)g),'[]'::jsonb),
 'capacity',(SELECT jsonb_build_object('accounts',count(*),'schedulable',count(*) FILTER(WHERE schedulable AND status='active'),'configured_concurrency',COALESCE(sum(concurrency),0)) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog_total',(SELECT count(DISTINCT key) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'account_details_limit',100,'historical_upstream_snapshot_requests',(SELECT count(*) FROM scoped WHERE attribution='historical_channel'),'historical_cost_multiplier_snapshot_requests',0,
 'cost_formula','supplier channel: matched upstream usage bill; unbound channel: usage_logs.total_cost * current extra.cost_multiplier (default 0.1)',
 'income_source','persisted usage_logs.actual_cost; subscription usage valuation, not cash receipts; no guarantee of debit in simple mode',
 'standard_source','persisted usage_logs.total_cost; historical model/cache/per-request pricing is not recalculated',
 'account_rate_multiplier_used',false,'error_rate',NULL)`
	out := &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Scope: f, QueriedAt: time.Now().UTC(), Attribution: "historical_channel_then_current_provider_url; supplier_uses_request_bills_only; unbound_uses_estimates"}, Currency: "USD billing units (no per-row currency snapshot)"}
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&out.Usage); err != nil {
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
