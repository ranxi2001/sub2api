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

// Profit reads the shared safe URL projection, then adds only current cost
// estimates in the same read-only snapshot. Endpoint resolution stays in channel operations.
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
	bindings, err := tx.QueryContext(ctx, `SELECT b.account_id,c.id,c.name,c.code,c.remark,c.revision FROM upstream_channel_bindings b JOIN upstream_channels c ON c.id=b.channel_id WHERE b.account_id IN (SELECT id FROM jsonb_to_recordset($1::jsonb) a(id bigint))`, string(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = bindings.Close() }()
	channels := map[int64]service.UpstreamChannel{}
	for bindings.Next() {
		var id int64
		var c service.UpstreamChannel
		if err = bindings.Scan(&id, &c.ID, &c.Name, &c.Code, &c.Remark, &c.Revision); err != nil {
			return nil, err
		}
		channels[id] = c
	}
	if err = bindings.Err(); err != nil {
		return nil, err
	}
	for i := range accounts {
		a := &accounts[i]
		a.URLKey = a.Key
		a.Assignment = "unbound_url"
		if strings.HasPrefix(a.Key, "unknown:") {
			a.Assignment = "unbound_unknown"
		}
		if c, ok := channels[a.ID]; ok {
			a.Key = fmt.Sprintf("channel:%d", c.ID)
			a.ChannelID = c.ID
			a.ChannelName = c.Name
			a.ChannelCode = c.Code
			a.ChannelRemark = c.Remark
			a.ChannelRevision = c.Revision
			a.Assignment = "explicit"
			a.Attribution = "current_explicit_channel"
		}
	}
	return json.Marshal(accounts)
}

const profitOperationsCohort = `accounts_config AS (
 SELECT * FROM jsonb_to_recordset($3::jsonb) AS c(id bigint,name text,key text,attribution text,updated_at timestamptz,deleted boolean,multiplier numeric,cost_basis text,schedulable boolean,concurrency integer,status text,url_key text,channel_id bigint,channel_name text,channel_code text,channel_remark text,channel_revision bigint,assignment text)
), cohort AS (
 SELECT * FROM accounts_config WHERE ($4='' OR key=$4) AND ($5::bigint=0 OR id=$5) AND ($11::bigint=0 OR channel_id=$11)
)`

// Join the complete projection before applying the selected cohort. Joining only
// cohort would turn every configured account outside it into account_missing.
const profitOperationsBase = `WITH ` + profitOperationsCohort + `, scoped AS (
 SELECT u.account_id,u.requested_model,u.model,u.upstream_model,u.total_cost,u.actual_cost,u.stream,u.first_token_ms,
 u.account_stats_cost,u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_creation_tokens,u.duration_ms,u.billing_type,u.created_at,u.queue_timing,u.queue_wait_ms,
 COALESCE(c.key,'unknown:account_missing') AS upstream_key,
 COALESCE(c.attribution,'account_missing') AS attribution,c.name AS account_name,c.updated_at AS config_updated_at,c.deleted,c.multiplier,c.channel_id AS explicit_channel_id,c.channel_name,c.channel_code,c.channel_remark,c.channel_revision,c.assignment,COALESCE(c.cost_basis,'account_missing') AS cost_basis,
 COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,
 u.total_cost AS cost_base
 FROM usage_logs u LEFT JOIN accounts_config c ON c.id=u.account_id
 WHERE u.created_at >= $1 AND u.created_at < $2 AND ($5::bigint=0 OR u.account_id=$5) AND ($11::bigint=0 OR c.id IN (SELECT id FROM cohort))
 AND ($4='' OR COALESCE(c.key,'unknown:account_missing')=$4) AND ($6::bigint=0 OR u.group_id=$6)
 AND ($7='' OR CASE $8 WHEN 'public' THEN COALESCE(NULLIF(u.requested_model,''),u.model) WHEN 'recorded' THEN u.model WHEN 'upstream' THEN u.upstream_model END=$7)
), priced AS (
 SELECT *,CASE WHEN cost_base>=0 AND cost_base<'Infinity'::numeric AND multiplier>=0 AND multiplier<'Infinity'::numeric THEN cost_base*multiplier END AS estimated,
 CASE WHEN total_cost>=0 AND total_cost<'Infinity'::numeric THEN total_cost END AS standard,
 CASE WHEN actual_cost>=0 AND actual_cost<'Infinity'::numeric THEN actual_cost END AS income,
 CASE WHEN stream AND first_token_ms>0 AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0 OR input_tokens>0 OR output_tokens>0 OR cache_read_tokens>0 OR cache_creation_tokens>0) THEN first_token_ms END AS ttft,
 CASE WHEN duration_ms>0 AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0 OR input_tokens>0 OR output_tokens>0 OR cache_read_tokens>0 OR cache_creation_tokens>0) THEN duration_ms END AS measured_duration
 FROM scoped
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
 count(*) FILTER(WHERE attribution IN ('current_config','current_explicit_channel')) AS attributed_requests,
 count(*) FILTER(WHERE attribution NOT IN ('current_config','current_explicit_channel')) AS unknown_upstream_requests,
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
	channelFields := `,COALESCE(max(explicit_channel_id),0) AS channel_id,max(channel_code) AS channel_code,max(channel_remark) AS channel_remark,max(channel_revision) AS channel_revision,COALESCE(max(assignment),'unbound_unknown') AS assignment`
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
 'accounts',COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis,url_key,channel_id,channel_name,channel_code,channel_remark,channel_revision,assignment FROM cohort WHERE ($5::bigint>0 OR id IN (SELECT account_id FROM scoped)) ORDER BY id LIMIT 100)c),'[]'::jsonb),
 'members',COALESCE((SELECT jsonb_agg(to_jsonb(m)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis,url_key,channel_id,channel_name,channel_code,channel_remark,channel_revision,assignment,schedulable,concurrency,status FROM cohort c WHERE ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) ORDER BY id LIMIT $9 OFFSET $10)m),'[]'::jsonb),
 'member_total',(SELECT count(*) FROM cohort c WHERE ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog',COALESCE((SELECT jsonb_agg(to_jsonb(g)) FROM (SELECT key AS key,COALESCE(NULLIF(max(channel_name),''),key) AS name,max(channel_id) AS channel_id,max(channel_code) AS channel_code,max(channel_remark) AS channel_remark,max(channel_revision) AS channel_revision,max(assignment) AS assignment,count(*) AS accounts FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) GROUP BY key ORDER BY key LIMIT $9 OFFSET $10)g),'[]'::jsonb),
 'capacity',(SELECT jsonb_build_object('accounts',count(*),'schedulable',count(*) FILTER(WHERE schedulable AND status='active'),'configured_concurrency',COALESCE(sum(concurrency),0)) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog_total',(SELECT count(DISTINCT key) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'account_details_limit',100,'historical_upstream_snapshot_requests',0,'historical_cost_multiplier_snapshot_requests',0,
 'cost_formula','usage_logs.total_cost * current extra.cost_multiplier; missing multiplier uses scheduling default 0.1',
 'income_source','persisted usage_logs.actual_cost; subscription usage valuation, not cash receipts; no guarantee of debit in simple mode',
 'standard_source','persisted usage_logs.total_cost; historical model/cache/per-request pricing is not recalculated',
 'account_rate_multiplier_used',false,'error_rate',NULL)`
	out := &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Scope: f, QueriedAt: time.Now().UTC(), Attribution: "current_explicit_binding_or_url_config_estimate_not_historical_routing_no_procurement_snapshot"}, Currency: "USD billing units (no per-row currency snapshot)"}
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
