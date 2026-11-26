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
	rows, err := r.sql.QueryContext(ctx, `
 WITH base_usage AS (
  SELECT id, account_id, created_at, stream, first_token_ms, actual_cost, total_cost,
   account_stats_cost, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens
  FROM usage_logs
  WHERE account_id=ANY($1) AND created_at >= $2 AND created_at <= $8
   AND COALESCE(NULLIF(requested_model,''),model)=$3
   AND group_id IS NOT DISTINCT FROM $4::bigint
   AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0
    OR input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens>0)
 ), measured AS (
  SELECT *, first_token_ms::double precision AS latency_ms
  FROM base_usage
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
  WHERE account_id=ANY($1) AND created_at >= $2 AND created_at <= $8
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
  COALESCE(u.quantiles[CASE $7::int WHEN 50 THEN 1 WHEN 70 THEN 2 WHEN 90 THEN 3 WHEN 95 THEN 4 END],0),
  COALESCE(u.slow_samples,0),COALESCE(e.error_samples,0)
 FROM all_buckets k
 LEFT JOIN usage_buckets u ON u.account_id=k.account_id AND u.bucket=k.bucket
 LEFT JOIN errors e ON e.account_id=k.account_id AND e.bucket=k.bucket
 ORDER BY k.account_id,k.bucket
 `, pq.Array(query.AccountIDs), query.Since, query.Model, query.GroupID, bucketMinutes, slowTTFTMs, percentile, query.Until)
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
