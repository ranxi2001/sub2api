package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Rebuild a closed slice under a fresh READ COMMITTED snapshot after the
// writer barrier. Replacing it also handles deleted or re-attributed usage.
func (r *dashboardAggregationRepository) RefreshAccountUsageFinancial(ctx context.Context) (bool, error) {
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var bucket time.Time
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(
 (SELECT bucket FROM account_usage_financial_pending WHERE bucket < account_usage_bucket(now()) ORDER BY bucket DESC LIMIT 1),
 (SELECT bucket FROM account_profit_legacy_buckets WHERE bucket < account_usage_bucket(now()) ORDER BY bucket DESC LIMIT 1))
 WHERE EXISTS (SELECT 1 FROM account_usage_financial_pending WHERE bucket < account_usage_bucket(now()))
    OR EXISTS (SELECT 1 FROM account_profit_legacy_buckets WHERE bucket < account_usage_bucket(now()))`).Scan(&bucket)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var acquired bool
	err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(825393,(extract(epoch FROM $1::timestamptz)/300)::integer)`, bucket).Scan(&acquired)
	if err != nil || !acquired {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_usage_financial_rollups WHERE bucket=$1`, bucket); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_usage_financial_rollups (bucket,account_id,group_id,model,public_model,upstream_model,requests,tokens,standard_charge,user_charge,account_charge,duration_sum,duration_count,unit_rate_charge,procurement_source,procurement_amount,image_quantities,standard_token_amount,token_basis_projected)
 SELECT $1::timestamptz,account_id,group_id,model,public_model,upstream_model,
 count(*),COALESCE(sum(tokens),0),COALESCE(sum(standard_charge),0),COALESCE(sum(user_charge),0),
 COALESCE(sum(account_charge),0),sum(duration_sum),sum(duration_count),COALESCE(sum(unit_rate_charge),0),procurement_source,sum(procurement_amount),image_quantities,sum(standard_token_amount),true
 FROM account_profit_events WHERE created_at >= $1 AND created_at < $1::timestamptz+interval '5 minutes'
 GROUP BY account_id,group_id,model,public_model,upstream_model,procurement_source,(procurement_amount IS NULL),image_quantities,(standard_token_amount IS NULL)`, bucket)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_usage_financial_pending WHERE bucket=$1`, bucket); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_profit_legacy_buckets WHERE bucket=$1`, bucket); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Full five-minute buckets use saved statistics. Only partial boundaries and
// the most recent five to ten minutes can read events. All three ranges are
// disjoint even for a window shorter than one bucket.
const financialWindowBounds = `WITH bounds AS (
 SELECT LEAST($2::timestamptz, CASE WHEN $1=account_usage_bucket($1) THEN $1 ELSE account_usage_bucket($1)+interval '5 minutes' END) AS lo,
 GREATEST($1::timestamptz,LEAST(account_usage_bucket($2),account_usage_bucket(now())-interval '5 minutes')) AS hi
)`

const accountUsageWindow = financialWindowBounds + `, facts AS (
 SELECT bucket AS at,account_id,group_id,model,public_model,upstream_model,requests,tokens,standard_charge,user_charge,account_charge,duration_sum,duration_count,unit_rate_charge
 FROM account_usage_financial_rollups,bounds WHERE bucket>=lo AND bucket<hi AND ($3::bigint=0 OR account_id=$3)
 UNION ALL
 SELECT created_at,account_id,group_id,model,public_model,upstream_model,requests,tokens,standard_charge,user_charge,account_charge,duration_sum,duration_count,unit_rate_charge
 FROM account_usage_financial_events,bounds WHERE created_at>=$1 AND created_at<lo AND ($3::bigint=0 OR account_id=$3)
 UNION ALL
 SELECT created_at,account_id,group_id,model,public_model,upstream_model,requests,tokens,standard_charge,user_charge,account_charge,duration_sum,duration_count,unit_rate_charge
 FROM account_usage_financial_events,bounds WHERE created_at>=GREATEST(lo,hi) AND created_at<$2 AND ($3::bigint=0 OR account_id=$3)
)`

func accountUsageReady(ctx context.Context, tx *sql.Tx, from, until time.Time) error {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_usage_financial_pending
 WHERE bucket >= $1 AND bucket < LEAST(account_usage_bucket($2),account_usage_bucket(now())-interval '5 minutes'))`, from, until).Scan(&pending)
	if err == nil && pending {
		return service.ErrAccountUsageBuilding
	}
	return err
}
