.PHONY: build build-backend build-frontend test test-backend test-frontend test-frontend-critical

FRONTEND_CRITICAL_VITEST := \
	src/components/account/__tests__/OpenAIAuthConnections.spec.ts \
	src/views/admin/__tests__/RiskControlView.spec.ts \
	src/i18n/__tests__/riskControlLocales.spec.ts \
	src/components/common/__tests__/PlatformTypeBadge.openaiPlans.spec.ts \
	src/components/common/__tests__/PlatformTypeBadge.grok.spec.ts \
	src/views/admin/__tests__/AutoConfigView.spec.ts \
	src/utils/__tests__/openaiTwoFAAdmission.spec.ts \
	src/components/admin/account/__tests__/BulkExport2FADialog.spec.ts \
	src/components/admin/account/__tests__/AccountBulkActionsBar.spec.ts \
	src/features/security-penalties/__tests__/SecurityRecords.spec.ts \
	src/views/user/__tests__/ProfileView.spec.ts \
	src/components/account/__tests__/SIWCAccountSettings.spec.ts \
	src/api/admin/__tests__/modelTrace.spec.ts \
	src/components/admin/account/__tests__/ModelTracePanel.spec.ts \
	src/components/admin/account/__tests__/IQTestModal.modelTrace.spec.ts \
	src/components/admin/account/__tests__/IQTestModal.spec.ts \
	src/components/admin/account/__tests__/PelicanRecordsDashboard.spec.ts \
	src/api/admin/__tests__/scheduledTests.spec.ts \
	src/features/ip-risk/__tests__/IPRiskSettings.spec.ts \
	src/components/admin/account/__tests__/OpenAISessionExportDialog.spec.ts \
	src/components/admin/account/__tests__/AccountActionMenu.spark_shadow.spec.ts \
	src/api/__tests__/groupCacheTest.spec.ts \
	src/views/admin/__tests__/GroupCacheTestView.spec.ts \
	src/components/account/__tests__/OpenAIRequestTimezoneField.spec.ts \
	src/components/admin/__tests__/HarvestGatewayBorrowPanel.spec.ts \
	src/components/admin/__tests__/AstraGatewayRuntime.spec.ts \
	src/components/admin/__tests__/AstraGatewayHistory.spec.ts \
	src/views/admin/ops/__tests__/TokenGuardV2View.spec.ts \
	src/features/channel-monitor-v2/__tests__/MonitorCandySettings.spec.ts \
	src/features/channel-monitor-v2/__tests__/MonitorStatusCards.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorCards.spec.ts \
	src/features/channel-monitor-v3/__tests__/monitorV3.spec.ts \
	src/features/channel-monitor-v3/__tests__/StatusPage.spec.ts \
	src/features/channel-monitor-v3/__tests__/V3SettingsPanel.spec.ts \
	src/features/channel-monitor-v3/__tests__/IdleProbeSettings.spec.ts \
	src/features/support-tickets/__tests__/supportTickets.spec.ts \
	src/features/support-tickets/__tests__/TicketComponents.spec.ts \
	src/views/user/__tests__/SupportTicketsViews.spec.ts \
	src/views/admin/__tests__/SupportTicketsAdmin.spec.ts \
	src/stores/__tests__/supportTickets.spec.ts \
	src/views/admin/__tests__/ChannelMonitorView.modeSwitch.spec.ts \
	src/views/admin/__tests__/AccountQualityView.spec.ts \
	src/views/admin/__tests__/AccountsView.bulkEdit.spec.ts \
	src/utils/__tests__/qualityRulePatch.spec.ts \
	src/components/admin/operations/__tests__/QualityOwnershipReview.spec.ts \
	src/utils/__tests__/accountAutoBPS.spec.ts \
	src/components/admin/operations/__tests__/QualityProbeSchedule.spec.ts \
	src/components/admin/operations/__tests__/SmartOpsNav.spec.ts \
	src/components/layout/__tests__/AppSidebar.spec.ts \
	src/components/account/__tests__/CreateAccountModal.autoBPS.spec.ts \
	src/components/account/__tests__/EditAccountModal.autoBPS.spec.ts \
	src/stores/__tests__/accountQuality.spec.ts \
	src/api/__tests__/observerUsage.spec.ts \
	src/views/admin/__tests__/UsageView.spec.ts \
	src/components/common/__tests__/DateRangePicker.rolling24h.spec.ts \
	src/components/common/__tests__/DateRangePicker.spec.ts \
	src/components/common/__tests__/DateRangePicker.dismiss.spec.ts \
	src/components/common/__tests__/DateRangePicker.midnight.spec.ts \
	src/components/admin/usage/__tests__/UsageStatsCards.spec.ts \
	src/components/admin/usage/__tests__/UsageCleanupDialog.spec.ts \
	src/views/user/__tests__/UsageView.spec.ts \
	src/components/usage/__tests__/TrafficPanel.spec.ts \
	src/api/__tests__/traffic.spec.ts \
	src/views/user/__tests__/UsageEntryView.spec.ts \
	src/components/admin/usage/__tests__/UsageFilters.spec.ts \
	src/components/admin/usage/__tests__/LatencyObservabilityPanel.spec.ts \
	src/components/admin/usage/__tests__/UsageTimingDialog.spec.ts \
	src/components/admin/usage/__tests__/UsageTable.spec.ts \
	src/views/user/__tests__/PelicanShowcaseView.spec.ts \
	src/utils/__tests__/usageTps.spec.ts \
	src/utils/__tests__/usageCacheRate.spec.ts \
	src/views/admin/ops/components/__tests__/OpsErrorDetailModal.spec.ts \
	src/router/__tests__/feature-access.spec.ts \
	src/api/admin/__tests__/requestCaptures.spec.ts \
	src/views/admin/__tests__/RequestCaptureView.spec.ts \
	src/stores/__tests__/adminSettings.retry.spec.ts \
	src/i18n/__tests__/localeKeyCompleteness.spec.ts \
	src/api/__tests__/client.spec.ts \
	src/api/__tests__/keys.spec.ts \
	src/api/__tests__/tokenRefresh.spec.ts \
	src/api/__tests__/keys.bulkUpdate.spec.ts \
	src/composables/__tests__/useModelWhitelist.spec.ts \
	src/components/account/__tests__/UpstreamBillingRateCell.spec.ts \
	src/components/account/__tests__/OpenAIReferralCell.spec.ts \
	src/components/account/__tests__/OpenAIReferralCell.transport.spec.ts \
	src/components/account/__tests__/OpenAIQuotaResetCell.spark_shadow.spec.ts \
	src/components/account/__tests__/ExcelBPS403Badge.spec.ts \
	src/components/account/__tests__/BulkEditAccountModal.spec.ts \
	src/constants/__tests__/platforms.spec.ts \
	src/components/account/__tests__/credentialsBuilder.platformCatalog.spec.ts \
	src/components/account/__tests__/CreateAccountModal.spec.ts \
	src/components/account/__tests__/EditAccountModal.spec.ts \
	src/components/account/__tests__/credentialsBuilder.spec.ts \
	src/components/account/__tests__/OpenCodeGoProtocolRulesEditor.spec.ts \
	src/components/keys/__tests__/BulkEditKeysModal.spec.ts \
	src/components/admin/user/__tests__/UserPlatformQuotaModal.spec.ts \
	src/components/admin/user/__tests__/UserEditModal.spec.ts \
	src/components/admin/moderation/__tests__/ModerationUsers.spec.ts \
	src/components/admin/moderation/__tests__/ModerationUserQuickEnable.spec.ts \
	src/api/__tests__/admin.users.spec.ts \
	src/views/user/__tests__/KeysView.spec.ts \
	src/api/__tests__/channelMonitorV2.spec.ts \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/views/user/__tests__/ChannelStatusView.mode.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/components/settings/ServerlessSettings.spec.ts \
	src/components/settings/AccountPerformanceSettings.spec.ts \
	src/components/account/__tests__/AccountPerformanceCell.spec.ts \
	src/components/account/__tests__/AccountPerformanceDialog.spec.ts \
	src/components/user/profile/__tests__/ProfileIdentityBindingsSection.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts \
	src/views/admin/__tests__/HarvestFlowView.spec.ts \
	src/views/admin/settings/MihomoSettings.spec.ts \
	src/views/admin/settings/MihomoCountryFilter.spec.ts \
	src/features/channel-monitor-v2/__tests__/designSystem.structure.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorFormat.spec.ts \
	src/features/channel-monitor-v2/__tests__/monitorZoom.spec.ts \
	src/components/admin/channel/__tests__/PricingEntryCard.modelDefaultPrice.spec.ts

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck
	@$(MAKE) test-frontend-critical

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST) src/components/admin/usage/__tests__/UsageConnections.spec.ts
