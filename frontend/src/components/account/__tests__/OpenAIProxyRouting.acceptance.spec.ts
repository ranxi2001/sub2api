import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import CreateAccountModal from '../CreateAccountModal.vue'
import EditAccountModal from '../EditAccountModal.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { defaultExcelBPSDefaults } from '@/utils/excelBPSDefaults'
import type { Account, Proxy } from '@/types'

const mocks = vi.hoisted(() => ({
  create: vi.fn(), update: vi.fn(), generateAuthUrl: vi.fn(), exchangeCode: vi.fn(),
  getProxyAccounts: vi.fn(), testProxy: vi.fn(), showError: vi.fn(), getSIWCStatus: vi.fn()
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({
  showError: mocks.showError, showWarning: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn()
}) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({
  user: { id: 41, role: 'admin' }, isAdmin: true, isSimpleMode: true
}) }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  accounts: {
    create: mocks.create, update: mocks.update, generateAuthUrl: mocks.generateAuthUrl, exchangeCode: mocks.exchangeCode,
    getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
    checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
    syncUpstreamModels: vi.fn().mockResolvedValue({ models: [], metadata: {} }),
    probeUpstreamBilling: vi.fn().mockResolvedValue({})
  },
  proxies: { getProxyAccounts: mocks.getProxyAccounts, testProxy: mocks.testProxy },
  scheduledTests: { listByAccount: vi.fn().mockResolvedValue([]) },
  settings: {
    getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
    getSettings: vi.fn().mockResolvedValue({})
  },
  tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
} }))
vi.mock('@/api/admin/autoConfig', () => ({
  getAutoConfig: vi.fn(async () => ({ excel_bps: defaultExcelBPSDefaults() }))
}))
vi.mock('@/api/admin/accounts', () => ({ getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/admin/upstreamChannels', () => ({
  listUpstreamChannels: vi.fn(), readChannelBindings: vi.fn(), bindChannelAccounts: vi.fn()
}))
vi.mock('@/api/admin/credentialEncryption', () => ({
  getCredentialEncryption: vi.fn().mockResolvedValue({ configured: true, source: 'server_config' }),
  initializeCredentialEncryption: vi.fn()
}))
vi.mock('@/api/admin/siwc', async () => ({
  ...await vi.importActual<typeof import('@/api/admin/siwc')>('@/api/admin/siwc'),
  getSIWCStatus: mocks.getSIWCStatus
}))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

enableAutoUnmount(afterEach)
const BaseDialogStub = defineComponent({
  props: { show: Boolean }, template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})
// The approved seam is the real account modal through its administrator API
// payload. This exposes the authorization child interface without OAuth traffic.
const AuthorizationFlow = defineComponent({
  name: 'OAuthAuthorizationFlow',
  data: () => ({ inputMethod: 'manual', authCode: '', oauthState: '' }),
  methods: { reset() { this.authCode = ''; this.oauthState = '' } },
  emits: ['generate-url'],
  template: `<button type="button" data-testid="authorize" @click="$emit('generate-url')">authorize</button>`
})
const catalog = [
  { id: 11, name: 'Proxy Alpha', host: 'alpha.example.test' },
  { id: 22, name: 'Proxy Beta', host: 'beta.example.test' },
  { id: 45, name: 'Previous single proxy', host: 'single.example.test' }
].map(proxy => ({ ...proxy, protocol: 'http', port: 8080, status: 'active', expires_at: null })) as Proxy[]

function account(bps = false, extra: Record<string, unknown> = {}): Account {
  return {
    id: 7, name: 'OAuth proxy acceptance', notes: '', platform: 'openai', type: 'oauth',
    parent_account_id: null, credentials: { access_token: 'fixture-access', refresh_token: 'fixture-refresh' },
    extra: { ...(bps ? { openai_excel_bps: true } : {}), ...extra },
    proxy_id: 45, concurrency: 6, priority: 1, rate_multiplier: 1, status: 'active',
    group_ids: [], expires_at: null, auto_pause_on_expired: false
  } as Account
}
function options(pinia = createPinia()) {
  return {
    plugins: [pinia],
    stubs: {
      BaseDialog: BaseDialogStub, teleport: true, OAuthAuthorizationFlow: AuthorizationFlow,
      Select: true, Icon: true, PlatformIcon: true, ProxyAdBanner: true, ConfirmDialog: true,
      GroupSelector: true, ModelWhitelistSelector: true, QuotaLimitCard: true
    }
  }
}
async function createModal(bps = false, pinia = createPinia()) {
  const wrapper = mount(CreateAccountModal, { props: { show: true, proxies: catalog, groups: [] }, global: options(pinia) })
  const openai = wrapper.findAll('button').find(button => button.text().trim() === 'OpenAI')!
  await openai.trigger('click')
  if (bps) await wrapper.get('[data-testid="openai-bps-oauth"]').trigger('click')
  await flushPromises()
  return wrapper
}
async function editModal(value: Account, pinia = createPinia()) {
  const wrapper = mount(EditAccountModal, { props: { show: true, account: value, proxies: catalog, groups: [] }, global: options(pinia) })
  await flushPromises()
  return wrapper
}
async function requestMode(wrapper: VueWrapper) {
  await wrapper.get('[data-testid="openai-proxy-mode-request"]').setValue()
  await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
}
async function saveEdit(wrapper: VueWrapper) {
  await wrapper.get('form#edit-account-form').trigger('submit.prevent')
  await flushPromises()
  expect(mocks.update).toHaveBeenCalledWith(7, expect.any(Object))
  return mocks.update.mock.lastCall![1]
}

beforeEach(() => {
  localStorage.clear()
  setActivePinia(createPinia())
  mocks.create.mockReset().mockResolvedValue(account())
  mocks.update.mockReset().mockResolvedValue(account())
  mocks.generateAuthUrl.mockReset().mockResolvedValue({ auth_url: 'https://auth.example.test/?state=fixture', session_id: 'fixture-session' })
  mocks.exchangeCode.mockReset().mockResolvedValue({ access_token: 'fixture-access', refresh_token: 'fixture-refresh', expires_at: 1900000000 })
  mocks.getProxyAccounts.mockReset().mockResolvedValue([])
  mocks.testProxy.mockReset().mockResolvedValue({ success: true, message: 'Connected', latency_ms: 12 })
  mocks.showError.mockReset()
  mocks.getSIWCStatus.mockReset().mockResolvedValue({ bound: true, enabled: false })
})

describe('unified OpenAI proxy routing acceptance', () => {
  it('saves SIWC protocol and the account proxy group together without changing concurrency or the single proxy', async () => {
    const original = account(false, { fixture: 'retained' })
    const wrapper = await editModal(original)
    expect(wrapper.findAll('[data-testid="openai-proxy-settings"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="siwc-protocol"] option[value="siwc"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="siwc-protocol"]').setValue('siwc')
    await requestMode(wrapper)
    await wrapper.get('[data-testid="bps-proxy-11"]').setValue(true)
    await wrapper.get('[data-testid="bps-proxy-22"]').setValue(true)
    const saved = await saveEdit(wrapper)
    expect(saved).toMatchObject({ proxy_id: 45, concurrency: 6, extra: {
      fixture: 'retained', openai_siwc: true, openai_proxy_mode: 'request', openai_proxy_ids: [11, 22]
    } })
    wrapper.unmount()

    const reopened = await editModal({ ...original, ...saved })
    expect(reopened.get<HTMLSelectElement>('[data-testid="siwc-protocol"]').element.value).toBe('siwc')
    expect(reopened.get<HTMLInputElement>('[data-testid="openai-proxy-mode-request"]').element.checked).toBe(true)
    await reopened.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    expect(reopened.get<HTMLInputElement>('[data-testid="bps-proxy-11"]').element.checked).toBe(true)
    expect(reopened.get<HTMLInputElement>('[data-testid="bps-proxy-22"]').element.checked).toBe(true)
    await reopened.get('[data-testid="openai-proxy-mode-single"]').setValue()
    const single = await saveEdit(reopened)
    expect(single).toMatchObject({ proxy_id: 45, concurrency: 6, extra: {
      openai_siwc: true, openai_proxy_mode: 'single', openai_proxy_ids: [11, 22]
    } })
  })

  it.each([false, true])('offers one proxy entry in Create and Edit for BPS=%s', async bps => {
    const wrappers = [await createModal(bps), await editModal(account(bps))]
    for (const wrapper of wrappers) {
      expect(wrapper.findAll('[data-testid="openai-proxy-settings"]')).toHaveLength(1)
      expect(wrapper.findAll('[data-testid="openai-proxy-mode-single"]')).toHaveLength(1)
      expect(wrapper.find('[data-testid="bps-proxy-mode-request"]').exists()).toBe(false)
      await requestMode(wrapper)
      expect(wrapper.findAll('[data-testid="bps-proxy-trigger"]')).toHaveLength(1)
      expect(wrapper.findAll('[data-testid="bps-proxy-11"]')).toHaveLength(1)
      expect(wrapper.find('[data-testid="bps-oauth-models"] [data-testid="bps-proxy-routing"]').exists()).toBe(false)
    }
  })

  it.each([false, true])('saves and reopens the request group while retaining single proxy and concurrency for BPS=%s', async bps => {
    const original = account(bps, {
      fixture: 'retained', openai_proxy_mode: 'single', openai_proxy_ids: [22],
      openai_excel_bps_proxy_mode: 'request', openai_excel_bps_proxy_ids: [11]
    })
    const wrapper = await editModal(original)
    expect(wrapper.get<HTMLInputElement>('[data-testid="openai-proxy-mode-single"]').element.checked).toBe(true)
    const selector = wrapper.get('[data-testid="openai-proxy-settings"]')
    expect(selector.text()).toContain('Previous single proxy')
    expect(wrapper.find('[data-testid="bps-proxy-trigger"]').exists()).toBe(false)

    await requestMode(wrapper)
    expect(wrapper.get<HTMLInputElement>('[data-testid="bps-proxy-22"]').element.checked).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-testid="bps-proxy-11"]').element.checked).toBe(false)
    await wrapper.get('[data-testid="bps-proxy-11"]').setValue(true)
    await wrapper.get('[data-testid="openai-proxy-mode-single"]').setValue()
    expect(selector.text()).toContain('Previous single proxy')
    await requestMode(wrapper)
    expect(wrapper.get<HTMLInputElement>('[data-testid="bps-proxy-22"]').element.checked).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-testid="bps-proxy-11"]').element.checked).toBe(true)
    const saved = await saveEdit(wrapper)
    expect(saved).toMatchObject({ proxy_id: 45, concurrency: 6, extra: {
      fixture: 'retained', openai_proxy_mode: 'request', openai_proxy_ids: [22, 11]
    } })
    wrapper.unmount()

    const reopened = await editModal({ ...original, ...saved })
    expect(reopened.get<HTMLInputElement>('[data-testid="openai-proxy-mode-request"]').element.checked).toBe(true)
    await reopened.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    expect(reopened.get<HTMLInputElement>('[data-testid="bps-proxy-22"]').element.checked).toBe(true)
    expect(reopened.get<HTMLInputElement>('[data-testid="bps-proxy-11"]').element.checked).toBe(true)
    await reopened.get('[data-testid="openai-proxy-mode-single"]').setValue()
    const single = await saveEdit(reopened)
    expect(single).toMatchObject({ proxy_id: 45, concurrency: 6, extra: {
      fixture: 'retained', openai_proxy_mode: 'single', openai_proxy_ids: [22, 11]
    } })
    reopened.unmount()

    const reopenedSingle = await editModal({ ...original, ...single })
    expect(reopenedSingle.get<HTMLInputElement>('[data-testid="openai-proxy-mode-single"]').element.checked).toBe(true)
    expect(reopenedSingle.get('[data-testid="openai-proxy-settings"]').text()).toContain('Previous single proxy')
    expect(reopenedSingle.find('[data-testid="bps-proxy-trigger"]').exists()).toBe(false)
  })

  it.each([false, true])('authorizes through the first usable selected proxy and preserves the original single proxy for BPS=%s', async bps => {
    mocks.generateAuthUrl.mockResolvedValueOnce({
      auth_url: 'https://auth.example.test/?state=fixture', session_id: 'fixture-session', proxy_mode: 'request', proxy_id: 22,
    })
    if (bps) mocks.exchangeCode.mockResolvedValueOnce({
      access_token: 'fixture-access', refresh_token: 'fixture-refresh',
      client_id: 'app_fnr0pYvVwwFDocDumLG3H2Bp', expires_at: 1900000000
    })
    const wrapper = await createModal(bps)
    const single = wrapper.get('[data-testid="openai-proxy-settings"]')
    await single.get('.select-trigger').trigger('click')
    const option = single.findAll('.select-option').find(candidate => candidate.text().includes('Previous single proxy'))!
    await option.trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Proxy group account')
    await requestMode(wrapper)
    await wrapper.get('[data-testid="bps-proxy-11"]').setValue(true)
    await wrapper.get('[data-testid="bps-proxy-22"]').setValue(true)
    // The first selection becomes expired before authorization; never use the
    // unrelated single-proxy fallback or silently leave the explicit group.
    await wrapper.setProps({ proxies: catalog.map(proxy => proxy.id === 11
      ? { ...proxy, expires_at: '2000-01-01T00:00:00Z' } : proxy) })
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    const flow = wrapper.getComponent(AuthorizationFlow)
    await wrapper.get('[data-testid="authorize"]').trigger('click')
    await flushPromises()
    expect(mocks.generateAuthUrl).toHaveBeenCalledWith('/admin/openai/generate-auth-url',
      bps ? { oauth_client: 'excel', proxy_mode: 'request', proxy_ids: [11, 22] } : { proxy_mode: 'request', proxy_ids: [11, 22] })
    await flow.setData({ authCode: 'fixture-code', oauthState: 'fixture-state' })
    const complete = wrapper.findAll('button').find(button => button.text().includes('admin.accounts.oauth.completeAuth'))!
    await complete.trigger('click')
    await flushPromises()
    expect(mocks.exchangeCode).toHaveBeenCalledWith('/admin/openai/exchange-code', expect.objectContaining({ proxy_mode: 'request', proxy_ids: [11, 22] }))
    expect(mocks.exchangeCode.mock.lastCall?.[1]).not.toHaveProperty('proxy_id')
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ proxy_id: 45, extra: expect.objectContaining({
      openai_proxy_mode: 'request', openai_proxy_ids: [11, 22]
    }) }))
  })

  it.each([false, true])('blocks authorization with an empty explicit request group for BPS=%s', async bps => {
    const wrapper = await createModal(bps)
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Empty proxy group')
    await wrapper.get('[data-testid="openai-proxy-mode-request"]').setValue()
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(wrapper.findComponent(AuthorizationFlow).exists()).toBe(false)
    expect(mocks.showError).toHaveBeenCalledWith('admin.accounts.openai.proxyRequired')
    expect(mocks.generateAuthUrl).not.toHaveBeenCalled()
    expect(mocks.create).not.toHaveBeenCalled()
  })

  it('keeps account search, filtered bulk selection, and testing inside the original proxy entry', async () => {
    mocks.getProxyAccounts.mockImplementation(async (id: number) => id === 11 ? [{ id: 101, name: 'Associated team' }] : [])
    const wrapper = await editModal(account())
    await requestMode(wrapper)
    await flushPromises()
    expect(mocks.getProxyAccounts).toHaveBeenCalledWith(11)
    expect(wrapper.get('[data-testid="bps-proxy-row-11"]').text()).toContain('Associated team #101')
    const search = wrapper.get('input[type="search"]')
    await search.setValue('Associated team')
    expect(wrapper.find('[data-testid="bps-proxy-row-22"]').exists()).toBe(false)
    await wrapper.get('[data-testid="bps-proxy-select-results"]').trigger('click')
    await wrapper.get('[data-testid="bps-proxy-batch-test"]').trigger('click')
    await flushPromises()
    expect(mocks.testProxy).toHaveBeenCalledTimes(1)
    expect(mocks.testProxy).toHaveBeenCalledWith(11)
    expect(wrapper.get('[data-testid="bps-proxy-row-11"]').text()).toContain('12ms')
    await search.setValue('Proxy Beta')
    await wrapper.get('[data-testid="bps-proxy-select-results"]').trigger('click')
    await wrapper.get('[data-testid="bps-proxy-clear-results"]').trigger('click')
    await search.setValue('')
    expect(wrapper.get<HTMLInputElement>('[data-testid="bps-proxy-11"]').element.checked).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-testid="bps-proxy-22"]').element.checked).toBe(false)
    const saved = await saveEdit(wrapper)
    expect(saved.extra).toMatchObject({ openai_proxy_mode: 'request', openai_proxy_ids: [11] })
  })

  it('shows the new announcement after an old dismissal and synchronizes Create/Edit dismissal across reloads', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const notices = useNewFeatureNoticeStore()
    notices.dismiss(41, NEW_FEATURE_NOTICES.bpsAccountMultiProxy)
    const created = await createModal(false, pinia)
    const edited = await editModal(account(true), pinia)
    const noticeId = NEW_FEATURE_NOTICES.openaiAccountMultiProxy
    expect(noticeId).toBe('2026-11-16-openai-account-multi-proxy')
    function notice(wrapper: VueWrapper) {
      return wrapper.findAllComponents(NewFeatureBadge).find(badge => badge.props('noticeId') === noticeId)!
    }
    expect(notice(created).get('button').exists()).toBe(true)
    expect(notice(edited).get('button').exists()).toBe(true)
    await notice(created).get('button').trigger('click')
    expect(notice(created).find('button').exists()).toBe(false)
    expect(notice(edited).find('button').exists()).toBe(false)
    created.unmount()
    edited.unmount()

    const reloadedPinia = createPinia()
    setActivePinia(reloadedPinia)
    const reopenedCreate = await createModal(false, reloadedPinia)
    const reopenedEdit = await editModal(account(true), reloadedPinia)
    expect(notice(reopenedCreate).find('button').exists()).toBe(false)
    expect(notice(reopenedEdit).find('button').exists()).toBe(false)
    expect(useNewFeatureNoticeStore().isDismissed(41, NEW_FEATURE_NOTICES.bpsAccountMultiProxy)).toBe(true)
    const futureNotice = mount(NewFeatureBadge, {
      props: { noticeId: 'fixture-future-independent-feature', userId: 41 },
      global: { plugins: [reloadedPinia] }
    })
    expect(futureNotice.find('button').exists()).toBe(true)
  })
})
