import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { readFileSync } from 'node:fs'
import IPRiskSettings from '@/features/ip-risk/IPRiskSettings.vue'
import type { IPRiskSnapshot } from '@/features/ip-risk/api'
import { flushPromises, mount } from '@vue/test-utils'
import { RouterView } from 'vue-router'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import { i18n } from '@/i18n'

const state = vi.hoisted(() => ({
  auth: { checkAuth: vi.fn(), isAuthenticated: true, isAdmin: true, isObserver: false, canManageAccounts: true, isSimpleMode: false, hasPendingAuthSession: false },
  app: { siteName: 'Test', publicSettingsLoaded: true, cachedPublicSettings: { risk_control_enabled: true }, backendModeEnabled: false, showError: vi.fn(), showSuccess: vi.fn() },
  prompt: { getConfig: vi.fn(), getRuntime: vi.fn(), listGroups: vi.fn(), listEvents: vi.fn() },
  listIPAudit: vi.fn(),
  risk: { get: vi.fn(), save: vi.fn(), events: vi.fn(), bans: vi.fn(), anonymousBans: vi.fn(), unban: vi.fn(), unbanAnonymous: vi.fn(), clearEvents: vi.fn(), check: vi.fn() },
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('@/stores/adminCompliance', () => ({ useAdminComplianceStore: () => ({ initialized: true }) }))
vi.mock('@/composables/useNavigationLoading', () => ({ useNavigationLoadingState: () => ({ startNavigation: vi.fn(), endNavigation: vi.fn() }) }))
vi.mock('@/composables/useRoutePrefetch', () => ({ useRoutePrefetch: () => ({ triggerPrefetch: vi.fn() }) }))
vi.mock('@/features/ip-risk/api', () => ({ ipRiskAPI: state.risk }))
vi.mock('@/features/prompt-audit/api', () => ({ default: state.prompt }))
vi.mock('@/features/ip-audit/api', async () => ({ ...await vi.importActual('@/features/ip-audit/api'), listIPAudit: state.listIPAudit }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: defineComponent({ template: '<main><slot /></main>' }) }))
// Keep unrelated landing pages out of this focused route integration test.
vi.mock('@/views/user/DashboardView.vue', () => ({ default: defineComponent({ template: '<div>Dashboard</div>' }) }))
vi.mock('@/views/admin/SettingsView.vue', () => ({ default: defineComponent({ template: '<div>Settings</div>' }) }))
vi.mock('@/views/auth/LoginView.vue', () => ({ default: defineComponent({ template: '<div>Login</div>' }) }))
import router from '@/router'

function riskSnapshot(): IPRiskSnapshot {
  return {
    current_ip: '192.0.2.10', trusted_proxies_configured: true,
    health: { redis_available: true, emergency_disabled: false, config_error: '' },
    config: {
      enabled: true, observe_only: false, whitelist: ['192.0.2.10'],
      blacklist: [{ network: '198.51.100.0/24', note: 'fixture', enabled: true, expires_at: 0 }],
      allowed_countries: ['CN'], blocked_countries: ['US'], block_unknown_country: true,
      deny_user_agents: ['fixture-agent'], block_empty_ua: true, ipv6_prefix: 96, ban_seconds: 7200, event_retention_hours: 48,
      anonymous_api: { enabled: true, threshold: 43, window_seconds: 120, ban_seconds: 600, max_tracked_ips: 500, reset_on_success: true },
      login_attempts: { limit: 14, window_seconds: 90 }, login_failures: { limit: 6, window_seconds: 600 },
      register_attempts: { limit: 9, window_seconds: 300 }, register_failures: { limit: 4, window_seconds: 300 },
      verification_attempts: { limit: 8, window_seconds: 450 },
    },
  }
}
let wrapper: ReturnType<typeof mount> | undefined
async function open(url: string, locale: 'en' | 'zh' = 'en') {
  wrapper?.unmount()
  wrapper = undefined
  i18n.global.setLocaleMessage(locale, locale === 'en' ? en : zh)
  i18n.global.locale.value = locale
  await router.push(url)
  await router.isReady()
  wrapper = mount(RouterView, { global: { plugins: [createPinia(), router, createI18n({ legacy: false, locale, messages: { en, zh } })], stubs: { RuntimeOverview: true, EndpointPool: true, PolicyPanel: true, PenaltyPolicyPanel: true, EventWorkspace: true, EventDetailDialog: true, FilterDeleteDialog: true, ConfirmDialog: true } } })
  await flushPromises()
  return wrapper
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('scrollTo', vi.fn())
  Object.assign(state.auth, { isAuthenticated: true, isAdmin: true, isObserver: false })
  state.app.cachedPublicSettings.risk_control_enabled = true
  state.prompt.getConfig.mockRejectedValue(new Error('prompt offline'))
  state.prompt.getRuntime.mockResolvedValue(null)
  state.prompt.listGroups.mockResolvedValue([])
  state.prompt.listEvents.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
  state.risk.get.mockResolvedValue(riskSnapshot())
  state.risk.save.mockImplementation(async config => structuredClone(config))
  state.risk.events.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
  state.risk.bans.mockResolvedValue([])
  state.risk.anonymousBans.mockResolvedValue([])
  state.listIPAudit.mockResolvedValue({ items: [], sources: [], has_more: false, next_cursor: 0, unrestricted: false })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals() })

describe('security audit real routes', () => {
  it('redirects the old IP tab before mounting prompt code and keeps other query/hash values', async () => {
    const page = await open('/admin/prompt-audit?tab=ip&source=login_failures#retained')
    expect(router.currentRoute.value.fullPath).toBe('/admin/ip-audit?source=login_failures#retained')
    expect(page.find('[data-testid="ip-audit-panel"]').exists()).toBe(true)
    expect(state.prompt.getConfig).not.toHaveBeenCalled()
    expect(page.text()).not.toContain(en.admin.promptAudit.description)
  })
  it.each(['en', 'zh'] as const)('renders separate full-width %s pages and active navigation', async locale => {
    const messages = locale === 'en' ? en : zh
    const page = await open('/admin/ip-audit', locale)
    expect(page.get('h1').text()).toBe(messages.ipAudit.title)
    expect(document.title).toBe(`${messages.ipAudit.title} - Test`)
    expect(page.get('a[href="/admin/ip-audit"]').attributes('aria-current')).toBe('page')
    expect(page.html()).not.toContain('max-w-[1600px]')
    const navigated = new Promise<void>(resolve => {
      const remove = router.afterEach(() => { remove(); resolve() })
    })
    await page.get('a[href="/admin/prompt-audit"]').trigger('click')
    await navigated
    await flushPromises()
    await vi.waitFor(() => expect(page.get('h1').text()).toBe(messages.admin.promptAudit.title))
    expect(page.find('[data-testid="ip-audit-panel"]').exists()).toBe(false)
    expect(page.find('[data-test="tab-ip"]').exists()).toBe(false)
    expect(page.get('a[href="/admin/prompt-audit"]').attributes('aria-current')).toBe('page')
    expect(page.html()).not.toContain('max-w-[1600px]')
  })
  it('restores configuration/events deep links and browser back/forward without rewriting history', async () => {
    const page = await open('/admin/prompt-audit?tab=config&keep=1')
    expect(page.get('[data-test="tab-config"]').attributes('aria-selected')).toBe('true')
    await page.get('[data-test="tab-events"]').trigger('click'); await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ tab: 'events', keep: '1' })
    router.back()
    await vi.waitFor(() => expect(router.currentRoute.value.query.tab).toBe('config'))
    expect(page.get('[data-test="tab-config"]').attributes('aria-selected')).toBe('true')
    router.forward()
    await vi.waitFor(() => expect(router.currentRoute.value.query.tab).toBe('events'))
    await router.push('/admin/prompt-audit?tab=ip')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/admin/ip-audit')
    expect(page.find('[data-test="tab-panel-config"]').exists()).toBe(false)
  })
  it.each(['observer', 'user'] as const)('keeps %s roles out of direct and legacy pages without sensitive requests', async role => {
    Object.assign(state.auth, { isAdmin: false, isObserver: role === 'observer' })
    for (const url of ['/admin/prompt-audit?tab=config', '/admin/ip-audit', '/admin/prompt-audit?tab=ip', '/admin/ip-audit?tab=config', '/admin/settings?tab=security#ip-risk-settings']) {
      await open(url)
      expect(router.currentRoute.value.path).toBe('/dashboard')
      expect(state.prompt.getConfig).not.toHaveBeenCalled()
      expect(state.listIPAudit).not.toHaveBeenCalled()
      expect(state.risk.get).not.toHaveBeenCalled()
      expect(state.risk.events).not.toHaveBeenCalled()
      expect(state.risk.save).not.toHaveBeenCalled()
      wrapper?.unmount()
      wrapper = undefined
    }
  })
  it('keeps anonymous deep links behind login with a canonical return URL', async () => {
    state.auth.isAuthenticated = false; state.auth.isAdmin = false
    await open('/admin/prompt-audit?tab=ip')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe('/admin/ip-audit')
    expect(state.prompt.getConfig).not.toHaveBeenCalled()
    expect(state.listIPAudit).not.toHaveBeenCalled()
  })
  it('keeps prompt feature gating without preventing IP-only audit access', async () => {
    state.app.cachedPublicSettings.risk_control_enabled = false
    const page = await open('/admin/ip-audit')
    expect(router.currentRoute.value.path).toBe('/admin/ip-audit')
    expect(page.find('a[href="/admin/prompt-audit"]').exists()).toBe(false)
    await router.push('/admin/prompt-audit?tab=config')
    expect(router.currentRoute.value.path).toBe('/admin/settings')
    expect(state.prompt.getConfig).not.toHaveBeenCalled()
  })
})


describe('IP settings migration through real routes', () => {
  it('closes the blacklist dialog and releases scroll lock when leaving the cached configuration tab', async () => {
    const page = await open('/admin/ip-audit?tab=config')
    await page.get('[data-testid="risk-manage-blacklist"]').trigger('click'); await flushPromises()
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    await router.push('/admin/ip-audit?tab=sources'); await flushPromises()
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull())
    expect(document.body.classList.contains('modal-open')).toBe(false)
    await router.push('/admin/ip-audit?tab=config'); await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(state.risk.save).not.toHaveBeenCalled()
  })

  it.each([
    '/admin/settings?tab=security#ip-risk-settings',
    '/admin/settings#ip-risk-settings',
    '/admin/settings?tab=general&keep=1#ip-risk-settings',
  ])('redirects %s to the actual configuration component without saving', async url => {
    const page = await open(url)
    expect(router.currentRoute.value.path).toBe('/admin/ip-audit')
    expect(router.currentRoute.value.query.tab).toBe('config')
    expect(router.currentRoute.value.hash).toBe('#ip-risk-settings')
    if (url.includes('keep=1')) expect(router.currentRoute.value.query.keep).toBe('1')
    expect(page.findAll('[data-testid="ip-risk-settings"]')).toHaveLength(1)
    expect(page.find('[data-testid="ip-audit-panel"]').exists()).toBe(false)
    expect(state.risk.get).toHaveBeenCalledOnce()
    expect(state.risk.events).toHaveBeenCalledOnce()
    expect(state.risk.save).not.toHaveBeenCalled()
    expect(page.findAll('form')).toHaveLength(1)
  })
  it.each(['en', 'zh'] as const)('loads existing values on the %s mobile configuration deep link and preserves the draft across tabs', async locale => {
    vi.stubGlobal('innerWidth', 375)
    const page = await open('/admin/ip-audit?tab=config', locale)
    const settings = page.getComponent(IPRiskSettings)
    expect(page.get('[role="tablist"]').classes()).toContain('flex-wrap')
    expect(settings.find('.overflow-x-auto').exists()).toBe(true)
    expect(page.get('[data-test="ip-tab-config"]').text()).toBe(locale === 'zh' ? '风控配置与访问记录' : 'Risk settings and access records')
    expect((settings.get('[data-testid="anonymous-threshold"]').element as HTMLInputElement).value).toBe('43')
    expect((settings.get('[data-testid="risk-enabled"]').element as HTMLInputElement).checked).toBe(true)
    expect((settings.get('[data-testid="risk-observe"]').element as HTMLInputElement).checked).toBe(false)
    expect(state.risk.save).not.toHaveBeenCalled()
    await settings.get('[data-testid="risk-whitelist"]').setValue('192.0.2.10\n192.0.2.11')
    await page.get('[data-test="ip-tab-sources"]').trigger('click'); await flushPromises()
    expect(router.currentRoute.value.query.tab).toBe('sources')
    expect(page.find('[data-testid="ip-risk-settings"]').exists()).toBe(false)
    await page.get('[data-test="ip-tab-config"]').trigger('click'); await flushPromises()
    expect(state.risk.get).toHaveBeenCalledOnce()
    expect((settings.get('[data-testid="risk-whitelist"]').element as HTMLTextAreaElement).value).toContain('192.0.2.11')
    expect(state.risk.save).not.toHaveBeenCalled()
    await settings.get('form').trigger('submit'); await flushPromises()
    expect(state.risk.save).toHaveBeenCalledOnce()
    expect(state.risk.save).toHaveBeenCalledWith({ ...riskSnapshot().config, whitelist: ['192.0.2.10', '192.0.2.11'] })
    expect(page.find('[data-test="tab-panel-config"]').exists()).toBe(false)
    expect(state.prompt.getConfig).not.toHaveBeenCalled()
  })
  it('opens settings from source management and restores both tabs through history', async () => {
    const page = await open('/admin/ip-audit?tab=sources')
    expect(state.risk.get).not.toHaveBeenCalled()
    await page.get('[data-testid="ip-open-settings"]').trigger('click'); await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/admin/ip-audit?tab=config#ip-risk-settings')
    expect(page.findAll('[data-testid="ip-risk-settings"]')).toHaveLength(1)
    expect(state.risk.get).toHaveBeenCalledOnce()
    router.back()
    await vi.waitFor(() => expect(router.currentRoute.value.query.tab).toBe('sources'))
    expect(page.find('[data-testid="ip-audit-panel"]').exists()).toBe(true)
    router.forward()
    await vi.waitFor(() => expect(router.currentRoute.value.query.tab).toBe('config'))
    expect(page.find('[data-testid="ip-risk-settings"]').exists()).toBe(true)
    expect(state.risk.get).toHaveBeenCalledOnce()
    expect(state.risk.save).not.toHaveBeenCalled()
  })
  it('retains validation constraints and server rejection without clearing the migrated draft', async () => {
    const page = await open('/admin/ip-audit?tab=config')
    const settings = page.getComponent(IPRiskSettings)
    await settings.get('[data-testid="anonymous-threshold"]').setValue('1')
    expect((settings.get('form').element as HTMLFormElement).checkValidity()).toBe(false)
    expect(state.risk.save).not.toHaveBeenCalled()
    await settings.get('[data-testid="anonymous-threshold"]').setValue('43')
    state.risk.save.mockRejectedValueOnce(new Error('fixture self-lockout validation'))
    await settings.get('form').trigger('submit'); await flushPromises()
    expect(settings.get('[role="alert"]').text()).toBe('fixture self-lockout validation')
    expect((settings.get('[data-testid="risk-whitelist"]').element as HTMLTextAreaElement).value).toBe('192.0.2.10')
  })
  it('keeps unrelated settings routes and removes the duplicate settings form/import', async () => {
    const page = await open('/admin/settings?tab=security#settings-section-registration')
    expect(router.currentRoute.value.path).toBe('/admin/settings')
    expect(page.text()).toBe('Settings')
    expect(state.risk.get).not.toHaveBeenCalled()
    const source = readFileSync('src/views/admin/SettingsView.vue', 'utf8')
    expect(source.includes('<IPRiskSettings')).toBe(false)
    expect(source.includes('import IPRiskSettings')).toBe(false)
  })
  it('normalizes anonymous legacy links to a guarded configuration return URL', async () => {
    state.auth.isAuthenticated = false; state.auth.isAdmin = false
    await open('/admin/settings?tab=security#ip-risk-settings')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe('/admin/ip-audit?tab=config#ip-risk-settings')
    expect(state.risk.get).not.toHaveBeenCalled()
    expect(state.risk.save).not.toHaveBeenCalled()
  })
})
