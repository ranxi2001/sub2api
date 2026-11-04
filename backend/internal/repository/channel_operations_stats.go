package repository

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Values are explicit persisted identities, never inferred endpoint/capability aliases.
const channelOperationsUsageBase = `WITH base AS (
 SELECT u.account_id,u.channel_id,u.model,u.upstream_model,u.model_mapping_chain,u.stream,u.first_token_ms,u.queue_wait_ms,u.queue_timing,u.duration_ms,u.created_at, COALESCE(m.upstream_id,0) AS operation_upstream_id,
 COALESCE(NULLIF(u.requested_model,''),u.model) AS public_model,
 (u.actual_cost>0 OR u.total_cost>0 OR u.account_stats_cost>0 OR u.input_tokens>0 OR u.output_tokens>0 OR u.cache_read_tokens>0 OR u.cache_creation_tokens>0) AS measured
 FROM usage_logs u LEFT JOIN channel_operation_members m ON m.channel_id=u.channel_id AND m.account_id=u.account_id
 WHERE u.created_at >= $1 AND u.created_at < $2
 AND ($3::bigint=0 OR u.channel_id=$3) AND ($4::bigint=0 OR m.upstream_id=$4 OR ($4=-1 AND m.upstream_id IS NULL))
 AND ($5::bigint=0 OR u.account_id=$5) AND ($6::bigint=0 OR u.group_id=$6)
 AND ($7='' OR CASE $8 WHEN 'public' THEN COALESCE(NULLIF(u.requested_model,''),u.model) WHEN 'recorded' THEN u.model WHEN 'upstream' THEN u.upstream_model END=$7)
), measured AS (
 SELECT *, CASE WHEN stream AND measured AND first_token_ms>0 THEN first_token_ms END AS ttft,
 CASE WHEN measured AND duration_ms>0 THEN duration_ms END AS duration
 FROM base
)`

// Canonical quality history expects len(frozen ModelIDs)*ParallelCount samples
// across the whole plan round (ScheduledTestService.ListQualityHistory). Model
// filtering must happen after this check: an absent peer cannot become a pass.
// Rows and their outcomes remain per model, matching qualityModelOutcomes;
// skipped/unknown/partial model samples keep the existing conservative outcome.
const channelOperationsQualityBase = `WITH scoped AS (
 SELECT p.account_id,r.id,r.plan_id,r.status,r.created_at,r.pelican_config,r.quality_round_id,r.quality_action,r.quality_judgment,r.error_message,NULLIF(r.pelican_config->>'model_id','') AS measured_model
 FROM scheduled_test_results r JOIN scheduled_test_plans p ON p.id=r.plan_id
 WHERE r.created_at >= $1 AND r.created_at < $2
 AND ($3::bigint=0 OR EXISTS (SELECT 1 FROM account_groups ag JOIN channel_groups cg ON cg.group_id=ag.group_id WHERE ag.account_id=p.account_id AND cg.channel_id=$3) OR EXISTS(SELECT 1 FROM channel_operation_members m WHERE m.account_id=p.account_id AND m.channel_id=$3))
 AND ($4::bigint=0 OR EXISTS (SELECT 1 FROM channel_operation_members m WHERE m.account_id=p.account_id AND m.channel_id=$3 AND m.upstream_id=$4) OR ($4=-1 AND NOT EXISTS (SELECT 1 FROM channel_operation_members m WHERE m.account_id=p.account_id AND m.channel_id=$3)))
 AND ($5::bigint=0 OR p.account_id=$5)
 AND ($6::bigint=0 OR EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id=p.account_id AND ag.group_id=$6))
 AND jsonb_typeof(r.pelican_config->'quality')='object'
), model_sets AS (
 SELECT scoped.*,CASE WHEN NOT (pelican_config ? 'model_ids') THEN jsonb_build_array(measured_model)
  WHEN jsonb_typeof(pelican_config->'model_ids')='array' THEN
   CASE WHEN jsonb_array_length(pelican_config->'model_ids') BETWEEN 1 AND 50 THEN pelican_config->'model_ids' END
  END AS expected_models
 FROM scoped
), rounds AS (
 SELECT account_id,plan_id,COALESCE(NULLIF(quality_round_id,''),'legacy-'||id::text) AS round_key,measured_model,
 max(id) AS id,max(created_at) AS observed_at,count(*) AS probes,
 count(*) FILTER(WHERE status='success' AND quality_judgment->>'verdict'='correct') AS passed_probes,
 bool_or(COALESCE(status='failed' AND quality_judgment->>'verdict'='incorrect' AND error_message IN ('answer_mismatch','state_degraded')
 AND (COALESCE(pelican_config->>'question_kind','')<>'random_reasoning' OR pelican_config->'quality_batch'->>'decision'='peer_passed')
 AND quality_action NOT IN ('','stale_run','comparison_pending') AND quality_round_id<>'',false)) AS degraded,
 bool_and(COALESCE(status='success' AND quality_judgment->>'verdict'='correct'
 AND (COALESCE(pelican_config->>'question_kind','')<>'random_reasoning' OR pelican_config->'quality_batch'->>'decision'='passed')
 AND quality_action NOT IN ('','stale_run','comparison_pending') AND quality_round_id<>'',false)) AS passed,
 bool_and(COALESCE(jsonb_typeof(pelican_config->'parallel_count')='number' AND (pelican_config->>'parallel_count') ~ '^[1-8]$',false)) AS valid_count,
 max(CASE WHEN jsonb_typeof(pelican_config->'parallel_count')='number' AND (pelican_config->>'parallel_count') ~ '^[1-8]$' THEN (pelican_config->>'parallel_count')::int END) AS expected,
 jsonb_agg(DISTINCT NULLIF(quality_action,'')) AS actions,
 bool_and(COALESCE(quality_round_id<>'',false)) AS valid_round,
 bool_and(COALESCE(expected_models ? measured_model,false) AND NOT EXISTS (
  SELECT 1 FROM jsonb_array_elements(COALESCE(expected_models,'[]'::jsonb)) m(value)
  WHERE jsonb_typeof(value)<>'string' OR btrim(value#>>'{}')=''
 )) AS valid_models,
 min(expected_models::text) AS model_set_min,max(expected_models::text) AS model_set_max,
 max(jsonb_array_length(expected_models)) AS expected_model_count
 FROM model_sets GROUP BY account_id,plan_id,COALESCE(NULLIF(quality_round_id,''),'legacy-'||id::text),measured_model
), round_integrity AS (
 SELECT account_id,plan_id,round_key,sum(probes) AS round_probes,max(expected_model_count*expected) AS round_expected,
 bool_and(valid_round AND valid_count AND valid_models AND probes=expected)
  AND count(*)=max(expected_model_count) AND min(expected_model_count)=max(expected_model_count)
  AND min(model_set_min)=max(model_set_max) AND min(expected)=max(expected) AS complete
 FROM rounds GROUP BY account_id,plan_id,round_key
), classified AS (
 SELECT b.*,COALESCE(i.complete,false) AS round_complete,i.round_probes,i.round_expected,
 CASE WHEN NOT COALESCE(i.complete,false) THEN 'inconclusive' WHEN b.degraded THEN 'degraded' WHEN b.passed THEN 'passed' ELSE 'inconclusive' END AS outcome
 FROM rounds b JOIN round_integrity i USING(account_id,plan_id,round_key)
 WHERE ($7='' OR b.measured_model=$7)
)`

func (r *channelRepository) operationsSection(ctx context.Context, query string, args ...any) service.ChannelOperationsSection {
	out := service.ChannelOperationsSection{Status: "query_failed", Data: json.RawMessage("null"), QueriedAt: time.Now().UTC()}
	sectionCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var raw []byte
	if err := r.db.QueryRowContext(sectionCtx, query, args...).Scan(&raw); err != nil {
		slog.WarnContext(sectionCtx, "channel operations query failed", "error", logredact.RedactText(err.Error()))
		out.Error = "数据查询失败，请重试"
		return out
	}
	out.Status = "ready"
	out.Data = raw
	return out
}
func (r *channelRepository) ReadChannelOperations(ctx context.Context, f service.ChannelOperationsFilter) *service.ChannelOperationsSnapshot {
	out := &service.ChannelOperationsSnapshot{From: f.From, Until: f.Until, ModelScope: f.ModelScope, Scope: service.ChannelOperationsScope{ChannelID: f.ChannelID, UpstreamID: f.UpstreamID, AccountID: f.AccountID, GroupID: f.GroupID, Model: f.Model, ModelScope: f.ModelScope, Dimension: f.Dimension}}
	args := []any{f.From, f.Until, f.ChannelID, f.UpstreamID, f.AccountID, f.GroupID, f.Model, f.ModelScope, f.PageSize, (f.Page - 1) * f.PageSize}
	dimension := `COALESCE(channel_id,0)`
	names := `COALESCE((SELECT name FROM channels WHERE id=b.dimension_id),'历史未归属渠道')`
	switch f.Dimension {
	case "upstream":
		dimension = `operation_upstream_id`
		names = `COALESCE((SELECT name FROM channel_operation_upstreams WHERE id=b.dimension_id),'未分配上游')`
	case "account":
		dimension = `account_id`
		names = `COALESCE((SELECT name FROM accounts WHERE id=b.dimension_id),'已删除账号')`
	}
	aggregate := `count(*) AS requests,count(*) FILTER(WHERE ttft IS NOT NULL) AS samples,count(*) FILTER(WHERE stream AND measured AND ttft IS NULL) AS unknown_samples,count(*) FILTER(WHERE duration IS NOT NULL) AS duration_samples,
 percentile_cont(.5) WITHIN GROUP(ORDER BY ttft) AS p50_ms,percentile_cont(.9) WITHIN GROUP(ORDER BY ttft) AS p90_ms,percentile_cont(.95) WITHIN GROUP(ORDER BY ttft) AS p95_ms,
 percentile_cont(.9) WITHIN GROUP(ORDER BY duration) AS duration_p90_ms,max(created_at) AS latest_sample_at` + queueInclusiveOperationsAggregate("duration")
	out.Usage = r.operationsSection(ctx, channelOperationsUsageBase+`, buckets AS (SELECT `+dimension+` AS dimension_id,`+aggregate+` FROM measured GROUP BY `+dimension+`), summary AS (SELECT `+aggregate+` FROM measured), lineage AS (
 SELECT public_model,model AS recorded_model,upstream_model,model_mapping_chain,count(*) AS samples FROM measured GROUP BY public_model,model,upstream_model,model_mapping_chain
 ) SELECT jsonb_build_object('source','usage_logs.channel_id / persisted first_token_ms','attribution','current_operational_membership','latency_measurement','legacy_forward_stream_ttft','summary',(SELECT to_jsonb(s) FROM summary s),'total',(SELECT count(*) FROM buckets),'rows',COALESCE((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT b.*,`+names+` AS name FROM buckets b ORDER BY b.requests DESC,b.dimension_id LIMIT $9 OFFSET $10)x),'[]'::jsonb),'lineage',COALESCE((SELECT jsonb_agg(to_jsonb(l)) FROM (SELECT * FROM lineage ORDER BY samples DESC,public_model,recorded_model,upstream_model,model_mapping_chain LIMIT 50)l),'[]'::jsonb),'lineage_total',(SELECT count(*) FROM lineage),'error_rate',NULL,'error_scope','no_complete_request_failure_denominator')`, args...)
	if f.Model != "" && f.ModelScope != "upstream" {
		out.Quality = service.ChannelOperationsSection{Status: "unsupported_scope", Data: json.RawMessage("null"), Error: "质量模型是探测模型，不能推断公开或已记录模型别名", QueriedAt: time.Now().UTC()}
	} else {
		qualityArgs := []any{f.From, f.Until, f.ChannelID, f.UpstreamID, f.AccountID, f.GroupID, f.Model, f.QualityBefore, f.PageSize}
		out.Quality = r.operationsSection(ctx, channelOperationsQualityBase+` SELECT jsonb_build_object('source','scheduled_test_results: frozen config / judgment / plan+round+model','scope','current operational members or canonical group accounts; group filter uses current account groups; not historical request attribution','summary',(SELECT jsonb_build_object('rounds',count(*),'passed',count(*) FILTER(WHERE outcome='passed'),'degraded',count(*) FILTER(WHERE outcome='degraded'),'inconclusive',count(*) FILTER(WHERE outcome='inconclusive'),'latest_sample_at',max(observed_at)) FROM classified),'rows',COALESCE((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT id,account_id,plan_id,measured_model,observed_at,probes,passed_probes,expected,outcome,actions,round_complete,round_probes,round_expected FROM classified WHERE ($8::bigint=0 OR id<$8) ORDER BY id DESC LIMIT $9)x),'[]'::jsonb),'has_more',(SELECT count(*)>$9 FROM classified WHERE ($8::bigint=0 OR id<$8)))`, qualityArgs...)
	}
	errorArgs := []any{f.From, f.Until, f.ChannelID, f.UpstreamID, f.AccountID, f.GroupID, f.Model, f.ModelScope}
	out.Errors = r.operationsSection(ctx, channelOperationsErrorQuery, errorArgs...)
	capacityArgs := []any{f.ChannelID, f.UpstreamID, f.AccountID, f.GroupID}
	out.Capacity = r.operationsSection(ctx, `SELECT jsonb_build_object('source','accounts current configuration','scope','current_account_group_membership; model-independent; not live load','accounts',count(*),'schedulable',count(*) FILTER(WHERE a.schedulable AND a.status='active'),'configured_concurrency',COALESCE(sum(a.concurrency),0)) FROM accounts a WHERE a.deleted_at IS NULL AND ($1::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag JOIN channel_groups cg ON cg.group_id=ag.group_id WHERE ag.account_id=a.id AND cg.channel_id=$1)) AND ($2::bigint=0 OR EXISTS(SELECT 1 FROM channel_operation_members m WHERE m.account_id=a.id AND m.channel_id=$1 AND m.upstream_id=$2) OR ($2=-1 AND NOT EXISTS(SELECT 1 FROM channel_operation_members m WHERE m.account_id=a.id AND m.channel_id=$1))) AND ($3::bigint=0 OR a.id=$3) AND ($4::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=a.id AND ag.group_id=$4))`, capacityArgs...)
	return out
}

var _ service.ChannelOperationsRepository = (*channelRepository)(nil)

// Error logs have no historical channel_id. This is explicitly a current-account
// cohort diagnostic count, never a request/attempt error-rate denominator.
const channelOperationsErrorQuery = `WITH scoped AS (
 SELECT e.error_type,e.error_phase,COALESCE(e.status_code,0) AS status_code,e.upstream_status_code,e.created_at
 FROM ops_error_logs e
 WHERE e.created_at >= $1 AND e.created_at < $2 AND COALESCE(e.is_count_tokens,false)=false
 AND ($3::bigint=0 OR EXISTS(SELECT 1 FROM account_groups ag JOIN channel_groups cg ON cg.group_id=ag.group_id WHERE ag.account_id=e.account_id AND cg.channel_id=$3) OR EXISTS(SELECT 1 FROM channel_operation_members m WHERE m.account_id=e.account_id AND m.channel_id=$3))
 AND ($4::bigint=0 OR EXISTS(SELECT 1 FROM channel_operation_members m WHERE m.account_id=e.account_id AND m.channel_id=$3 AND m.upstream_id=$4) OR ($4=-1 AND NOT EXISTS(SELECT 1 FROM channel_operation_members m WHERE m.account_id=e.account_id AND m.channel_id=$3)))
 AND ($5::bigint=0 OR e.account_id=$5) AND ($6::bigint=0 OR e.group_id=$6)
 AND ($7='' OR CASE $8 WHEN 'public' THEN COALESCE(NULLIF(e.requested_model,''),e.model) WHEN 'recorded' THEN e.model WHEN 'upstream' THEN e.upstream_model END=$7)
), buckets AS (SELECT error_type,error_phase,status_code,count(*) AS records FROM scoped GROUP BY error_type,error_phase,status_code)
SELECT jsonb_build_object('source','ops_error_logs','scope','current operational members or canonical group accounts; actual log group filter; count_tokens excluded; no historical channel attribution','records',(SELECT count(*) FROM scoped),'upstream_5xx',(SELECT count(*) FROM scoped WHERE upstream_status_code>=500),'latest_sample_at',(SELECT max(created_at) FROM scoped),'rows',COALESCE((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT * FROM buckets ORDER BY records DESC,error_type,error_phase,status_code LIMIT 20)x),'[]'::jsonb),'bucket_total',(SELECT count(*) FROM buckets),'error_rate',NULL)`
