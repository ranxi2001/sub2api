package service

import (
	"context"
	"fmt"
	"time"
)

// This is a read-only history type, not a redeemable code type.
const BalanceHistoryTypeAutoCredit = "auto_credit"

// AutoCreditBalanceHistory preserves ledger precision and collection metadata
// without exposing the billing request identity in the balance history API.
type AutoCreditBalanceHistory struct {
	LedgerID      int64      `json:"ledger_id"`
	Amount        string     `json:"amount"`
	BalanceBefore string     `json:"balance_before"`
	BalanceAfter  string     `json:"balance_after"`
	Status        string     `json:"status"`
	SettledAt     *time.Time `json:"settled_at"`
	SettledBy     *int64     `json:"settled_by"`
}

func (s *adminServiceImpl) listAutoCreditBalanceHistory(ctx context.Context, userID int64, offset, limit int) ([]RedeemCode, int64, error) {
	if s == nil || s.entClient == nil || userID <= 0 || limit <= 0 {
		return nil, 0, nil
	}
	// Count separately so an empty page still reports the complete total.
	counts, err := s.entClient.QueryContext(ctx, `SELECT COUNT(*) FROM auto_credit_ledger WHERE user_id=$1`, userID)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if counts.Next() {
		err = counts.Scan(&total)
	}
	if err == nil {
		err = counts.Err()
	}
	_ = counts.Close()
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.entClient.QueryContext(ctx, `
SELECT id,amount::double precision,amount::text,balance_before::text,balance_after::text,
       note,status,created_at,settled_at,settled_by
FROM auto_credit_ledger
WHERE user_id=$1
ORDER BY created_at DESC,id DESC
OFFSET $2 LIMIT $3`, userID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	codes := make([]RedeemCode, 0)
	for rows.Next() {
		credit := &AutoCreditBalanceHistory{}
		code := RedeemCode{Type: BalanceHistoryTypeAutoCredit, Status: StatusUsed, UsedBy: &userID, AutoCredit: credit}
		if err := rows.Scan(&credit.LedgerID, &code.Value, &credit.Amount, &credit.BalanceBefore, &credit.BalanceAfter,
			&code.Notes, &credit.Status, &code.CreatedAt, &credit.SettledAt, &credit.SettledBy); err != nil {
			return nil, 0, err
		}
		code.ID = credit.LedgerID
		code.Code = fmt.Sprintf("VIP-%d", credit.LedgerID)
		code.UsedAt = &code.CreatedAt
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return codes, total, nil
}
