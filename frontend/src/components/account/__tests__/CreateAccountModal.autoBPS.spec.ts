
vi.mock('@/api/admin/credentialEncryption', () => ({
  getCredentialEncryption: vi.fn().mockResolvedValue({ configured: true, source: 'server_config' }),
  initializeCredentialEncryption: vi.fn(),
}))
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

enableAutoUnmount(afterEach)

const mocks = vi.hoisted(() => ({
  createAccount: vi.fn(),
  refreshOpenAIToken: vi.fn(),
  importCodexSession: vi.fn(),
  createOpenAICodexPAT: vi.fn(),
  createPlan: vi.fn(),
  createCredentialOperations: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  showInfo: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn(), showWarning: mocks.showWarning, showInfo: mocks.showInfo })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
      create: mocks.createAccount,
      refreshOpenAIToken: mocks.refreshOpenAIToken,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      importCodexSession: mocks.importCodexSession,
      createOpenAICodexPAT: mocks.createOpenAICodexPAT
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) },
    scheduledTests: { create: mocks.createPlan }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([])
}))

vi.mock('@/api/admin/accountTokenGuardV2', () => ({
  createTokenGuardV2Account: mocks.createCredentialOperations,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import CreateAccountModal from '../CreateAccountModal.vue'
import OpenAITwoFAImport from '../OpenAITwoFAImport.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  data: () => ({ inputMethod: 'manual' }),
  emits: ['import-codex-session', 'import-codex-pat', 'validate-refresh-token'],
  template: '<div data-testid="oauth-flow" />'
})

function mountModal() {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: {
      stubs: {
        NewFeatureBadge: true,
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true
      }
    }
  })
}

type Wrapper = ReturnType<typeof mountModal>

async function selectButtonByText(wrapper: Wrapper, text: string) {
  const button = wrapper.findAll('button').find(candidate => candidate.text().includes(text))
  expect(button).toBeDefined()
  await button?.trigger('click')
  await flushPromises()
}

const toggleSelector = '[data-testid="account-auto-bps-toggle"]'

// 选 OpenAI OAuth，按需打开「降智后自动开启 BPS」，进入第 2 步。
async function openOAuthStep({ autoBPS = true, twoFA = false, interval = '', cacheCreationAsInput = true } = {}) {
  const wrapper = mountModal()
  await selectButtonByText(wrapper, 'OpenAI')
  if (twoFA) await wrapper.get('[data-testid="openai-two-fa"]').trigger('click')
  else await wrapper.get('form#create-account-form input[type="text"]').setValue('Codex')
  if (autoBPS) await wrapper.get(toggleSelector).trigger('click')
  if (interval) await wrapper.get('[data-testid="quality-probe-interval"]').setValue(interval)
  if (autoBPS) await wrapper.get('[data-testid="quality-bps-cache_creation_as_input"]').setValue(cacheCreationAsInput)
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  await flushPromises()
  return wrapper
}

function importResult(items: { action: string; account_id?: number }[]) {
  return {
    created: items.filter(item => item.action === 'created').length,
    updated: items.filter(item => item.action === 'updated').length,
    skipped: 0, failed: 0, errors: [], warnings: [], items
  }
}

describe('CreateAccountModal auto BPS switch', () => {
  beforeEach(() => {
    Object.values(mocks).forEach(mock => mock.mockReset())
    mocks.createAccount.mockResolvedValueOnce({ id: 81 }).mockResolvedValueOnce({ id: 82 })
    mocks.refreshOpenAIToken.mockResolvedValue({ access_token: 'at', refresh_token: 'rt', expires_in: 3600, email: 'user@example.com' })
    mocks.createPlan.mockResolvedValue({ id: 1 })
    mocks.createOpenAICodexPAT.mockResolvedValue({})
  })

  it('is offered for OpenAI OAuth but not for API Key', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe('false')
    expect(wrapper.find('[data-testid="quality-bps-settings"]').exists()).toBe(false)
    await wrapper.get(toggleSelector).trigger('click')
    expect(wrapper.find('[data-testid="quality-bps-settings"]').exists()).toBe(true)
    await selectButtonByText(wrapper, 'API Key')
    expect(wrapper.find('[data-testid="account-auto-bps"]').exists()).toBe(false)
  })

  it('creates one rule per account added from refresh tokens', async () => {
    const wrapper = await openOAuthStep()
    wrapper.getComponent(OAuthAuthorizationFlowStub).vm.$emit('validate-refresh-token', ['rt-1', 'rt-2'].join('\n'))
    await flushPromises()
    expect(mocks.createAccount).toHaveBeenCalledTimes(2)
    expect(mocks.createPlan.mock.calls.map(call => call[0].account_id)).toEqual([81, 82])
    expect(mocks.createPlan.mock.calls[0][0]).toMatchObject({ model_id: 'gpt-6-astra', enabled: true, cron_expression: '*/2 * * * *',
      pelican_config: { question_kind: 'state_probe', quality: { action: 'enable_bps', auto_restore: true, bps: {
        omit_unsupported_tools: false, ignore_encrypted_content: true, auto_disable_on_403: true,
        auto_recover_on_403: false, auto_move_on_403: false, session_proxy: false, cache_creation_as_input: true,
      } } } })
    expect(wrapper.emitted('created')).toHaveLength(1)
  })

  it('applies the configured interval and BPS options to each newly created account', async () => {
    const wrapper = await openOAuthStep({ interval: '*/10 * * * *', cacheCreationAsInput: false })
    wrapper.getComponent(OAuthAuthorizationFlowStub).vm.$emit('validate-refresh-token', ['rt-1', 'rt-2'].join('\n'))
    await flushPromises()
    expect(mocks.createPlan).toHaveBeenCalledTimes(2)
    for (const [request] of mocks.createPlan.mock.calls) expect(request).toMatchObject({
      cron_expression: '*/10 * * * *', pelican_config: { quality: { bps: { cache_creation_as_input: false, auto_disable_on_403: true } } },
    })
  })

  it('does not create rules while the switch is off', async () => {
    const wrapper = await openOAuthStep({ autoBPS: false })
    wrapper.getComponent(OAuthAuthorizationFlowStub).vm.$emit('validate-refresh-token', 'rt-1')
    await flushPromises()
    expect(mocks.createAccount).toHaveBeenCalledTimes(1)
    expect(mocks.createPlan).not.toHaveBeenCalled()
  })

  it('only covers newly created accounts from a session import', async () => {
    mocks.importCodexSession.mockResolvedValue(importResult([{ action: 'created', account_id: 91 }, { action: 'updated', account_id: 92 }]))
    const wrapper = await openOAuthStep()
    wrapper.getComponent(OAuthAuthorizationFlowStub).vm.$emit('import-codex-session', JSON.stringify({ tokens: { access_token: 'at' } }))
    await flushPromises()
    expect(mocks.importCodexSession).toHaveBeenCalledTimes(1)
    expect(mocks.createPlan).toHaveBeenCalledTimes(1)
    expect(mocks.createPlan.mock.calls[0][0].account_id).toBe(91)
  })

  it('creates a rule for a 2FA account', async () => {
    mocks.importCodexSession.mockResolvedValue(importResult([{ action: 'created', account_id: 93 }]))
    const wrapper = await openOAuthStep({ twoFA: true })
    const importer = wrapper.getComponent(OpenAITwoFAImport)
    const login = { email: 'user@example.com', password: 'test-password', mfa_secret: 'test-secret' }
    await expect(importer.props('importCredential')({ access_token: 'at', refresh_token: 'rt', account_id: 'ws' }, login.email, login)).resolves.toBe('created')
    expect(mocks.createPlan).toHaveBeenCalledTimes(1)
    expect(mocks.createPlan.mock.calls[0][0].account_id).toBe(93)
  })

  it('skips Codex PAT and Agent Identity accounts with a notice', async () => {
    const pat = await openOAuthStep()
    pat.getComponent(OAuthAuthorizationFlowStub).vm.$emit('import-codex-pat', 'pat-token')
    await flushPromises()
    expect(mocks.createOpenAICodexPAT).toHaveBeenCalledTimes(1)
    expect(mocks.showInfo).toHaveBeenCalledWith('admin.accounts.openai.autoBPSUnsupportedSkipped', 6000)
    pat.unmount()

    mocks.importCodexSession.mockResolvedValue(importResult([{ action: 'created', account_id: 94 }]))
    const agent = await openOAuthStep()
    const flow = agent.getComponent(OAuthAuthorizationFlowStub)
    ;(flow.vm as unknown as { inputMethod: string }).inputMethod = 'agent_identity'
    flow.vm.$emit('import-codex-session', JSON.stringify({ auth_mode: 'agentIdentity', agent_identity: { agent_runtime_id: 'runtime' } }))
    await flushPromises()
    expect(mocks.importCodexSession).toHaveBeenCalledTimes(1)
    expect(mocks.showInfo).toHaveBeenCalledTimes(2)
    expect(mocks.createPlan).not.toHaveBeenCalled()
  })

  it('stops before step 2 when the BPS trigger is empty', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Codex')
    await wrapper.get(toggleSelector).trigger('click')
    await wrapper.get('[data-testid="quality-bps-threshold"]').setValue(0)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('qualityOps.bpsTriggerRequired')
    expect(wrapper.findComponent(OAuthAuthorizationFlowStub).exists()).toBe(false)
  })

  it('warns but keeps the new account when the rule cannot be created', async () => {
    mocks.createPlan.mockRejectedValue(new Error('rule failed'))
    const wrapper = await openOAuthStep()
    wrapper.getComponent(OAuthAuthorizationFlowStub).vm.$emit('validate-refresh-token', 'rt-1')
    await flushPromises()
    expect(mocks.createAccount).toHaveBeenCalledTimes(1)
    expect(mocks.showWarning).toHaveBeenCalledWith('admin.accounts.openai.autoBPSCreateFailed', 8000)
    expect(wrapper.emitted('created')).toHaveLength(1)
  })
})
