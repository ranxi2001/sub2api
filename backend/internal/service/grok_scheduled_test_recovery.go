package service

import "context"

type grokTestRecoveryContextKey struct{}

// Background connectivity tests may observe quota immediately, but positive
// recovery belongs after result insertion. This is attempt-local evidence, not
// a durable continuation or permission to replay a probe after a failed save.
type grokTestRecovery struct {
	observed bool
	account  *Account
}

func (s *AccountTestService) recoverGrokTestRateLimit(ctx context.Context, account *Account) {
	if account == nil || account.RateLimitedAt == nil || account.RateLimitResetAt == nil {
		return
	}
	if recovery, ok := ctx.Value(grokTestRecoveryContextKey{}).(*grokTestRecovery); ok {
		// Copy the observed generation, never a later re-read or mutable pointer.
		limitedAt, resetAt := *account.RateLimitedAt, *account.RateLimitResetAt
		recovery.account = &Account{ID: account.ID, RateLimitedAt: &limitedAt, RateLimitResetAt: &resetAt}
		return
	}
	clearGrokRateLimitAfterRecovery(ctx, s.accountRepo, account)
}

func (r *grokTestRecovery) complete(ctx context.Context, accounts AccountRepository) {
	if r != nil && r.account != nil {
		clearGrokRateLimitAfterRecovery(ctx, accounts, r.account)
	}
}
