package service

import (
	"context"
	"math"
)

// ProbeAPIKeyGroup checks scheduler eligibility without reserving slots,
// registering sessions, setting account errors, or binding sticky accounts.
func (s *GatewayService) ProbeAPIKeyGroup(ctx context.Context, group *Group, model, kind string) (APIKeyGroupAvailability, error) {
	if primary, restricted := primaryOnlyGroupFromContext(ctx); restricted && (group == nil || group.ID != primary) {
		return APIKeyGroupAvailability{}, nil
	}
	ctx = s.withGroupContext(ctx, group)
	platform, forced, err := s.resolvePlatform(ctx, &group.ID, group, model)
	if err != nil {
		return APIKeyGroupAvailability{}, nil
	}
	if group.Platform == PlatformComposite {
		decision, ok, err := s.resolveCompositeRouteDecision(ctx, group, model, CompositeRouteEndpointAny)
		if err != nil {
			return APIKeyGroupAvailability{}, err
		}
		if !ok {
			return APIKeyGroupAvailability{}, nil
		}
		model = decision.UpstreamModel
		ctx = WithCompositeRouteDecision(ctx, decision)
	}
	if kind == "token" {
		ctx, _ = WithGatewayTokenRequestPricing(ctx)
		ctx = s.withGatewayProfitControlGate(ctx, &group.ID)
	}
	if s.checkChannelPricingRestriction(ctx, &group.ID, model) {
		return APIKeyGroupAvailability{}, nil
	}
	accounts, _, err := s.listSchedulableAccounts(ctx, &group.ID, platform, forced)
	if err != nil {
		return APIKeyGroupAvailability{}, err
	}
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)
	eligible := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if !s.isAccountSchedulableForSelection(account) || !s.isGatewayAccountProfitEligible(ctx, account) ||
			(group.RequirePrivacySet && !account.IsPrivacySet()) ||
			(model != "" && !s.isModelSupportedByAccountInGroup(ctx, account, &group.ID, model)) ||
			!s.isAccountSchedulableForModelSelection(ctx, account, model) ||
			!s.isAccountSchedulableForQuota(account) ||
			!s.isAccountSchedulableForWindowCost(ctx, account, false) ||
			!s.isAccountSchedulableForRPM(ctx, account, false) ||
			s.isUpstreamModelRestrictedByChannel(ctx, group.ID, account, model) {
			continue
		}
		eligible = append(eligible, AccountWithConcurrency{ID: account.ID, MaxConcurrency: account.Concurrency})
	}
	if len(eligible) == 0 {
		return APIKeyGroupAvailability{}, nil
	}
	if s.concurrencyService == nil {
		return APIKeyGroupAvailability{Available: true}, nil
	}
	loads, err := s.concurrencyService.GetAccountsLoadBatch(ctx, eligible)
	if err != nil {
		return APIKeyGroupAvailability{}, err
	}
	minLoad := math.Inf(1)
	for _, account := range eligible {
		if account.MaxConcurrency <= 0 {
			return APIKeyGroupAvailability{Available: true}, nil
		}
		load, ok := loads[account.ID]
		if !ok || load == nil {
			continue
		}
		ratio := float64(load.CurrentConcurrency) / float64(account.MaxConcurrency)
		if ratio < minLoad {
			minLoad = ratio
		}
	}
	if math.IsInf(minLoad, 1) {
		return APIKeyGroupAvailability{}, nil
	}
	return APIKeyGroupAvailability{Available: true, Busy: minLoad >= 1, Load: minLoad}, nil
}
