import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it } from 'vitest'
import SmartOpsNav from '../SmartOpsNav.vue'
import { useAdminSettingsStore } from '@/stores/adminSettings'

const smartOpsPaths = ['/admin/auto-config', '/admin/priority-scheduling',
  '/admin/channel-operations', '/admin/profit-operations', '/admin/account-quality', '/admin/controlled-experiments', '/admin/account-ops', '/admin/token-guard', '/admin/token-guard-v2', '/admin/pelican-tests', '/admin/group-cache-tests']

async function mountAt(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [...smartOpsPaths, '/admin/request-captures', '/admin/harvest-flow'].map(route => ({ path: route, component: { template: '<div />' } }))
  })
  await router.push(path)
  await router.isReady()
  const i18n = createI18n({ legacy: false, locale: 'zh', missingWarn: false, fallbackWarn: false, messages: { zh: {} } })
  return mount(SmartOpsNav, { global: { plugins: [router, i18n] } })
}

const hrefs = (wrapper: Awaited<ReturnType<typeof mountAt>>) => wrapper.findAll('a').map(link => link.attributes('href'))

describe('SmartOpsNav', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('ends with the ticket harvest flow and hides request capture while the feature is off', async () => {
    const wrapper = await mountAt('/admin/harvest-flow')
    expect(hrefs(wrapper)).toEqual([...smartOpsPaths, '/admin/harvest-flow'])
    expect(wrapper.find('[aria-current="page"]').text()).toBe('nav.harvestFlow')
  })

  it('lists request capture before the ticket harvest flow when the feature is on', async () => {
    useAdminSettingsStore().setRequestCaptureEnabledLocal(true)
    const wrapper = await mountAt('/admin/request-captures')
    expect(hrefs(wrapper)).toEqual([...smartOpsPaths, '/admin/request-captures', '/admin/harvest-flow'])
    expect(wrapper.find('[aria-current="page"]').text()).toBe('admin.requestCapture.title')
  })

  it('follows the feature switch without remounting', async () => {
    const wrapper = await mountAt('/admin/pelican-tests')
    expect(hrefs(wrapper)).not.toContain('/admin/request-captures')
    useAdminSettingsStore().setRequestCaptureEnabledLocal(true)
    await nextTick()
    expect(hrefs(wrapper)).toContain('/admin/request-captures')
  })
})
