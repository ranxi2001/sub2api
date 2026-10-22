import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import SecurityAuditNav from '../SecurityAuditNav.vue'

vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: { riskControl: 'risk-control' },
  isFeatureFlagEnabled: () => true
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('security audit navigation', () => {
  it('exposes the safety prompt audit as a fourth tab', () => {
    const wrapper = mount(SecurityAuditNav, {
      props: { active: '/admin/safety-prompt-audit' },
      global: {
        stubs: {
          RouterLink: {
            props: ['to'],
            template: '<a :href="to"><slot /></a>'
          }
        }
      }
    })
    const links = wrapper.findAll('a')
    expect(links).toHaveLength(4)
    const target = wrapper.get('a[href="/admin/safety-prompt-audit"]')
    expect(target.text()).toBe('admin.safetyPromptAudit.nav')
    expect(target.attributes('aria-current')).toBe('page')
    wrapper.unmount()
  })
})
