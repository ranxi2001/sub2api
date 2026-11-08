import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it } from 'vitest'
import SmartOpsNav from '../SmartOpsNav.vue'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { useAuthStore } from '@/stores/auth'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import type { User } from '@/types'

const smartOpsPaths = ['/admin/lottery', '/admin/auto-config', '/admin/priority-scheduling',
  '/admin/channel-operations', '/admin/profit-operations', '/admin/account-quality', '/admin/controlled-experiments', '/admin/account-ops', '/admin/token-guard', '/admin/token-guard-v2', '/admin/pelican-tests', '/admin/group-cache-tests']

async function mountAt(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [...smartOpsPaths, '/admin/request-captures', '/admin/ai-ops', '/admin/harvest-flow'].map(route => ({ path: route, component: { template: '<div />' } }))
  })
  await router.push(path)
  await router.isReady()
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: {} } })
  return mount(SmartOpsNav, { global: { plugins: [router, i18n] } })
}

const hrefs = (wrapper: Awaited<ReturnType<typeof mountAt>>) => wrapper.findAll('a').map(link => link.attributes('href'))

describe('SmartOpsNav', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    useAuthStore().user = { id: 41, role: 'admin' } as User
  })

  it('keeps lottery administration reachable and marks the selected pool management page', async () => {
    const wrapper = await mountAt('/admin/lottery')
    expect(hrefs(wrapper)).toContain('/admin/lottery')
    expect(wrapper.find('[aria-current="page"]').text()).toBe('lottery.adminTitle')
  })

  it('ends with the ticket harvest flow and hides request capture while the feature is off', async () => {
    const wrapper = await mountAt('/admin/harvest-flow')
    expect(hrefs(wrapper)).toEqual([...smartOpsPaths, '/admin/ai-ops', '/admin/harvest-flow'])
    expect(wrapper.find('[aria-current="page"]').text()).toBe('nav.harvestFlow')
  })

  it('lists request capture before the ticket harvest flow when the feature is on', async () => {
    useAdminSettingsStore().setRequestCaptureEnabledLocal(true)
    const wrapper = await mountAt('/admin/request-captures')
    expect(hrefs(wrapper)).toEqual([...smartOpsPaths, '/admin/request-captures', '/admin/ai-ops', '/admin/harvest-flow'])
    expect(wrapper.find('[aria-current="page"]').text()).toBe('admin.requestCapture.title')
  })

  it('follows the feature switch without remounting', async () => {
    const wrapper = await mountAt('/admin/pelican-tests')
    expect(hrefs(wrapper)).not.toContain('/admin/request-captures')
    useAdminSettingsStore().setRequestCaptureEnabledLocal(true)
    await nextTick()
    expect(hrefs(wrapper)).toContain('/admin/request-captures')
  })

  it('hides the AI operations entry from observers', async () => {
    useAuthStore().user = { id: 42, role: 'observer' } as User
    const wrapper = await mountAt('/admin/pelican-tests')
    expect(hrefs(wrapper)).not.toContain('/admin/ai-ops')
  })

  it('keeps the AI notice in the normal nav flow so dismissing it reserves no space', async () => {
    const wrapper = await mountAt('/admin/ai-ops')
    const entry = wrapper.get('a[href="/admin/ai-ops"]')
    const badge = wrapper.get('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]')
    expect(entry.classes()).not.toContain('pr-16')
    expect(badge.classes()).not.toContain('absolute')
    await badge.trigger('click')
    await nextTick()
    expect(wrapper.find('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(entry.classes()).not.toContain('pr-16')
  })

  it('shares the AI notice dismissal across entries and retains it after reloading preferences', async () => {
    useNewFeatureNoticeStore().dismiss(41, NEW_FEATURE_NOTICES.protocolFeatureSwitches)
    const first = await mountAt('/admin/ai-ops')
    const second = await mountAt('/admin/auto-config')
    expect(first.find('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]').exists()).toBe(true)
    expect(second.find('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]').exists()).toBe(true)
    await first.get('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]').trigger('click')
    await nextTick()
    expect(second.find('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]').exists()).toBe(false)
    first.unmount(); second.unmount()
    setActivePinia(createPinia())
    useAuthStore().user = { id: 41, role: 'admin' } as User
    const reloaded = await mountAt('/admin/ai-ops')
    expect(reloaded.find('a[href="/admin/ai-ops"] + [data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(useNewFeatureNoticeStore().isDismissed(41, NEW_FEATURE_NOTICES.protocolFeatureSwitches)).toBe(true)
    reloaded.unmount()
  })

  it('shares quality rest dismissal across navigation entries without dismissing other features', async () => {
    const first = await mountAt('/admin/account-quality')
    const second = await mountAt('/admin/auto-config')
    const selector = 'a[href="/admin/account-quality"] + [data-testid="new-feature-badge"]'
    expect(second.find(selector).exists()).toBe(true)
    await first.get(selector).trigger('click'); await nextTick()
    expect(second.find(selector).exists()).toBe(false)
    expect(useNewFeatureNoticeStore().isDismissed(41, NEW_FEATURE_NOTICES.qualityRetestRest)).toBe(true)
    expect(useNewFeatureNoticeStore().isDismissed(41, NEW_FEATURE_NOTICES.aiOpsCodex)).toBe(false)
    first.unmount(); second.unmount()
  })
})
