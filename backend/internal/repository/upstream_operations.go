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

type operationsAccount struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Key         string    `json:"key"`
	Attribution string    `json:"attribution"`
	UpdatedAt   time.Time `json:"updated_at"`
	Deleted     bool      `json:"deleted"`
	Multiplier  *float64  `json:"multiplier"`
	CostBasis   string    `json:"cost_basis"`
	Schedulable bool      `json:"schedulable"`
	Concurrency int       `json:"concurrency"`
	Status      string    `json:"status"`
}

// The projection never includes credentials. Only the selected endpoint fields
// are read internally, and neither DB errors nor raw URLs are logged/returned.
func operationsAccounts(ctx context.Context, tx *sql.Tx, f service.UpstreamOperationsFilter) ([]byte, error) {
	var grokMode string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM settings WHERE key=$1),'')`, service.SettingKeyGrokDefaultBaseURLMode).Scan(&grokMode); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,a.platform,a.type,
 jsonb_build_object('base_url',a.credentials->'base_url','api_base_urls',a.credentials->'api_base_urls','account_mode',a.credentials->'account_mode','api_protocol',a.credentials->'api_protocol','provider_profile',a.credentials->'provider_profile','auth_mode',a.credentials->'auth_mode','openai_auth_mode',a.credentials->'openai_auth_mode'),
 jsonb_build_object('cost_multiplier',a.extra->'cost_multiplier','custom_base_url_enabled',a.extra->'custom_base_url_enabled','custom_base_url',a.extra->'custom_base_url','openai_siwc',a.extra->'openai_siwc','openai_prism_browser',a.extra->'openai_prism_browser','openai_excel_bps',a.extra->'openai_excel_bps'),
 a.updated_at,a.deleted_at IS NOT NULL,a.parent_account_id,a.schedulable,a.concurrency,a.status
 FROM accounts a WHERE ($3::bigint=0 OR a.id=$3) AND (a.deleted_at IS NULL OR EXISTS(SELECT 1 FROM usage_logs u WHERE u.account_id=a.id AND u.created_at >= $1 AND u.created_at < $2))`, f.From, f.Until, f.AccountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []operationsAccount{}
	for rows.Next() {
		var a service.Account
		var creds, extra []byte
		var deleted bool
		var parent sql.NullInt64
		if err = rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &creds, &extra, &a.UpdatedAt, &deleted, &parent, &a.Schedulable, &a.Concurrency, &a.Status); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(creds, &a.Credentials); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, err
		}
		if parent.Valid {
			a.ParentAccountID = &parent.Int64
		}
		if a.IsGrok() && a.GetCredential("base_url") == "" {
			a.Credentials["base_url"] = service.GrokBaseURLForMode(grokMode)
		}
		key, attribution := service.ResolveOperationsUpstream(&a)
		if key == "" {
			key = "unknown:" + attribution
		}
		multiplier, basis := service.OperationsCostMultiplier(&a)
		out = append(out, operationsAccount{ID: a.ID, Name: a.Name, Key: key, Attribution: attribution, UpdatedAt: a.UpdatedAt, Deleted: deleted, Multiplier: multiplier, CostBasis: basis, Schedulable: a.Schedulable, Concurrency: a.Concurrency, Status: a.Status})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

const upstreamOperationsCohort = `accounts_config AS (
 SELECT * FROM jsonb_to_recordset($3::jsonb) AS c(id bigint,name text,key text,attribution text,updated_at timestamptz,deleted boolean,multiplier numeric,cost_basis text,schedulable boolean,concurrency integer,status text)
), cohort AS (
 SELECT * FROM accounts_config WHERE ($4='' OR key=$4) AND ($5::bigint=0 OR id=$5)
)`
const upstreamOperationsBase = `WITH ` + upstreamOperationsCohort + `, scoped AS (
 SELECT u.*,COALESCE(c.key,'unknown:account_missing') AS upstream_key,
 COALESCE(c.attribution,'account_missing') AS attribution,c.name AS account_name,c.updated_at AS config_updated_at,c.deleted,c.multiplier,COALESCE(c.cost_basis,'account_missing') AS cost_basis,
 COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,
 COALESCE(u.account_stats_cost,u.total_cost) AS cost_base
 FROM usage_logs u LEFT JOIN accounts_config c ON c.id=u.account_id
 WHERE u.created_at >= $1 AND u.created_at < $2 AND ($5::bigint=0 OR u.account_id=$5)
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
const upstreamOperationsAggregate = `count(*) AS requests,
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
 count(*) FILTER(WHERE attribution='current_config') AS attributed_requests,
 count(*) FILTER(WHERE attribution<>'current_config') AS unknown_upstream_requests,
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
 jsonb_agg(DISTINCT cost_basis) AS cost_bases`

func (r *channelRepository) ReadUpstreamOperations(ctx context.Context, f service.UpstreamOperationsFilter) (*service.UpstreamOperationsSnapshot, error) {
	if err := service.ValidateUpstreamOperationsFilter(f); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, err := operationsAccounts(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	args := []any{f.From, f.Until, string(raw), f.Upstream, f.AccountID, f.GroupID, f.Model, f.ModelScope, f.PageSize, (f.Page - 1) * f.PageSize}
	dimension, name := `upstream_key`, `upstream_key`
	switch f.Dimension {
	case "account":
		dimension = `account_id::text`
		name = `COALESCE(max(account_name),'Deleted account') || ' #' || account_id::text`
	case "model":
		dimension = `CASE $8 WHEN 'public' THEN public_model WHEN 'recorded' THEN model WHEN 'upstream' THEN COALESCE(upstream_model,'(unknown upstream model)') END`
		name = dimension
	}
	order := f.Sort + " " + f.Direction + " NULLS LAST,key ASC"
	// Sort and direction are strictly validated before SQL construction.
	query := upstreamOperationsBase + `, buckets AS (SELECT ` + dimension + ` AS key,` + name + ` AS name,` + upstreamOperationsAggregate + ` FROM priced GROUP BY ` + dimension + `), summary AS (SELECT ` + upstreamOperationsAggregate + ` FROM priced)
 SELECT jsonb_build_object('summary',(SELECT to_jsonb(s) FROM summary s),'total',(SELECT count(*) FROM buckets),
 'rows',COALESCE((SELECT jsonb_agg(to_jsonb(b)) FROM (SELECT * FROM buckets ORDER BY ` + order + ` LIMIT $9 OFFSET $10)b),'[]'::jsonb),
 'accounts',COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM (SELECT id,name,key,attribution,updated_at,deleted,multiplier,cost_basis FROM cohort WHERE ($5::bigint>0 OR id IN (SELECT account_id FROM scoped)) ORDER BY id LIMIT 100)c),'[]'::jsonb),
 'catalog',COALESCE((SELECT jsonb_agg(to_jsonb(g)) FROM (SELECT key AS key,key AS name,count(*) AS accounts FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6)) GROUP BY key ORDER BY key LIMIT $9 OFFSET $10)g),'[]'::jsonb),
 'capacity',(SELECT jsonb_build_object('accounts',count(*),'schedulable',count(*) FILTER(WHERE schedulable AND status='active'),'configured_concurrency',COALESCE(sum(concurrency),0)) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'catalog_total',(SELECT count(DISTINCT key) FROM cohort c WHERE NOT deleted AND ($6::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=c.id AND ag.group_id=$6))),
 'account_details_limit',100,'historical_upstream_snapshot_requests',0,'historical_cost_multiplier_snapshot_requests',0,
 'cost_formula','COALESCE(usage_logs.account_stats_cost, usage_logs.total_cost) * current extra.cost_multiplier; missing multiplier uses scheduling default 0.1',
 'income_source','persisted usage_logs.actual_cost; subscription usage valuation, not cash receipts; no guarantee of debit in simple mode',
 'standard_source','persisted usage_logs.total_cost; historical model/cache/per-request pricing is not recalculated',
 'account_rate_multiplier_used',false,'error_rate',NULL)`
	out := &service.UpstreamOperationsSnapshot{Scope: f, QueriedAt: time.Now().UTC(), Attribution: "current_account_config_estimate_no_historical_url_or_procurement_multiplier_snapshot", Currency: "USD billing units (no per-row currency snapshot)"}
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&out.Usage); err != nil {
		return nil, fmt.Errorf("read upstream usage: %w", err)
	}
	out.Errors = readUpstreamSection(ctx, tx, upstreamOperationsErrors, args[:8]...)
	if f.Model != "" && f.ModelScope != "upstream" {
		out.Quality = service.ChannelOperationsSection{Status: "unsupported_scope", Data: json.RawMessage("null"), Error: "Quality uses probe model identity; public/recorded aliases cannot be inferred", QueriedAt: out.QueriedAt}
	} else {
		// Reuse canonical complete frozen plan×round×model classification. Only the
		// account cohort changes; filtering by model still follows round integrity.
		classifier := channelOperationsQualityBase[strings.Index(channelOperationsQualityBase, "), model_sets AS ("):]
		quality := `WITH ` + upstreamOperationsCohort + `, scoped AS (
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

func readUpstreamSection(ctx context.Context, tx *sql.Tx, query string, args ...any) service.ChannelOperationsSection {
	out := service.ChannelOperationsSection{Status: "query_failed", Data: json.RawMessage("null"), QueriedAt: time.Now().UTC(), Error: "Data query failed; retry"}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT operations_section"); err != nil {
		return out
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT operations_section")
		return out
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT operations_section"); err != nil {
		return out
	}
	out.Status = "ready"
	out.Error = ""
	out.Data = raw
	return out
}

const upstreamOperationsErrors = `WITH ` + upstreamOperationsCohort + `, params AS (SELECT $8::text AS model_scope), scoped AS (
 SELECT e.error_type,e.error_phase,e.status_code,e.upstream_status_code,e.created_at FROM ops_error_logs e
 WHERE e.created_at >= $1 AND e.created_at < $2 AND COALESCE(e.is_count_tokens,false)=false
 AND ($4='' OR e.account_id IN (SELECT id FROM cohort)) AND ($5::bigint=0 OR e.account_id=$5) AND ($6::bigint=0 OR e.group_id=$6)
 AND ($7='' OR CASE $8 WHEN 'public' THEN COALESCE(NULLIF(e.requested_model,''),e.model) WHEN 'recorded' THEN e.model WHEN 'upstream' THEN e.upstream_model END=$7)
), buckets AS (SELECT error_type,error_phase,status_code,count(*) AS records FROM scoped GROUP BY error_type,error_phase,status_code)
 SELECT jsonb_build_object('source','retained ops_error_logs; current account cohort; count_tokens excluded; no complete request failure denominator','records',(SELECT count(*) FROM scoped),'upstream_5xx',(SELECT count(*) FROM scoped WHERE upstream_status_code>=500),'latest_sample_at',(SELECT max(created_at) FROM scoped),'rows',COALESCE((SELECT jsonb_agg(to_jsonb(b)) FROM (SELECT * FROM buckets ORDER BY records DESC,error_type,error_phase,status_code LIMIT 20)b),'[]'::jsonb),'bucket_total',(SELECT count(*) FROM buckets),'error_rate',NULL)`

var _ service.UpstreamOperationsRepository = (*channelRepository)(nil)
