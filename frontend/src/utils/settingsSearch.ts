import type { SearchNavItem } from '@/utils/featureSearch'

export const SETTINGS_TABS = ['general', 'agreement', 'features', 'security', 'users', 'gateway', 'payment', 'email', 'backup'] as const
export type SettingsTab = typeof SETTINGS_TABS[number]

interface SettingsSection {
  tab: SettingsTab
  id: string
  titleKey?: string
  title?: readonly [string, string]
  keywordKeys?: string[]
}

// Match the headings and stable anchors in SettingsView. These are settings
// entry points, including switches for features whose business pages are hidden.
export const SETTINGS_SECTIONS: SettingsSection[] = [
  { tab: 'security', id: 'admin-api-key', titleKey: 'admin.settings.adminApiKey.title' },
  { tab: 'gateway', id: 'overload-cooldown', titleKey: 'admin.settings.overloadCooldown.title' },
  { tab: 'gateway', id: 'rate-limit429cooldown', titleKey: 'admin.settings.rateLimit429Cooldown.title' },
  { tab: 'gateway', id: 'openai-response-header-timeout', titleKey: 'admin.settings.openaiResponseHeaderTimeout.title' },
  { tab: 'gateway', id: 'stream-timeout', titleKey: 'admin.settings.streamTimeout.title' },
  { tab: 'gateway', id: 'account-performance', titleKey: 'accountPerformance.settings.title', keywordKeys: ['accountPerformance.settings.windowMinutes', 'accountPerformance.settings.description'] },
  { tab: 'gateway', id: 'rectifier', titleKey: 'admin.settings.rectifier.title' },
  { tab: 'gateway', id: 'beta-policy', titleKey: 'admin.settings.betaPolicy.title' },
  { tab: 'gateway', id: 'openai-fast-policy', titleKey: 'admin.settings.openaiFastPolicy.title' },
  { tab: 'security', id: 'registration', titleKey: 'admin.settings.registration.title' },
  { tab: 'security', id: 'api-key-acl', titleKey: 'admin.settings.apiKeyAcl.title' },
  { tab: 'security', id: 'panel-rate-limit', titleKey: 'admin.settings.panelRateLimit.title' },
  { tab: 'security', id: 'captcha', titleKey: 'admin.settings.captcha.title' },
  { tab: 'security', id: 'linuxdo', titleKey: 'admin.settings.linuxdo.title' },
  { tab: 'security', id: 'email-oauth', title: ['邮箱快捷登录', 'Email OAuth Sign-in'] },
  { tab: 'security', id: 'wechat-connect', titleKey: 'admin.settings.wechatConnect.title' },
  { tab: 'security', id: 'dingtalk', titleKey: 'admin.settings.dingtalk.title' },
  { tab: 'security', id: 'oidc', titleKey: 'admin.settings.oidc.title' },
  { tab: 'users', id: 'defaults', titleKey: 'admin.settings.defaults.title' },
  { tab: 'users', id: 'auth-source-defaults', titleKey: 'admin.settings.authSourceDefaults.title' },
  { tab: 'gateway', id: 'claude-code', titleKey: 'admin.settings.claudeCode.title' },
  { tab: 'gateway', id: 'gateway-forwarding-codex-hardening-title', titleKey: 'admin.settings.gatewayForwarding.codexHardeningTitle' },
  { tab: 'gateway', id: 'upstream-billing-probe', titleKey: 'admin.settings.upstreamBillingProbe.title' },
  { tab: 'gateway', id: 'ollama-cloud-usage', titleKey: 'admin.settings.ollamaCloudUsage.title' },
  { tab: 'gateway', id: 'opencode-go-usage', titleKey: 'admin.settings.opencodeGoUsage.title' },
  { tab: 'gateway', id: 'scheduling', titleKey: 'admin.settings.scheduling.title' },
  { tab: 'gateway', id: 'gateway-forwarding', titleKey: 'admin.settings.gatewayForwarding.title' },
  { tab: 'gateway', id: 'web-search-emulation', titleKey: 'admin.settings.webSearchEmulation.title' },
  { tab: 'gateway', id: 'usage-records', titleKey: 'admin.settings.usageRecords.title', keywordKeys: ['admin.settings.cache_rate.label', 'admin.settings.cache_rate_to_users.label'] },
  { tab: 'general', id: 'site', titleKey: 'admin.settings.site.title' },
  { tab: 'general', id: 'custom-menu', titleKey: 'admin.settings.customMenu.title' },
  { tab: 'agreement', id: 'login-agreement', title: ['登录条款确认', 'Login agreement'] },
  { tab: 'features', id: 'request-capture', titleKey: 'admin.requestCapture.title' },
  { tab: 'features', id: 'smart-ops-sidebar', titleKey: 'admin.settings.features.smartOpsSidebar.title' },
  { tab: 'features', id: 'features-excel-bps-images', titleKey: 'admin.settings.features.excelBpsImages.title' },
  { tab: 'features', id: 'features-channel-monitor', titleKey: 'admin.settings.features.channelMonitor.title' },
  { tab: 'features', id: 'features-available-channels', titleKey: 'admin.settings.features.availableChannels.title' },
  { tab: 'features', id: 'features-pelican-showcase', titleKey: 'admin.settings.features.pelicanShowcase.title' },
  { tab: 'features', id: 'features-model-plaza', titleKey: 'admin.settings.features.modelPlaza.title' },
  { tab: 'features', id: 'features-site-billing-mode', titleKey: 'admin.settings.features.siteBillingMode.title' },
  { tab: 'features', id: 'features-plugin-management', titleKey: 'admin.settings.features.pluginManagement.title' },
  { tab: 'features', id: 'features-support-tickets', titleKey: 'admin.settings.features.supportTickets.title', keywordKeys: ['enabled', 'categories', 'maxOpen', 'notice'].map(key => `admin.settings.features.supportTickets.${key}`) },
  { tab: 'features', id: 'features-risk-control', titleKey: 'admin.settings.features.riskControl.title', keywordKeys: ['enabled', 'cyberSessionBlock', 'cyberSessionBlockTTL', 'cyberSessionIdentityStrict'].map(key => `admin.settings.features.riskControl.${key}`) },
  { tab: 'features', id: 'features-affiliate', titleKey: 'admin.settings.features.affiliate.title' },
  { tab: 'payment', id: 'payment', titleKey: 'admin.settings.payment.title' },
  { tab: 'email', id: 'smtp', titleKey: 'admin.settings.smtp.title' },
  { tab: 'email', id: 'test-email', titleKey: 'admin.settings.testEmail.title' }
]

export function settingsSearchItems(t: (key: string) => string, locale: string): SearchNavItem[] {
  return SETTINGS_TABS.map(tab => ({
    path: `/admin/settings?tab=${tab}`,
    label: t(`admin.settings.tabs.${tab}`),
    children: SETTINGS_SECTIONS.filter(section => section.tab === tab).map(section => ({
      path: `/admin/settings?tab=${tab}#settings-section-${section.id}`,
      label: section.titleKey ? t(section.titleKey) : section.title![locale.startsWith('zh') ? 0 : 1],
      keywords: [section.titleKey, ...(section.title ?? []), ...(section.keywordKeys ?? []).map(key => t(key))].filter(Boolean).join(' ')
    }))
  }))
}

// Only augment a settings entry that the existing navigation permits.
export function withSettingsSearch(items: SearchNavItem[], t: (key: string) => string, locale: string): SearchNavItem[] {
  return items.map(item => ({
    ...item,
    children: item.path === '/admin/settings' && !item.expandOnly
      ? [...(item.children ?? []), ...settingsSearchItems(t, locale)]
      : item.children ? withSettingsSearch(item.children, t, locale) : undefined
  }))
}

export function settingsLocation(tab: unknown, hash: string) {
  const section = SETTINGS_SECTIONS.find(item => hash === `#settings-section-${item.id}`)
  const validTab = SETTINGS_TABS.find(item => item === tab)
  return { tab: section?.tab ?? validTab ?? 'general', anchor: section ? `settings-section-${section.id}` : undefined, linked: Boolean(section || validTab) }
}
