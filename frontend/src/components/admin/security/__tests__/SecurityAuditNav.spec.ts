import { createPinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import SecurityAuditNav from '../SecurityAuditNav.vue'
import IPAuditView from '@/features/ip-audit/IPAuditView.vue'

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: { risk_control_enabled: true } }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/features/ip-risk/IPRiskSettings.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/ip-audit/IPAuditPanel.vue', () => ({ default: { template: '<div />' } }))
const paths = ['/admin/risk-control', '/admin/prompt-audit', '/admin/safety-prompt-audit', '/admin/ip-audit']

function routerFixture() {
  return createRouter({ history: createMemoryHistory(), routes: paths.map(path => ({ path, component: { template: '<div />' } })) })
}

describe('security audit navigation', () => {
  it('keeps exactly the current page highlighted across all four routes including query/hash deep links', async () => {
    const router = routerFixture()
    await router.push(paths[0]); await router.isReady()
    const wrapper = mount({ components: { SecurityAuditNav }, template: '<SecurityAuditNav :active="$route.path" />' }, { global: { plugins: [createPinia(), router] } })
    for (const path of paths) {
      await router.push(`${path}?tab=config#settings`); await flushPromises()
      expect(wrapper.findAll('a')).toHaveLength(4)
      const active = wrapper.findAll('[aria-current="page"]')
      expect(active).toHaveLength(1)
      expect(active[0].attributes('href')).toBe(path)
      expect(active[0].classes()).toContain('bg-gray-900')
      expect(wrapper.findAll('a').filter(link => link.classes().includes('bg-gray-900'))).toHaveLength(1)
    }
    wrapper.unmount()
  })

  it('passes the IP route from the real IP page to the shared navigation', async () => {
    const router = routerFixture()
    await router.push('/admin/ip-audit?tab=config'); await router.isReady()
    const wrapper = mount(IPAuditView, { global: { plugins: [createPinia(), router], stubs: { AppLayout: { template: '<div><slot /></div>' }, IPRiskSettings: true, IPAuditPanel: true } } })
    expect(wrapper.getComponent(SecurityAuditNav).props('active')).toBe('/admin/ip-audit')
    expect(wrapper.get('[aria-current="page"]').attributes('href')).toBe('/admin/ip-audit')
    wrapper.unmount()
  })
})
