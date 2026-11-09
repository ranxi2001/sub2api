package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// ReadPriorityLatencySeries returns the configured latency population and the
// matching upstream error events. A bucket may contain errors without usage.
func (r *usageLogRepository) ReadPriorityLatencySeries(ctx context.Context, query service.PriorityLatencySeriesQuery) ([]service.PriorityLatencyBucket, error) {
	if query.Until.IsZero() {
		query.Until = time.Now()
	}
	slowTTFTMs := query.SlowTTFTMs
	if slowTTFTMs <= 0 {
		slowTTFTMs = 10000
	}
	bucketMinutes := query.BucketMinutes
	if bucketMinutes <= 0 {
		bucketMinutes = 5
	}
	percentile := query.LatencyPercentile
	if percentile == 0 {
		percentile = 90
	}
	measurement := query.LatencyMeasurement
	if measurement == "" {
		measurement = "legacy_forward"
	}
	rows, err := r.sql.QueryContext(ctx, `
 WITH base_usage AS (
  SELECT id, account_id, created_at, stream, first_token_ms, actual_cost, total_cost,
   account_stats_cost, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens
  FROM usage_logs
  WHERE account_id=ANY($1) AND created_at >= $2 AND created_at <= $9
   AND COALESCE(NULLIF(requested_model,''),model)=$3
   AND group_id IS NOT DISTINCT FROM $4::bigint
   AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0
    OR input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens>0)
 ), traces AS (
  SELECT b.*, d.detail, a.attempt
  FROM base_usage b
  LEFT JOIN LATERAL (
   SELECT (array_agg(detail))[1] AS detail FROM (
    SELECT detail FROM request_timing_details WHERE usage_log_id=b.id LIMIT 2
   ) bounded_traces HAVING COUNT(*)=1
  ) d ON true
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
     THEN (detail->'events'->>'first_write')::double precision END AS first_write_ms
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
    AND (detail->'events'->'first_write' IS NULL OR first_write_ms>=semantic_ms)
    THEN semantic_ms-attempt_start_ms END AS latency_ms
  FROM timings
 ), usage_buckets AS (
  SELECT account_id,
   date_bin(make_interval(mins => $5), created_at, TIMESTAMPTZ '2026-01-01') AS bucket,
   COUNT(*) FILTER (WHERE stream AND latency_ms>0) AS samples,
   percentile_cont(ARRAY[.5,.7,.9,.95,.99]) WITHIN GROUP (ORDER BY latency_ms)
    FILTER (WHERE stream AND latency_ms>0) AS quantiles,
   AVG(latency_ms) FILTER (WHERE stream AND latency_ms>0) AS mean_ttft,
   COUNT(*) FILTER (WHERE stream AND latency_ms>$6) AS slow_samples
  FROM measured
  GROUP BY account_id,bucket
 ), errors AS (
  SELECT account_id,
   date_bin(make_interval(mins => $5), created_at, TIMESTAMPTZ '2026-01-01') AS bucket,
   COUNT(DISTINCT COALESCE(NULLIF(client_request_id,''), NULLIF(request_id,''), id::text)) AS error_samples
  FROM ops_error_logs
  WHERE account_id=ANY($1) AND created_at >= $2 AND created_at <= $9
   AND COALESCE(NULLIF(requested_model,''),model)=$3
   AND group_id IS NOT DISTINCT FROM $4::bigint
   AND error_phase='upstream'
  GROUP BY account_id,bucket
 ), all_buckets AS (
  SELECT account_id,bucket FROM usage_buckets
  UNION
  SELECT account_id,bucket FROM errors
 )
 SELECT k.account_id,k.bucket,COALESCE(u.samples,0),
  COALESCE(u.quantiles[1],0),COALESCE(u.quantiles[2],0),COALESCE(u.quantiles[3],0),
  COALESCE(u.quantiles[4],0),COALESCE(u.quantiles[5],0),COALESCE(u.mean_ttft,0),
  COALESCE(u.quantiles[CASE $8::int WHEN 50 THEN 1 WHEN 70 THEN 2 WHEN 90 THEN 3 WHEN 95 THEN 4 END],0),
  COALESCE(u.slow_samples,0),COALESCE(e.error_samples,0)
 FROM all_buckets k
 LEFT JOIN usage_buckets u ON u.account_id=k.account_id AND u.bucket=k.bucket
 LEFT JOIN errors e ON e.account_id=k.account_id AND e.bucket=k.bucket
 ORDER BY k.account_id,k.bucket
 `, pq.Array(query.AccountIDs), query.Since, query.Model, query.GroupID, bucketMinutes, slowTTFTMs, measurement, percentile, query.Until)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.PriorityLatencyBucket, 0, 128)
	for rows.Next() {
		var item service.PriorityLatencyBucket
		if err := rows.Scan(&item.AccountID, &item.Bucket, &item.Samples,
			&item.P50TTFTMs, &item.P70TTFTMs, &item.P90TTFTMs, &item.P95TTFTMs, &item.P99TTFTMs,
			&item.MeanTTFTMs, &item.SelectedTTFTMs, &item.SlowSamples, &item.ErrorSamples); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
