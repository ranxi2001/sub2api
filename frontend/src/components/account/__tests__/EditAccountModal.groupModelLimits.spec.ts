import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'

const { updateAccountMock } = vi.hoisted(() => ({ updateAccountMock: vi.fn() }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
      update: updateAccountMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false })
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
  }
}))
vi.mock('@/api/admin/accounts', () => ({ getAntigravityDefaultModelMapping: vi.fn() }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const groups = [
  { id: 3, name: 'Group A', platform: 'anthropic', status: 'active' },
  { id: 5, name: 'Group B', platform: 'anthropic', status: 'active' }
]

const account = () => ({
  id: 12, name: 'Claude key', notes: '', platform: 'anthropic', type: 'apikey',
  credentials: { api_key: 'sk-test', base_url: 'https://api.anthropic.com' }, extra: {},
  proxy_id: null, concurrency: 1, priority: 1, rate_multiplier: 1, status: 'active',
  group_ids: [3, 5], expires_at: null, auto_pause_on_expired: false,
  account_groups: [
    { account_id: 12, group_id: 3, priority: 1, created_at: '' },
    { account_id: 12, group_id: 5, priority: 2, allowed_models: ['claude-sonnet-4-6'], created_at: '' }
  ]
})

function mountModal(overrides: Record<string, unknown> = {}) {
  const pinia = createPinia()
  setActivePinia(pinia)
  return mount(EditAccountModal, {
    props: { show: true, account: { ...account(), ...overrides }, proxies: [], groups },
    global: { plugins: [pinia], stubs: {
      BaseDialog: BaseDialogStub, Select: true, Icon: true, ProxySelector: true,
      GroupSelector: true, ModelWhitelistSelector: true
    } }
  })
}

const submit = async (wrapper: ReturnType<typeof mountModal>) => {
  await wrapper.get('form#edit-account-form').trigger('submit.prevent')
  await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalled())
  return updateAccountMock.mock.calls[0][1] as Record<string, unknown>
}

describe('EditAccountModal per-group model limits', () => {
  beforeEach(() => {
    updateAccountMock.mockReset()
    updateAccountMock.mockResolvedValue(account())
  })

  it('renders and saves the OpenAI OAuth strict RPM setting', async () => {
    const wrapper = mountModal({ platform: 'openai', type: 'oauth', base_rpm: 20, extra: { base_rpm: 20 } })
    const settings = wrapper.findComponent({ name: 'AccountRpmSettings' })
    expect(settings.exists()).toBe(true)
    expect(settings.text()).toContain('admin.accounts.quotaControl.rpmLimit.openaiHint')
    expect(settings.text()).not.toContain('admin.accounts.quotaControl.rpmLimit.strategyTiered')
    await settings.get('input[type="number"]').setValue('30')
    const payload = await submit(wrapper)
    expect(payload.extra).toMatchObject({ base_rpm: 30 })
    expect(payload.extra).not.toHaveProperty('rpm_strategy')
  })

  it.each(['apikey', 'setup-token'])('does not expose RPM controls for OpenAI %s', (type) => {
    const wrapper = mountModal({ platform: 'openai', type })
    expect(wrapper.findComponent({ name: 'AccountRpmSettings' }).exists()).toBe(false)
  })

  it('loads the saved limits and sends them back unchanged', async () => {
    const wrapper = mountModal()
    expect(wrapper.get('[data-testid="group-model-limit-5"]').text()).toContain('Group B')

    const payload = await submit(wrapper)
    expect(payload.group_allowed_models).toEqual({ 5: ['claude-sonnet-4-6'] })
  })

  it('clears a limit when the group is switched back to all models', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="group-model-limit-5"]').get('[data-testid="group-model-limit-all"]').trigger('click')

    const payload = await submit(wrapper)
    expect(payload.group_allowed_models).toEqual({})
  })

  it('does not send a limit for a group switched to selected models without picking any', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="group-model-limit-3"]').get('[data-testid="group-model-limit-selected"]').trigger('click')
    expect(wrapper.get('[data-testid="group-model-limit-3"]').find('[data-testid="group-model-limit-empty"]').exists()).toBe(true)

    const payload = await submit(wrapper)
    expect(payload.group_allowed_models).toEqual({ 5: ['claude-sonnet-4-6'] })
  })
})
