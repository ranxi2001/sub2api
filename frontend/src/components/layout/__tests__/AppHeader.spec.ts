import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { reactive, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en/common'
import zh from '@/i18n/locales/zh/common'
import AppHeader from '../AppHeader.vue'

const copyToClipboard = vi.fn().mockResolvedValue(true)
const authStore = reactive({
  user: {
    id: 42,
    username: 'Example',
    email: 'example@example.com',
    role: 'user',
    balance: 0,
  } as { id: number; username: string; email: string; role: string; balance: number } | null,
  get isAdmin() { return this.user?.role === 'admin' },
  isSimpleMode: false,
})

vi.mock('@/stores', () => ({
  useAuthStore: () => authStore,
  useAppStore: () => ({ contactInfo: '', docUrl: '', cachedPublicSettings: {} }),
  useOnboardingStore: () => ({ replay: vi.fn() }),
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ name: 'Dashboard', params: {}, meta: {} }),
}))

vi.mock('@/router/title', () => ({
  resolveRouteMetaKeys: () => ({}),
}))

vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: { modelPlaza: {}, subscription: {} },
  isFeatureFlagEnabled: () => false,
  resolveFeatureFlag: () => false,
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: ref(false), copyToClipboard }),
}))

enableAutoUnmount(afterEach)

function mountHeader(locale = 'zh') {
  return shallowMount(AppHeader, {
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: { en, zh }, missingWarn: false, fallbackWarn: false })],
      renderStubDefaultSlot: true,
      stubs: { RouterLink: { template: '<a><slot /></a>' } },
    },
  })
}

describe('AppHeader user ID', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authStore.user = {
      id: 42,
      username: 'Example',
      email: 'example@example.com',
      role: 'user',
      balance: 0,
    }
  })

  it.each(['user', 'admin'])('shows the current ID for %s accounts in the user menu', async (role) => {
    authStore.user!.role = role
    const wrapper = mountHeader()
    expect(wrapper.find('button[aria-label="复制用户 ID：42"]').exists()).toBe(false)

    await wrapper.get('button[aria-label="用户菜单"]').trigger('click')

    const button = wrapper.get('button[aria-label="复制用户 ID：42"]')
    expect(button.attributes('type')).toBe('button')
    expect(button.attributes('title')).toBe('复制用户 ID：42')
    expect(button.text()).toContain('用户 ID: 42')
    const badge = wrapper.findComponent({ name: 'NewFeatureBadge' })
    expect(badge.exists()).toBe(role === 'admin')
    if (role === 'admin') {
      expect(badge.props('noticeId')).toBe('user-id-copy-initial-announcement')
      expect(badge.props('userId')).toBe(42)
    }
  })

  it('copies only the ID and leaves the menu open', async () => {
    const wrapper = mountHeader()
    await wrapper.get('button[aria-label="用户菜单"]').trigger('click')

    await wrapper.get('button[aria-label="复制用户 ID：42"]').trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledTimes(1)
    expect(copyToClipboard).toHaveBeenCalledWith('42')
    expect(wrapper.get('button[aria-label="复制用户 ID：42"]').isVisible()).toBe(true)
  })

  it('updates the displayed and copied ID when the current user changes', async () => {
    const wrapper = mountHeader()
    await wrapper.get('button[aria-label="用户菜单"]').trigger('click')

    authStore.user = { ...authStore.user!, id: 73 }
    await flushPromises()

    expect(wrapper.find('button[aria-label="复制用户 ID：42"]').exists()).toBe(false)
    const button = wrapper.get('button[aria-label="复制用户 ID：73"]')
    expect(button.text()).toContain('用户 ID: 73')
    await button.trigger('click')
    expect(copyToClipboard).toHaveBeenCalledTimes(1)
    expect(copyToClipboard).toHaveBeenCalledWith('73')
  })

  it('provides an English copy label', async () => {
    const wrapper = mountHeader('en')
    await wrapper.get('button[aria-label="User menu"]').trigger('click')

    const button = wrapper.get('button[aria-label="Copy user ID: 42"]')
    expect(button.text()).toContain('User ID: 42')
    expect(button.attributes('title')).toBe('Copy user ID: 42')
  })

  it('hides the ID when the user logs out', async () => {
    const wrapper = mountHeader()
    await wrapper.get('button[aria-label="用户菜单"]').trigger('click')
    expect(wrapper.find('button[aria-label="复制用户 ID：42"]').exists()).toBe(true)

    authStore.user = null
    await flushPromises()

    expect(wrapper.find('button[aria-label="复制用户 ID：42"]').exists()).toBe(false)
    expect(wrapper.find('button[aria-label="用户菜单"]').exists()).toBe(false)
    expect(copyToClipboard).not.toHaveBeenCalled()
  })
})
