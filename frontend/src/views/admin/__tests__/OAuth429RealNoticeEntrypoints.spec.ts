import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import OAuthDegradationCard from '@/components/admin/operations/OAuthDegradationCard.vue'
import AutoConfigHistory from '@/components/admin/operations/AutoConfigHistory.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { defaultOAuthDegradationConfig } from '@/utils/oauthDegradation'
import { authAPI } from '@/api/auth'
import en from '@/i18n/locales/en'
import type { User } from '@/types'

// Only network boundaries are mocked. Both product entries, their badges,
// Pinia stores, authentication actions, and translations are real components.
vi.mock('@/api/admin/autoConfig', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/admin/autoConfig')>(),
  getAutoConfigEvents: vi.fn(async () => ({ items: [], has_more: false }))
}))
vi.mock('@/api/auth', async importOriginal => {
  const actual = await importOriginal<typeof import('@/api/auth')>()
  return {
    ...actual,
    authAPI: {
      ...actual.authAPI,
      login: vi.fn(),
      logout: vi.fn(async () => {}),
      getCurrentUser: vi.fn()
    }
  }
})

const admin: User = {
  id: 41, username: 'fixture-admin', email: 'fixture@example.invalid', role: 'admin',
  balance: 0, concurrency: 1, status: 'active', allowed_groups: null,
  balance_notify_enabled: false, balance_notify_threshold: null, balance_notify_extra_emails: [],
  created_at: '2026-11-20T00:00:00Z', updated_at: '2026-11-20T00:00:00Z'
}
const credentials = { email: admin.email, password: 'offline-fixture' }
const A = NEW_FEATURE_NOTICES.oauthDegradation429
const B = NEW_FEATURE_NOTICES.oauth429IndependentTests
const C = 'future-fixture-independent-announcement'
let auth: ReturnType<typeof useAuthStore>
let pinia: ReturnType<typeof createPinia>

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  pinia = createPinia()
  setActivePinia(pinia)
  auth = useAuthStore()
  vi.mocked(authAPI.login).mockResolvedValue({ access_token: 'offline-fixture', token_type: 'Bearer', user: admin })
  vi.mocked(authAPI.getCurrentUser).mockResolvedValue({ data: admin } as Awaited<ReturnType<typeof authAPI.getCurrentUser>>)
})
afterEach(async () => { await auth.logout() })

it.each(['card', 'history'] as const)('real card/history share dismissal from %s across refresh and actual logout/login', async entry => {
  await auth.login(credentials)
  useNewFeatureNoticeStore().dismiss(admin.id, A)
  const future = ref(false)
  const Page = defineComponent({
    setup() {
      const currentAuth = useAuthStore()
      const config = defaultOAuthDegradationConfig()
      return () => h('main', [
        h(OAuthDegradationCard, { modelValue: config, userId: currentAuth.user?.id }),
        h(AutoConfigHistory, { refreshKey: 0 }),
        future.value ? h(NewFeatureBadge, { noticeId: C, userId: currentAuth.user?.id }) : null
      ])
    }
  })
  const open = () => mount(Page, { global: { plugins: [
    // Mount with the current real Pinia instance, including after refresh.
    pinia,
    createI18n({ legacy: false, locale: 'en', messages: { en } })
  ] } })
  const byID = (page: ReturnType<typeof open>, id: string) => page.findAllComponents(NewFeatureBadge).filter(badge => badge.props('noticeId') === id)
  const visible = (page: ReturnType<typeof open>, id: string) => byID(page, id).filter(badge => badge.find('[data-testid="new-feature-badge"]').exists()).length
  let page = open()
  await flushPromises()
  const cardBadge = page.getComponent(OAuthDegradationCard).findAllComponents(NewFeatureBadge).find(badge => badge.props('noticeId') === B)!
  const historyBadge = page.getComponent(AutoConfigHistory).getComponent(NewFeatureBadge)
  expect(cardBadge.props('noticeId')).toBe(B)
  expect(historyBadge.props('noticeId')).toBe(B)
  expect(visible(page, B)).toBe(2)
  expect(visible(page, A)).toBe(0)
  await (entry === 'card' ? cardBadge : historyBadge).get('[data-testid="new-feature-badge"]').trigger('click')
  expect(visible(page, B)).toBe(0)
  expect(JSON.parse(localStorage.getItem('sub2api:new-feature-notices:v1:41')!)).toEqual(expect.arrayContaining([A, B]))

  // Page refresh: discard components and in-memory stores, then restore the
  // same persisted login through the real auth.checkAuth action.
  page.unmount()
  const savedAuth = ['auth_token', 'auth_user'].map(key => [key, localStorage.getItem(key)!] as const)
  await auth.logout()
  savedAuth.forEach(([key, value]) => localStorage.setItem(key, value))
  pinia = createPinia()
  setActivePinia(pinia)
  auth = useAuthStore()
  auth.checkAuth()
  page = open()
  await flushPromises()
  expect(visible(page, A)).toBe(0)
  expect(visible(page, B)).toBe(0)

  await auth.logout()
  await flushPromises()
  expect(visible(page, B)).toBe(0)
  await auth.login(credentials)
  await flushPromises()
  expect(byID(page, B)).toHaveLength(2)
  expect(visible(page, A)).toBe(0)
  expect(visible(page, B)).toBe(0)
  future.value = true
  await flushPromises()
  expect(visible(page, C)).toBe(1)
  expect(visible(page, A)).toBe(0)
  expect(visible(page, B)).toBe(0)
  page.unmount()
})
