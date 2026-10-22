import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SafetyPromptAuditView from '../SafetyPromptAuditView.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { plugins: api } }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key })
}))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/security/SecurityAuditNav.vue', () => ({ default: { template: '<nav data-testid="security-nav" />' } }))

const mountOptions = {
  global: {
    stubs: {
      RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }
    }
  }
}

const installation = {
  id: 7,
  plugin_key: 'local.sub2api.safety-prompt',
  name: 'Safety Prompt',
  version: '1.6.3',
  state: 'enabled',
  manifest: { id: 'local.sub2api.safety-prompt' }
}

function auditStatus() {
  return {
    healthy: true,
    message: 'running',
    status_json: JSON.stringify({
      audit: {
        persistent: true,
        retention_days: 30,
        limit: 500,
        capacity_evictions: 2,
        events: [
          { time: '2026-10-22T08:29:42Z', id: 'sp-1', request_id: 'req-1', model: 'gpt-6.1-sol', upstream_account_id: 12, summary: 'synthetic summary' },
          { time: '2026-10-21T01:00:00Z', id: 'sp-2', request_id: 'req-2', model: 'other-model', upstream_account_id: 13, summary: 'second summary' }
        ]
      }
    })
  }
}

beforeEach(() => {
  vi.resetAllMocks()
  api.list.mockResolvedValue([installation])
  api.status.mockResolvedValue(auditStatus())
})

describe('safety prompt audit', () => {
  it('renders plugin audit events, storage state and filters', async () => {
    const wrapper = mount(SafetyPromptAuditView, mountOptions)
    await flushPromises()
    expect(api.status).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-testid="safety-audit-storage"]').text()).toContain('admin.safetyPromptAudit.storageReady')
    expect(wrapper.get('[data-testid="safety-audit-table"]').text()).toContain('gpt-6.1-sol')
    expect(wrapper.get('[data-testid="safety-audit-table"]').text()).toContain('synthetic summary')
    expect(wrapper.get('[data-testid="safety-audit-count"]').text()).toContain('"shown":2')
    await wrapper.get('input[type="search"]').setValue('no-match')
    expect(wrapper.get('[data-testid="safety-audit-empty"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows a clear install state when the plugin is absent', async () => {
    api.list.mockResolvedValue([])
    const wrapper = mount(SafetyPromptAuditView, mountOptions)
    await flushPromises()
    expect(wrapper.get('[data-testid="safety-audit-missing"]').text()).toContain('admin.safetyPromptAudit.pluginMissing')
    expect(api.status).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not invent events when the plugin omits audit data', async () => {
    api.status.mockResolvedValue({ healthy: false, message: 'not running', status_json: '' })
    const wrapper = mount(SafetyPromptAuditView, mountOptions)
    await flushPromises()
    expect(wrapper.text()).toContain('admin.safetyPromptAudit.noAudit')
    expect(wrapper.find('[data-testid="safety-audit-table"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
