package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// ReadPrioritySchedulingSignals aggregates only the requested model. Missing
// TTFTs and failed placeholders are not zero-latency successes. Quality rounds
// count once regardless of the number of probes; inconclusive rounds are absent.
func (r *usageLogRepository) ReadPrioritySchedulingSignals(ctx context.Context, query service.PrioritySchedulingQuery) (map[int64]service.PrioritySchedulingSignal, error) {
	if query.LatencyPercentile == 0 {
		query.LatencyPercentile = 90
	}
	if query.LatencyMeasurement == "" {
		query.LatencyMeasurement = "legacy_forward"
	}
	if query.TargetTTFTMs == 0 {
		query.TargetTTFTMs = 15000
	}
	if query.UsageUntil.IsZero() {
		query.UsageUntil = time.Now()
	}
	cfg := service.DefaultPrioritySchedulingConfig()
	cfg.LatencyPercentile, cfg.LatencyMeasurement, cfg.TargetTTFTMs = query.LatencyPercentile, query.LatencyMeasurement, query.TargetTTFTMs
	if err := service.ValidatePrioritySchedulingConfig(cfg); err != nil {
		return nil, fmt.Errorf("priority signal query: %w", err)
	}
	qualityModel := query.QualityModel
	if qualityModel == "" {
		qualityModel = query.Model
	}
	querySQL := `
 WITH base_usage AS (
  SELECT id, account_id, created_at, stream, first_token_ms, actual_cost,
   COALESCE(account_stats_cost,total_cost) AS base_cost
  FROM usage_logs
  WHERE account_id=ANY($1) AND created_at >= $3 AND created_at <= $9
   AND COALESCE(NULLIF(requested_model,''),model)=$2
   AND group_id IS NOT DISTINCT FROM $5::bigint
   AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0
    OR input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens>0)
 ), traces AS (
  SELECT b.*, d.detail, a.attempt
  FROM base_usage b
  -- Multiple traces for one billed row are ambiguous; inspect at most two.
  LEFT JOIN LATERAL (
   SELECT (array_agg(detail))[1] AS detail FROM (
    SELECT detail FROM request_timing_details WHERE usage_log_id=b.id LIMIT 2
   ) bounded_traces HAVING COUNT(*)=1
  ) d ON true
  -- The final recorded attempt must be physical HTTP, across ALL accounts.
  -- A later uninstrumented parent makes attribution unknown too; never
  -- substitute a successful parent or an earlier retry.
  LEFT JOIN LATERAL (
   SELECT value AS attempt FROM jsonb_array_elements(
    CASE WHEN jsonb_typeof(d.detail->'attempts')='array' THEN d.detail->'attempts' ELSE '[]'::jsonb END
   ) WITH ORDINALITY a(value,n)
   WHERE value->>'kind'='http' AND n=jsonb_array_length(
    CASE WHEN jsonb_typeof(d.detail->'attempts')='array' THEN d.detail->'attempts' ELSE '[]'::jsonb END
   ) LIMIT 1
  ) a ON true
 ), timings AS (
  SELECT *,
   CASE WHEN jsonb_typeof(attempt->'start_ms')='number'
    AND length((attempt->'start_ms')::text)<32
    THEN (attempt->>'start_ms')::double precision END AS attempt_start_ms,
   CASE WHEN jsonb_typeof(attempt->'events'->'first_semantic')='number'
    AND length((attempt->'events'->'first_semantic')::text)<32
    THEN (attempt->'events'->>'first_semantic')::double precision END AS semantic_ms,
   CASE WHEN jsonb_typeof(detail->'events'->'first_write')='number'
    AND length((detail->'events'->'first_write')::text)<32
    THEN (detail->'events'->>'first_write')::double precision END AS first_write_ms,
   CASE WHEN jsonb_typeof(detail->'events'->'first_visible')='number'
    AND length((detail->'events'->'first_visible')::text)<32
    THEN (detail->'events'->>'first_visible')::double precision END AS visible_ms
  FROM traces
 ), measured AS (
  SELECT *, CASE WHEN $7='legacy_forward' THEN first_token_ms::double precision
   WHEN detail->>'version'='1' AND detail->>'outcome'='success'
    AND detail->>'terminal'='completed' AND detail->>'status'='200'
    AND detail->>'truncated'='false' AND detail->>'canceled'='false'
    AND detail->>'client_disconnect'='false' AND detail->>'downstream_error'='false'
    AND detail->>'body_complete'='true'
    AND attempt->>'account_id'=account_id::text
    AND attempt->>'status'='200' AND COALESCE(attempt->>'error','')=''
    AND attempt->>'terminal'='completed'
    AND attempt_start_ms>=0 AND semantic_ms>attempt_start_ms
    -- Earlier writes can block parsing before semantic output. Without a
    -- separable interval such a sample stays unknown, not an account penalty.
    AND (detail->'events'->'first_write' IS NULL OR first_write_ms>=semantic_ms)
    THEN semantic_ms-attempt_start_ms END AS latency_ms,
   CASE WHEN detail->>'version'='1' AND detail->>'truncated'='false'
    THEN visible_ms END AS experience_ms
  FROM timings
 ), usage AS (
  SELECT account_id,
   COUNT(*) FILTER (WHERE stream AND latency_ms>0) AS samples,
   percentile_cont(ARRAY[.5,.7,.9,.95,.99]) WITHIN GROUP (ORDER BY latency_ms)
    FILTER (WHERE stream AND latency_ms>0) AS quantiles,
   MAX(created_at) FILTER (WHERE stream AND latency_ms>0) AS latest_latency_at,
   MAX(created_at) FILTER (WHERE stream AND latency_ms>$8) AS latest_bad_latency_at,
   COUNT(*) FILTER (WHERE stream AND (latency_ms IS NULL OR latency_ms<=0)) AS unknown_samples,
   COUNT(*) FILTER (WHERE stream AND experience_ms>0) AS experience_samples,
   percentile_cont(.9) WITHIN GROUP (ORDER BY experience_ms) FILTER (WHERE stream AND experience_ms>0) AS experience_p90,
   COUNT(*) FILTER (WHERE stream AND latency_ms>$8) AS slow_samples,
   COUNT(*) FILTER (WHERE actual_cost>=0 AND base_cost>=0 AND actual_cost+base_cost>0) AS profit_samples,
   SUM(actual_cost) FILTER (WHERE actual_cost>=0 AND base_cost>=0 AND actual_cost+base_cost>0) AS revenue,
   SUM(base_cost) FILTER (WHERE actual_cost>=0 AND base_cost>=0 AND actual_cost+base_cost>0) AS base_cost
  FROM measured GROUP BY account_id
 ), errors AS (
  SELECT account_id,
   COUNT(DISTINCT COALESCE(NULLIF(client_request_id,''), NULLIF(request_id,''), id::text)) AS error_samples
  FROM ops_error_logs
  WHERE account_id=ANY($1) AND created_at >= $3 AND created_at <= $9
   AND COALESCE(NULLIF(requested_model,''),model)=$2
   AND group_id IS NOT DISTINCT FROM $5::bigint
   AND error_phase='upstream'
  GROUP BY account_id
 ), rounds AS (
  SELECT p.account_id, r.quality_round_id, MAX(r.created_at) AS observed_at,
   bool_or(r.quality_judgment->>'verdict'='incorrect' AND r.status='failed'
    AND r.error_message IN ('answer_mismatch','state_degraded')) AS failed,
   bool_and(COALESCE(r.quality_judgment->>'verdict'='correct' AND r.status='success',false)) AS passed
  FROM scheduled_test_plans p JOIN scheduled_test_results r ON r.plan_id=p.id
  WHERE p.account_id=ANY($1) AND p.enabled
   AND COALESCE(NULLIF(r.pelican_config->>'model_id',''),p.model_id)=$10
   AND jsonb_typeof(p.pelican_config->'quality')='object' AND r.quality_round_id<>''
   AND r.created_at >= $4 AND r.created_at >= p.updated_at AND r.created_at <= NOW()
   AND r.quality_action<>'' AND r.quality_action<>'stale_run'
  GROUP BY p.account_id,p.id,r.quality_round_id
  HAVING bool_and((r.pelican_config->'parallel_count' IS NULL OR jsonb_typeof(r.pelican_config->'parallel_count')='number')
    AND COALESCE(r.pelican_config->>'parallel_count','1') ~ '^[1-8]$')
   AND COUNT(*) = MAX(CASE WHEN COALESCE(r.pelican_config->>'parallel_count','1') ~ '^[1-8]$'
    THEN COALESCE(r.pelican_config->>'parallel_count','1')::int END)
 ), latest_quality AS (
  SELECT DISTINCT ON (account_id) account_id, (passed AND NOT COALESCE(failed,false)) AS passed
  FROM rounds WHERE passed OR failed
  ORDER BY account_id, observed_at DESC, failed DESC NULLS LAST
 ), quality AS (
  SELECT account_id, COUNT(*) FILTER (WHERE passed AND NOT COALESCE(failed,false)) AS passed,
   COUNT(*) FILTER (WHERE passed OR failed) AS samples
  FROM rounds GROUP BY account_id
 )
 SELECT ids.id, COALESCE(u.samples,0),COALESCE(u.quantiles[3],0),COALESCE(q.passed,0),COALESCE(q.samples,0),
 COALESCE(u.profit_samples,0),COALESCE(u.revenue,0),COALESCE(u.base_cost,0),lq.passed,u.latest_latency_at,
 COALESCE(u.quantiles[1],0), COALESCE(u.quantiles[2],0), COALESCE(u.quantiles[4],0), COALESCE(u.quantiles[5],0),
 u.quantiles[CASE $6::int WHEN 50 THEN 1 WHEN 70 THEN 2 WHEN 90 THEN 3 WHEN 95 THEN 4 WHEN 99 THEN 5 END],
 u.latest_bad_latency_at, COALESCE(u.unknown_samples,0), COALESCE(u.experience_samples,0), COALESCE(u.experience_p90,0),
 COALESCE(u.slow_samples,0), COALESCE(e.error_samples,0)
 FROM unnest($1::bigint[]) ids(id)
 LEFT JOIN usage u ON u.account_id=ids.id
 LEFT JOIN errors e ON e.account_id=ids.id
 LEFT JOIN quality q ON q.account_id=ids.id
 LEFT JOIN latest_quality lq ON lq.account_id=ids.id
 `
	args := []any{pq.Array(query.AccountIDs), query.Model, query.UsageSince, query.QualitySince, query.GroupID, query.LatencyPercentile, query.LatencyMeasurement, query.TargetTTFTMs, query.UsageUntil, qualityModel}
	if query.HalfOpen {
		querySQL = strings.ReplaceAll(querySQL, "created_at <= $9", "created_at < $9")
	}
	if query.InboundEndpoint != nil {
		args = append(args, *query.InboundEndpoint)
		querySQL = strings.Replace(querySQL, "AND group_id IS NOT DISTINCT FROM $5::bigint", fmt.Sprintf("AND group_id IS NOT DISTINCT FROM $5::bigint AND (%s)=$%d", priorityLifecycleEndpointSQL, len(args)), 1)
	}
	if query.RequestType != nil {
		args = append(args, int16(*query.RequestType))
		querySQL = strings.Replace(querySQL, "AND group_id IS NOT DISTINCT FROM $5::bigint", fmt.Sprintf("AND group_id IS NOT DISTINCT FROM $5::bigint AND request_type=$%d", len(args)), 1)
	}
	if query.ObservationEffort != nil {
		// Exact effort population; no max/xhigh inference or cross-effort fallback.
		querySQL = strings.Replace(querySQL, "AND group_id IS NOT DISTINCT FROM $5::bigint", fmt.Sprintf("AND group_id IS NOT DISTINCT FROM $5::bigint AND COALESCE(reasoning_effort,'')=$%d", len(args)+1), 1)
		args = append(args, *query.ObservationEffort)
	}
	rows, err := r.sql.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]service.PrioritySchedulingSignal, len(query.AccountIDs))
	for rows.Next() {
		var id int64
		var signal service.PrioritySchedulingSignal
		if err := rows.Scan(&id, &signal.Samples, &signal.P90TTFTMs, &signal.QualityPassed, &signal.QualitySamples, &signal.ProfitSamples, &signal.Revenue, &signal.BaseCost, &signal.LatestQualityPassed, &signal.LatestLatencyAt, &signal.P50TTFTMs, &signal.P70TTFTMs, &signal.P95TTFTMs, &signal.P99TTFTMs, &signal.SelectedTTFTMs, &signal.LatestBadLatencyAt, &signal.LatencyUnknownSamples, &signal.ExperienceSamples, &signal.ExperienceP90Ms, &signal.SlowSamples, &signal.ErrorSamples); err != nil {
			return nil, err
		}
		signal.LatencyPercentile, signal.LatencyMeasurement, signal.LatencyEvidenceVersion = query.LatencyPercentile, query.LatencyMeasurement, 1
		if denom := signal.Samples + signal.ErrorSamples; denom > 0 {
			signal.UpstreamErrorRate = float64(signal.ErrorSamples) / float64(denom)
		}
		out[id] = signal
	}
	return out, rows.Err()
}
