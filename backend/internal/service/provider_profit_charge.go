package service

import (
	"context"
	"strings"
	"time"
	"unicode"
)

const ProviderProfitClockSkew = 5 * time.Minute

// A legacy local ID cut at 128 bytes could lose up to three more bytes
// when backing up to a valid UTF-8 boundary. Never match a possible prefix.
func ProviderProfitRequestIDUsable(id string) bool {
	return id != "" && len(id) < 125 && !strings.ContainsRune(id, ',') && strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

type ProviderProfitWindow struct {
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
}

func (w ProviderProfitWindow) Valid() bool {
	return !w.From.IsZero() && !w.Until.IsZero() && w.From.Before(w.Until) && w.Until.Sub(w.From) <= 32*24*time.Hour && w.From.Year() > 1 && w.Until.Year() < 9999
}

func (w ProviderProfitWindow) Contains(at time.Time) bool {
	return !at.Before(w.From) && at.Before(w.Until)
}

// ProviderProfitRemoteCharge is an allowlist, not the upstream DTO (which
// can contain a complete API key). Amount is the exact reported actual_cost,
// not confirmation of a wallet debit. Only explicit settlement can fill
// SettledAmount. Neither is a local billing command.
type ProviderProfitRemoteCharge struct {
	UsageID   int64
	KeyID     int64
	UserID    int64
	RequestID string
	// UniqueAtSite is positive evidence from all authenticated pages, before
	// filtering out unpriced and outside-window rows. Required for an unknown key.
	UniqueAtSite  bool
	CreatedAt     time.Time
	Amount        string
	SettledAmount *string
}

// Counts are LOCAL candidate usage rows in [from,until), including identical
// matches from earlier syncs. Unrelated rows on a shared wallet are not part
// of either count. Missing or ambiguous evidence counts as unmatched.
type ProviderProfitSyncResult struct {
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`
}

// Kept separate so existing channel/catalog repositories and mocks do not
// acquire any new dependencies or methods.
type ProviderProfitChargeRepository interface {
	ProviderProfitIdentityFrom(context.Context, int64) (time.Time, error)
	ReconcileProviderProfitCharges(context.Context, int64, time.Time, string, ProviderProfitWindow, []ProviderProfitRemoteCharge) (ProviderProfitSyncResult, error)
}
