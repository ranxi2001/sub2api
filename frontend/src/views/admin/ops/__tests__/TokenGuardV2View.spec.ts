
vi.mock('@/api/admin/credentialEncryption', () => ({
  getCredentialEncryption: vi.fn().mockResolvedValue({ configured: true, source: 'server_config' }),
  initializeCredentialEncryption: vi.fn(),
}))
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import TokenGuardV2View from '../TokenGuardV2View.vue'
import { getCredentialEncryption } from '@/api/admin/credentialEncryption'
import type { TokenGuardV2Rules, TokenGuardV2Status } from '@/api/admin/accountTokenGuardV2'

const api = vi.hoisted(() => ({
  listGuard: vi.fn(),
  createGuard: vi.fn(),
  updateGuard: vi.fn(),
  deleteGuard: vi.fn(),
  probeGuard: vi.fn(),
  reloginGuard: vi.fn(),
  saveRules: vi.fn(),
  saveRuntime: vi.fn(),
  updateSwitches: vi.fn(),
  resumeBudget: vi.fn(),
  listAccounts: vi.fn(),
  listGroups: vi.fn(),
  listProxies: vi.fn(),
}))

vi.mock('@/api/admin/accountTokenGuardV2', () => ({
  listTokenGuardV2Accounts: api.listGuard,
  createTokenGuardV2Account: api.createGuard,
  updateTokenGuardV2Account: api.updateGuard,
  deleteTokenGuardV2Account: api.deleteGuard,
  probeTokenGuardV2Account: api.probeGuard,
  reloginTokenGuardV2Account: api.reloginGuard,
  saveTokenGuardV2Rules: api.saveRules,
  saveTokenGuardV2Runtime: api.saveRuntime,
  updateTokenGuardV2Switches: api.updateSwitches,
  resumeTokenGuardV2Budget: api.resumeBudget,
}))
vi.mock('@/api/admin/accounts', () => ({ list: api.listAccounts, default: { list: api.listAccounts } }))
vi.mock('@/api/admin/groups', () => ({ getByPlatform: api.listGroups, default: { getByPlatform: api.listGroups } }))
vi.mock('@/api/admin/proxies', () => ({
  getAllWithCount: api.listProxies,
  default: { getAllWithCount: api.listProxies },
}))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${Object.values(params).join(' ')}` : key,
    }),
  }
})

let wrapper: VueWrapper | undefined
let restoreConfirmation: (() => void) | undefined

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getCredentialEncryption).mockResolvedValue({ configured: true, source: 'server_config' })
  api.listGuard.mockResolvedValue({
    runtime_settings: { engine: 'local_worker', worker_concurrency: 3 },
    probe_interval_seconds: 1800,
    retry_interval_seconds: 300,
    relogin_cooldown_seconds: 1800,
    fail_streak_threshold: 2,
    accounts: [{
      account_id: 42,
      account_name: 'Primary',
      account_status: 'active',
      schedulable: true,
      enabled: true,
      auto_relogin_enabled: true,
      probe_state: 'ok',
      probe_detail: 'credential accepted',
      fail_streak: 0,
      next_probe_at: '2026-09-28T10:00:00Z',
      login_config: {
        account_id: 42,
        login_email: 'user@example.com',
        credential_mode: 'password_totp',
        proxy_source: 'managed_proxy',
        proxy_id: 7,
        password_configured: true,
        totp_configured: true,
        configured: true,
      },
    }],
  })
  api.listAccounts.mockResolvedValue({ items: [
    { id: 42, name: 'Primary', group_ids: [1], credentials: { plan_type: 'plus' } },
    { id: 43, name: 'Backup', group_ids: [2], credentials: { plan_type: 'free' }, extra: { email_address: 'backup@example.com' } },
    { id: 44, name: 'Secondary', group_ids: [1], credentials: { plan_type: 'plus' } },
  ] })
  api.listGroups.mockResolvedValue([
    { id: 1, name: 'Main pool' },
    { id: 2, name: 'Backup pool' },
  ])
  api.listProxies.mockResolvedValue([
    { id: 7, name: 'Tokyo', protocol: 'socks5h', host: '203.0.113.7', port: 1080 },
    { id: 8, name: 'Singapore', protocol: 'http', host: '198.51.100.8', port: 8080 },
  ])
  api.updateGuard.mockResolvedValue({})
  api.saveRuntime.mockImplementation(async input => input)
  api.updateSwitches.mockResolvedValue({ enabled: false, auto_relogin_enabled: true })
  api.saveRules.mockResolvedValue({
    probe_interval_seconds: 1800,
    retry_interval_seconds: 300,
    relogin_cooldown_seconds: 1800,
    fail_streak_threshold: 2,
  })
})

afterEach(() => {
  restoreConfirmation?.()
  restoreConfirmation = undefined
  wrapper?.unmount()
  wrapper = undefined
})

describe('TokenGuardV2View', () => {
  it('supports both per-account login modes without echoing saved secrets', async () => {
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).not.toContain('password-secret')
    expect(wrapper.text()).not.toContain('totp-secret')
    await wrapper.findAll('button').find(button => button.text() === 'tokenGuardV2.edit')!.trigger('click')

    const modes = wrapper.findAll('input[type="radio"]').map(input => input.attributes('value'))
    expect(modes).toEqual(['password_totp', 'email_otp_url'])
    expect(wrapper.findAll('input[type="password"]').map(input => input.element.value)).toEqual(['', ''])

    expect(wrapper.text()).toContain('SOCKS5H')
    await wrapper.get('#token-guard-v2-proxy').setValue('proxy:8')

    const saveButton = wrapper.findAll('button').find(button => button.text() === 'tokenGuardV2.save')!
    expect(saveButton.attributes()).toMatchObject({ type: 'submit', form: 'token-guard-v2-editor' })
    await wrapper.get('#token-guard-v2-editor').trigger('submit')
    await flushPromises()
    expect(api.updateGuard).toHaveBeenCalledWith(42, expect.objectContaining({
      credential_mode: 'password_totp',
      proxy_source: 'managed_proxy',
      proxy_id: 8,
      password: '',
      totp_secret: '',
    }))
  })

  it('saves the managed Mihomo pool without a proxy id', async () => {
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })
    await flushPromises()

    await wrapper.findAll('button').find(button => button.text() === 'tokenGuardV2.edit')!.trigger('click')
    await wrapper.get('#token-guard-v2-proxy').setValue('mihomo')
    await wrapper.get('#token-guard-v2-editor').trigger('submit')
    await flushPromises()

    expect(api.updateGuard).toHaveBeenCalledWith(42, expect.objectContaining({
      proxy_source: 'mihomo',
      proxy_id: null,
    }))
  })

  it('fills the login email from the selected managed account', async () => {
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })
    await flushPromises()

    await wrapper.findAll('button').find(button => button.text() === 'tokenGuardV2.addAccount')!.trigger('click')
    await wrapper.get('[data-account-id="43"]').trigger('click')

    expect((wrapper.get('input[type="email"]').element as HTMLInputElement).value).toBe('backup@example.com')
  })

  it('filters available accounts by group and shows their plan labels', async () => {
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })
    await flushPromises()

    await wrapper.findAll('button').find(button => button.text() === 'tokenGuardV2.addAccount')!.trigger('click')
    expect(api.listGroups).toHaveBeenCalledWith('openai')

    const accountList = wrapper.get('.account-picker-list')
    expect(accountList.text()).toContain('Backup')
    expect(accountList.text()).toContain('Free')
    expect(accountList.text()).toContain('Secondary')
    expect(accountList.text()).toContain('Plus')
    expect(accountList.text()).not.toContain('Primary')

    await wrapper.get('[data-group-id="1"]').trigger('click')
    expect(accountList.text()).toContain('Secondary')
    expect(accountList.text()).not.toContain('Backup')
    expect(accountList.text()).not.toContain('Primary')

    await wrapper.get('[data-group-id="all"]').trigger('click')
    await wrapper.get('#token-guard-v2-account-search').setValue('backup')
    expect(accountList.text()).toContain('Backup')
    expect(accountList.text()).not.toContain('Secondary')
  })

  it('filters monitored accounts and shows the enforced probe rules', async () => {
    api.listGuard.mockResolvedValueOnce({
      probe_interval_seconds: 1800,
      retry_interval_seconds: 300,
      relogin_cooldown_seconds: 1800,
      fail_streak_threshold: 2,
      accounts: [
        {
          account_id: 42,
          account_name: 'Primary',
          account_status: 'active',
          schedulable: true,
          enabled: true,
          auto_relogin_enabled: true,
          probe_state: 'ok',
          probe_detail: 'credential accepted',
          fail_streak: 0,
          next_probe_at: '2026-09-28T10:00:00Z',
          login_config: { account_id: 42, login_email: 'user@example.com', credential_mode: 'password_totp', proxy_source: 'account', password_configured: true, totp_configured: false, configured: true },
        },
        {
          account_id: 44,
          account_name: 'Secondary',
          account_status: 'error',
          schedulable: false,
          enabled: true,
          auto_relogin_enabled: true,
          probe_state: 'auth',
          probe_detail: 'credential was rejected',
          fail_streak: 2,
          next_probe_at: '2026-09-28T10:00:00Z',
          login_config: { account_id: 44, login_email: 'secondary@example.com', credential_mode: 'email_otp_url', proxy_source: 'mihomo', password_configured: false, totp_configured: false, configured: true },
        },
      ],
    })
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })
    await flushPromises()

    await wrapper.get('[data-filter="attention"]').trigger('click')
    expect(wrapper.get('tbody').text()).toContain('Secondary')
    expect(wrapper.get('tbody').text()).not.toContain('Primary')

    await wrapper.get('[data-filter="all"]').trigger('click')
    await wrapper.get('#token-guard-v2-search').setValue('user@example.com')
    expect(wrapper.get('tbody').text()).toContain('Primary')
    expect(wrapper.get('tbody').text()).not.toContain('Secondary')

    await wrapper.get('[data-testid="guard-tab-rules"]').trigger('click')
    expect(wrapper.text()).toContain('tokenGuardV2.ruleProbeInterval')
    expect(wrapper.text()).toContain('tokenGuardV2.ruleRetryInterval')
    expect(wrapper.text()).toContain('tokenGuardV2.ruleCooldown')
  })

  it('saves configurable rules in seconds and keeps dirty values during refresh', async () => {
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })
    await flushPromises()
    await wrapper.get('[data-testid="guard-tab-rules"]').trigger('click')
    await wrapper.get('#token-guard-v2-rule-probe').setValue('15')

    api.listGuard.mockResolvedValueOnce({
      probe_interval_seconds: 3600,
      retry_interval_seconds: 600,
      relogin_cooldown_seconds: 3600,
      fail_streak_threshold: 4,
      accounts: [],
    })
    await wrapper.findAll('button').find(button => button.text() === 'tokenGuardV2.refresh')!.trigger('click')
    await flushPromises()
    expect((wrapper.get('#token-guard-v2-rule-probe').element as HTMLInputElement).value).toBe('15')

    await wrapper.get('#token-guard-v2-rule-retry').setValue('2')
    await wrapper.get('#token-guard-v2-rule-threshold').setValue('3')
    await wrapper.get('#token-guard-v2-rule-cooldown').setValue('120')
    api.saveRules.mockResolvedValueOnce({
      probe_interval_seconds: 900,
      retry_interval_seconds: 120,
      relogin_cooldown_seconds: 7200,
      fail_streak_threshold: 3,
    })
    await wrapper.get('#token-guard-v2-rules').trigger('submit')
    await flushPromises()

    expect(api.saveRules).toHaveBeenCalledWith({
      relogin_backoff_max_seconds: 7200,
      relogin_max_failures: 3,
      probe_interval_seconds: 900,
      retry_interval_seconds: 120,
      relogin_cooldown_seconds: 7200,
      fail_streak_threshold: 3,
    })
    expect(wrapper.text()).toContain('tokenGuardV2.rulesSaved')
  })

  it('paginates monitored accounts and resets to page one after searching', async () => {
    api.listGuard.mockResolvedValueOnce({
      probe_interval_seconds: 1800,
      retry_interval_seconds: 300,
      relogin_cooldown_seconds: 1800,
      fail_streak_threshold: 2,
      accounts: Array.from({ length: 25 }, (_, index) => ({
        account_id: index + 1,
        account_name: `Account ${index + 1}`,
        account_status: 'active',
        schedulable: true,
        enabled: true,
        auto_relogin_enabled: false,
        probe_state: 'ok',
        probe_detail: 'credential accepted',
        fail_streak: 0,
        next_probe_at: '2026-09-28T10:00:00Z',
        login_config: { account_id: index + 1, login_email: `user${index + 1}@example.com`, credential_mode: 'password_totp', proxy_source: 'account', password_configured: true, totp_configured: false, configured: true },
      })),
    })
    wrapper = mount(TokenGuardV2View, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          SmartOpsNav: true,
          Icon: true,
          BaseDialog: true,
          Pagination: {
            props: ['page'],
            emits: ['update:page'],
            template: '<button data-testid="next-page" @click="$emit(\'update:page\', page + 1)">next</button>',
          },
        },
      },
    })
    await flushPromises()

    expect(wrapper.findAll('tbody tr')).toHaveLength(20)
    await wrapper.get('[data-testid="next-page"]').trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(5)
    expect(wrapper.get('tbody').text()).toContain('Account 25')

    await wrapper.get('#token-guard-v2-search').setValue('Account 2')
    expect(wrapper.get('tbody').text()).toContain('Account 2')
    expect(wrapper.findAll('tbody tr')).toHaveLength(7)
  })
})

describe('managed re-login availability', () => {
  it('requires confirmation to restore an exhausted budget without creating a login', async () => {
    const data = await api.listGuard()
    const exhausted = { ...data.accounts[0], relogin_fail_streak: 3, auto_relogin_exhausted: true,
      cooldown_until: '2026-10-24T16:00:00Z' }
    api.listGuard.mockResolvedValue({ ...data, relogin_max_failures: 3, accounts: [exhausted] })
    api.resumeBudget.mockResolvedValue({ ...exhausted, relogin_fail_streak: 0, auto_relogin_exhausted: false })
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    restoreConfirmation = () => confirm.mockRestore()
    wrapper = mount(TokenGuardV2View, { global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' }, SmartOpsNav: true, Pagination: true, OpenAITOTPDialog: true
    } } })
    await flushPromises()
    expect(wrapper.text()).toContain('tokenGuardV2.reloginBudgetExhausted')
    await wrapper.get('[data-testid="resume-budget-42"]').trigger('click')
    expect(api.resumeBudget).not.toHaveBeenCalled()
    confirm.mockReturnValue(true)
    await wrapper.get('[data-testid="resume-budget-42"]').trigger('click')
    await flushPromises()
    expect(api.resumeBudget).toHaveBeenCalledWith(42)
    expect(api.reloginGuard).not.toHaveBeenCalled()
    expect(api.updateSwitches).not.toHaveBeenCalled()
  })
  it('shows the safe protocol phase without presenting failed as successful', async () => {
    const data = await api.listGuard()
    api.listGuard.mockResolvedValue({ ...data, accounts: [{
      ...data.accounts[0], latest_task: { id: 7, account_id: 42, status: 'failed', stage: 'failed',
        error: 'synthetic-private', attempt: 1,
        diagnostic: { stage: 'token_exchange', reason: 'token_exchange_failed', elapsed_ms: 120, exit_code: 1 } }
    }] })
    wrapper = mount(TokenGuardV2View, { global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' }, SmartOpsNav: true, Pagination: true, OpenAITOTPDialog: true
    } } })
    await flushPromises()
    expect(wrapper.text()).toContain('failed: token_exchange · token_exchange_failed · 120 ms')
    expect(wrapper.text()).not.toContain('synthetic-private')
  })
  it('shows a Session Studio HTTP rejection as a failed client phase', async () => {
    const data = await api.listGuard()
    api.listGuard.mockResolvedValue({ ...data, accounts: [{
      ...data.accounts[0], latest_task: { id: 7, account_id: 42, status: 'failed', stage: 'failed',
        error: 'synthetic-private', attempt: 1,
        diagnostic: { engine: 'session_studio', stage: 'http_request', reason: 'http_rejected', elapsed_ms: 120, http_status: 403 } }
    }] })
    wrapper = mount(TokenGuardV2View, { global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' }, SmartOpsNav: true, Pagination: true, OpenAITOTPDialog: true
    } } })
    await flushPromises()
    expect(wrapper.text()).toContain('failed: http_request · http_rejected · 120 ms · HTTP 403')
    expect(wrapper.text()).not.toContain('synthetic-private')
  })
  it('separates encrypted credential readiness from a failed runtime', async () => {
    const data = await api.listGuard()
    api.listGuard.mockResolvedValue({
      ...data, worker: { mode: 'managed', state: 'unavailable', reason: 'runtime_install_failed' }
    })
    wrapper = mount(TokenGuardV2View, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true, SmartOpsNav: true } }
    })
    await flushPromises()
    const runtime = wrapper.get('[data-testid="reauth-runtime-status"]')
    expect(runtime.text()).toContain('tokenGuardV2.runtimeStates.unavailable')
    expect(runtime.text()).toContain('tokenGuardV2.runtimeReasons.runtime_install_failed')
    expect(runtime.text()).toContain('tokenGuardV2.runtimeManaged')
  })
})

async function mountControls() {
  wrapper = mount(TokenGuardV2View, {
    global: { stubs: { AppLayout: { template: '<main><slot /></main>' }, SmartOpsNav: true, Icon: true,
      BaseDialog: { props: ['show'], template: '<section v-if="show"><slot /><footer><slot name="footer" /></footer></section>' } } },
  })
  await flushPromises()
  return wrapper
}

describe('manual operation conflicts', () => {
  it.each(['OPENAI_REAUTH_TASK_ALREADY_ACTIVE', 'OPENAI_REAUTH_TASK_ACTIVE'])(
    'explains an occupied login task for %s without exposing raw errors', async reason => {
      const view = await mountControls()
      api.reloginGuard.mockRejectedValueOnce({ status: 409, reason, message: 'synthetic-private' })

      await view.findAll('button').find(button => button.text() === 'tokenGuardV2.reloginNow')!.trigger('click')
      await flushPromises()

      expect(api.reloginGuard).toHaveBeenCalledWith(42)
      expect(view.text()).toContain(`tokenGuardV2.operationFailure.manual.taskBusy (${reason})`)
      expect(view.text()).not.toContain('tokenGuardV2.reloginQueued')
      expect(view.text()).not.toContain('synthetic-private')
    }
  )

  it('keeps an inspection lease conflict separate from login and 2FA tasks', async () => {
    const view = await mountControls()
    api.probeGuard.mockRejectedValueOnce({ status: 409, reason: 'TOKEN_GUARD_V2_PROBE_BUSY' })
    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.inspectNow')!.trigger('click')
    await flushPromises()
    expect(view.text()).toContain('tokenGuardV2.operationFailure.manual.busy (TOKEN_GUARD_V2_PROBE_BUSY)')
    expect(view.text()).not.toContain('tokenGuardV2.operationFailure.manual.taskBusy')
  })

  it.each([
    ['TOTP_ACCOUNT_BUSY', 'totpBusy'],
    ['OPENAI_REAUTH_CONFIG_CHANGED', 'changed']
  ])('explains why login configuration cannot be saved for %s', async (reason, category) => {
    const view = await mountControls()
    api.updateGuard.mockRejectedValueOnce({ status: 409, reason, message: 'synthetic-private' })
    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.edit')!.trigger('click')
    await view.get('#token-guard-v2-editor').trigger('submit')
    await flushPromises()
    expect(api.updateGuard).toHaveBeenCalledWith(42, expect.any(Object))
    expect(view.text()).toContain(`tokenGuardV2.operationFailure.manual.${category} (${reason})`)
    expect(view.text()).not.toContain('synthetic-private')
  })

  it('explains a budget recovery conflict without starting another login', async () => {
    const data = await api.listGuard()
    api.listGuard.mockResolvedValue({ ...data, accounts: [{ ...data.accounts[0], auto_relogin_exhausted: true }] })
    api.resumeBudget.mockRejectedValueOnce({ status: 409, reason: 'TOKEN_GUARD_V2_BUDGET_BUSY' })
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    restoreConfirmation = () => confirm.mockRestore()
    const view = await mountControls()
    await view.get('[data-testid="resume-budget-42"]').trigger('click')
    await flushPromises()
    expect(api.resumeBudget).toHaveBeenCalledWith(42)
    expect(view.text()).toContain('tokenGuardV2.operationFailure.manual.budgetBusy (TOKEN_GUARD_V2_BUDGET_BUSY)')
    expect(api.reloginGuard).not.toHaveBeenCalled()
  })

  it.each(['UNKNOWN_PRIVATE_REASON', '__proto__', 'toString', undefined])(
    'does not display unrecognized reason %s or raw response data', async reason => {
      const view = await mountControls()
      api.reloginGuard.mockRejectedValueOnce({ status: 409, reason, message: 'synthetic-private', metadata: { token: 'synthetic-token' } })
      await view.findAll('button').find(button => button.text() === 'tokenGuardV2.reloginNow')!.trigger('click')
      await flushPromises()
      expect(view.text()).toContain('tokenGuardV2.error')
      if (reason) expect(view.text()).not.toContain(reason)
      expect(view.text()).not.toContain('synthetic-private')
      expect(view.text()).not.toContain('synthetic-token')
    }
  )
})

it('loads the configured encryption fixture before allowing account editing', async () => {
  const view = await mountControls()
  const add = view.findAll('button').find(button => button.text() === 'tokenGuardV2.addAccount')!
  expect(add.attributes('disabled')).toBeUndefined()
  expect(getCredentialEncryption).toHaveBeenCalled()
})

it('still blocks account creation when encryption is actually unconfigured', async () => {
  vi.mocked(getCredentialEncryption).mockResolvedValueOnce({ configured: false, source: 'unconfigured' })
  const view = await mountControls()
  const add = view.findAll('button').find(button => button.text() === 'tokenGuardV2.addAccount')!
  expect(add.attributes('disabled')).toBeDefined()
  await add.trigger('click')
  expect(view.find('#token-guard-v2-editor').exists()).toBe(false)
  expect(api.createGuard).not.toHaveBeenCalled()
})

it('moves engine and worker count into encryption settings and saves immediately', async () => {
  const view = await mountControls()
  const controls = view.get('[data-testid="credential-encryption-setup"]')
  await controls.get('#token-guard-v2-engine').setValue('session_studio'); await flushPromises()
  expect(api.saveRuntime).toHaveBeenLastCalledWith({ engine: 'session_studio', worker_concurrency: 3 })
  expect(controls.text()).toContain('tokenGuardV2.sessionStudioGlobalHint')
  await controls.get('#token-guard-v2-concurrency').setValue('6'); await flushPromises()
  expect(api.saveRuntime).toHaveBeenLastCalledWith({ engine: 'session_studio', worker_concurrency: 6 })
  expect(api.updateGuard).not.toHaveBeenCalled()
})

it('keeps runtime switches out of account forms and preserves credentials', async () => {
  const view = await mountControls()
  await view.findAll('button').find(button => button.text() === 'tokenGuardV2.edit')!.trigger('click')
  const form = view.get('#token-guard-v2-editor')
  expect(form.find('#token-guard-v2-engine').exists()).toBe(false)
  expect(form.find('#token-guard-v2-concurrency').exists()).toBe(false)
  expect(form.find('input[role="switch"]').exists()).toBe(false)
  expect(form.text()).not.toContain('tokenGuardV2.autoInspect')
  expect(form.text()).not.toContain('tokenGuardV2.autoRelogin')
  await form.trigger('submit'); await flushPromises()
  const payload = api.updateGuard.mock.calls[0][1]
  expect(payload).not.toHaveProperty('engine')
  expect(payload).not.toHaveProperty('enabled')
  expect(payload).not.toHaveProperty('auto_relogin_enabled')
  expect(payload.password).toBe('')
})

it('updates only the selected automation flag without resubmitting credentials', async () => {
  const view = await mountControls()
  await view.get('[data-testid="auto-inspect-42"]').setValue(false); await flushPromises()
  expect(api.updateSwitches).toHaveBeenCalledWith(42, { enabled: false })
  expect(api.updateGuard).not.toHaveBeenCalled()
  expect(view.find('#token-guard-v2-editor').exists()).toBe(false)
  expect((view.get('[data-testid="auto-inspect-42"]').element as HTMLInputElement).checked).toBe(false)
  api.updateSwitches.mockResolvedValueOnce({ enabled: false, auto_relogin_enabled: false })
  await view.get('[data-testid="auto-relogin-42"]').setValue(false); await flushPromises()
  expect(api.updateSwitches).toHaveBeenLastCalledWith(42, { auto_relogin_enabled: false })
})

it('rolls controls back when saving fails', async () => {
  const view = await mountControls()
  api.saveRuntime.mockRejectedValueOnce(new Error('save failed'))
  await view.get('#token-guard-v2-engine').setValue('session_studio'); await flushPromises()
  expect((view.get('#token-guard-v2-engine').element as HTMLSelectElement).value).toBe('local_worker')
  api.updateSwitches.mockRejectedValueOnce(new Error('save failed'))
  await view.get('[data-testid="auto-inspect-42"]').setValue(false); await flushPromises()
  expect((view.get('[data-testid="auto-inspect-42"]').element as HTMLInputElement).checked).toBe(true)
  expect(view.text()).toContain('tokenGuardV2.error')
  expect(view.text()).not.toContain('save failed')
})

it('preserves legacy engine choices when only concurrency changes', async () => {
  const data = await api.listGuard(); data.runtime_settings.engine = ''
  api.listGuard.mockResolvedValue(data)
  const view = await mountControls()
  expect(view.text()).toContain('tokenGuardV2.legacyEngineHint')
  await view.get('#token-guard-v2-concurrency').setValue('4'); await flushPromises()
  expect(api.saveRuntime).toHaveBeenCalledWith({ engine: '', worker_concurrency: 4 })
})

it('uses local proxies for email OTP under the remote global engine', async () => {
  const data = await api.listGuard(); data.runtime_settings.engine = 'session_studio'
  api.listGuard.mockResolvedValue(data)
  const view = await mountControls()
  await view.findAll('button').find(button => button.text() === 'tokenGuardV2.edit')!.trigger('click')
  expect(view.get('#token-guard-v2-proxy').attributes('disabled')).toBeDefined()
  await view.get('input[value="email_otp_url"]').setValue()
  expect(view.get('#token-guard-v2-proxy').attributes('disabled')).toBeUndefined()
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(complete => { resolve = complete })
  return { promise, resolve }
}

describe('inspection recovery progress', () => {
  const queuedTask = {
    id: 12, account_id: 42, status: 'queued', stage: 'queued', attempt: 0,
    created_at: '2026-11-08T10:00:00Z', updated_at: '2026-11-08T10:00:00Z'
  }

  it('reads the completed inspection immediately without waiting for the polling interval', async () => {
    const data = await api.listGuard()
    const failed = { ...data.accounts[0], probe_state: 'auth', fail_streak: 1 }
    api.listGuard.mockResolvedValue({ ...data, accounts: [failed] })
    const view = await mountControls()
    const healthy = { ...failed, probe_state: 'ok', fail_streak: 0, last_probe_at: '2026-11-08T10:00:00Z' }
    api.probeGuard.mockResolvedValue(healthy)
    api.listGuard.mockResolvedValue({ ...data, accounts: [healthy] })
    const callsBeforeAction = api.listGuard.mock.calls.length

    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.inspectNow')!.trigger('click')
    await flushPromises()

    expect(api.probeGuard).toHaveBeenCalledWith(42)
    expect(api.listGuard).toHaveBeenCalledTimes(callsBeforeAction + 1)
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.healthy')
    expect(view.get('tbody').text()).toContain('tokenGuardV2.probeStates.ok')
  })

  it('keeps duplicate actions blocked until the post-action read finishes', async () => {
    const data = await api.listGuard()
    const view = await mountControls()
    const fresh = deferred<TokenGuardV2Status>()
    api.reloginGuard.mockResolvedValue(queuedTask)
    api.listGuard.mockReturnValueOnce(fresh.promise)
    const button = view.findAll('button').find(item => item.text() === 'tokenGuardV2.reloginNow')!

    await button.trigger('click')
    await flushPromises()
    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
    expect(api.reloginGuard).toHaveBeenCalledTimes(1)

    fresh.resolve({ ...data, accounts: [{ ...data.accounts[0], latest_task: queuedTask }] })
    await flushPromises()
    expect(button.attributes('disabled')).toBeUndefined()
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.queued')
  })

  it('does not let a rule submission invalidate the post-action account read', async () => {
    const data = await api.listGuard()
    const view = await mountControls()
    await view.get('[data-testid="guard-tab-rules"]').trigger('click')
    await view.get('#token-guard-v2-rule-threshold').setValue('3')
    await view.get('[data-testid="guard-tab-accounts"]').trigger('click')
    const fresh = deferred<TokenGuardV2Status>()
    api.reloginGuard.mockResolvedValue(queuedTask)
    api.listGuard.mockReturnValueOnce(fresh.promise)

    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.reloginNow')!.trigger('click')
    await flushPromises()
    await view.get('[data-testid="guard-tab-rules"]').trigger('click')
    const rulesForm = view.get('#token-guard-v2-rules')
    expect(rulesForm.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    // Programmatic form submission must honor the same exclusion as the button.
    await rulesForm.trigger('submit')
    await flushPromises()
    expect(api.saveRules).not.toHaveBeenCalled()

    fresh.resolve({ ...data, accounts: [{ ...data.accounts[0], latest_task: queuedTask }] })
    await flushPromises()
    expect(rulesForm.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    await view.get('[data-testid="guard-tab-accounts"]').trigger('click')
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.queued')
  })

  it('does not start an account action while a rule save is pending', async () => {
    const data = await api.listGuard()
    const view = await mountControls()
    await view.get('[data-testid="guard-tab-rules"]').trigger('click')
    await view.get('#token-guard-v2-rule-threshold').setValue('3')
    const saved = deferred<TokenGuardV2Rules>()
    api.saveRules.mockReturnValueOnce(saved.promise)
    await view.get('#token-guard-v2-rules').trigger('submit')
    await flushPromises()
    await view.get('[data-testid="guard-tab-accounts"]').trigger('click')
    const action = view.findAll('button').find(button => button.text() === 'tokenGuardV2.reloginNow')!
    expect(action.attributes('disabled')).toBeDefined()
    // Dispatch directly to exercise the function guard as well as disabled UI.
    action.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    expect(api.reloginGuard).not.toHaveBeenCalled()
    expect(api.probeGuard).not.toHaveBeenCalled()

    saved.resolve({ ...data, fail_streak_threshold: 3 })
    await flushPromises()
    expect(action.attributes('disabled')).toBeUndefined()
    expect(view.text()).toContain('tokenGuardV2.rulesSaved')
  })

  it('ignores an older refresh response after the action read has applied', async () => {
    const data = await api.listGuard()
    const view = await mountControls()
    const old = deferred<TokenGuardV2Status>()
    api.listGuard.mockReturnValueOnce(old.promise)
    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.refresh')!.trigger('click')
    api.reloginGuard.mockResolvedValue(queuedTask)
    api.listGuard.mockResolvedValueOnce({ ...data, accounts: [{ ...data.accounts[0], latest_task: queuedTask }] })

    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.reloginNow')!.trigger('click')
    await flushPromises()
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.queued')

    old.resolve(data)
    await flushPromises()
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.queued')
    expect(view.text()).toContain('tokenGuardV2.reloginQueued')
  })

  it('keeps historical authentication failure inside details while recovery is queued', async () => {
    const data = await api.listGuard()
    const account = {
      ...data.accounts[0], probe_state: 'auth', fail_streak: 3, latest_task: queuedTask,
      probe_failure: { code: 'requires_relogin', category: 'requires_relogin', stage: 'inspection', action: 'reauthorize' }
    }
    api.listGuard.mockResolvedValue({ ...data, fail_streak_threshold: 3, accounts: [account] })
    api.probeGuard.mockResolvedValue(account)
    const view = await mountControls()

    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.inspectNow')!.trigger('click')
    await flushPromises()

    expect(view.find('[role="alert"]').exists()).toBe(false)
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.queued')
    const history = view.get('[data-testid="probe-history-42"]')
    expect(history.attributes('open')).toBeUndefined()
    expect(history.text()).toContain('tokenGuardV2.operationFailure.reasons.requires_relogin')
    expect(history.find('[data-testid="credential-operation-failure"]').exists()).toBe(true)
  })

  it.each([
    { label: 'paused', account: { enabled: false } },
    { label: 'disabled', account: { auto_relogin_enabled: false } },
    { label: 'configuration', account: { login_config: undefined } },
    { label: 'waiting', account: { probe_state: 'auth', fail_streak: 2 }, text: 'tokenGuardV2.automationStates.waiting 2 3' },
    { label: 'scheduled', account: { probe_state: 'auth', fail_streak: 3 } },
    { label: 'queued', account: { latest_task: queuedTask } },
    { label: 'running', account: { latest_task: { ...queuedTask, status: 'callback_processing' } } },
    { label: 'cooldown', account: { cooldown_until: '2099-11-08T10:00:00Z', relogin_fail_streak: 0 } },
    { label: 'scheduled', account: {
      probe_state: 'auth', fail_streak: 3, cooldown_until: '2000-01-01T00:00:00Z',
      blocked_reason: 'automatic re-login cooling down'
    } },
    { label: 'blocked', account: {
      probe_state: 'auth', fail_streak: 3, cooldown_until: '2000-01-01T00:00:00Z',
      blocked_reason: 'automatic re-login could not be queued'
    } },
    { label: 'blocked', account: { blocked_reason: 'automatic re-login could not be queued' } },
    { label: 'exhausted', account: { auto_relogin_exhausted: true }, text: 'tokenGuardV2.reloginBudgetExhausted' },
    { label: 'verifying', account: {
      probe_state: 'auth', last_probe_at: '2026-11-08T09:59:00Z',
      latest_task: { ...queuedTask, status: 'succeeded', finished_at: '2026-11-08T10:00:00Z' }
    } }
  ])('shows $label as the current recovery state', async ({ label, account, text }) => {
    const data = await api.listGuard()
    api.listGuard.mockResolvedValue({ ...data, fail_streak_threshold: 3, accounts: [{ ...data.accounts[0], ...account }] })
    const view = await mountControls()

    expect(view.get('[data-testid="automation-state-42"]').text()).toContain(text || `tokenGuardV2.automationStates.${label}`)
  })

  it('ends credential verification only after a later inspection', async () => {
    const data = await api.listGuard()
    const account = {
      ...data.accounts[0], last_probe_at: '2026-11-08T09:59:00Z',
      latest_task: { ...queuedTask, status: 'succeeded', finished_at: '2026-11-08T10:00:00Z' }
    }
    api.listGuard.mockResolvedValue({ ...data, accounts: [account] })
    const view = await mountControls()
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.verifying')
    api.listGuard.mockResolvedValue({ ...data, accounts: [{ ...account, last_probe_at: '2026-11-08T10:01:00Z' }] })

    await view.findAll('button').find(button => button.text() === 'tokenGuardV2.refresh')!.trigger('click')
    await flushPromises()
    expect(view.get('[data-testid="automation-state-42"]').text()).toBe('tokenGuardV2.automationStates.healthy')
  })
})
