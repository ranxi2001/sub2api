// Package balanceforecast evaluates estimates from committed settlement receipts.
package balanceforecast

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Store struct{ DB *sql.DB }

type Candidate struct {
	ID                                              int64
	Balance, Spent, ObservationHours, Hourly, Hours float64
	Covered                                         bool
	Samples                                         int
	First, Last                                     time.Time
}

func (c Candidate) Reliable() bool {
	return c.Covered && c.ObservationHours >= 1 && c.Samples >= 3 && c.Last.Sub(c.First) >= time.Hour && c.Hourly > 0
}

func (s *Store) Candidates(ctx context.Context, now time.Time, after int64, limit int) ([]Candidate, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("forecast batch limit must be 1..200")
	}
	rows, err := s.DB.QueryContext(ctx, candidateSQL, now.UTC(), after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Candidate
	for rows.Next() {
		var c Candidate
		var first, last sql.NullTime
		if err = rows.Scan(&c.ID, &c.Balance, &c.ObservationHours, &c.Spent, &c.Samples, &first, &last, &c.Covered); err != nil {
			return nil, err
		}
		c.First, c.Last = first.Time, last.Time
		if c.Covered && c.ObservationHours >= 1 && c.Samples >= 3 && c.Last.Sub(c.First) >= time.Hour && c.Spent > 0 {
			c.Hourly = c.Spent / c.ObservationHours
			c.Hours = c.Balance / c.Hourly
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// The LIMIT applies to users before any settlement aggregation. Every source
// joins that bounded page and its effective UTC window; no per-user queries.
const candidateSQL = `WITH page AS MATERIALIZED (
 SELECT u.id,u.balance,GREATEST($1::timestamptz-INTERVAL '24 hours',u.created_at,c.started_at) AS since
 FROM users u CROSS JOIN balance_forecast_coverage c
 WHERE u.id>$2 AND u.deleted_at IS NULL AND u.status='active' AND u.balance_notify_enabled AND u.balance>0
 ORDER BY u.id LIMIT $3
), receipts AS (
 SELECT p.id AS user_id,e.request_id,e.api_key_id,round((e.payload->'command'->>'BalanceCost')::numeric,8) AS amount,e.applied_at AS at
 FROM page p JOIN usage_billing_events e ON (e.payload->'command'->>'UserID')::bigint=p.id
 WHERE e.state IN ('applied','complete') AND e.schema_version=1 AND (e.result->>'Applied')::boolean
 AND e.result->>'NewBalance' IS NOT NULL AND (e.payload->'command'->>'BalanceCost')::numeric>0
 AND e.applied_at>=p.since AND e.applied_at<$1
 UNION ALL
 SELECT p.id,'batch_image_capture:'||h.batch_id,h.api_key_id,h.actual_amount,COALESCE(d.created_at,a.created_at)
 FROM page p JOIN batch_image_money_holds h ON h.user_id=p.id AND h.state='captured' AND h.actual_amount>0
 LEFT JOIN usage_billing_dedup d ON d.request_id='batch_image_capture:'||h.batch_id AND d.api_key_id=h.api_key_id
 LEFT JOIN usage_billing_dedup_archive a ON a.request_id='batch_image_capture:'||h.batch_id AND a.api_key_id=h.api_key_id
 WHERE COALESCE(d.created_at,a.created_at)>=p.since AND COALESCE(d.created_at,a.created_at)<$1
 UNION ALL
 SELECT p.id,r.request_id,(r.event->'usage'->>'api_key_id')::bigint,(r.event->'usage'->>'charge_nano_usd')::numeric/1000000000,
 (r.event->'usage'->>'completed_at')::timestamptz
 FROM page p JOIN regional_primary_requests r ON r.user_id=p.id AND r.state='completed' AND r.balance_funded
 WHERE (r.event->'usage'->>'charge_nano_usd')::numeric>0 AND (r.event->'usage'->>'completed_at')::timestamptz>=p.since
 AND (r.event->'usage'->>'completed_at')::timestamptz<$1
 UNION ALL
 SELECT p.id,e.event_id,(e.event->'usage'->>'api_key_id')::bigint,(e.event->'usage'->>'charge_nano_usd')::numeric/1000000000,e.settled_at
 FROM page p JOIN regional_quota_leases l ON l.user_id=p.id JOIN regional_settled_events e ON e.lease_id=l.lease_id
 JOIN regional_budget_allocations a ON a.allocation_id='grant:'||l.lease_id AND a.subscription_id=0
 WHERE e.settled_at>=p.since AND e.settled_at<$1 AND (e.event->'usage'->>'charge_nano_usd')::numeric>0
), unique_receipts AS (
 SELECT DISTINCT ON (user_id,request_id,api_key_id) * FROM receipts ORDER BY user_id,request_id,api_key_id
)
 SELECT p.id,p.balance,EXTRACT(EPOCH FROM ($1::timestamptz-p.since))/3600,
 COALESCE(SUM(r.amount),0),COUNT(r.request_id),MIN(r.at),MAX(r.at),
 NOT EXISTS (SELECT 1 FROM usage_logs u WHERE u.user_id=p.id AND u.billing_type=0 AND u.actual_cost>0
 AND u.created_at>=p.since AND u.created_at<$1 AND NOT EXISTS
 (SELECT 1 FROM unique_receipts x WHERE x.user_id=u.user_id AND x.request_id=u.request_id AND x.api_key_id=u.api_key_id))
 FROM page p LEFT JOIN unique_receipts r ON r.user_id=p.id GROUP BY p.id,p.balance,p.since ORDER BY p.id`
