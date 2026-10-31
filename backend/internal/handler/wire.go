package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
)

// ProvideAdminHandlers creates the AdminHandlers struct
func ProvideAdminHandlers(
	requestCaptureHandler *admin.RequestCaptureHandler,
	dashboardHandler *admin.DashboardHandler,
	userHandler *admin.UserHandler,
	groupHandler *admin.GroupHandler,
	accountHandler *admin.AccountHandler,
	openaiOAuthReauthService *service.OpenAIOAuthReauthService,
	announcementHandler *admin.AnnouncementHandler,
	dataManagementHandler *admin.DataManagementHandler,
	backupHandler *admin.BackupHandler,
	oauthHandler *admin.OAuthHandler,
	openaiOAuthHandler *admin.OpenAIOAuthHandler,
	openaiOAuthReauthHandler *admin.OpenAIOAuthReauthHandler,
	geminiOAuthHandler *admin.GeminiOAuthHandler,
	antigravityOAuthHandler *admin.AntigravityOAuthHandler,
	grokOAuthHandler *admin.GrokOAuthHandler,
	cnProviderHandler *admin.CNProviderHandler,
	proxyHandler *admin.ProxyHandler,
	redeemHandler *admin.RedeemHandler,
	promoHandler *admin.PromoHandler,
	settingHandler *admin.SettingHandler,
	opsHandler *admin.OpsHandler,
	systemHandler *admin.SystemHandler,
	subscriptionHandler *admin.SubscriptionHandler,
	usageHandler *admin.UsageHandler,
	userAttributeHandler *admin.UserAttributeHandler,
	errorPassthroughHandler *admin.ErrorPassthroughHandler,
	tlsFingerprintProfileHandler *admin.TLSFingerprintProfileHandler,
	pluginHandler *admin.PluginHandler,
	apiKeyHandler *admin.AdminAPIKeyHandler,
	scheduledTestHandler *admin.ScheduledTestHandler,
	pelicanGroupTestHandler *admin.PelicanGroupTestHandler,
	controlledExperimentHandler *admin.ControlledExperimentHandler,
	accountOpsHandler *admin.AccountOpsHandler,
	accountTokenGuardHandler *admin.AccountTokenGuardHandler,
	accountTokenGuardV2Handler *admin.AccountTokenGuardV2Handler,
	channelHandler *admin.ChannelHandler,
	channelMonitorHandler *admin.ChannelMonitorHandler,
	channelMonitorTemplateHandler *admin.ChannelMonitorRequestTemplateHandler,
	contentModerationHandler *admin.ContentModerationHandler,
	promptAuditHandler *securityaudit.PromptAdminHandler,
	paymentHandler *admin.PaymentHandler,
	affiliateHandler *admin.AffiliateHandler,
	complianceHandler *admin.ComplianceHandler,
	auditLogHandler *admin.AuditLogHandler,
	upstreamBillingProbe *service.UpstreamBillingProbeService,
	ollamaCloudUsage *service.OllamaCloudUsageService,
	opencodeGoUsage *service.OpenCodeGoUsageService,
	settingService *service.SettingService,
	codexHarvest *service.CodexHarvestService,
	openAIGatewayService *service.OpenAIGatewayService,
) *AdminHandlers {
	accountHandler.SetUpstreamBillingProbeService(upstreamBillingProbe)
	accountHandler.SetOllamaCloudUsageService(ollamaCloudUsage)
	accountHandler.SetCodexTicketSettings(settingService)
	accountHandler.SetCodexHarvestService(codexHarvest)
	accountHandler.SetOpenAIGatewayService(openAIGatewayService)
	accountHandler.SetOpenCodeGoUsageService(opencodeGoUsage)
	accountHandler.SetOpenAIOAuthReauthService(openaiOAuthReauthService)
	return &AdminHandlers{
		RequestCapture:         requestCaptureHandler,
		Dashboard:              dashboardHandler,
		User:                   userHandler,
		Group:                  groupHandler,
		Account:                accountHandler,
		Announcement:           announcementHandler,
		DataManagement:         dataManagementHandler,
		Backup:                 backupHandler,
		OAuth:                  oauthHandler,
		OpenAIOAuth:            openaiOAuthHandler,
		OpenAIOAuthReauth:      openaiOAuthReauthHandler,
		GeminiOAuth:            geminiOAuthHandler,
		AntigravityOAuth:       antigravityOAuthHandler,
		GrokOAuth:              grokOAuthHandler,
		CNProvider:             cnProviderHandler,
		Proxy:                  proxyHandler,
		Redeem:                 redeemHandler,
		Promo:                  promoHandler,
		Setting:                settingHandler,
		Ops:                    opsHandler,
		System:                 systemHandler,
		Subscription:           subscriptionHandler,
		Usage:                  usageHandler,
		UserAttribute:          userAttributeHandler,
		ErrorPassthrough:       errorPassthroughHandler,
		TLSFingerprintProfile:  tlsFingerprintProfileHandler,
		Plugin:                 pluginHandler,
		APIKey:                 apiKeyHandler,
		AccountOps:             accountOpsHandler,
		AccountTokenGuard:      accountTokenGuardHandler,
		AccountTokenGuardV2:    accountTokenGuardV2Handler,
		ScheduledTest:          scheduledTestHandler,
		PelicanGroupTest:       pelicanGroupTestHandler,
		ControlledExperiment:   controlledExperimentHandler,
		Channel:                channelHandler,
		ChannelMonitor:         channelMonitorHandler,
		ChannelMonitorTemplate: channelMonitorTemplateHandler,
		ContentModeration:      contentModerationHandler,
		PromptAudit:            promptAuditHandler,
		Payment:                paymentHandler,
		Affiliate:              affiliateHandler,
		Compliance:             complianceHandler,
		AuditLog:               auditLogHandler,
	}
}

func ProvideGatewayHandler(
	gatewayService *service.GatewayService,
	openAIGatewayService *service.OpenAIGatewayService,
	geminiCompatService *service.GeminiMessagesCompatService,
	antigravityGatewayService *service.AntigravityGatewayService,
	userService *service.UserService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	usageService *service.UsageService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	errorPassthroughService *service.ErrorPassthroughService,
	contentModerationService *service.ContentModerationService,
	userMsgQueueService *service.UserMessageQueueService,
	cfg *config.Config,
	settingService *service.SettingService,
	coordinator *securityaudit.Coordinator,
	geminiCachedContentService *service.GeminiCachedContentService,
) *GatewayHandler {
	h := NewGatewayHandler(gatewayService, openAIGatewayService, geminiCompatService, antigravityGatewayService,
		userService, concurrencyService, billingCacheService, usageService, apiKeyService, usageRecordWorkerPool,
		errorPassthroughService, contentModerationService, userMsgQueueService, cfg, settingService)
	h.securityAuditCoordinator = coordinator
	h.geminiCachedContentService = geminiCachedContentService
	return h
}

func ProvideOpenAIGatewayHandler(
	gatewayService *service.OpenAIGatewayService,
	pluginManager *service.PluginManager,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	errorPassthroughService *service.ErrorPassthroughService,
	contentModerationService *service.ContentModerationService,
	opsService *service.OpsService,
	grokQuotaService *service.GrokQuotaService,
	cfg *config.Config,
	coordinator *securityaudit.Coordinator,
	compositeResolver *service.CompositeRouteResolver,
) *OpenAIGatewayHandler {
	gatewayService.SetPluginManager(pluginManager)
	h := NewOpenAIGatewayHandler(gatewayService, concurrencyService, billingCacheService, apiKeyService,
		usageRecordWorkerPool, errorPassthroughService, contentModerationService, opsService, cfg)
	h.compositeResolver = compositeResolver
	h.securityAuditCoordinator = coordinator
	h.grokMediaEligibilityProber = grokQuotaService
	return h
}

func ProvideBatchImageHandler(
	batchService *service.BatchImagePublicService,
	download *service.BatchImageDownloadService,
	cleanup *service.BatchImageCleanupService,
	openAI *OpenAIGatewayHandler,
) *BatchImageHandler {
	h := NewBatchImageHandler(batchService, download, cleanup)
	h.openAI = openAI
	return h
}

// ProvideSystemHandler creates admin.SystemHandler with UpdateService and the
// Mihomo node checks backed by the admin proxy diagnostics.
func ProvideSystemHandler(updateService *service.UpdateService, lockService *service.SystemOperationLockService, adminService service.AdminService) *admin.SystemHandler {
	h := admin.NewSystemHandler(updateService, lockService)
	h.SetMihomoNodeChecker(adminService)
	return h
}

// ProvideSettingHandler creates SettingHandler with version from BuildInfo
func ProvideSettingHandler(settingService *service.SettingService, buildInfo BuildInfo, notificationEmailService *service.NotificationEmailService) *SettingHandler {
	h := NewSettingHandler(settingService, buildInfo.Version)
	h.SetNotificationEmailService(notificationEmailService)
	return h
}

// ProvideAdminSettingHandler creates admin.SettingHandler with notification template APIs.
func ProvideAdminSettingHandler(settingService *service.SettingService, emailService *service.EmailService, turnstileService *service.TurnstileService, aliyunCaptchaService *service.AliyunCaptchaService, opsService *service.OpsService, paymentConfigService *service.PaymentConfigService, paymentService *service.PaymentService, userAttributeService *service.UserAttributeService, notificationEmailService *service.NotificationEmailService, totpService *service.TotpService, userService *service.UserService) *admin.SettingHandler {
	h := admin.NewSettingHandler(settingService, emailService, turnstileService, opsService, paymentConfigService, paymentService, userAttributeService)
	h.SetNotificationEmailService(notificationEmailService)
	h.SetAliyunCaptchaService(aliyunCaptchaService)
	h.SetStepUpDeps(totpService, userService)
	return h
}

// ProvideHandlers creates the Handlers struct
func ProvideHandlers(
	lotteryHandler *LotteryHandler,
	authHandler *AuthHandler,
	userHandler *UserHandler,
	apiKeyHandler *APIKeyHandler,
	usageHandler *UsageHandler,
	redeemHandler *RedeemHandler,
	subscriptionHandler *SubscriptionHandler,
	announcementHandler *AnnouncementHandler,
	channelMonitorUserHandler *ChannelMonitorUserHandler,
	channelMonitorV2Handler *ChannelMonitorV2Handler,
	channelMonitorV3Handler *ChannelMonitorV3Handler,
	adminHandlers *AdminHandlers,
	gatewayHandler *GatewayHandler,
	openaiGatewayHandler *OpenAIGatewayHandler,
	settingHandler *SettingHandler,
	totpHandler *TotpHandler,
	passkeyHandler *PasskeyHandler,
	paymentHandler *PaymentHandler,
	paymentWebhookHandler *PaymentWebhookHandler,
	availableChannelHandler *AvailableChannelHandler,
	pelicanShowcaseHandler *PelicanShowcaseHandler,
	supportTicketHandler *SupportTicketHandler,
	modelPlazaHandler *ModelPlazaHandler,
	asyncImageHandler *AsyncImageHandler,
	batchImageHandler *BatchImageHandler,
	_ *service.IdempotencyCoordinator,
	_ *service.IdempotencyCleanupService,
	_ *service.OpenAIQuotaAutoResetService,
) *Handlers {
	return &Handlers{
		Lottery:          lotteryHandler,
		Auth:             authHandler,
		User:             userHandler,
		APIKey:           apiKeyHandler,
		Usage:            usageHandler,
		Redeem:           redeemHandler,
		Subscription:     subscriptionHandler,
		Announcement:     announcementHandler,
		ChannelMonitor:   channelMonitorUserHandler,
		ChannelMonitorV2: channelMonitorV2Handler,
		ChannelMonitorV3: channelMonitorV3Handler,
		Admin:            adminHandlers,
		Gateway:          gatewayHandler,
		OpenAIGateway:    openaiGatewayHandler,
		Setting:          settingHandler,
		Totp:             totpHandler,
		Passkey:          passkeyHandler,
		Payment:          paymentHandler,
		PaymentWebhook:   paymentWebhookHandler,
		AvailableChannel: availableChannelHandler,
		PelicanShowcase:  pelicanShowcaseHandler,
		SupportTicket:    supportTicketHandler,
		ModelPlaza:       modelPlazaHandler,
		AsyncImage:       asyncImageHandler,
		BatchImage:       batchImageHandler,
	}
}

// ProviderSet is the Wire provider set for all handlers
var ProviderSet = wire.NewSet(
	// Top-level handlers
	NewLotteryHandler,
	NewAuthHandler,
	NewUserHandler,
	NewAPIKeyHandler,
	NewUsageHandler,
	NewRedeemHandler,
	NewSubscriptionHandler,
	NewAnnouncementHandler,
	NewChannelMonitorUserHandler,
	NewChannelMonitorV2Handler,
	NewChannelMonitorV3Handler,
	ProvideGatewayHandler,
	ProvideOpenAIGatewayHandler,
	NewTotpHandler,
	NewPasskeyHandler,
	ProvideSettingHandler,
	NewPaymentHandler,
	NewPaymentWebhookHandler,
	NewAvailableChannelHandler,
	NewPelicanShowcaseHandler,
	NewSupportTicketHandler,
	NewModelPlazaHandler,
	NewAsyncImageHandler,
	ProvideBatchImageHandler,

	// Admin handlers
	admin.NewDashboardHandler,
	admin.NewUserHandler,
	admin.NewGroupHandlerWithConfig,
	admin.ProvideAccountHandler,
	admin.NewAnnouncementHandler,
	admin.NewDataManagementHandler,
	admin.NewBackupHandler,
	admin.NewOAuthHandler,
	admin.NewOpenAIOAuthHandler,
	admin.NewOpenAIOAuthReauthHandler,
	admin.NewGeminiOAuthHandler,
	admin.NewAntigravityOAuthHandler,
	admin.NewGrokOAuthHandler,
	admin.NewCNProviderHandler,
	admin.NewProxyHandler,
	admin.NewRedeemHandler,
	admin.NewPromoHandler,
	ProvideAdminSettingHandler,
	admin.NewOpsHandler,
	ProvideSystemHandler,
	admin.NewSubscriptionHandler,
	admin.NewUsageHandler,
	admin.NewUserAttributeHandler,
	admin.NewErrorPassthroughHandler,
	admin.NewTLSFingerprintProfileHandler,
	admin.NewPluginHandler,
	admin.NewAdminAPIKeyHandler,
	admin.NewScheduledTestHandler,
	admin.NewPelicanGroupTestHandler,
	admin.NewControlledExperimentHandler,
	admin.NewAccountOpsHandler,
	admin.NewAccountTokenGuardHandler,
	admin.NewAccountTokenGuardV2Handler,
	admin.NewChannelHandler,
	admin.NewChannelMonitorHandler,
	admin.NewChannelMonitorRequestTemplateHandler,
	admin.NewContentModerationHandlerWithSafetyPrompt,
	admin.NewPaymentHandler,
	admin.NewAffiliateHandler,
	admin.NewComplianceHandler,
	admin.NewAuditLogHandler,
	admin.NewRequestCaptureHandler,

	// AdminHandlers and Handlers constructors
	ProvideAdminHandlers,
	ProvideHandlers,
)
