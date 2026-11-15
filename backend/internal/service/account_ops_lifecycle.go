package service

import (
	"context"
	"errors"
)

const accountOpsOAuthLifecycleKey = "oauth_lifecycle"

func accountOpsOAuthLifecycle(a *Account) string {
	if value, _ := a.Extra[accountOpsOAuthLifecycleKey].(string); value == "long_term" {
		return value
	}
	// Existing OAuth inventory is disposable. This label never deletes accounts
	// or treats an expiring, refreshable access token as account expiry.
	return "temporary"
}
func (s *AccountOpsService) SetOAuthLifecycle(ctx context.Context, id int64, value string) error {
	if value != "temporary" && value != "long_term" {
		return accountOpsConfigValidation("invalid OAuth lifecycle")
	}
	if s.accounts == nil {
		return errors.New("account repository unavailable")
	}
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if account == nil || account.Type != AccountTypeOAuth {
		return accountOpsConfigValidation("lifecycle applies only to OAuth accounts")
	}
	return s.accounts.UpdateExtra(ctx, id, map[string]any{accountOpsOAuthLifecycleKey: value})
}
