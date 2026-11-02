package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
)

// NewRegionalSlotAuthority uses authoritative limits in the SAME transaction
// as global allocation. Region callers cannot choose or raise the total limit.
// The runtime must route primary acquisitions through this authority before
// enabling regional grants; a legacy Redis-only primary would overallocate.
func NewRegionalSlotAuthority(db *sql.DB) *slots.Primary {
	return &slots.Primary{DB: db, Limit: func(ctx context.Context, tx *sql.Tx, r slots.Resource) (int, error) {
		var query string
		switch r.Kind {
		case "account":
			query = `SELECT CASE WHEN EXISTS (SELECT 1 FROM account_performance_monitors m WHERE m.account_id=accounts.id AND m.paused) THEN 0 ELSE concurrency END FROM accounts WHERE id=$1 AND deleted_at IS NULL AND status='active' AND schedulable=true FOR SHARE`
		case "user":
			query = `SELECT concurrency FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' FOR SHARE`
		case "api_key":
			query = `SELECT concurrency_limit FROM api_keys WHERE id=$1 AND deleted_at IS NULL AND status='active' AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE`
		default:
			return 0, errors.New("invalid slot resource kind")
		}
		var limit int
		err := tx.QueryRowContext(ctx, query, r.ID).Scan(&limit)
		return limit, err
	}}
}
