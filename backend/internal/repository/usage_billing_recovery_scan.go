package repository

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageBillingRecoveryPosition struct {
	priority    int
	nextAttempt time.Time
	stagedAt    time.Time
	sweepAt     time.Time
	key         service.UsageBillingEventKey
}

type usageBillingRecoveryScan struct {
	sync.Mutex
	after *usageBillingRecoveryPosition
}

func (r *usageBillingRepository) PendingUsageBilling(ctx context.Context, limit int) ([]service.UsageBillingEventKey, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid usage recovery batch size")
	}
	// Selection is only a scan position, never a claim or lease. Advance even
	// when a selected owner is busy and its locked rows cannot be backed off.
	// Pin a database-time cutoff for the sweep: new arrivals and retry updates
	// cannot keep extending its suffix. At the end, wrap so cancellation and
	// retry-time/state changes cannot lose an event behind the cursor. A restart
	// simply begins another sweep.
	r.recoveryScan.Lock()
	defer r.recoveryScan.Unlock()
	release, err := r.admission.acquireSQL(ctx, r.db.Stats().MaxOpenConnections)
	if err != nil {
		return nil, err
	}
	defer release()
	after := r.recoveryScan.after
	// At most one bounded page and, only for an empty suffix, one wrap query.
	// Do not refill indefinitely while live deliveries keep rows locked.
	for range 2 {
		query := `SELECT request_id,api_key_id,CASE WHEN state='pending' THEN 0 ELSE 1 END,next_attempt_at,staged_at,COALESCE($2::timestamptz,NOW())
			FROM usage_billing_events WHERE state IN ('pending','applied')
			AND next_attempt_at<=COALESCE($2::timestamptz,NOW()) AND staged_at<=COALESCE($2::timestamptz,NOW())`
		args := []any{limit, nil}
		if after != nil {
			args[1] = after.sweepAt
			query += ` AND (CASE WHEN state='pending' THEN 0 ELSE 1 END,next_attempt_at,staged_at,request_id,api_key_id)>($3,$4,$5,$6,$7)`
			args = append(args, after.priority, after.nextAttempt, after.stagedAt, after.key.RequestID, after.key.APIKeyID)
		}
		query += ` ORDER BY CASE WHEN state='pending' THEN 0 ELSE 1 END,next_attempt_at,staged_at,request_id,api_key_id LIMIT $1`
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		var keys []service.UsageBillingEventKey
		var last usageBillingRecoveryPosition
		for rows.Next() {
			if err = rows.Scan(&last.key.RequestID, &last.key.APIKeyID, &last.priority, &last.nextAttempt, &last.stagedAt, &last.sweepAt); err != nil {
				break
			}
			keys = append(keys, last.key)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			r.recoveryScan.after = &last
			return keys, nil
		}
		if after == nil {
			r.recoveryScan.after = nil
			return nil, nil
		}
		after = nil
	}
	return nil, nil
}
