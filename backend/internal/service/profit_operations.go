package service

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const profitOperationsQueryTimeout = 5 * time.Minute

type ProfitOperationsSnapshot struct {
	UpstreamOperationsSnapshot
	Currency string `json:"currency"`
}

type ProfitOperationsRepository interface {
	ReadProfitOperations(context.Context, UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error)
}

func ValidateProfitOperationsFilter(f UpstreamOperationsFilter) error {
	if !slices.Contains([]string{"requests", "standard_charge", "request_income", "estimated_cost", "estimated_profit", "margin", "name"}, f.Sort) {
		return ErrUpstreamOperationsValidation
	}
	if strings.HasPrefix(f.Upstream, "channel:") {
		id, e := strconv.ParseInt(strings.TrimPrefix(f.Upstream, "channel:"), 10, 64)
		if e != nil || id < 1 || f.Upstream != "channel:"+strconv.FormatInt(id, 10) {
			return ErrUpstreamOperationsValidation
		}
		f.Upstream = ""
	}
	f.Sort = "requests"
	return ValidateUpstreamOperationsFilter(f)
}

func NormalizeProfitOperationsFilter(f *UpstreamOperationsFilter) error {
	if f == nil {
		return ErrUpstreamOperationsValidation
	}
	if strings.HasPrefix(f.Upstream, "channel:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(f.Upstream, "channel:"), 10, 64)
		if err != nil || id < 1 || f.Upstream != "channel:"+strconv.FormatInt(id, 10) || (f.ChannelID != 0 && f.ChannelID != id) {
			return ErrUpstreamOperationsValidation
		}
		f.ChannelID, f.Upstream = id, ""
	}
	return ValidateProfitOperationsFilter(*f)
}
func (s *ChannelService) ProfitOperations(ctx context.Context, f UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error) {
	if err := NormalizeProfitOperationsFilter(&f); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, profitOperationsQueryTimeout)
	defer cancel()
	repo, ok := s.repo.(ProfitOperationsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("PROFIT_OPERATIONS_UNAVAILABLE", "Profit operations data source unavailable")
	}
	out, err := repo.ReadProfitOperations(ctx, f)
	// Database drivers can return their own cancellation error. Preserve the
	// request's deadline/cancellation so callers can distinguish a timeout.
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}

// OperationsCostMultiplier distinguishes the scheduling default from a saved
// estimate, and corrupt legacy metadata from an explicit zero cost.
func OperationsCostMultiplier(a *Account) (*float64, string) {
	if a == nil {
		return nil, "account_missing"
	}
	raw, present := a.Extra[AccountCostMultiplierExtraKey]
	if !present || raw == nil {
		v := DefaultAccountCostMultiplier
		return &v, "current_default_0.1"
	}
	v, ok := accountCostMultiplierNumber(raw)
	if !ok {
		return nil, "invalid_multiplier"
	}
	return &v, "current_saved_multiplier"
}
