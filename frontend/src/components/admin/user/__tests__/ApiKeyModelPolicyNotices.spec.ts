import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import KeysView from '@/views/user/KeysView.vue'
import UserApiKeysModal from '../UserApiKeysModal.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import type { AdminUser, ApiKey, User } from '@/types'

const { login, logout, currentUser, listKeys } = vi.hoisted(() => ({
  login: vi.fn(), logout: vi.fn(), currentUser: vi.fn(), listKeys: vi.fn(),
}))
vi.mock('@/api', () => ({
  authAPI: { login, logout, getCurrentUser: currentUser, getPublicSettings: vi.fn().mockResolvedValue({}) },
  isTotp2FARequired: () => false,
  passkeyAPI: {},
  keysAPI: { list: listKeys, getConcurrency: vi.fn().mockResolvedValue({ items: [] }) },
  usageAPI: { getDashboardApiKeysUsage: vi.fn().mockResolvedValue({ stats: {} }) },
  userGroupsAPI: { getAvailable: vi.fn().mockResolvedValue([]), getUserGroupRates: vi.fn().mockResolvedValue({}) },
}))
vi.mock('@/api/admin', () => ({ adminAPI: {
  users: { getUserApiKeys: listKeys }, groups: { getAll: vi.fn().mockResolvedValue([]) },
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ isCurrentStep: () => false }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

const admin = { id: 41, role: 'admin', email: 'admin@example.test' } as User
const customer = { id: 99, email: 'customer@example.test', username: 'customer' } as AdminUser
const key = {
  id: 17, user_id: 41, name: 'Fixture key', key: 'sk-notice-fixture-only', status: 'active',
  group_id: null, group_ids: [], created_at: '2026-11-04', quota: 0, quota_used: 0,
  rate_limit_5h: 0, rate_limit_1d: 0, rate_limit_7d: 0,
} as ApiKey
const storageKey = 'sub2api:new-feature-notices:v1:41'
const noticeA = NEW_FEATURE_NOTICES.protocolFeatureSwitches
// A future announcement fixture; it does not register a production feature.
const noticeC = 'notice-regression-future-feature-c'
const badge = '[data-testid="new-feature-badge"]'

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="table" /></div>' },
  DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>' },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  ApiKeyModelPolicyDialog: true, ConfirmDialog: true, EmptyState: true, Select: true,
  Pagination: true, Icon: true, UseKeyModal: true, BulkEditKeysModal: true,
  EndpointPopover: true, GroupBadge: true, GroupOptionItem: true,
}

async function mountEntries(pinia: Pinia, realPolicyDialog = false) {
  const entryStubs = { ...stubs, ApiKeyModelPolicyDialog: !realPolicyDialog }
  const view = mount(KeysView, { global: { plugins: [pinia], stubs: entryStubs } })
  const modal = mount(UserApiKeysModal, {
    props: { show: false, user: customer }, global: { plugins: [pinia], stubs: entryStubs },
  })
  await modal.setProps({ show: true })
  await flushPromises()
  const edit = view.findAll('button').find(button => button.text() === 'common.edit')
  expect(edit, 'the real KeysView edit action must be present').toBeDefined()
  await edit!.trigger('click')
  return { view, modal }
}

function noticeAt(wrapper: VueWrapper, noticeId: string) {
  const notice = wrapper.findAllComponents(NewFeatureBadge).find(component => component.props('noticeId') === noticeId)
  expect(notice, `notice ${noticeId} must be connected to this entry`).toBeDefined()
  return notice!
}

function modelNotice(wrapper: VueWrapper) {
  return noticeAt(wrapper, NEW_FEATURE_NOTICES.apiKeyModelPolicy)
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  localStorage.clear()
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  listKeys.mockResolvedValue({ items: [key], total: 1, page: 1, page_size: 20, pages: 1 })
  login.mockResolvedValue({ access_token: 'fixture-token', token_type: 'Bearer', user: admin })
  logout.mockResolvedValue(undefined)
  currentUser.mockResolvedValue({ data: admin })
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('API key model policy announcement at both real administrator entries', () => {
  it('registers a stable unique ID separate from the old feature', () => {
    expect(NEW_FEATURE_NOTICES.apiKeyModelPolicy).toBe('api-key-model-policy-initial-announcement')
    const ids = Object.values(NEW_FEATURE_NOTICES)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it.each(['view', 'modal'] as const)('dismisses both entries from %s and preserves A/B across refresh, re-login and C', async entry => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const auth = useAuthStore(pinia)
    await auth.login({ email: admin.email, password: 'fixture-password' })
    useNewFeatureNoticeStore(pinia).dismiss(admin.id, noticeA)
    const oldBadge = mount(NewFeatureBadge, {
      props: { noticeId: noticeA, userId: admin.id }, global: { plugins: [pinia] },
    })
    expect(oldBadge.find(badge).exists()).toBe(false)

    const entries = await mountEntries(pinia)
    for (const wrapper of Object.values(entries)) {
      expect(modelNotice(wrapper).find(badge).exists()).toBe(true)
      expect(modelNotice(wrapper).props()).toMatchObject({
        noticeId: NEW_FEATURE_NOTICES.apiKeyModelPolicy, userId: admin.id,
      })
    }
    await modelNotice(entries[entry]).get(badge).trigger('click')
    for (const wrapper of Object.values(entries)) {
      expect(modelNotice(wrapper).find(badge).exists()).toBe(false)
      expect(wrapper.getComponent({ name: 'ApiKeyModelPolicyDialog' }).props('show')).toBe(false)
    }
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual([noticeA, NEW_FEATURE_NOTICES.apiKeyModelPolicy])
    expect(oldBadge.find(badge).exists()).toBe(false)
    entries.view.unmount()
    entries.modal.unmount()
    oldBadge.unmount()

    // Recreate stores and components against the same browser storage, as on refresh.
    vi.clearAllTimers()
    const refreshedPinia = createPinia()
    setActivePinia(refreshedPinia)
    const refreshedAuth = useAuthStore(refreshedPinia)
    refreshedAuth.checkAuth()
    await flushPromises()
    expect(refreshedAuth.isAuthenticated).toBe(true)
    expect(refreshedAuth.user?.id).toBe(admin.id)
    const refreshed = await mountEntries(refreshedPinia)
    for (const wrapper of Object.values(refreshed)) expect(modelNotice(wrapper).find(badge).exists()).toBe(false)

    // Exercise the real logout/login store methods with offline auth API fixtures.
    await refreshedAuth.logout()
    await refreshedAuth.login({ email: admin.email, password: 'fixture-password' })
    await flushPromises()
    expect(refreshedAuth.user?.id).toBe(admin.id)
    for (const wrapper of Object.values(refreshed)) expect(modelNotice(wrapper).find(badge).exists()).toBe(false)
    const futureBadge = mount(NewFeatureBadge, {
      props: { noticeId: noticeC, userId: admin.id }, global: { plugins: [refreshedPinia] },
    })
    expect(futureBadge.find(badge).exists()).toBe(true)
    expect(useNewFeatureNoticeStore(refreshedPinia).isDismissed(admin.id, noticeA)).toBe(true)
    expect(useNewFeatureNoticeStore(refreshedPinia).isDismissed(admin.id, NEW_FEATURE_NOTICES.apiKeyModelPolicy)).toBe(true)
    for (const wrapper of Object.values(refreshed)) expect(modelNotice(wrapper).find(badge).exists()).toBe(false)
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual([noticeA, NEW_FEATURE_NOTICES.apiKeyModelPolicy])
    await refreshedAuth.logout()
  })
})

describe('API key long-context announcement', () => {
  const longContextNotice = NEW_FEATURE_NOTICES.apiKeyLongContext

  it('registers a stable unique ID independent of the model-policy announcement', () => {
    expect(longContextNotice).toBe('api-key-long-context-control-initial-announcement')
    expect(longContextNotice).not.toBe(NEW_FEATURE_NOTICES.apiKeyModelPolicy)
    expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
  })

  it.each(['view', 'modal', 'dialog'] as const)('keeps old dismissals and synchronizes all three placements from %s', async placement => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const auth = useAuthStore(pinia)
    await auth.login({ email: admin.email, password: 'fixture-password' })
    const notices = useNewFeatureNoticeStore(pinia)
    notices.dismiss(admin.id, noticeA)
    notices.dismiss(admin.id, NEW_FEATURE_NOTICES.apiKeyModelPolicy)

    const entries = await mountEntries(pinia, true)
    await entries.view.get('[data-test="edit-key-model-policy"]').trigger('click')
    const dialog = entries.view.getComponent({ name: 'ApiKeyModelPolicyDialog' })
    const placements = { ...entries, dialog }
    for (const wrapper of Object.values(entries)) expect(modelNotice(wrapper).find(badge).exists()).toBe(false)
    for (const wrapper of Object.values(placements)) {
      expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(true)
      expect(noticeAt(wrapper, longContextNotice).props('userId')).toBe(admin.id)
    }
    expect(noticeAt(entries.view, longContextNotice).props('compact')).toBe(true)
    expect(noticeAt(dialog, longContextNotice).props('compact')).toBe(false)
    expect(dialog.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('true')

    await noticeAt(placements[placement], longContextNotice).get(badge).trigger('click')
    for (const wrapper of Object.values(placements)) expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(false)
    expect(dialog.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('true')
    expect(entries.modal.getComponent({ name: 'ApiKeyModelPolicyDialog' }).props('show')).toBe(false)
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual([
      noticeA, NEW_FEATURE_NOTICES.apiKeyModelPolicy, longContextNotice,
    ])
    entries.view.unmount()
    entries.modal.unmount()

    vi.clearAllTimers()
    const refreshedPinia = createPinia()
    setActivePinia(refreshedPinia)
    const refreshedAuth = useAuthStore(refreshedPinia)
    refreshedAuth.checkAuth()
    await flushPromises()
    const refreshed = await mountEntries(refreshedPinia, true)
    await refreshed.view.get('[data-test="edit-key-model-policy"]').trigger('click')
    const refreshedDialog = refreshed.view.getComponent({ name: 'ApiKeyModelPolicyDialog' })
    for (const wrapper of [...Object.values(refreshed), refreshedDialog]) expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(false)

    await refreshedAuth.logout()
    await refreshedAuth.login({ email: admin.email, password: 'fixture-password' })
    await flushPromises()
    for (const wrapper of [...Object.values(refreshed), refreshedDialog]) expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(false)
    expect(useNewFeatureNoticeStore(refreshedPinia).isDismissed(admin.id, NEW_FEATURE_NOTICES.apiKeyModelPolicy)).toBe(true)
    const future = mount(NewFeatureBadge, { props: { noticeId: noticeC, userId: admin.id }, global: { plugins: [refreshedPinia] } })
    expect(future.find(badge).exists()).toBe(true)
  })

  it('does not show the new administrator notice to ordinary users', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    login.mockResolvedValueOnce({ access_token: 'fixture-token', token_type: 'Bearer', user: { ...admin, role: 'user' } })
    await useAuthStore(pinia).login({ email: admin.email, password: 'fixture-password' })
    const entries = await mountEntries(pinia, true)
    expect(entries.view.find('[data-test="edit-key-model-policy"]').exists()).toBe(false)
    for (const wrapper of Object.values(entries)) expect(wrapper.findAll(badge)).toHaveLength(0)
  })
})

describe('API key context token ceiling announcement', () => {
  const longContextNotice = NEW_FEATURE_NOTICES.apiKeyContextTokenLimit

  it('registers a stable unique ID independent of the model-policy announcement', () => {
    expect(longContextNotice).toBe('api-key-context-token-limit-initial-announcement')
    expect(longContextNotice).not.toBe(NEW_FEATURE_NOTICES.apiKeyModelPolicy)
    expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
  })

  it.each(['view', 'modal', 'dialog'] as const)('keeps old dismissals and synchronizes all three placements from %s', async placement => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const auth = useAuthStore(pinia)
    await auth.login({ email: admin.email, password: 'fixture-password' })
    const notices = useNewFeatureNoticeStore(pinia)
    notices.dismiss(admin.id, noticeA)
    notices.dismiss(admin.id, NEW_FEATURE_NOTICES.apiKeyModelPolicy)
    notices.dismiss(admin.id, NEW_FEATURE_NOTICES.apiKeyLongContext)

    const entries = await mountEntries(pinia, true)
    await entries.view.get('[data-test="edit-key-model-policy"]').trigger('click')
    const dialog = entries.view.getComponent({ name: 'ApiKeyModelPolicyDialog' })
    const placements = { ...entries, dialog }
    for (const wrapper of Object.values(entries)) {
      expect(modelNotice(wrapper).find(badge).exists()).toBe(false)
      expect(noticeAt(wrapper, NEW_FEATURE_NOTICES.apiKeyLongContext).find(badge).exists()).toBe(false)
    }
    for (const wrapper of Object.values(placements)) {
      expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(true)
      expect(noticeAt(wrapper, longContextNotice).props('userId')).toBe(admin.id)
    }
    expect(noticeAt(entries.view, longContextNotice).props('compact')).toBe(true)
    expect(noticeAt(dialog, longContextNotice).props('compact')).toBe(false)
    expect(dialog.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('true')

    await noticeAt(placements[placement], longContextNotice).get(badge).trigger('click')
    for (const wrapper of Object.values(placements)) expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(false)
    expect(dialog.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('true')
    expect(entries.modal.getComponent({ name: 'ApiKeyModelPolicyDialog' }).props('show')).toBe(false)
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual([
      noticeA, NEW_FEATURE_NOTICES.apiKeyModelPolicy, NEW_FEATURE_NOTICES.apiKeyLongContext, longContextNotice,
    ])
    entries.view.unmount()
    entries.modal.unmount()

    vi.clearAllTimers()
    const refreshedPinia = createPinia()
    setActivePinia(refreshedPinia)
    const refreshedAuth = useAuthStore(refreshedPinia)
    refreshedAuth.checkAuth()
    await flushPromises()
    const refreshed = await mountEntries(refreshedPinia, true)
    await refreshed.view.get('[data-test="edit-key-model-policy"]').trigger('click')
    const refreshedDialog = refreshed.view.getComponent({ name: 'ApiKeyModelPolicyDialog' })
    for (const wrapper of [...Object.values(refreshed), refreshedDialog]) expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(false)

    await refreshedAuth.logout()
    await refreshedAuth.login({ email: admin.email, password: 'fixture-password' })
    await flushPromises()
    for (const wrapper of [...Object.values(refreshed), refreshedDialog]) expect(noticeAt(wrapper, longContextNotice).find(badge).exists()).toBe(false)
    expect(useNewFeatureNoticeStore(refreshedPinia).isDismissed(admin.id, NEW_FEATURE_NOTICES.apiKeyModelPolicy)).toBe(true)
    const future = mount(NewFeatureBadge, { props: { noticeId: noticeC, userId: admin.id }, global: { plugins: [refreshedPinia] } })
    expect(future.find(badge).exists()).toBe(true)
  })

  it('does not show the new administrator notice to ordinary users', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    login.mockResolvedValueOnce({ access_token: 'fixture-token', token_type: 'Bearer', user: { ...admin, role: 'user' } })
    await useAuthStore(pinia).login({ email: admin.email, password: 'fixture-password' })
    const entries = await mountEntries(pinia, true)
    expect(entries.view.find('[data-test="edit-key-model-policy"]').exists()).toBe(false)
    for (const wrapper of Object.values(entries)) expect(wrapper.findAll(badge)).toHaveLength(0)
  })
})
