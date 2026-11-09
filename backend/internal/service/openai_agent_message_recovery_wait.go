package service

import (
	"context"
	"encoding/hex"
	"time"
)

// A cooldown is not evidence of incompatible ciphertext. Preserve the existing
// evidence while waiting, without admitting a request or guessing another owner.
func (s *defaultOpenAIAccountScheduler) checkOpenAIAgentRecoveryCooldown(ctx context.Context, req OpenAIAccountScheduleRequest, recovery *openAIAgentRecoveryRequest) error {
	if recovery == nil || recovery.owner == nil || s.service.accountRepo == nil {
		return nil
	}
	// The scheduler snapshot may filter out quota-blocked accounts entirely.
	// Use current authoritative state for this diagnosis; never mutate it.
	account, err := s.service.accountRepo.GetByID(ctx, recovery.owner.accountID)
	if err != nil || account == nil {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	// HTTP continuation requires an API-key owner even when its authorization
	// anchors exist. A quota reset cannot make OAuth previous_response_id valid.
	// Check before retaining any evidence; WS OAuth continuation remains valid.
	if recovery.userID > 0 && req.PreviousResponseID != "" && !account.IsOpenAIApiKey() {
		return openAIAgentRecoveryError("http_previous_response_unsupported")
	}
	now := time.Now()
	var retryAt time.Time
	reason := "owner_rate_limited"
	if account.RateLimitResetAt != nil && account.RateLimitResetAt.After(now) {
		retryAt = *account.RateLimitResetAt
	}
	if account.OverloadUntil != nil && account.OverloadUntil.After(now) && account.OverloadUntil.After(retryAt) {
		retryAt, reason = *account.OverloadUntil, "owner_overloaded"
	}
	if retryAt.IsZero() {
		return nil
	}
	// Revocation, manual pauses and credential/route changes still require
	// intervention; a future quota reset must not disguise those conditions.
	if !account.IsActive() || !account.Schedulable || account.PerformancePaused ||
		(account.AutoPauseOnExpired && account.ExpiresAt != nil && !account.ExpiresAt.After(now)) ||
		(account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now)) ||
		(account.SupportsAccountQuota() && account.IsQuotaExceeded()) ||
		account.HasManagedOpenAIConnections() || account.AuthConnectionID != "" ||
		!account.IsOpenAI() || !s.service.openAIAccountMatchesSchedulingGroup(account, req.GroupID) ||
		(req.RequirePrivacySet && !account.IsPrivacySet()) ||
		!supportsOpenAIRequestModel(account, req.RequestedModel, req.RequireCompact) ||
		!account.IsModelAllowedInGroup(req.GroupID, req.RequestedModel) ||
		!pelicanGroupTestModelAllowed(ctx, account, req.RequestedModel) ||
		!accountSupportsOpenAICapabilitiesForRequest(ctx, account, req.RequestedModel, req.RequiredCapability, req.RequiredImageCapability) ||
		!s.isAccountTransportCompatible(account, req.RequiredTransport, req.RequestedModel) {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	if req.GroupID != nil && s.service.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID) &&
		s.service.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, req.RequestedModel, req.RequireCompact) {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	if vetoed, _ := openAIProfitControlVetoReason(ctx, account); vetoed {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	if err := s.service.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
		return err
	}
	if req.GuardianParentAccountID > 0 && req.GuardianParentAccountID != account.ID {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	if _, excluded := req.ExcludedIDs[account.ID]; excluded {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	// Leave a full normal affinity window after reset, even if the client waits
	// without polling. These are local observations, not provider-side leases.
	ttl := time.Until(retryAt) + s.service.openAIWSSessionStickyTTL()
	storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if req.PreviousResponseID != "" {
		if recovery.userID > 0 {
			store, ok := s.service.getOpenAIWSStateStore().(OpenAIHTTPResponseOwnerRetentionStore)
			if !ok {
				return openAIAgentRecoveryError("owner_unavailable")
			}
			retained, err := store.RefreshHTTPResponseOwner(storeCtx, derefGroupID(req.GroupID), req.PreviousResponseID, recovery.userID, recovery.keyID, ttl)
			if err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
			if !retained {
				return openAIAgentRecoveryError("owner_unavailable")
			}
		}
		store, ok := s.service.getOpenAIWSStateStore().(OpenAIWSResponseAccountRetentionStore)
		if !ok {
			return openAIAgentRecoveryError("owner_unavailable")
		}
		retained, err := store.RefreshResponseAccount(storeCtx, derefGroupID(req.GroupID), req.PreviousResponseID, account.ID, ttl)
		if err != nil {
			return openAIAgentRecoveryError("store_unavailable")
		}
		if !retained {
			return openAIAgentRecoveryError("owner_unavailable")
		}
	}
	if err := s.service.retainOpenAIAgentRecovery(storeCtx, recovery, ttl); err != nil {
		return err
	}
	return &OpenAIAgentRecoveryError{Reason: reason, RetryAt: retryAt}
}

func (s *OpenAIGatewayService) retainOpenAIAgentRecovery(ctx context.Context, request *openAIAgentRecoveryRequest, ttl time.Duration) error {
	if _, shared := s.cache.(OpenAIAgentRecoveryCache); shared {
		cache, ok := s.cache.(OpenAIAgentRecoveryRetentionCache)
		if !ok {
			return openAIAgentRecoveryError("store_unavailable")
		}
		for key := range request.known {
			retained, err := cache.RefreshOpenAIAgentRecovery(ctx, hex.EncodeToString(key[:]), encodeOpenAIAgentRecoveryBinding(*request.owner), ttl)
			if err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
			if !retained {
				return openAIAgentRecoveryError("owner_unavailable")
			}
		}
		return nil
	}
	state := &s.agentMessageRecovery
	state.mu.Lock()
	defer state.mu.Unlock()
	now, until := time.Now(), time.Now().Add(ttl)
	for key := range request.known {
		binding, found := state.bindings[key]
		if !found || !binding.expiresAt.After(now) || binding.conflicted || binding.failed || binding.pending != "" || !sameOpenAIAgentRecoveryRoute(binding, *request.owner) {
			return openAIAgentRecoveryError("owner_unavailable")
		}
	}
	for key := range request.known {
		binding := state.bindings[key]
		if until.After(binding.expiresAt) {
			binding.expiresAt = until
			state.bindings[key] = binding
		}
	}
	return nil
}
