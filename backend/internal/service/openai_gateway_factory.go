package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
)

// The production composition root attaches optional services before starting
// the harvest loop; no post-construction setter races with startup.
type OpenAIGatewayDependencies struct {
	GroupProtection  *GroupAntiDistillationService
	TimezoneProber   ProxyExitInfoProber
	RegionalMetering *RegionalMetering
	Accounts         AccountRepository
	Proxies          ProxyRepository
	UsageLogs        UsageLogRepository
	UsageBilling     UsageBillingRepository
	Users            UserRepository
	Subscriptions    UserSubscriptionRepository
	GroupRates       UserGroupRateRepository
	Cache            GatewayCache
	Config           *config.Config
	Scheduler        *SchedulerSnapshotService
	Concurrency      *ConcurrencyService
	Billing          *BillingService
	RateLimit        *RateLimitService
	BillingCache     *BillingCacheService
	Upstream         HTTPUpstream
	Deferred         *DeferredService
	OpenAITokens     *OpenAITokenProvider
	GrokTokens       *GrokTokenProvider
	Pricing          *ModelPricingResolver
	Channels         *ChannelService
	BalanceNotify    *BalanceNotifyService
	Settings         *SettingService
	Quotas           UserPlatformQuotaRepository
	RPMCache         RPMCache
	Harvest          *CodexHarvestService

	SafetyPromptStore *SafetyPromptStore
}

func ProvideOpenAIGatewayService(d OpenAIGatewayDependencies) *OpenAIGatewayService {
	if d.RegionalMetering != nil && d.RegionalMetering.Run != nil {
		go d.RegionalMetering.Run(context.Background())
	}
	return NewOpenAIGatewayService(d.Accounts, d.Proxies, d.UsageLogs, d.UsageBilling, d.Users, d.Subscriptions,
		d.GroupRates, d.Cache, d.Config, d.Scheduler, d.Concurrency, d.Billing, d.RateLimit,
		d.BillingCache, d.Upstream, d.Deferred, d.OpenAITokens, d.GrokTokens, d.Pricing,
		d.Channels, d.BalanceNotify, d.Settings, d.Quotas,
		WithOpenAIRPMCache(d.RPMCache),
		WithRegionalMetering(d.RegionalMetering),
		func(s *OpenAIGatewayService) {
			s.SetGroupAntiDistillationService(d.GroupProtection)
			s.codexHarvest = d.Harvest
			s.requestTimezoneProber = d.TimezoneProber
			s.safetyPromptStore = d.SafetyPromptStore
		})
}
