package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *usageLogRepository) ReadUsageSettledDebits(ctx context.Context, user int64, ids []int64) ([]service.UsageSettledDebit, error) {
	// The usage log and the debit must BOTH belong to this user. (request_id,api_key_id)
	// is unique on each side. Missing/foreign/unproven ids are indistinguishable.
	rows, err := r.sql.QueryContext(ctx, `SELECT u.id,u.api_key_id,u.user_id,u.request_id,q.wallet_debit::text
		FROM usage_logs u JOIN usage_billing_quotes q ON q.api_key_id=u.api_key_id AND q.request_id=u.request_id
		WHERE u.id=ANY($1::bigint[]) AND u.user_id=$2 AND q.debited_user_id=$2 AND q.wallet_debit>0
		ORDER BY u.id`, pq.Array(ids), user)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.UsageSettledDebit, 0)
	for rows.Next() {
		row := service.UsageSettledDebit{Currency: "USD"}
		if err := rows.Scan(&row.UsageID, &row.APIKeyID, &row.UserID, &row.RequestID, &row.Amount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
