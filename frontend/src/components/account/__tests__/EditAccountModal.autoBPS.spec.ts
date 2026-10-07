import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defaultQualityBPS } from '@/utils/qualityRulePatch'

enableAutoUnmount(afterEach)

const mocks = vi.hoisted(() => ({
  updateAccount: vi.fn(),
  listByAccount: vi.fn(),
  createPlan: vi.fn(),
  updatePlan: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  publicSettings: { excel_bps_enabled: true, prism_browser_enabled: true }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: mocks.showWarning, cachedPublicSettings: mocks.publicSettings })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
      update: mocks.updateAccount,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false })
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) },
    scheduledTests: { listByAccount: mocks.listByAccount, create: mocks.createPlan, update: mocks.updatePlan }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function buildOAuthAccount(patch: Record<string, unknown> = {}) {
  return {
    id: 7, name: 'OpenAI OAuth', notes: '', platform: 'openai', type: 'oauth',
    credentials: { access_token: 'oauth-token', chatgpt_account_id: 'acc' }, extra: {},
    proxy_id: null, concurrency: 1, priority: 1, rate_multiplier: 1, status: 'active',
    group_ids: [], expires_at: null, auto_pause_on_expired: false, parent_account_id: null,
    ...patch
  } as any
}

function buildRule(patch: Record<string, unknown> = {}) {
  return {
    id: 31, account_id: 7, model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: true, max_results: 100,
    auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: '',
    pelican_config: { question_kind: 'state_probe', prompt: '', quality: {
      expected_answer: '', action: 'enable_bps', remove_group_ids: [], auto_restore: true, bps: { ...defaultQualityBPS(), models: [], all_models: true } } },
    ...patch
  }
}

function mountModal(account = buildOAuthAccount()) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: {
      stubs: { BaseDialog: BaseDialogStub, Select: true, Icon: true, ProxySelector: true, GroupSelector: true, ModelWhitelistSelector: true }
    }
  })
}

async function submit(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('form#edit-account-form').trigger('submit.prevent')
  await flushPromises()
}

const toggleSelector = '[data-testid="account-auto-bps-toggle"]'

describe('EditAccountModal Prism OAuth switch', () => {
  beforeEach(() => {
    Object.entries(mocks).forEach(([key, mock]) => {
      if (key !== 'publicSettings') mock.mockReset()
    })
    mocks.publicSettings = { excel_bps_enabled: true, prism_browser_enabled: true }
    mocks.updateAccount.mockImplementation(async (_id: number, payload: Record<string, unknown>) => ({ ...buildOAuthAccount(), ...payload }))
    mocks.listByAccount.mockResolvedValue([])
  })

  it('hides BPS and Prism account settings when the global switches are off', () => {
    mocks.publicSettings = { excel_bps_enabled: false, prism_browser_enabled: false }
    const wrapper = mountModal()
    expect(wrapper.find('[data-testid="openai-prism-browser-oauth-settings"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="excel-bps-all-models"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('persists the Prism switch while preserving unrelated extra fields', async () => {
    const wrapper = mountModal(buildOAuthAccount({ extra: { fixture_flag: true } }))
    await flushPromises()
    await wrapper.get('[data-testid="openai-prism-browser-oauth-toggle"]').setValue(true)
    await submit(wrapper)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(mocks.updateAccount.mock.calls[0][1].extra).toMatchObject({ fixture_flag: true, openai_prism_browser: true,
      openai_prism_browser_models: ['gpt-6.1-sol', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-6-luna'] })
  })

  it('removes the flag when disabled', async () => {
    const wrapper = mountModal(buildOAuthAccount({ extra: { openai_prism_browser: true, openai_prism_browser_models: ['gpt-6.1-sol'] } }))
    await flushPromises()
    await wrapper.get('[data-testid="openai-prism-browser-oauth-toggle"]').setValue(false)
    await submit(wrapper)
    expect(mocks.updateAccount.mock.calls[0][1].extra.openai_prism_browser).toBeUndefined()
    expect(mocks.updateAccount.mock.calls[0][1].extra.openai_prism_browser_models).toBeUndefined()
  })

  it('limits legacy accounts to the four supported models and persists a subset', async () => {
    const wrapper = mountModal(buildOAuthAccount({ extra: { openai_prism_browser: true } }))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="prism-model-scope"] input:checked')).toHaveLength(4)
    for (const model of ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-6-luna']) {
      await wrapper.get(`[data-testid="prism-model-${model}"]`).setValue(false)
    }
    await submit(wrapper)
    expect(mocks.updateAccount.mock.calls[0][1].extra.openai_prism_browser_models).toEqual(['gpt-6.1-sol'])
  })

  it.each([[], ['gpt-4o-audio-preview'], 'malformed'])('does not widen an empty or invalid scope: %j', async (models) => {
    const wrapper = mountModal(buildOAuthAccount({ extra: { openai_prism_browser: true, openai_prism_browser_models: models } }))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="prism-model-scope"] input:checked')).toHaveLength(0)
    await submit(wrapper)
    expect(mocks.updateAccount.mock.calls[0][1].extra.openai_prism_browser_models).toEqual([])
  })

  it('hides Prism for API-key accounts', async () => {
    const wrapper = mountModal(buildOAuthAccount({ type: 'apikey' }))
    await flushPromises()
    expect(wrapper.find('[data-testid="openai-prism-browser-oauth-toggle"]').exists()).toBe(false)
  })
})

describe('EditAccountModal auto BPS switch', () => {
  beforeEach(() => {
    Object.values(mocks).forEach(mock => {
      if (typeof mock === 'function') mock.mockReset()
    })
    mocks.publicSettings.excel_bps_enabled = true
    mocks.publicSettings.prism_browser_enabled = true
    mocks.updateAccount.mockImplementation(async (_id: number, payload: Record<string, unknown>) => ({ ...buildOAuthAccount(), ...payload }))
    mocks.listByAccount.mockResolvedValue([])
    mocks.createPlan.mockImplementation(async (request: Record<string, unknown>) => ({ ...buildRule(), ...request, id: 40 }))
    mocks.updatePlan.mockImplementation(async (id: number, request: Record<string, unknown>) => ({ ...buildRule(), ...request, id }))
  })

  it('shows the running rule and pauses it when switched off', async () => {
    mocks.listByAccount.mockResolvedValue([buildRule({ id: 30, pelican_config: { question_kind: 'pelican' } }), buildRule()])
    const wrapper = mountModal()
    await flushPromises()
    expect(mocks.listByAccount).toHaveBeenCalledWith(7)
    const toggle = wrapper.get(toggleSelector)
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(wrapper.find('[data-testid="account-auto-bps-pause-hint"]').exists()).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-bps-auto-disable"]').element.checked).toBe(true)

    await toggle.trigger('click')
    expect(wrapper.find('[data-testid="quality-bps-settings"]').exists()).toBe(false)
    await submit(wrapper)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(mocks.updatePlan).toHaveBeenCalledWith(31, { enabled: false })
    expect(mocks.createPlan).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it.each([true, false])('creates BPS alongside a group/scheduling rule, enabled=%s', async (enabled) => {
    mocks.listByAccount.mockResolvedValue([buildRule({ enabled, pelican_config: {
      question_kind: 'state_probe', prompt: '', parallel_count: 1,
      quality: { expected_answer: '', action: 'disable_scheduling', remove_group_ids: [], auto_restore: true }
    } })])
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get(toggleSelector).attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-testid="account-auto-bps-conflict"]').exists()).toBe(false)
    await wrapper.get(toggleSelector).trigger('click')
    await submit(wrapper)
    expect(mocks.createPlan).toHaveBeenCalledTimes(1)
    expect(mocks.createPlan.mock.calls[0][0].pelican_config.quality.action).toBe('enable_bps')
    expect(mocks.updatePlan).not.toHaveBeenCalled()
  })

  it('edits only the BPS rule when both rule types exist', async () => {
    mocks.listByAccount.mockResolvedValue([buildRule({ id: 47, pelican_config: {
      question_kind: 'candy', quality: { action: 'remove_groups', remove_group_ids: [1] }
    } }), buildRule()])
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe('true')
    expect(wrapper.get(toggleSelector).attributes('disabled')).toBeUndefined()
    await wrapper.get(toggleSelector).trigger('click')
    await submit(wrapper)
    expect(mocks.updatePlan).toHaveBeenCalledWith(31, { enabled: false })
    expect(mocks.createPlan).not.toHaveBeenCalled()
  })

  it('leaves the rule alone when nothing about it changed', async () => {
    mocks.listByAccount.mockResolvedValue([buildRule()])
    const wrapper = mountModal()
    await flushPromises()
    await submit(wrapper)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(mocks.updatePlan).not.toHaveBeenCalled()
    expect(mocks.createPlan).not.toHaveBeenCalled()
  })

  it('creates a rule for the account when switched on', async () => {
    const wrapper = mountModal()
    await flushPromises()
    const toggle = wrapper.get(toggleSelector)
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(wrapper.find('[data-testid="account-auto-bps-pause-hint"]').exists()).toBe(false)
    await toggle.trigger('click')
    await wrapper.get('[data-testid="quality-bps-threshold"]').setValue(3)
    await submit(wrapper)
    expect(mocks.createPlan).toHaveBeenCalledTimes(1)
    const request = mocks.createPlan.mock.calls[0][0]
    expect(request).toMatchObject({ account_id: 7, model_id: 'gpt-6-astra', cron_expression: '*/2 * * * *', enabled: true })
    expect(request.pelican_config).toMatchObject({ question_kind: 'state_probe', quality: { action: 'enable_bps', auto_restore: true, bps: {
      failure_threshold: 3, omit_unsupported_tools: false, ignore_encrypted_content: true, auto_disable_on_403: true,
      auto_recover_on_403: false, auto_move_on_403: false, session_proxy: false, cache_creation_as_input: true,
    } } })
    expect(mocks.updatePlan).not.toHaveBeenCalled()
  })

  it('updates the probe interval and selected options on the same rule', async () => {
    mocks.listByAccount.mockResolvedValue([buildRule()])
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get<HTMLSelectElement>('[data-testid="quality-probe-interval"]').element.value).toBe('*/30 * * * *')
    await wrapper.get('[data-testid="quality-probe-interval"]').setValue('*/5 * * * *')
    await wrapper.get('[data-testid="quality-bps-ignore_encrypted_content"]').setValue(false)
    await wrapper.get('[data-testid="quality-bps-auto_disable_on_403"]').setValue(false)
    await wrapper.get('[data-testid="quality-bps-cache_creation_as_input"]').setValue(false)
    await wrapper.get('[data-testid="quality-bps-omit_unsupported_tools"]').setValue(true)
    await wrapper.get('[data-testid="quality-bps-auto_move_on_403"]').setValue(true)
    await wrapper.get('[data-testid="quality-bps-target-group"]').setValue('0')
    await wrapper.get('[data-testid="quality-bps-session_proxy"]').setValue(true)
    await wrapper.get('input[type="radio"][value="ip_pool"]').setValue()
    await submit(wrapper)
    expect(mocks.createPlan).not.toHaveBeenCalled()
    expect(mocks.updatePlan).toHaveBeenCalledWith(31, expect.objectContaining({
      cron_expression: '*/5 * * * *', pelican_config: expect.objectContaining({ quality: expect.objectContaining({ bps: expect.objectContaining({
        ignore_encrypted_content: false, auto_disable_on_403: false, cache_creation_as_input: false,
        omit_unsupported_tools: true, auto_move_on_403: true, target_group_id: 0, session_proxy: true, proxy_source: 'ip_pool',
      }) }) }),
    }))
    expect(mocks.updateAccount.mock.calls[0][1].extra?.openai_excel_bps).not.toBe(true)
  })

  it('keeps an existing custom schedule and explicit false options when editing a condition', async () => {
    const rule = buildRule({ cron_expression: '13 9 * * 1-5' })
    Object.assign(rule.pelican_config.quality.bps, { ignore_encrypted_content: false, auto_disable_on_403: false, cache_creation_as_input: false })
    mocks.listByAccount.mockResolvedValue([rule])
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-probe-cron"]').element.value).toBe('13 9 * * 1-5')
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-bps-auto_disable_on_403"]').element.checked).toBe(false)
    await wrapper.get('[data-testid="quality-bps-threshold"]').setValue(4)
    await submit(wrapper)
    expect(mocks.updatePlan.mock.calls[0][1]).not.toHaveProperty('cron_expression')
    expect(mocks.updatePlan.mock.calls[0][1].pelican_config.quality.bps).toMatchObject({
      failure_threshold: 4, ignore_encrypted_content: false, auto_disable_on_403: false, cache_creation_as_input: false,
    })
  })

  it('rejects an empty custom schedule before saving the account', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get(toggleSelector).trigger('click')
    await wrapper.get('[data-testid="quality-probe-interval"]').setValue('custom')
    await wrapper.get('[data-testid="quality-probe-cron"]').setValue('')
    await submit(wrapper)
    expect(mocks.showError).toHaveBeenCalledWith('qualityOps.scheduleRequired')
    expect(mocks.updateAccount).not.toHaveBeenCalled()
    expect(mocks.createPlan).not.toHaveBeenCalled()
  })

  it('blocks saving when the BPS trigger is empty', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get(toggleSelector).trigger('click')
    await wrapper.get('[data-testid="quality-bps-threshold"]').setValue(0)
    await submit(wrapper)
    expect(mocks.showError).toHaveBeenCalledWith('qualityOps.bpsTriggerRequired')
    expect(mocks.updateAccount).not.toHaveBeenCalled()
    expect(mocks.createPlan).not.toHaveBeenCalled()
  })

  it('locks the switch and keeps the rule when it cannot be loaded', async () => {
    mocks.listByAccount.mockRejectedValue(new Error('boom'))
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get(toggleSelector).attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="account-auto-bps-load-error"]').exists()).toBe(true)
    await submit(wrapper)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(mocks.createPlan).not.toHaveBeenCalled()
    expect(mocks.updatePlan).not.toHaveBeenCalled()
  })

  it('still saves the account when the rule cannot be saved', async () => {
    mocks.createPlan.mockRejectedValue(new Error('rule failed'))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get(toggleSelector).trigger('click')
    await submit(wrapper)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(mocks.showWarning).toHaveBeenCalledWith('admin.accounts.openai.autoBPSSaveFailed', 8000)
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it.each([
    ['PAT', buildOAuthAccount({ credentials: { access_token: 'pat', auth_mode: 'personalaccesstoken' } })],
    ['Agent Identity', buildOAuthAccount({ credentials: { access_token: 'x', auth_mode: 'agentIdentity' } })],
    ['spark shadow', buildOAuthAccount({ parent_account_id: 1 })],
    ['API key', buildOAuthAccount({ type: 'apikey', credentials: { api_key: 'sk-test' } })]
  ])('hides the switch for %s accounts', async (_name, account) => {
    const wrapper = mountModal(account)
    await flushPromises()
    expect(wrapper.find('[data-testid="account-auto-bps"]').exists()).toBe(false)
    expect(mocks.listByAccount).not.toHaveBeenCalled()
    await submit(wrapper)
    expect(mocks.createPlan).not.toHaveBeenCalled()
    expect(mocks.updatePlan).not.toHaveBeenCalled()
  })
})
