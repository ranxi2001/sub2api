import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { Account } from '@/types'
import { defaultExcelBPSDefaults } from '@/utils/excelBPSDefaults'
import EditAccountModal from '../EditAccountModal.vue'

const { update, checkRisk, showError } = vi.hoisted(() => ({
  update: vi.fn(), checkRisk: vi.fn(), showError: vi.fn()
}))
vi.mock('@/api/admin/autoConfig', () => ({ getAutoConfig: vi.fn(async () => ({ excel_bps: defaultExcelBPSDefaults() })) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41, role: 'admin' }, isSimpleMode: true }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showWarning: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update, checkMixedChannelRisk: checkRisk,
      getManagementCapabilities: vi.fn(async () => ({ web_search_enabled: false, account_quota_notify_enabled: false }))
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn(async () => ({ enabled: false, providers: [] })),
      getSettings: vi.fn(async () => ({}))
    },
    tlsFingerprintProfiles: { list: vi.fn(async () => []) }
  }
}))

enableAutoUnmount(afterEach)
beforeEach(() => {
  setActivePinia(createPinia())
  update.mockReset().mockResolvedValue({ id: 1 })
  checkRisk.mockReset().mockResolvedValue({ has_risk: false })
  showError.mockReset()
})

function fixture(platform = 'openai', type = 'apikey'): Account {
  return {
    id: 1, name: 'Synthetic account', notes: '', platform, type,
    credentials: { base_url: 'https://source.example.invalid' },
    credentials_status: { has_api_key: true }, extra: {}, proxy_id: null,
    concurrency: 1, priority: 1, rate_multiplier: 1, status: 'active',
    group_ids: [], expires_at: null, auto_pause_on_expired: false
  } as Account
}

function mountEditor(account: Account) {
  return mount(EditAccountModal, {
    props: { show: true, account, groups: [], proxies: [] },
    global: {
      stubs: {
        BaseDialog: defineComponent({ props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }),
        teleport: true, Icon: true, ProxySelector: true, GroupSelector: true,
        ModelWhitelistSelector: true
      }
    }
  })
}

type Editor = ReturnType<typeof mountEditor>
const keyInput = (wrapper: Editor) => wrapper.get<HTMLInputElement>('input[autocomplete="new-password"]')
const baseInput = (wrapper: Editor) => wrapper.get<HTMLInputElement>('input[placeholder^="https://api."], input[placeholder="https://generativelanguage.googleapis.com"]')
async function choosePlatform(wrapper: Editor, label: string) {
  // Exercise the production Select and its rendered options.
  await wrapper.get('#edit-account-platform').trigger('click')
  const option = wrapper.findAll('[role="option"]').find(option => option.text() === label)
  expect(option, `rendered platform option ${label}`).toBeDefined()
  await option!.trigger('click')
  await flushPromises()
}
async function submit(wrapper: Editor) {
  await wrapper.get('#edit-account-form').trigger('submit.prevent')
  await flushPromises()
  expect(showError).not.toHaveBeenCalled()
}

describe('PR899 platform credential regressions with the real Select', () => {
  it.each(['apikey', ' apikey ', 'APIKEY', 'oauth', '', null, false, 1, {}, []])('keeps unsupported Vertex mode %j on its source platform', async mode => {
    const account = fixture('gemini', 'service_account')
    account.credentials = { auth_mode: mode, location: 'global', tier_id: 'vertex' }
    const wrapper = mountEditor(account)
    await flushPromises()
    expect(wrapper.get('#edit-account-platform').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#edit-account-platform').text()).toContain('Gemini')
    expect(wrapper.text()).toContain('admin.accounts.platformSwitch.boundCredentials')
  })

  it.each(['gemini', 'anthropic'])('keeps Service Account JSON portable from %s', async source => {
    const account = fixture(source, 'service_account')
    account.credentials = { auth_mode: 'service_account', project_id: 'fixture-project', client_email: 'fixture@example.invalid', location: 'us-central1', tier_id: 'vertex' }
    account.credentials_status = { has_service_account_json: true }
    const target = source === 'gemini' ? 'anthropic' : 'gemini'
    const wrapper = mountEditor(account)
    await flushPromises()
    await choosePlatform(wrapper, target === 'anthropic' ? 'Anthropic' : 'Gemini')
    await submit(wrapper)
    expect(update).toHaveBeenCalledWith(1, expect.objectContaining({
      platform: target, credentials: expect.objectContaining({ auth_mode: 'service_account', project_id: 'fixture-project' })
    }))
    expect(account.platform).toBe(source)
  })

  it.each([false, true])('retains the replacement key and URL through repeated switches (switch back: %s)', async switchBack => {
    const account = fixture()
    const original = JSON.stringify(account)
    const wrapper = mountEditor(account)
    await flushPromises()
    await keyInput(wrapper).setValue('fixture-replacement-key')
    await baseInput(wrapper).setValue('https://replacement.example.invalid')
    await choosePlatform(wrapper, 'Anthropic')
    expect(keyInput(wrapper).element.value).toBe('fixture-replacement-key')
    expect(baseInput(wrapper).element.value).toBe('https://replacement.example.invalid')
    await keyInput(wrapper).setValue('fixture-latest-key')
    await baseInput(wrapper).setValue('https://latest.example.invalid')
    await choosePlatform(wrapper, 'Gemini')
    if (switchBack) await choosePlatform(wrapper, 'OpenAI')
    expect(keyInput(wrapper).element.value).toBe('fixture-latest-key')
    expect(baseInput(wrapper).element.value).toBe('https://latest.example.invalid')
    await submit(wrapper)
    expect(update).toHaveBeenCalledWith(1, expect.objectContaining({
      credentials: expect.objectContaining({ api_key: 'fixture-latest-key', base_url: 'https://latest.example.invalid' })
    }))
    if (switchBack) expect(update.mock.calls[0][1]).not.toHaveProperty('platform')
    else expect(update.mock.calls[0][1].platform).toBe('gemini')
    expect(JSON.stringify(account)).toBe(original)
  })

  it('clears the portable draft on cancel/reopen and on account replacement', async () => {
    const account = fixture()
    const wrapper = mountEditor(account)
    await flushPromises()
    await keyInput(wrapper).setValue('fixture-cancelled-key')
    await baseInput(wrapper).setValue('https://cancelled.example.invalid')
    await choosePlatform(wrapper, 'Anthropic')
    await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(update).not.toHaveBeenCalled()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(keyInput(wrapper).element.value).toBe('')
    expect(baseInput(wrapper).element.value).toBe('https://source.example.invalid')
    expect(wrapper.get('#edit-account-platform').text()).toContain('OpenAI')
    await submit(wrapper)
    expect(update.mock.calls[0][1].credentials).not.toHaveProperty('api_key')
    await keyInput(wrapper).setValue('fixture-other-account-key')
    await wrapper.setProps({ account: { ...fixture(), id: 2 } })
    await flushPromises()
    expect(keyInput(wrapper).element.value).toBe('')
  })
})
