import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { reactive, ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import OAuthInitialModelSelection from '../OAuthInitialModelSelection.vue'
import SmartOpsNav from '../SmartOpsNav.vue'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { useAuthStore } from '@/stores/auth'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

const { login, logout, currentUser } = vi.hoisted(() => ({ login: vi.fn(), logout: vi.fn(), currentUser: vi.fn() }))
vi.mock('@/api', () => ({ authAPI: { login, logout, getCurrentUser: currentUser }, passkeyAPI: {}, isTotp2FARequired: () => false }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: ref('zh') }) }))
vi.mock('@/composables/useBatchImageAccess', () => ({ useBatchImageAccess: () => ({ canUseBatchImage: ref(false), refreshBatchImageAccess: vi.fn() }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ publicSettingsLoaded: true, cachedPublicSettings: {} }) }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ requestCaptureEnabled: false }) }))
vi.mock('@/stores', async () => ({
 useAuthStore: (await import('@/stores/auth')).useAuthStore,
 useAdminSettingsStore: () => ({ fetch: vi.fn(), smartOpsHiddenMenuItems: [], customMenuItems: [], requestCaptureEnabled: false }),
 useAppStore: () => ({ sidebarCollapsed: false, mobileOpen: false, publicSettingsLoaded: true, cachedPublicSettings: {}, siteName: 'Fixture', siteLogo: '', siteVersion: '', sidebarScrollTop: 0 }),
 useOnboardingStore: () => ({ isCurrentStep: () => false }),
 useSupportTicketStore: () => ({ userUnread: 0, adminPending: 0 }),
}))
const admin = { id: 41, role: 'admin', email: 'admin@example.test' }
const id = NEW_FEATURE_NOTICES.oauthInitialModelSelection, old = NEW_FEATURE_NOTICES.protocolFeatureSwitches
const badge = '[data-testid="new-feature-badge"]'
function notice(wrapper: VueWrapper) {
 const component = wrapper.findAllComponents(NewFeatureBadge).find(component => component.props('noticeId') === id)
 expect(component, 'the actual entry must share the stable notice ID').toBeDefined()
 return component!
}
async function entries(pinia: Pinia) {
 const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/admin/auto-config', component: { template: '<div />' } }] })
 await router.push('/admin/auto-config'); await router.isReady()
 const global = { plugins: [pinia, router], stubs: { VersionBadge: true, FeatureSearch: true, Icon: true } }
 const sidebar = mount(AppSidebar, { global })
 const nav = mount(SmartOpsNav, { global })
 const selection = mount(OAuthInitialModelSelection, { props: { modelValue: null, platform: 'openai', userId: admin.id, mappings: [] }, global })
 await flushPromises()
 return { sidebar, nav, selection }
}
enableAutoUnmount(afterEach)
beforeEach(() => {
 localStorage.clear()
 vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] })
 login.mockResolvedValue({ access_token: 'fixture', token_type: 'Bearer', user: admin })
 logout.mockResolvedValue(undefined); currentUser.mockResolvedValue({ data: admin })
})
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.clearAllMocks() })

describe('OAuth initial selection notice on real entries', () => {
 it.each(['sidebar', 'nav', 'selection'] as const)('preserves A, dismisses all B from %s, reloads and reauthenticates without reviving A/B, then shows C', async entry => {
  const pinia = createPinia(); setActivePinia(pinia)
  await useAuthStore(pinia).login({ email: admin.email, password: 'fixture-password' })
  useNewFeatureNoticeStore(pinia).dismiss(admin.id, old)
  const mounted = await entries(pinia)
  for (const wrapper of Object.values(mounted)) expect(notice(wrapper).find(badge).exists()).toBe(true)
  await notice(mounted[entry]).get(badge).trigger('click')
  for (const wrapper of Object.values(mounted)) expect(notice(wrapper).find(badge).exists()).toBe(false)
  for (const wrapper of Object.values(mounted)) wrapper.unmount()
  vi.clearAllTimers()
  const refreshed = createPinia(); setActivePinia(refreshed)
  const auth = useAuthStore(refreshed); auth.checkAuth(); await flushPromises()
  expect(auth.user?.id).toBe(admin.id)
  const remounted = await entries(refreshed)
  for (const wrapper of Object.values(remounted)) expect(notice(wrapper).find(badge).exists()).toBe(false)
  await auth.logout(); await auth.login({ email: admin.email, password: 'fixture-password' }); await flushPromises()
  for (const wrapper of Object.values(remounted)) expect(notice(wrapper).find(badge).exists()).toBe(false)
  const future = mount(NewFeatureBadge, { props: { noticeId: 'oauth-selection-future-C', userId: admin.id }, global: { plugins: [refreshed] } })
  expect(future.find(badge).exists()).toBe(true)
  expect(useNewFeatureNoticeStore(refreshed).isDismissed(admin.id, old)).toBe(true)
  expect(useNewFeatureNoticeStore(refreshed).isDismissed(admin.id, id)).toBe(true)
  expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
  await auth.logout()
 })
 it('shows current platform candidates and prevents malformed custom names', async () => {
  const pinia = createPinia(); setActivePinia(pinia)
  const props = reactive({ modelValue: ['custom-saved'], platform: 'openai', userId: 41, mappings: [] })
  const wrapper = mount(OAuthInitialModelSelection, { props, global: { plugins: [pinia] } })
  expect(wrapper.find('[data-model="gpt-6-luna"]').exists()).toBe(true)
  expect(wrapper.find('[data-model="custom-saved"]').exists()).toBe(true)
  await wrapper.get('[data-testid="model-selection-custom"]').setValue('invalid*')
  expect(wrapper.get('[data-testid="model-selection-add"]').attributes('disabled')).toBeDefined()
  await wrapper.setProps({ platform: 'anthropic', modelValue: [] })
  expect(wrapper.find('[data-model="gpt-6-luna"]').exists()).toBe(false)
  expect(wrapper.find('[data-model="claude-sonnet-4-6"]').exists()).toBe(true)
 })
})
