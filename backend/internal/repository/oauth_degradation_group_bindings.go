package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Membership writers share the account lock with the degradation evaluator.
// Degradation removes native bindings once; later administrator edits may restore
// them. The historical action must never filter an ordinary binding save.
func lockOAuthDegradationGroupBindings(ctx context.Context, exec sqlExecutor, accountID int64, proposed []int64) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, `SELECT COALESCE(extra,'{}'::jsonb) FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, err
	}
	return proposed, rows.Err()
}
