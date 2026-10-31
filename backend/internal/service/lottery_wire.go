package service

import (
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
)

func ProvideRedeemService(redeemRepo RedeemCodeRepository, userRepo UserRepository, subscriptions *SubscriptionService, cache RedeemCache, billing *BillingCacheService, client *dbent.Client, auth APIKeyAuthCacheInvalidator, affiliate *AffiliateService, lottery *LotteryService) *RedeemService {
	s := NewRedeemService(redeemRepo, userRepo, subscriptions, cache, billing, client, auth, affiliate)
	s.lottery = lottery
	return s
}

func ProvideAdminService(
	cfg *config.Config, users UserRepository, groups AdminGroupRepository, accounts AdminAccountRepository,
	proxies ProxyRepository, keys APIKeyRepository, codes RedeemCodeRepository, rates UserGroupRateRepository,
	rpm UserRPMCache, billing *BillingCacheService, prober ProxyExitInfoProber, latency ProxyLatencyCache,
	auth APIKeyAuthCacheInvalidator, client *dbent.Client, settings *SettingService, assigner DefaultSubscriptionAssigner,
	subs UserSubscriptionRepository, privacy PrivacyClientFactory, blocker AccountRuntimeBlocker, affiliate *AffiliateService,
	routes CompositeModelRouteRepository, resolver *CompositeRouteResolver, channels ChannelCacheInvalidator, lottery *LotteryService,
) AdminService {
	s := NewAdminService(cfg, users, groups, accounts, proxies, keys, codes, rates, rpm, billing, prober, latency, auth, client, settings, assigner, subs, privacy, blocker, affiliate, routes, resolver, channels)
	s.(*adminServiceImpl).lottery = lottery
	return s
}
