package service

import (
	"context"
	"encoding/json"
	"time"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrAutoCreditInvalid  = apperrors.BadRequest("AUTO_CREDIT_INVALID", "Invalid auto-credit configuration or decimal amount")
	ErrAutoCreditConflict = apperrors.Conflict("AUTO_CREDIT_CONFLICT", "Auto-credit configuration changed; reload before saving")
)

// RawMessage distinguishes omitted fields from explicit null. Money is always
// decimal text on this API; float64 is never authoritative for new ledger math.
type AutoCreditPatch struct {
	Version   int64           `json:"version"`
	Enabled   *bool           `json:"enabled"`
	Threshold json.RawMessage `json:"threshold"`
	Amount    json.RawMessage `json:"amount"`
	Cap       json.RawMessage `json:"cap"`
}
type AutoCreditConfig struct {
	Enabled   bool    `json:"enabled"`
	Threshold *string `json:"threshold"`
	Amount    *string `json:"amount"`
	Cap       *string `json:"cap"`
	Version   int64   `json:"version"`
}
type AutoCreditEntry struct {
	ID            int64      `json:"id"`
	UserID        int64      `json:"user_id"`
	Epoch         int64      `json:"epoch"`
	Source        string     `json:"source"`
	Trigger       string     `json:"trigger"`
	Amount        string     `json:"amount"`
	BalanceBefore string     `json:"balance_before"`
	BalanceAfter  string     `json:"balance_after"`
	Note          string     `json:"note"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	SettledAt     *time.Time `json:"settled_at"`
	SettledBy     *int64     `json:"settled_by"`
}
type AutoCreditState struct {
	Config       AutoCreditConfig  `json:"config"`
	Outstanding  string            `json:"outstanding"`
	CapBlocked   bool              `json:"cap_blocked"`
	HasMore      bool              `json:"has_more"`
	NextBeforeID int64             `json:"next_before_id"`
	Entries      []AutoCreditEntry `json:"entries"`
}
type AutoCreditResult struct {
	Issued  bool   `json:"issued"`
	Balance string `json:"balance"`
}
type AutoCreditRepository interface {
	Get(context.Context, int64, string) (*AutoCreditState, error)
	GetPage(context.Context, int64, string, int64) (*AutoCreditState, error)
	OutstandingByUsers(context.Context, []int64) (map[int64]string, error)
	Save(context.Context, int64, int64, AutoCreditPatch) (*AutoCreditState, error)
	Evaluate(context.Context, int64) (*AutoCreditResult, error)
	EvaluateAdmission(context.Context, int64, int64) (*AutoCreditResult, error)
	CanRecover(context.Context, int64, string) (bool, error)
	CreditUserBalance(context.Context, int64) (*string, error)
	Settle(context.Context, int64, int64, int64) (*AutoCreditState, error)
	PendingInvalidations(context.Context) ([]AutoCreditInvalidation, error)
	AcknowledgeInvalidation(context.Context, AutoCreditInvalidation) error
}

type AutoCreditInvalidation struct {
	ID, UserID int64
	Stage      int
}
