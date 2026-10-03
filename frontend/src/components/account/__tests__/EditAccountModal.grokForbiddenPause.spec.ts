import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import type { Account } from '@/types'

enableAutoUnmount(afterEach)

const { updateAccountMock } = vi.hoisted(() => ({ updateAccountMock: vi.fn() }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: true }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({
        web_search_enabled: false,
        account_quota_notify_enabled: false
      }),
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
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})
const toggleSelector = '[data-testid="grok-skip-forbidden-pause-toggle"]'

function buildAccount(
  extra: Record<string, unknown> = {},
  type: Account['type'] = 'oauth',
  platform: Account['platform'] = 'grok'
) {
  return {
    id: 51,
    name: 'Grok policy fixture',
    notes: '',
    platform,
    type,
    credentials: {
      expires_at: '2027-01-01T00:00:00Z',
      token_type: 'Bearer'
    },
    credentials_status: { has_api_key: true, has_access_token: true, has_refresh_token: true },
    extra,
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as Account
}

function mountModal(account = buildAccount()) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: true,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: true
      }
    }
  })
}

describe('EditAccountModal Grok unclassified 403 policy', () => {
  beforeEach(() => {
    updateAccountMock.mockReset()
    updateAccountMock.mockImplementation(async (_id, payload) => ({ ...buildAccount(), ...payload }))
  })

  it.each(['oauth', 'apikey'] as const)('defaults off for %s and saves explicit false', async (type) => {
    const account = buildAccount({ custom_setting: 'keep-me' }, type)
    const wrapper = mountModal(account)
    const toggle = wrapper.get(toggleSelector)
    expect(toggle.attributes('role')).toBe('switch')
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(toggle.attributes('aria-label')).toBe('admin.accounts.grokSkipForbiddenPause.title')
    expect(wrapper.get(`#${toggle.attributes('aria-describedby')}`).text())
      .toBe('admin.accounts.grokSkipForbiddenPause.hint')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalledTimes(1))
    expect(updateAccountMock.mock.calls[0]?.[1].extra).toMatchObject({
      custom_setting: 'keep-me', grok_skip_forbidden_pause: false
    })
    expect(account.extra).toEqual({ custom_setting: 'keep-me' })
  })

  it.each([
    { type: 'oauth', initial: false },
    { type: 'oauth', initial: true },
    { type: 'apikey', initial: false },
    { type: 'apikey', initial: true }
  ] as const)('saves toggled $initial for $type without removing unrelated extra', async ({ type, initial }) => {
    const extra = {
      grok_skip_forbidden_pause: initial,
      grok_client_tool_cache_enabled: false,
      grok_media_eligible: false,
      custom_setting: { nested: 'keep-me' }
    }
    const wrapper = mountModal(buildAccount(extra, type))
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe(String(initial))
    await wrapper.get(toggleSelector).trigger('click')
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe(String(!initial))
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalledTimes(1))
    const savedExtra = updateAccountMock.mock.calls[0]?.[1].extra
    expect(savedExtra).toMatchObject({ ...extra, grok_skip_forbidden_pause: !initial })
    expect(extra.grok_skip_forbidden_pause).toBe(initial)

    const reopened = mountModal(buildAccount(savedExtra, type))
    expect(reopened.get(toggleSelector).attributes('aria-checked')).toBe(String(!initial))
  })

  it('preserves an enabled deployed setting on an untouched save', async () => {
    const wrapper = mountModal(buildAccount({ grok_skip_forbidden_pause: true }))
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalledTimes(1))
    expect(updateAccountMock.mock.calls[0]?.[1].extra.grok_skip_forbidden_pause).toBe(true)
  })

  it.each(['true', 1, null])('does not enable malformed stored value %s', (value) => {
    const wrapper = mountModal(buildAccount({ grok_skip_forbidden_pause: value }))
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe('false')
  })

  it('resets the switch when changing accounts and hides it for non-Grok', async () => {
    const wrapper = mountModal(buildAccount({ grok_skip_forbidden_pause: true }))
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe('true')
    await wrapper.setProps({ account: { ...buildAccount(), id: 52 } })
    expect(wrapper.get(toggleSelector).attributes('aria-checked')).toBe('false')
    await wrapper.setProps({ account: buildAccount({}, 'oauth', 'openai') })
    expect(wrapper.find(toggleSelector).exists()).toBe(false)
  })

  it.each([undefined, true])('does not add or overwrite the hidden non-Grok setting %s', async (value) => {
    const extra = value === undefined
      ? { custom_setting: 'keep-me' }
      : { custom_setting: 'keep-me', grok_skip_forbidden_pause: value }
    const wrapper = mountModal(buildAccount(extra, 'oauth', 'openai'))
    expect(wrapper.find(toggleSelector).exists()).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalledTimes(1))
    const savedExtra = updateAccountMock.mock.calls[0]?.[1].extra
    expect(savedExtra.custom_setting).toBe('keep-me')
    if (value === undefined) {
      expect(savedExtra).not.toHaveProperty('grok_skip_forbidden_pause')
    } else {
      expect(savedExtra.grok_skip_forbidden_pause).toBe(value)
    }
  })
})
