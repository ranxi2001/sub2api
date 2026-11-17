package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (c *regionalConcurrencyCache) refreshUserLiveLoadProof(ctx context.Context, r service.LiveLeaseTransferRequest) error {
	refreshCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := c.database().BeginTx(refreshCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Lock first, then evaluate the clock in a separate statement. A locker
	// that never UPDATEs must not let a precomputed deadline survive the wait.
	var leaseID string
	if err = tx.QueryRowContext(refreshCtx, `SELECT lease_id FROM regional_live_handoffs
WHERE region_id=$1 AND lease_id=$2 FOR UPDATE`, c.region(), r.LeaseID).Scan(&leaseID); err != nil {
		return err
	}
	_, err = tx.ExecContext(refreshCtx, `UPDATE regional_live_handoffs
SET identity=jsonb_set(identity,'{_user_stats_expires_at}',to_jsonb(extract(epoch FROM clock_timestamp())+$6))
WHERE region_id=$1 AND lease_id=$2 AND state='active'
AND (identity->>'AccountID')::bigint=$3 AND (identity->>'UserID')::bigint=$4
AND (identity->>'APIKeyID')::bigint=$5
AND (identity->>'_user_stats_expires_at')::numeric>extract(epoch FROM clock_timestamp())`, c.region(), r.LeaseID, r.AccountID, r.UserID, r.APIKeyID, liveLeaseTTLSeconds)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// A SQL ownership record is permanent; only this bounded, fenced proof permits
// statistics to treat an unlimited-user Live handoff as currently observed.
// No statistics read creates/renews ownership or releases admission budgets.
type userLiveLoadSnapshotRow struct {
	LeaseID  string
	Version  string
	Identity string
	State    string
	UserID   int64
	Source   string
	Fresh    bool
}

func (c *regionalConcurrencyCache) userLiveLoadSnapshot(ctx context.Context, users []service.UserWithConcurrency) ([]userLiveLoadSnapshotRow, error) {
	ids := make([]int64, len(users))
	for i, user := range users {
		ids[i] = user.ID
	}
	rows, err := c.database().QueryContext(ctx, `SELECT lease_id,xmin::text,identity::text,state,
(identity->>'UserID')::bigint,identity->>'UserRequestID',
COALESCE((identity->>'_user_stats_expires_at')::numeric>extract(epoch FROM clock_timestamp()),false)
FROM regional_live_handoffs WHERE region_id=$1
AND (identity->>'UserMax')::int=0 AND (identity->>'UserID')::bigint=ANY($2::bigint[])
ORDER BY lease_id`, c.region(), pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []userLiveLoadSnapshotRow
	for rows.Next() {
		var row userLiveLoadSnapshotRow
		if err := rows.Scan(&row.LeaseID, &row.Version, &row.Identity, &row.State, &row.UserID, &row.Source, &row.Fresh); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
