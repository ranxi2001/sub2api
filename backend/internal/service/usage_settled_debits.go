package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Amount is decimal text in the billing ledger's USD denomination, not a float quote.
type UsageSettledDebit struct {
	UsageID   int64  `json:"usage_id"`
	APIKeyID  int64  `json:"api_key_id"`
	UserID    int64  `json:"user_id"`
	RequestID string `json:"request_id"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
}

type UsageSettledDebitReader interface {
	ReadUsageSettledDebits(context.Context, int64, []int64) ([]UsageSettledDebit, error)
}

func (s *UsageService) SettledDebits(ctx context.Context, user int64, ids []int64) ([]UsageSettledDebit, error) {
	invalid := infraerrors.BadRequest("INVALID_USAGE_IDS", "Expected 1 to 100 distinct positive usage IDs")
	if user <= 0 || len(ids) == 0 || len(ids) > 100 {
		return nil, invalid
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, invalid
		}
		seen[id] = true
	}
	r, ok := s.usageRepo.(UsageSettledDebitReader)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("DEBIT_EVIDENCE_UNAVAILABLE", "Debit evidence unavailable")
	}
	return r.ReadUsageSettledDebits(ctx, user, ids)
}
