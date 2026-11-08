package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

const OpenAIDisableSchedulingOn429ExtraKey = "openai_disable_scheduling_on_429"

func openAIDisableSchedulingOn429Enabled(account *Account) bool {
	// Managed connection projections retain the owning account's ID and policy,
	// so their 429s close that account's switch. Spark shadows have their own
	// scheduling identity and quota and must not close the primary account.
	if account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
		account.IsShadow() {
		return false
	}
	enabled, _ := account.Extra[OpenAIDisableSchedulingOn429ExtraKey].(bool)
	return enabled
}

type openAIDisableScheduling429ObservedKey struct{}

type openAIDisableScheduling429Observation struct {
	accountID int64
	disabled  bool
}

// Persist the normal scheduling switch, rather than a temporary cooldown. A
// successful test or an elapsed 429 window must not reopen the account. The
// context marker shares the result across gateway and common error handlers.
func (s *RateLimitService) maybeDisableOpenAISchedulingOn429(ctx context.Context, account *Account, status int) (context.Context, bool) {
	if s == nil || s.accountRepo == nil || status != http.StatusTooManyRequests || !openAIDisableSchedulingOn429Enabled(account) {
		return ctx, false
	}
	if observed, ok := ctx.Value(openAIDisableScheduling429ObservedKey{}).(openAIDisableScheduling429Observation); ok && observed.accountID == account.ID {
		return ctx, observed.disabled
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	err := s.accountRepo.SetSchedulable(stateCtx, account.ID, false)
	if err != nil {
		// Never log an upstream response, request body or credential here.
		slog.Warn("openai_429_disable_scheduling_failed", "account_id", account.ID)
	} else {
		// SetSchedulable refreshes the shared scheduler snapshot; bridge stale
		// in-flight candidates immediately and stop same-account 429 retries.
		s.notifyAccountSchedulingBlocked(account, time.Time{}, "openai_429_scheduling_disabled")
		slog.Info("openai_429_scheduling_disabled", "account_id", account.ID)
	}
	ctx = context.WithValue(ctx, openAIDisableScheduling429ObservedKey{}, openAIDisableScheduling429Observation{accountID: account.ID, disabled: err == nil})
	return ctx, err == nil
}
