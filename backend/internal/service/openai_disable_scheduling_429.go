package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

const OpenAIDisableSchedulingOn429ExtraKey = "openai_disable_scheduling_on_429"

func openAIDisableSchedulingOn429Enabled(account *Account) bool {
	// Evaluate the persisted owner, not a managed connection's projected type.
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
	if s == nil || s.accountRepo == nil || status != http.StatusTooManyRequests || account == nil ||
		account.ID <= 0 || account.Platform != PlatformOpenAI || account.IsShadow() {
		return ctx, false
	}
	if observed, ok := ctx.Value(openAIDisableScheduling429ObservedKey{}).(openAIDisableScheduling429Observation); ok && observed.accountID == account.ID {
		return ctx, observed.disabled
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	// A projected api_key connection can belong to an OAuth account. Conversely,
	// a projected OAuth type/Extra must not opt an API Key owner into this policy.
	// Resolve the owning account authoritatively, retaining one observation even
	// on failure so the common handler does not repeat the read for this error.
	ctx = context.WithValue(ctx, openAIDisableScheduling429ObservedKey{}, openAIDisableScheduling429Observation{accountID: account.ID})
	owner := account
	if account.AuthConnectionID != "" {
		if !account.HasManagedOpenAIConnections() {
			slog.Warn("openai_429_disable_scheduling_owner_unverified", "account_id", account.ID)
			return ctx, false
		}
		var err error
		owner, err = s.accountRepo.GetByID(stateCtx, account.ID)
		if err != nil || owner == nil || owner.ID != account.ID {
			slog.Warn("openai_429_disable_scheduling_owner_read_failed", "account_id", account.ID)
			return ctx, false
		}
	}
	if !openAIDisableSchedulingOn429Enabled(owner) {
		return ctx, false
	}
	err := s.accountRepo.SetSchedulable(stateCtx, owner.ID, false)
	if err != nil {
		// Never log an upstream response, request body or credential here.
		slog.Warn("openai_429_disable_scheduling_failed", "account_id", account.ID)
	} else {
		// SetSchedulable refreshes the shared scheduler snapshot; bridge stale
		// in-flight candidates immediately and stop same-account 429 retries.
		s.notifyAccountSchedulingBlocked(owner, time.Time{}, "openai_429_scheduling_disabled")
		slog.Info("openai_429_scheduling_disabled", "account_id", account.ID)
	}
	ctx = context.WithValue(ctx, openAIDisableScheduling429ObservedKey{}, openAIDisableScheduling429Observation{accountID: account.ID, disabled: err == nil})
	return ctx, err == nil
}
