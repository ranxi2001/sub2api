package service

import (
	"context"
	"slices"
)

// AllowsAccount applies request-error overrides independently of threshold rules.
// Missing overrides preserve the global notification policy for existing accounts.
func (c AccountOpsConfig) AllowsAccount(kind string, accountID int64) bool {
	return c.Allows(kind) && (kind != "balance_low" || !slices.Contains(c.BalanceLowDisabledAccountIDs, accountID))
}

func (s *AccountOpsService) SetBalanceLowNotification(ctx context.Context, id int64, enabled bool) (AccountOpsConfig, error) {
	if id <= 0 || s.accounts == nil {
		return AccountOpsConfig{}, accountOpsConfigValidation("account is required")
	}
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return AccountOpsConfig{}, err
	}
	if account == nil {
		return AccountOpsConfig{}, ErrAccountNotFound
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		ids := slices.Clone(c.BalanceLowDisabledAccountIDs)
		if enabled {
			ids = slices.DeleteFunc(ids, func(value int64) bool { return value == id })
		} else if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
		// A non-nil empty slice is an explicit clear, including for legacy saves.
		c.BalanceLowDisabledAccountIDs = append([]int64{}, ids...)
		return nil
	})
}
