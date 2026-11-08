/**
 * Stable notice IDs. Add an ID when announcing a feature, then reuse it beside
 * every related menu, tab, and setting. Dismissals are saved per admin in that
 * browser; use a new ID to announce the feature again in a later release.
 */
export const NEW_FEATURE_NOTICES = {
  qualityRetestRest: 'quality-retest-rest-initial-announcement',
  keyRisk: 'key-risk-notices-acknowledgement-initial-announcement',
  mistralBudgetUsage: 'mistral-budget-usage-initial-announcement',
  deepSeekSearch: 'deepseek-search-provider-initial-announcement',
  groupAntiDistillation: 'group-openai-anti-distillation-initial-announcement',
  channelPricingTransfer: 'channel-pricing-import-export-initial-announcement',
  channelPricingListActions: 'channel-pricing-list-actions-initial-announcement',
  smartOpsSidebar: 'smart-ops-sidebar-visibility-initial-announcement',
  balanceForecast: 'balance-exhaustion-forecast-initial-announcement',
  serverlessIPRouting: 'serverless-ip-affinity-primary-fallback-initial-announcement',
  accountUpstreamHTTPErrorRate: 'account-upstream-http-error-rate-initial-announcement',
  accountUpstreamHTTPErrorThreshold: 'account-upstream-http-error-threshold-initial-announcement',
  protocolFeatureSwitches: 'protocol-feature-switches-initial-announcement',
  apiKeyModelPolicy: 'api-key-model-policy-initial-announcement',
  apiKeyLongContext: 'api-key-long-context-control-initial-announcement',
  profitOperationsToday: 'profit-operations-today-filter-initial-announcement',
  profitOperationsTimeRanges: 'profit-operations-time-ranges-initial-announcement',
  vipAutoCredit: 'vip-auto-credit-initial-announcement',
  oauthInitialTOTPRotation429: 'oauth-initial-totp-rotation-on-429-initial-announcement',
  aiOpsCodex: 'ai-ops-codex-observe-initial-announcement',
  bepusdtPayment: 'bepusdt-embedded-payment-initial-announcement',
  accountDailyScheduling: 'account-daily-schedulable-timers-initial-announcement',
  accountAutoRefreshUpstreamModels: 'account-auto-refresh-upstream-models-initial-announcement',
  streamFirstEventTiming: 'stream-first-event-timing-initial-announcement',
  openaiImageHeaderTimeoutGroups: 'openai-image-header-timeout-groups-initial-announcement',
  oauthInitialModelSelection: 'oauth-initial-model-selection-initial-announcement',
} as const
