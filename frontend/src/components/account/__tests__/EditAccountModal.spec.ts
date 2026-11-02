import { apiClient } from '@/api/client'
import type { InternalAxiosRequestConfig } from 'axios'
import { getAutoConfig } from '@/api/admin/autoConfig'
import { defaultExcelBPSDefaults } from '@/utils/excelBPSDefaults'
vi.mock('@/api/admin/autoConfig', () => ({ getAutoConfig: vi.fn() }))
beforeEach(() => { vi.mocked(getAutoConfig).mockResolvedValue({ excel_bps: defaultExcelBPSDefaults() } as Awaited<ReturnType<typeof getAutoConfig>>) })
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

enableAutoUnmount(afterEach)
import {
  BUILTIN_PLATFORM_CATALOG,
  resetPlatformCatalog,
  setPlatformCatalog
} from '@/constants/platformCatalog'

const { updateAccountMock, checkMixedChannelRiskMock, authIsSimpleMode, showErrorMock } = vi.hoisted(() => ({
  showErrorMock: vi.fn(),
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
  authIsSimpleMode: { value: true }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get isSimpleMode() {
      return authIsSimpleMode.value
    }
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelMappings: { type: Array, default: () => [] },
    modelValue: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        type="button"
        data-testid="rewrite-to-snapshot"
        @click="$emit('update:modelValue', ['gpt-5.2-2025-12-11'])"
      >
        rewrite
      </button>
      <span data-testid="model-whitelist-value">
        {{ Array.isArray(modelValue) ? modelValue.join(',') : '' }}
      </span>
    </div>
  `
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <select
      v-bind="$attrs"
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value)"
    >
      <option v-for="option in options" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <div data-testid="group-selector">
      <button
        type="button"
        data-testid="set-shadow-group"
        @click="$emit('update:modelValue', [7])"
      >
        group
      </button>
    </div>
  `
})

function buildAccount() {
  return {
    id: 1,
    name: 'OpenAI Key',
    notes: '',
    platform: 'openai',
    type: 'apikey',
    credentials: {
      api_key: 'sk-test',
      base_url: 'https://api.openai.com',
      model_mapping: {
        'gpt-5.2': 'gpt-5.2'
      }
    },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function buildOpenAISparkShadowAccount() {
  const account = buildAccount()
  return {
    ...account,
    id: 4,
    name: 'OpenAI Spark Shadow',
    type: 'oauth',
    parent_account_id: 1,
    credentials: {
      access_token: 'parent-access-token',
      refresh_token: 'parent-refresh-token',
      api_key: 'sk-parent',
      base_url: 'https://api.openai.com',
      model_mapping: {
        'gpt-5.3-codex-spark': 'gpt-5.3-codex-spark'
      },
      compact_model_mapping: {
        'gpt-5.3-codex-spark': 'gpt-5.3-codex-spark-compact'
      }
    },
    group_ids: []
  } as any
}

function buildVertexAccount() {
  return {
    id: 2,
    name: 'Vertex SA',
    notes: '',
    platform: 'gemini',
    type: 'service_account',
    credentials: {
      service_account_json: '{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\\nMIIE\\n-----END PRIVATE KEY-----\\n"}',
      project_id: 'demo-project',
      client_email: 'sa@example.iam.gserviceaccount.com',
      location: 'us-central1',
      tier_id: 'vertex'
    },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function buildAntigravityAccount(projectId = 'configured-project') {
  return {
    id: 3,
    name: 'Antigravity OAuth',
    notes: '',
    platform: 'antigravity',
    type: 'oauth',
    credentials: {
      antigravity_project_id: projectId,
      model_mapping: {
        'gemini-2.5-flash': 'gemini-2.5-flash'
      }
    },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function buildGrokOAuthAccount() {
  return {
    id: 5,
    name: 'Grok OAuth',
    notes: '',
    platform: 'grok',
    type: 'oauth',
    credentials: {
      refresh_token: 'grok-rt',
      base_url: 'https://api.x.ai/v1',
      model_mapping: {
        'grok-latest': 'grok-4.3'
      }
    },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function buildGrokAPIKeyAccount() {
  return {
    ...buildAccount(),
    id: 6,
    name: 'Grok API Key',
    platform: 'grok',
    credentials: {},
    credentials_status: { has_api_key: true },
    concurrency: 2
  } as any
}

function buildOpenAISetupTokenAccount() {
  return {
    ...buildAccount(),
    type: 'setup-token',
    extra: {
      openai_oauth_responses_websockets_v2_mode: 'ctx_pool',
      openai_oauth_responses_websockets_v2_enabled: true
    }
  } as any
}

function buildOpenAIOAuthParentAccount() {
  return {
    ...buildAccount(),
    id: 7,
    name: 'OpenAI OAuth Parent',
    type: 'oauth',
    parent_account_id: null,
    credentials: { access_token: 'oauth-token' },
    extra: {}
  } as any
}

function mountModal(account = buildAccount(), renderGroupSelector = false) {
  return mount(EditAccountModal, {
    props: {
      show: true,
      account,
      proxies: [],
      groups: []
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Icon: true,
        ProxySelector: true,
        GroupSelector: renderGroupSelector ? false : GroupSelectorStub,
        ModelWhitelistSelector: ModelWhitelistSelectorStub
      }
    }
  })
}

describe('EditAccountModal', () => {
  it('round-trips and clears API-key image grants without affecting other extra fields', async () => {
    const account = buildAccount()
    account.extra = { extra_image_models: ['my-nai'], extra_image_model_prefixes: ['nai-diffusion-'], unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    const wrapper = mountModal(account)
    await flushPromises()
    expect(wrapper.get<HTMLTextAreaElement>('[data-testid="extra-image-model-ids"]').element.value).toBe('my-nai')
    expect(wrapper.get<HTMLTextAreaElement>('[data-testid="extra-image-model-prefixes"]').element.value).toBe('nai-diffusion-')
    await wrapper.get('[data-testid="extra-image-model-ids"]').setValue(' my-nai, other-image\nmy-nai ')
    await wrapper.get('#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra).toMatchObject({ extra_image_models: ['my-nai', 'other-image'], extra_image_model_prefixes: ['nai-diffusion-'], unrelated: 'preserve' })
    wrapper.unmount()
    const restored = mountModal({ ...account, extra })
    await flushPromises()
    expect(restored.get<HTMLTextAreaElement>('[data-testid="extra-image-model-ids"]').element.value).toBe('my-nai\nother-image')
    await restored.get('[data-testid="extra-image-model-ids"]').setValue('')
    await restored.get('[data-testid="extra-image-model-prefixes"]').setValue('')
    await restored.get('#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra).toMatchObject({ extra_image_models: [], extra_image_model_prefixes: [], unrelated: 'preserve' })
    restored.unmount()
    for (const type of ['oauth', 'setup-token']) {
      const oauth = mountModal({ ...account, type })
      await flushPromises()
      expect(oauth.find('[data-testid="extra-image-models"]').exists()).toBe(false)
      oauth.unmount()
    }
  })

  it('round-trips OAuth alias scope and lets an operator restore a whitelist', async () => {
    const account = { ...buildAccount(), type: 'oauth', credentials: { model_mapping_mode: 'aliases', model_mapping: { 'gpt-5.4': 'gpt-5.6-sol' } } }
    const wrapper = mountModal(account); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="openai-model-aliases"]').element.checked).toBe(true)
    await wrapper.get('[data-testid="openai-model-aliases"]').setValue(false)
    await wrapper.get('#edit-account-form').trigger('submit.prevent'); await flushPromises()
    expect(updateAccountMock).toHaveBeenCalledWith(1, expect.objectContaining({ credentials: expect.objectContaining({ model_mapping_mode: 'whitelist', model_mapping: { 'gpt-5.4': 'gpt-5.6-sol' } }) }))
    wrapper.unmount()
  })
  it('defaults WS SSE acceleration off and persists the OAuth opt-in across edits', async () => {
    const account = buildOpenAIOAuthParentAccount()
    account.extra = { unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="openai-ws-sse-acceleration"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    await toggle.trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_oauth_ws_sse_acceleration).toBe(true)
    expect(extra.unrelated).toBe('preserve')
    wrapper.unmount()

    const restored = mountModal({ ...account, extra })
    expect(restored.get('[data-testid="openai-ws-sse-acceleration"]').attributes('aria-checked')).toBe('true')
    await restored.get('[data-testid="openai-ws-sse-acceleration"]').trigger('click')
    await restored.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra).not.toHaveProperty('openai_oauth_ws_sse_acceleration')
    restored.unmount()
  })

  it('does not offer OAuth WS SSE acceleration for API keys or setup tokens', () => {
    for (const account of [buildAccount(), buildOpenAISetupTokenAccount()]) {
      const wrapper = mountModal(account)
      expect(wrapper.find('[data-testid="openai-ws-sse-acceleration"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })

  beforeEach(() => {
    showErrorMock.mockReset()
    authIsSimpleMode.value = true
  })

  afterEach(() => vi.useRealTimers())

  it('persists the BPS session proxy toggle and clears it when BPS is disabled', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const toggle = wrapper.get<HTMLInputElement>('[data-testid="excel-bps-mihomo"]')
    expect(toggle.element.checked).toBe(false)
    await toggle.setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps_mihomo).toBe(true)
    expect(extra.unrelated).toBe('preserve')
    wrapper.unmount()
    const restored = mountModal({ ...account, extra })
    expect(restored.get<HTMLInputElement>('[data-testid="excel-bps-mihomo"]').element.checked).toBe(true)
    await restored.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(restored.find('[data-testid="excel-bps-mihomo"]').exists()).toBe(false)
    await restored.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra?.openai_excel_bps_mihomo).toBeUndefined()
    restored.unmount()
  })

  it('persists the IP management pool as the session proxy source and drops it with the proxy', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_mihomo: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-proxy-source-mihomo"]').element.checked).toBe(true)
    await wrapper.get('[data-testid="excel-bps-proxy-source-ip-pool"]').setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps_proxy_source).toBe('ip_pool')
    wrapper.unmount()
    const restored = mountModal({ ...account, extra })
    expect(restored.get<HTMLInputElement>('[data-testid="excel-bps-proxy-source-ip-pool"]').element.checked).toBe(true)
    await restored.get('[data-testid="excel-bps-mihomo"]').setValue(false)
    expect(restored.find('[data-testid="excel-bps-proxy-source-ip-pool"]').exists()).toBe(false)
    await restored.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra?.openai_excel_bps_proxy_source).toBeUndefined()
    restored.unmount()
  })

  it('saves and restores Excel BPS independently of existing OAuth settings', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.credentials = { access_token: 'test-token', chatgpt_account_id: 'test-account' }
    account.extra = { unrelated: 'preserve', openai_oauth_responses_websockets_v2_mode: 'ctx_pool' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="excel-bps-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(wrapper.find('[data-testid="excel-bps-cache-creation-as-input"]').exists()).toBe(false)
    await toggle.trigger('click')
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-cache-creation-as-input"]').element.checked).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps).toBe(true)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_cache_creation_as_input).toBeUndefined()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.unrelated).toBe('preserve')
  })

  it('saves, restores and clears Excel BPS hosted tool omission', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const selector = '[data-testid="excel-bps-omit-unsupported-tools"]'
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(false)
    await wrapper.get(selector).setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const savedExtra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(savedExtra.openai_excel_bps_omit_unsupported_tools).toBe(true)
    expect(savedExtra.openai_excel_bps).toBe(true)
    expect(savedExtra.unrelated).toBe('preserve')

    await wrapper.setProps({ account: { ...account, extra: savedExtra } })
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(true)
    await wrapper.get(selector).setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const clearedExtra = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(clearedExtra.openai_excel_bps_omit_unsupported_tools).toBeUndefined()
    expect(clearedExtra.openai_excel_bps).toBe(true)
    expect(clearedExtra.unrelated).toBe('preserve')
  })

  it('clears hosted tool omission when Excel BPS is disabled', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_omit_unsupported_tools: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="excel-bps-omit-unsupported-tools"]').exists()).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps).toBeUndefined()
    expect(extra.openai_excel_bps_omit_unsupported_tools).toBeUndefined()
  })

  it('resets the BPS hosted tool omission checkbox when editing a different account', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_omit_unsupported_tools: true }
    const wrapper = mountModal(account)
    await wrapper.setProps({ account: { ...account, id: 2, extra: { openai_excel_bps: true } } })
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-omit-unsupported-tools"]').element.checked).toBe(false)
  })

  it('hides BPS hosted tool omission for API keys and shadow accounts even with stale settings', () => {
    for (const account of [buildAccount(), buildOpenAISparkShadowAccount()]) {
      account.extra = { openai_excel_bps: true, openai_excel_bps_omit_unsupported_tools: true }
      const wrapper = mountModal(account)
      expect(wrapper.find('[data-testid="excel-bps-omit-unsupported-tools"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })

  it('saves, restores and clears Excel BPS cache creation input billing', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const selector = '[data-testid="excel-bps-cache-creation-as-input"]'
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(false)
    await wrapper.get(selector).setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const savedExtra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(savedExtra.openai_excel_bps_cache_creation_as_input).toBe(true)
    expect(savedExtra.openai_excel_bps).toBe(true)
    expect(savedExtra.unrelated).toBe('preserve')

    await wrapper.setProps({ account: { ...account, extra: savedExtra } })
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(true)
    await wrapper.get(selector).setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const clearedExtra = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(clearedExtra.openai_excel_bps_cache_creation_as_input).toBeUndefined()
    expect(clearedExtra.openai_excel_bps).toBe(true)
    expect(clearedExtra.unrelated).toBe('preserve')
  })

  it('clears cache creation input billing when Excel BPS is disabled', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_cache_creation_as_input: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="excel-bps-cache-creation-as-input"]').exists()).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps).toBeUndefined()
    expect(extra.openai_excel_bps_cache_creation_as_input).toBeUndefined()
  })

  it('resets the BPS billing checkbox when editing a different account', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_cache_creation_as_input: true }
    const wrapper = mountModal(account)
    await wrapper.setProps({ account: { ...account, id: 2, extra: { openai_excel_bps: true } } })
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-cache-creation-as-input"]').element.checked).toBe(false)
  })

  it('hides BPS cache billing for API keys and shadow accounts even with stale settings', () => {
    for (const account of [buildAccount(), buildOpenAISparkShadowAccount()]) {
      account.extra = { openai_excel_bps: true, openai_excel_bps_cache_creation_as_input: true }
      const wrapper = mountModal(account)
      expect(wrapper.find('[data-testid="excel-bps-cache-creation-as-input"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })

  it('saves, restores and clears the BPS auto-disable option', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const selector = '[data-testid="excel-bps-auto-disable-on-403"]'
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(false)
    await wrapper.get(selector).setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const savedExtra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(savedExtra.openai_excel_bps_auto_disable_on_403).toBe(true)
    expect(savedExtra.openai_excel_bps).toBe(true)
    expect(savedExtra.unrelated).toBe('preserve')

    await wrapper.setProps({ account: { ...account, extra: savedExtra } })
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(true)
    await wrapper.get(selector).setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const clearedExtra = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(clearedExtra.openai_excel_bps_auto_disable_on_403).toBeUndefined()
    expect(clearedExtra.openai_excel_bps).toBe(true)
    expect(clearedExtra.unrelated).toBe('preserve')
    wrapper.unmount()
  })

  it.each([30, 360])('persists a %i minute recovery interval and disables recovery with auto-disable', async (minutes) => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const recovery = '[data-testid="excel-bps-auto-recover-on-403"]'
    expect(wrapper.get<HTMLInputElement>(recovery).element.checked).toBe(false)
    expect(wrapper.get<HTMLInputElement>(recovery).element.disabled).toBe(true)
    await wrapper.get('[data-testid="excel-bps-auto-disable-on-403"]').setValue(true)
    await wrapper.get(recovery).setValue(true)
    const interval = wrapper.get<HTMLInputElement>('[data-testid="excel-bps-recovery-interval"]')
    expect(interval.element.value).toBe('60')
    await interval.setValue(String(minutes))
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps_auto_recover_on_403).toBe(true)
    expect(extra.openai_excel_bps_403_recovery_interval_minutes).toBe(minutes)
    await wrapper.setProps({ account: { ...account, extra } })
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-recovery-interval"]').element.value).toBe(String(minutes))
    expect(wrapper.get<HTMLInputElement>(recovery).element.checked).toBe(true)
    await wrapper.get('[data-testid="excel-bps-auto-disable-on-403"]').setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra.openai_excel_bps_auto_recover_on_403).toBeUndefined()
  })

  it.each(['', '0', '-1', '1.5', '10081'])('rejects an invalid recovery interval: %s', async (value) => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_auto_disable_on_403: true, openai_excel_bps_auto_recover_on_403: true }
    updateAccountMock.mockReset()
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-recovery-interval"]').setValue(value)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('can enable and stop recovery on a 403-disabled account without losing hidden BPS options', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    const options = {
      openai_excel_bps_403_recovery_interval_minutes: 360,
      openai_excel_bps_models: ['gpt-6-astra'], openai_excel_bps_mihomo: true,
      openai_excel_bps_proxy_source: 'ip_pool',
      openai_excel_bps_ignore_encrypted_content: true, openai_excel_bps_cache_creation_as_input: true,
      openai_excel_bps_auto_move_on_403: true, openai_excel_bps_403_target_group_id: 0
    }
    account.extra = { ...options, openai_excel_bps: false, openai_excel_bps_auto_disable_on_403: true,
      openai_excel_bps_403_disabled_at: '2026-09-28T00:00:00Z' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const recovery = '[data-testid="excel-bps-auto-recover-on-403"]'
    await wrapper.get(recovery).setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const saved = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(saved).toMatchObject({ ...options, openai_excel_bps_auto_recover_on_403: true })
    expect(saved.openai_excel_bps).not.toBe(true)
    await wrapper.setProps({ account: { ...account, extra: saved } })
    await wrapper.get(recovery).setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const stopped = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(stopped).toMatchObject(options)
    expect(stopped.openai_excel_bps_auto_recover_on_403).toBeUndefined()
    expect(stopped.openai_excel_bps).not.toBe(true)
  })

  it('saves, restores and clears the BPS encrypted-content option', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const selector = '[data-testid="excel-bps-ignore-encrypted-content"]'
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(false)
    await wrapper.get(selector).setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const savedExtra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(savedExtra.openai_excel_bps_ignore_encrypted_content).toBe(true)
    expect(savedExtra.unrelated).toBe('preserve')

    await wrapper.setProps({ account: { ...account, extra: savedExtra } })
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(true)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.find(selector).exists()).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const clearedExtra = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(clearedExtra.openai_excel_bps).toBeUndefined()
    expect(clearedExtra.openai_excel_bps_ignore_encrypted_content).toBeUndefined()
    expect(clearedExtra.unrelated).toBe('preserve')
    wrapper.unmount()
  })

  it('clears the auto-disable option when manually turning off BPS', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_auto_disable_on_403: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="excel-bps-auto-disable-on-403"]').exists()).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps).toBeUndefined()
    expect(extra.openai_excel_bps_auto_disable_on_403).toBeUndefined()
    wrapper.unmount()
  })

  it('resets saved opt-ins when explicitly choosing initial settings and keeps account switching isolated', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: false, openai_excel_bps_auto_disable_on_403: true }
    const wrapper = mountModal(account)
    const selector = '[data-testid="excel-bps-auto-disable-on-403"]'
    expect(wrapper.find(selector).exists()).toBe(false)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(false)
    await wrapper.setProps({ account: { ...account, id: 2, extra: { openai_excel_bps: true } } })
    expect(wrapper.get<HTMLInputElement>(selector).element.checked).toBe(false)
    wrapper.unmount()
  })

  it('hides the auto-disable option for API keys and shadow accounts', () => {
    for (const account of [buildAccount(), buildOpenAISparkShadowAccount()]) {
      account.extra = { openai_excel_bps: true, openai_excel_bps_auto_disable_on_403: true }
      const wrapper = mountModal(account)
      expect(wrapper.find('[data-testid="excel-bps-auto-disable-on-403"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })

  it.each([0, 7])('saves and restores BPS 403 group target %s independently of disabling BPS', async target => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'keep' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.setProps({ groups: [{ id: 7, name: 'Quarantine', platform: 'openai' }, { id: 8, name: 'Other', platform: 'anthropic' }] as any })
    const toggle = '[data-testid="excel-bps-auto-move-on-403"]'
    expect(wrapper.get<HTMLInputElement>(toggle).element.checked).toBe(false)
    await wrapper.get(toggle).setValue(true)
    const selector = wrapper.get('[data-testid="excel-bps-403-target-group"]')
    expect(selector.find('option[value="8"]').exists()).toBe(false)
    await selector.setValue(String(target))
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps_auto_move_on_403).toBe(true)
    expect(extra.openai_excel_bps_403_target_group_id).toBe(target)
    expect(extra.openai_excel_bps_auto_disable_on_403).toBeUndefined()
    expect(extra.unrelated).toBe('keep')
    await wrapper.setProps({ account: { ...account, extra } })
    expect(wrapper.get<HTMLInputElement>(toggle).element.checked).toBe(true)
    expect(wrapper.get<HTMLSelectElement>('[data-testid="excel-bps-403-target-group"]').element.value).toBe(String(target))
    await wrapper.get(toggle).setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const cleared = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(cleared.openai_excel_bps_auto_move_on_403).toBeUndefined()
    expect(cleared.openai_excel_bps_403_target_group_id).toBeUndefined()
    wrapper.unmount()
  })

  it('requires an explicit BPS 403 group choice and resets it when changing accounts', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-auto-move-on-403"]').setValue(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="excel-bps-403-target-group"]').setValue('0')
    await wrapper.setProps({ account: { ...account, id: 2 } })
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-auto-move-on-403"]').element.checked).toBe(false)
    expect(wrapper.find('[data-testid="excel-bps-403-target-group"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('clears BPS 403 group settings when disabling BPS and hides them for ineligible accounts', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_auto_move_on_403: true, openai_excel_bps_403_target_group_id: 0 }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra.openai_excel_bps_auto_move_on_403).toBeUndefined()
    expect(extra.openai_excel_bps_403_target_group_id).toBeUndefined()
    wrapper.unmount()
    for (const ineligible of [buildAccount(), buildOpenAISparkShadowAccount()]) {
      ineligible.extra = account.extra
      const hidden = mountModal(ineligible)
      expect(hidden.find('[data-testid="excel-bps-auto-move-on-403"]').exists()).toBe(false)
      hidden.unmount()
    }
  })

  it('clears prior 403 options when re-enabling with initial settings', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: false, openai_excel_bps_auto_disable_on_403: true, openai_excel_bps_auto_move_on_403: true, openai_excel_bps_403_target_group_id: 0 }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-auto-disable-on-403"]').element.checked).toBe(false)
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-auto-move-on-403"]').element.checked).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toMatchObject({
      openai_excel_bps: true, openai_excel_bps_config_mode: 'initial'
    })
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_auto_disable_on_403).toBeUndefined()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_auto_move_on_403).toBeUndefined()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_403_target_group_id).toBeUndefined()
  })

  it.each([false, true])('limits 403 destinations according to simple mode %s', async simpleMode => {
    authIsSimpleMode.value = simpleMode
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_auto_move_on_403: true, openai_excel_bps_403_target_group_id: 8 }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.setProps({ groups: [{ id: 8, name: 'Composite', platform: 'composite' }] as any })
    const selector = wrapper.get('[data-testid="excel-bps-403-target-group"]')
    expect(selector.find('option[value="8"]').exists()).toBe(!simpleMode)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).toHaveBeenCalledTimes(simpleMode ? 0 : 1)
  })

  it('rejects a 403 destination removed while the edit modal is open', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_auto_move_on_403: true, openai_excel_bps_403_target_group_id: 7 }
    updateAccountMock.mockReset().mockResolvedValue(account)
    const wrapper = mountModal(account)
    await wrapper.setProps({ groups: [{ id: 7, name: 'Quarantine', platform: 'openai' }] as any })
    await wrapper.setProps({ groups: [] })
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).not.toHaveBeenCalled()
  })

  it('defaults new BPS settings to Astra, 5.6 Sol and 5.6 Terra', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = {}
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-all-models"]').element.checked).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_models).toEqual(['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'])
  })

  it('preserves legacy all-model settings and lets users select Astra only', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, unrelated: 'preserve' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-all-models"]').element.checked).toBe(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('openai_excel_bps_models')
    await wrapper.get('[data-testid="excel-bps-all-models"]').setValue(false)
    await wrapper.get('[data-testid="excel-bps-astra-only"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra?.openai_excel_bps_models).toEqual(['gpt-6-astra'])
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra?.unrelated).toBe('preserve')
  })

  it('restores the default BPS models when switching to an unconfigured account', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_models: ['gpt-6-sol'] }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await wrapper.setProps({ account: { ...account, id: 2, extra: {} } })
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="excel-bps-model-selection"]').text())
      .toContain('gpt-6-astra,gpt-5.6-sol,gpt-5.6-terra')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_models)
      .toEqual(['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'])
  })

  it.each([{ models: [] }, { models: ['gpt-6-astra'] }, { models: ['gpt-6-astra', 'gpt-6-sol'] }])('restores and saves explicit BPS selection $models', async ({ models }) => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_models: models }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-all-models"]').element.checked).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_excel_bps_models).toEqual(models)
    await wrapper.get('[data-testid="excel-bps-toggle"]').trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra).not.toHaveProperty('openai_excel_bps')
    expect(updateAccountMock.mock.calls[1]?.[1]?.extra).not.toHaveProperty('openai_excel_bps_models')
  })

  it('hides Excel BPS on API Key accounts', () => {
    expect(mountModal(buildAccount()).find('[data-testid="excel-bps-toggle"]').exists()).toBe(false)
  })

  it('loads and removes Copilot SDK mode without dropping unrelated extra', async () => {
    const account = buildAccount()
    account.extra = { openai_copilot_sdk: true, unrelated_setting: 'keep' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="copilot-sdk-toggle"]').element.checked).toBe(true)
    await wrapper.get('[data-testid="copilot-sdk-toggle"]').setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_copilot_sdk).toBeUndefined()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.unrelated_setting).toBe('keep')
  })

  it('passes existing non-identity mappings to the whitelist selector and preserves them on save', async () => {
    const account = buildAccount()
    account.credentials.model_mapping = { 'gpt-5.2': 'gpt-5.2', 'gpt-latest': 'deepseek-chat' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('modelMappings')).toEqual([
      { from: 'gpt-latest', to: 'deepseek-chat' }
    ])
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual(account.credentials.model_mapping)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, account: { ...account } })
    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('modelMappings')).toEqual([
      { from: 'gpt-latest', to: 'deepseek-chat' }
    ])
  })

  it('sets expiry presets from now instead of extending the saved expiry', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2028-02-29T12:34:00'))
    const account = buildAccount()
    account.expires_at = new Date('2030-06-15T09:00:00').getTime() / 1000
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const input = wrapper.get<HTMLInputElement>('input[type="datetime-local"]')

    for (const [label, expected] of [
      ['payment.oneMonth', '2028-03-29T12:34'],
      ['payment.oneYear', '2029-02-28T12:34'],
    ]) {
      const button = wrapper.findAll('button').find((candidate) => candidate.text() === label)!
      expect(button.attributes('type')).toBe('button')
      await button.trigger('click')
      expect(input.element.value).toBe(expected)
      expect(updateAccountMock).not.toHaveBeenCalled()
    }

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls[0]?.[1]?.expires_at).toBe(new Date('2029-02-28T12:34:00').getTime() / 1000)
    wrapper.unmount()
  })

  it('can clear a selected expiry preset before saving the account', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const button = wrapper.findAll('button').find((candidate) => candidate.text() === 'payment.oneYear')!
    await button.trigger('click')
    const input = wrapper.get<HTMLInputElement>('input[type="datetime-local"]')
    expect(input.element.value).not.toBe('')
    await input.setValue('')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls[0]?.[1]?.expires_at).toBe(0)
    wrapper.unmount()
  })

  it('allows removing assigned inactive groups and undoing the selection before saving', async () => {
    authIsSimpleMode.value = false
    const account = buildAccount()
    const activeGroup = {
      id: 1,
      name: 'Active group',
      platform: 'openai',
      status: 'active',
      subscription_type: 'standard',
      rate_multiplier: 1
    }
    const inactiveGroup = { ...activeGroup, id: 2, name: 'Paused group', status: 'inactive' }
    account.group_ids = [1, 2]
    account.groups = [
      { ...activeGroup, name: 'Outdated name' },
      inactiveGroup,
      inactiveGroup,
      { ...inactiveGroup, id: 3, name: 'Unassigned paused group' }
    ]
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account, true)
    await wrapper.setProps({ groups: [activeGroup] as any })
    const selector = wrapper.get('[data-tour="account-form-groups"]')
    expect(selector.findAll('input[type="checkbox"]').map(input => input.attributes('value')))
      .toEqual(['1', '2'])
    expect(selector.text()).toContain('Active group')
    expect(selector.text()).not.toContain('Outdated name')
    const pausedCheckbox = selector.get<HTMLInputElement>('input[value="2"]')
    expect(pausedCheckbox.element.checked).toBe(true)

    await pausedCheckbox.setValue(false)
    expect(selector.get<HTMLInputElement>('input[value="2"]').element.checked).toBe(false)
    await pausedCheckbox.setValue(true)
    expect(pausedCheckbox.element.checked).toBe(true)
    await pausedCheckbox.setValue(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.group_ids).toEqual([1])
    expect(account.group_ids).toEqual([1, 2])
  })

  it('reopening the same account rehydrates the OpenAI whitelist from props', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('gpt-5.2')

    await wrapper.get('[data-testid="rewrite-to-snapshot"]').trigger('click')
    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('gpt-5.2-2025-12-11')

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })

    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('gpt-5.2')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual({
      'gpt-5.2': 'gpt-5.2'
    })
  })

  it.each([false, true])('preserves Mistral settings and redacted key when rotating=%s', async (rotate) => {
    const account = buildAccount()
    account.platform = 'mistral'
    account.credentials = {
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://relay.example.com/v1',
      api_base_urls: { chat_completions: 'https://relay.example.com/v1' },
      model_mapping: { 'public-mistral': 'mistral-small-latest', 'codestral-latest': 'codestral-latest' },
    }
    account.credentials_status = { has_api_key: true }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    const keyInput = wrapper.get('form#edit-account-form input[type="password"]')
    expect((keyInput.element as HTMLInputElement).value).toBe('')
    if (rotate) await keyInput.setValue('fixture-rotated-mistral-key')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const credentials = updateAccountMock.mock.calls[0]?.[1]?.credentials
    expect(credentials).toMatchObject(account.credentials)
    expect(credentials.api_base_urls).toEqual({ chat_completions: 'https://relay.example.com/v1' })
    if (rotate) expect(credentials.api_key).toBe('fixture-rotated-mistral-key')
    else expect(credentials).not.toHaveProperty('api_key')
  })

  it('preserves OpenCode Zen account type and endpoints on submit', async () => {
    const account = buildAccount()
    account.platform = 'opencode_go'
    account.credentials = {
      api_key: 'sk-opencode',
      account_mode: 'zen',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/v1',
        anthropic: 'https://opencode.ai/zen',
        responses: 'https://opencode.ai/zen/v1'
      },
      protocol_rules: [
        { pattern: 'grok-*', protocol: 'responses' },
        { pattern: 'gpt-*', protocol: 'responses' },
        { pattern: 'muse-spark-*', protocol: 'responses' },
        { pattern: 'claude-*', protocol: 'anthropic' },
        { pattern: 'qwen*', protocol: 'anthropic' }
      ]
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      account_mode: 'zen',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/v1',
        anthropic: 'https://opencode.ai/zen',
        responses: 'https://opencode.ai/zen/v1'
      },
      protocol_rules: [
        { pattern: 'grok-*', protocol: 'responses' },
        { pattern: 'gpt-*', protocol: 'responses' },
        { pattern: 'muse-spark-*', protocol: 'responses' },
        { pattern: 'claude-*', protocol: 'anthropic' },
        { pattern: 'qwen*', protocol: 'anthropic' }
      ]
    })
  })

  it('treats a legacy OpenCode account without account_mode as GO', async () => {
    const account = buildAccount()
    account.platform = 'opencode_go'
    account.credentials = {
      api_key: 'sk-opencode',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/go/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/go/v1',
        anthropic: 'https://opencode.ai/zen/go',
        responses: 'https://opencode.ai/zen/go/v1'
      }
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      account_mode: 'go',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/go/v1'
    })
  })

  describe('providers using the generic form', () => {
    beforeEach(() => {
      setPlatformCatalog({
        platforms: [
          ...BUILTIN_PLATFORM_CATALOG.platforms,
          {
            id: 'acme_router',
            display_name: 'Acme Router',
            gateway: 'openai',
            cn_provider: false,
            multi_protocol: {
              default_mode: 'standard',
              routing: 'by_model',
              modes: [
                {
                  mode: 'standard',
                  base_urls: {
                    chat_completions: 'https://api.acme-router.example/provider/v1',
                    anthropic: 'https://api.acme-router.example/provider'
                  },
                  protocol_rules: [{ pattern: 'claude-*', protocol: 'anthropic' }]
                },
                {
                  mode: 'team',
                  base_urls: {
                    chat_completions: 'https://team.acme-router.example/provider/v1',
                    anthropic: 'https://team.acme-router.example/provider'
                  },
                  protocol_rules: [{ pattern: 'sonnet-*', protocol: 'anthropic' }]
                }
              ]
            }
          }
        ],
        composite_precedence: [...BUILTIN_PLATFORM_CATALOG.composite_precedence, 'acme_router']
      })
      checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    })

    afterEach(() => {
      resetPlatformCatalog()
    })

    function commandCodeAccount() {
      const account = buildAccount()
      account.platform = 'acme_router'
      account.credentials = {
        api_key: 'sk-cc',
        account_mode: 'standard',
        api_protocol: 'adaptive',
        base_url: 'https://relay.example.com/v1',
        api_base_urls: {
          chat_completions: 'https://relay.example.com/v1',
          anthropic: 'https://relay.example.com'
        },
        protocol_rules: [{ pattern: 'custom-*', protocol: 'anthropic' }]
      }
      updateAccountMock.mockReset().mockResolvedValue(account)
      return account
    }

    it('preserves stored endpoints and rules on submit', async () => {
      const wrapper = mountModal(commandCodeAccount())
      await wrapper.get('form#edit-account-form').trigger('submit.prevent')

      expect(updateAccountMock).toHaveBeenCalledTimes(1)
      expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
        account_mode: 'standard',
        api_protocol: 'adaptive',
        base_url: 'https://relay.example.com/v1',
        api_base_urls: {
          chat_completions: 'https://relay.example.com/v1',
          anthropic: 'https://relay.example.com'
        },
        protocol_rules: [{ pattern: 'custom-*', protocol: 'anthropic' }]
      })
      expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.api_base_urls).not.toHaveProperty('responses')
    })

    it('offers the provider modes and keeps customised endpoints when switching mode', async () => {
      const wrapper = mountModal(commandCodeAccount())
      const modeButtons = wrapper.get('[data-testid="edit-generic-account-mode"]').findAll('button')
      expect(modeButtons.map(button => button.text())).toEqual(['standard', 'team'])
      await modeButtons[1].trigger('click')
      await wrapper.get('form#edit-account-form').trigger('submit.prevent')

      expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
        account_mode: 'team',
        // 自定义端点与规则不是上一模式的默认值，切换模式时保留。
        api_base_urls: {
          chat_completions: 'https://relay.example.com/v1',
          anthropic: 'https://relay.example.com'
        },
        protocol_rules: [{ pattern: 'custom-*', protocol: 'anthropic' }]
      })
    })
  })

  it('preserves adaptive Kimi Responses endpoint on submit', async () => {
    const account = buildAccount()
    account.platform = 'kimi'
    account.credentials = {
      api_key: 'sk-kimi',
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.moonshot.cn/v1',
      api_base_urls: {
        chat_completions: 'https://api.moonshot.cn/v1',
        anthropic: 'https://api.moonshot.cn/anthropic',
        responses: 'https://api.moonshot.cn/v1'
      }
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.moonshot.cn/v1',
      api_base_urls: {
        chat_completions: 'https://api.moonshot.cn/v1',
        anthropic: 'https://api.moonshot.cn/anthropic',
        responses: 'https://api.moonshot.cn/v1'
      }
    })
  })

  it('preserves adaptive GLM endpoints on submit', async () => {
    const account = buildAccount()
    account.platform = 'zhipu'
    account.credentials = {
      api_key: 'sk-glm',
      account_mode: 'coding',
      api_protocol: 'adaptive',
      base_url: 'https://open.bigmodel.cn/api/coding/paas/v4',
      api_base_urls: {
        chat_completions: 'https://open.bigmodel.cn/api/coding/paas/v4',
        anthropic: 'https://open.bigmodel.cn/api/anthropic'
      }
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      account_mode: 'coding',
      api_protocol: 'adaptive',
      base_url: 'https://open.bigmodel.cn/api/coding/paas/v4',
      api_base_urls: {
        chat_completions: 'https://open.bigmodel.cn/api/coding/paas/v4',
        anthropic: 'https://open.bigmodel.cn/api/anthropic'
      }
    })
  })

  it.each([
    ['explicit Chat Completions', 'chat_completions'],
    ['legacy missing protocol', undefined]
  ])('preserves a custom CN relay for %s accounts', async (_name, storedProtocol) => {
    const account = buildAccount()
    account.platform = 'zhipu'
    account.credentials = {
      api_key: 'sk-glm',
      account_mode: 'payg',
      base_url: 'https://relay.example.com/v1'
    }
    if (storedProtocol) {
      account.credentials.api_protocol = storedProtocol
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const submittedCredentials = updateAccountMock.mock.calls[0]?.[1]?.credentials
    expect(submittedCredentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'chat_completions',
      base_url: 'https://relay.example.com/v1'
    })
    expect(submittedCredentials).not.toHaveProperty('api_base_urls')
  })

  it('uses the legacy base_url when adaptive endpoints are missing', async () => {
    const account = buildAccount()
    account.platform = 'zhipu'
    account.credentials = {
      api_key: 'sk-glm',
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://relay.example.com/v1',
      api_base_urls: {
        chat_completions: '   '
      }
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      api_protocol: 'adaptive',
      base_url: 'https://relay.example.com/v1',
      api_base_urls: {
        chat_completions: 'https://relay.example.com/v1',
        anthropic: 'https://open.bigmodel.cn/api/anthropic'
      }
    })
  })

  it('carries a fixed Chat relay into Adaptive when the user switches protocols', async () => {
    const account = buildAccount()
    account.platform = 'zhipu'
    account.credentials = {
      api_key: 'sk-glm',
      account_mode: 'payg',
      api_protocol: 'chat_completions',
      base_url: 'https://relay.example.com/v1'
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    const adaptiveButton = wrapper
      .findAll('button')
      .find(button => button.text().includes('admin.accounts.cnProviders.apiProtocol.adaptive'))
    expect(adaptiveButton).toBeDefined()
    await adaptiveButton!.trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      api_protocol: 'adaptive',
      base_url: 'https://relay.example.com/v1',
      api_base_urls: {
        chat_completions: 'https://relay.example.com/v1'
      }
    })
  })

  it.each([
    {
      name: 'Anthropic',
      platform: 'zhipu',
      protocol: 'anthropic',
      baseUrl: 'https://relay.example.com/anthropic',
      expectedBaseUrl: 'https://open.bigmodel.cn/api/paas/v4',
      expectedProtocolUrls: {
        chat_completions: 'https://open.bigmodel.cn/api/paas/v4',
        anthropic: 'https://relay.example.com/anthropic'
      }
    },
    {
      name: 'Responses',
      platform: 'deepseek',
      protocol: 'responses',
      baseUrl: 'https://relay.example.com/responses',
      expectedBaseUrl: 'https://api.deepseek.com',
      expectedProtocolUrls: {
        chat_completions: 'https://api.deepseek.com',
        anthropic: 'https://api.deepseek.com/anthropic',
        responses: 'https://relay.example.com/responses'
      }
    }
  ])('keeps a fixed $name relay in its protocol slot when switching to Adaptive', async (testCase) => {
    const account = buildAccount()
    account.platform = testCase.platform
    account.credentials = {
      api_key: 'sk-cn',
      account_mode: 'payg',
      api_protocol: testCase.protocol,
      base_url: testCase.baseUrl
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await flushPromises()
    const adaptiveButton = wrapper
      .findAll('button')
      .find(button => button.text().includes('admin.accounts.cnProviders.apiProtocol.adaptive'))
    expect(adaptiveButton).toBeDefined()
    await adaptiveButton!.trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(showErrorMock.mock.calls).toEqual([])
    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      api_protocol: 'adaptive',
      base_url: testCase.expectedBaseUrl,
      api_base_urls: testCase.expectedProtocolUrls
    })
  })

  it('preserves model mappings when editing the whitelist', async () => {
    const account = buildAccount()
    account.credentials.model_mapping = {
      'gpt-5.2': 'gpt-5.2',
      'gpt-latest': 'gpt-5.2'
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.get('[data-testid="model-whitelist-value"]').text()).toBe('gpt-5.2')

    await wrapper.get('[data-testid="rewrite-to-snapshot"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual({
      'gpt-5.2-2025-12-11': 'gpt-5.2-2025-12-11',
      'gpt-latest': 'gpt-5.2'
    })
  })

  it('submits OpenAI compact mode and compact-only model mapping', async () => {
    const account = buildAccount()
    account.extra = {
      openai_compact_mode: 'force_on'
    }
    account.credentials = {
      ...account.credentials,
      compact_model_mapping: {
        'gpt-5.4': 'gpt-5.4-openai-compact'
      }
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_compact_mode).toBe('force_on')
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.compact_model_mapping).toEqual({
      'gpt-5.4': 'gpt-5.4-openai-compact'
    })
  })

  it('loads and submits the per-account OpenAI long-context billing toggle', async () => {
    const account = buildAccount()
    account.extra = {
      openai_long_context_billing_enabled: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="openai-long-context-billing-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('true')

    await toggle.trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('loads and clears the OAuth-only Codex namespace flatten toggle', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = {
      openai_responses_flatten_namespaces: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="edit-openai-flatten-namespaces-toggle"]')

    // 关闭后应从 extra 中删除该键，而不是写入 false
    await toggle.trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty(
      'openai_responses_flatten_namespaces'
    )
  })

  it('submits the Codex namespace flatten toggle when switched on', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="edit-openai-flatten-namespaces-toggle"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_responses_flatten_namespaces).toBe(
      true
    )
  })

  it('writes the upstream request id header into extra only when it changes', async () => {
    const account = buildAccount()
    account.extra = { openai_compact_mode: 'force_on' }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const untouched = mountModal(account)
    await untouched.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.upstream_request_id_header).toBeUndefined()

    updateAccountMock.mockClear()
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="upstream-request-id-header"]').setValue(' X-Oneapi-Request-Id ')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toMatchObject({
      openai_compact_mode: 'force_on',
      upstream_request_id_header: 'X-Oneapi-Request-Id'
    })
  })

  it('removes the upstream request id header from extra when cleared', async () => {
    const account = buildAccount()
    account.extra = { upstream_request_id_header: 'X-Request-ID' }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    expect((wrapper.get('[data-testid="upstream-request-id-header"]').element as HTMLInputElement).value).toBe('X-Request-ID')
    await wrapper.get('[data-testid="upstream-request-id-header"]').setValue('')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toBeDefined()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('upstream_request_id_header')
  })

  it('writes images_url_to_b64_json into extra when toggled on', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="openai-images-url-to-b64-json-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    await toggle.trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.images_url_to_b64_json).toBe(true)
  })

  it('removes images_url_to_b64_json from extra when toggled off', async () => {
    const account = buildAccount()
    account.extra = { images_url_to_b64_json: true }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="openai-images-url-to-b64-json-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('true')
    await toggle.trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toBeDefined()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('images_url_to_b64_json')
  })

  it('hides the Codex namespace flatten toggle for non-OAuth OpenAI accounts', async () => {
    const account = buildAccount()
    const wrapper = mountModal(account)

    expect(wrapper.find('[data-testid="edit-openai-flatten-namespaces-toggle"]').exists()).toBe(
      false
    )
  })

  it('defaults legacy OpenAI accounts to long-context billing disabled', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="openai-long-context-billing-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('false')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('does not render or submit the long-context billing toggle for Spark shadow accounts', async () => {
    const account = buildOpenAISparkShadowAccount()
    account.extra = {
      openai_long_context_billing_enabled: false
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)

    expect(wrapper.find('[data-testid="openai-long-context-billing-toggle"]').exists()).toBe(false)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty(
      'openai_long_context_billing_enabled'
    )
  })

  it('preserves an explicit OpenAI long-context billing opt-out', async () => {
    const account = buildAccount()
    account.extra = {
      openai_long_context_billing_enabled: false
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="openai-long-context-billing-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('false')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('fails closed for malformed OpenAI long-context billing values', async () => {
    const account = buildAccount()
    account.extra = {
      openai_long_context_billing_enabled: 'false'
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.get('[data-testid="openai-long-context-billing-toggle"]').attributes('aria-checked')).toBe('false')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('loads and submits Grok OAuth model mapping edits', async () => {
    const account = buildGrokOAuthAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    expect(wrapper.text()).toContain('Imagine Image')
    expect(wrapper.text()).toContain('Imagine Video')

    const inputWithValue = (value: string) => {
      const input = wrapper
        .findAll('input')
        .find((input) => (input.element as HTMLInputElement).value === value)
      expect(input).toBeTruthy()
      return input!
    }

    await inputWithValue('grok-latest').setValue('grok')
    await inputWithValue('grok-4.3').setValue('grok-build-0.1')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual({
      grok: 'grok-build-0.1'
    })
  })

  it('uses the official xAI base URL when a Grok API-key account omits base_url', async () => {
    const account = buildGrokAPIKeyAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect((wrapper.get('input[placeholder="https://api.x.ai/v1"]').element as HTMLInputElement).value)
      .toBe('https://api.x.ai/v1')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.base_url).toBe('https://api.x.ai/v1')
  })

  it('only submits model mapping credentials when saving an OpenAI spark shadow account', async () => {
    authIsSimpleMode.value = false
    const account = buildOpenAISparkShadowAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('[data-testid="set-shadow-group"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.group_ids).toEqual([7])
    expect(payload?.credentials).toEqual({
      model_mapping: {
        'gpt-5.3-codex-spark': 'gpt-5.3-codex-spark'
      },
      compact_model_mapping: {
        'gpt-5.3-codex-spark': 'gpt-5.3-codex-spark-compact'
      }
    })
  })

  it('submits OpenAI APIKey Responses support override mode', async () => {
    const account = buildAccount()
    account.extra = {
      openai_responses_mode: 'force_chat_completions',
      openai_responses_supported: false
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('[data-testid="openai-responses-mode-select"]').setValue('force_responses')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_responses_mode).toBe('force_responses')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_responses_supported).toBe(false)
  })

  it('submits the account upstream billing auto-probe setting', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="upstream-billing-auto-probe"]')
    expect(toggle.attributes('aria-checked')).toBe('false')

    await toggle.trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.upstream_billing_probe_enabled).toBe(true)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty(
      'upstream_billing_probe_enabled'
    )
  })

  it('exposes the upstream billing auto-probe toggle for non-OpenAI API-key accounts', async () => {
    // 探测已放宽到全部 API-key 平台：grok 账号同样能开启并保存。
    const account = buildAccount()
    account.platform = 'grok'
    account.name = 'grok-relay'
    account.credentials = { api_key: 'sk-grok', base_url: 'https://relay.example/v1' }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const toggle = wrapper.get('[data-testid="upstream-billing-auto-probe"]')
    expect(toggle.attributes('aria-checked')).toBe('false')

    await toggle.trigger('click')
    await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.upstream_billing_probe_enabled).toBe(true)
  })

  it('keeps New API multipliers manual despite legacy automatic sync flags', async () => {
    const account = buildAccount()
    account.extra = {
      upstream_billing_provider: 'new_api',
      upstream_billing_probe_enabled: true,
      upstream_billing_rate_sync_enabled: true,
      cost_multiplier_auto_sync: true,
      cost_multiplier: 0.2
    }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    expect(wrapper.find('[data-testid="upstream-billing-rate-sync"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-cost-auto-sync"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.newAPI.groupRatioHint')
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-rate-multiplier"]').element.disabled).toBe(false)
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-cost-multiplier"]').element.disabled).toBe(false)
    expect(wrapper.get('[data-testid="upstream-billing-auto-probe"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="account-rate-multiplier"]').setValue(0.7)
    await wrapper.get('[data-testid="account-cost-multiplier"]').setValue(0.3)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]).toMatchObject({
      rate_multiplier: 0.7,
      upstream_billing_probe_enabled: true,
      upstream_billing_rate_sync_enabled: false,
      extra: { cost_multiplier: 0.3, cost_multiplier_auto_sync: false }
    })
  })

  it('enabling rate sync also enables probing and stops submitting a manual rate', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const syncToggle = wrapper.get('[data-testid="upstream-billing-rate-sync"]')
    const probeToggle = wrapper.get('[data-testid="upstream-billing-auto-probe"]')
    const rateInput = wrapper.get<HTMLInputElement>('[data-testid="account-rate-multiplier"]')
    expect(syncToggle.attributes('aria-checked')).toBe('false')
    expect(probeToggle.attributes('aria-checked')).toBe('false')
    expect(rateInput.element.disabled).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.billingRateMultiplierHint')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBilling.syncRateManagedHint')

    await syncToggle.trigger('click')
    expect(syncToggle.attributes('aria-checked')).toBe('true')
    expect(probeToggle.attributes('aria-checked')).toBe('true')
    expect(rateInput.element.disabled).toBe(true)
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.syncRateManagedHint')
    expect(wrapper.text()).not.toContain('admin.accounts.billingRateMultiplierHint')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.upstream_billing_probe_enabled).toBe(true)
    expect(payload?.upstream_billing_rate_sync_enabled).toBe(true)
    expect(payload).not.toHaveProperty('rate_multiplier')
  })

  it('disabling probing also disables rate sync and restores manual rate editing', async () => {
    const account = buildAccount()
    account.extra = {
      upstream_billing_probe_enabled: true,
      upstream_billing_rate_sync_enabled: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const syncToggle = wrapper.get('[data-testid="upstream-billing-rate-sync"]')
    const probeToggle = wrapper.get('[data-testid="upstream-billing-auto-probe"]')
    const rateInput = wrapper.get<HTMLInputElement>('[data-testid="account-rate-multiplier"]')
    expect(syncToggle.attributes('aria-checked')).toBe('true')
    expect(rateInput.element.disabled).toBe(true)

    await probeToggle.trigger('click')
    expect(probeToggle.attributes('aria-checked')).toBe('false')
    expect(syncToggle.attributes('aria-checked')).toBe('false')
    expect(rateInput.element.disabled).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.upstream_billing_probe_enabled).toBe(false)
    expect(payload?.upstream_billing_rate_sync_enabled).toBe(false)
    expect(payload?.rate_multiplier).toBe(1)
  })

  it('disabling only rate sync keeps automatic probing enabled', async () => {
    const account = buildAccount()
    account.extra = {
      upstream_billing_probe_enabled: true,
      upstream_billing_rate_sync_enabled: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="upstream-billing-rate-sync"]').trigger('click')
    expect(wrapper.get('[data-testid="upstream-billing-auto-probe"]').attributes('aria-checked')).toBe(
      'true'
    )
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.upstream_billing_probe_enabled).toBe(true)
    expect(payload?.upstream_billing_rate_sync_enabled).toBe(false)
    expect(payload?.rate_multiplier).toBe(1)
  })

  it('clears OpenAI APIKey Responses override when set back to auto', async () => {
    const account = buildAccount()
    account.extra = {
      openai_responses_mode: 'force_chat_completions',
      openai_responses_supported: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('[data-testid="openai-responses-mode-select"]').setValue('auto')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('openai_responses_mode')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_responses_supported).toBe(true)
  })

  it('submits OpenAI APIKey endpoint capabilities from credentials', async () => {
    const account = buildAccount()
    account.credentials.openai_capabilities = ['chat_completions']
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.findAll('input[type="checkbox"]').some((input) => (input.element as HTMLInputElement).checked)).toBe(true)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.openai_capabilities).toEqual([
      'chat_completions'
    ])
  })

	it('submits OpenAI quota auto-pause thresholds in extra', async () => {
	  const account = buildAccount()
	  account.extra = {
		auto_pause_5h_threshold: 0.9,
		auto_pause_7d_threshold: 0.8
	  }
	  updateAccountMock.mockReset()
	  checkMixedChannelRiskMock.mockReset()
	  checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
	  updateAccountMock.mockResolvedValue(account)

	  const wrapper = mountModal(account)

	  await wrapper.get('[data-testid="auto-pause-5h-threshold"]').setValue('95')
	  await wrapper.get('[data-testid="auto-pause-7d-threshold"]').setValue('96')
	  await wrapper.get('form#edit-account-form').trigger('submit.prevent')

	  expect(updateAccountMock).toHaveBeenCalledTimes(1)
	  expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.auto_pause_5h_threshold).toBe(0.95)
	  expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.auto_pause_7d_threshold).toBe(0.96)
	})

	it('submits OpenAI quota auto-pause disable flag in extra', async () => {
	  // Toggling the per-account disable flag must persist as auto_pause_5h_disabled
	  // so an admin can exempt one account from auto-pause even when a global default
	  // threshold is configured (otherwise leaving the threshold blank would silently
	  // fall back to the global default).
	  const account = buildAccount()
	  updateAccountMock.mockReset()
	  checkMixedChannelRiskMock.mockReset()
	  checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
	  updateAccountMock.mockResolvedValue(account)

	  const wrapper = mountModal(account)

	  await wrapper.get('[data-testid="auto-pause-5h-disabled"]').trigger('click')
	  await wrapper.get('form#edit-account-form').trigger('submit.prevent')

	  expect(updateAccountMock).toHaveBeenCalledTimes(1)
	  expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.auto_pause_5h_disabled).toBe(true)
	  expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.auto_pause_7d_disabled).toBeUndefined()
	})

  it('preserves Seedance when exactly two endpoint capabilities are selected', async () => {
    const account = buildAccount()
    account.credentials.openai_capabilities = ['chat_completions', 'seedance']
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="openai-endpoint-capability-seedance"]').element.checked).toBe(true)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.openai_capabilities).toEqual(['chat_completions', 'seedance'])
  })

  it('keeps at least one OpenAI APIKey endpoint capability selected', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    const chatCheckbox = wrapper.get<HTMLInputElement>(
      '[data-testid="openai-endpoint-capability-chat_completions"]'
    )
    const embeddingsCheckbox = wrapper.get<HTMLInputElement>(
      '[data-testid="openai-endpoint-capability-embeddings"]'
    )

    expect(chatCheckbox.element.checked).toBe(true)
    expect(embeddingsCheckbox.element.checked).toBe(true)

    await embeddingsCheckbox.setValue(false)

    expect(chatCheckbox.element.checked).toBe(true)
    expect(embeddingsCheckbox.element.checked).toBe(false)

    await chatCheckbox.setValue(false)

    expect(chatCheckbox.element.checked).toBe(true)
    expect(embeddingsCheckbox.element.checked).toBe(false)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.openai_capabilities).toEqual([
      'chat_completions'
    ])
  })

  it('disables text generation protocol when only embeddings requests are accepted', async () => {
    const account = buildAccount()
    account.credentials.openai_capabilities = ['embeddings']
    account.extra = {
      openai_responses_mode: 'force_responses',
      openai_responses_supported: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    const responsesModeSelect = wrapper.get<HTMLSelectElement>(
      '[data-testid="openai-responses-mode-select"]'
    )

    expect(responsesModeSelect.element.disabled).toBe(true)
    expect(wrapper.find('[data-testid="openai-responses-mode-not-applicable"]').exists()).toBe(true)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.openai_capabilities).toEqual([
      'embeddings'
    ])
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('openai_responses_mode')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_responses_supported).toBe(true)
  })

  it('submits Codex image tool force-inject mode as bridge override', async () => {
    const account = buildAccount()
    account.extra = {
      codex_image_generation_bridge: false,
      codex_image_generation_bridge_enabled: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.text()).toContain('admin.accounts.openai.codexImageTool')
    expect(wrapper.text()).toContain('admin.accounts.openai.codexImageToolDesc')
    expect(wrapper.text()).toContain('admin.accounts.openai.codexImageToolEnabledDesc')

    await wrapper.get('button[data-testid="codex-image-tool-enabled"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.codex_image_generation_bridge).toBe(true)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('codex_image_generation_bridge_enabled')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('codex_image_generation_explicit_tool_policy')
  })

  it('submits Codex image tool no-injection mode without strip policy', async () => {
    const account = buildAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('button[data-testid="codex-image-tool-disabled"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.codex_image_generation_bridge).toBe(false)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('codex_image_generation_explicit_tool_policy')
  })

  it('submits Codex image tool block mode as strip policy and clears bridge override', async () => {
    const account = buildAccount()
    account.extra = {
      codex_image_generation_bridge: true
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    expect(wrapper.text()).toContain('admin.accounts.openai.codexImageToolBlock')
    expect(wrapper.text()).toContain('admin.accounts.openai.codexImageToolBlockDesc')

    await wrapper.get('button[data-testid="codex-image-tool-block"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.codex_image_generation_explicit_tool_policy).toBe('strip')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('codex_image_generation_bridge')
  })

  it('loads strip policy as block mode and clears both keys when reset to inherit', async () => {
    const account = buildAccount()
    account.extra = {
      codex_image_generation_explicit_tool_policy: 'strip'
    }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('button[data-testid="codex-image-tool-inherit"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('codex_image_generation_explicit_tool_policy')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('codex_image_generation_bridge')
  })

  it('setup-token account can select and submit OAuth WS mode', async () => {
    const account = buildOpenAISetupTokenAccount()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('[data-testid="edit-openai-ws-mode-select"]').setValue('http_bridge')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_oauth_responses_websockets_v2_mode).toBe('http_bridge')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra?.openai_oauth_responses_websockets_v2_enabled).toBe(true)
  })

  it('allows saving apikey account when backend redacted api_key but credentials_status reports it exists', async () => {
    // 新前端 + 新后端：响应已脱敏，credentials 里没有 api_key，credentials_status.has_api_key=true
    const account = buildAccount()
    account.credentials = {
      base_url: 'https://api.openai.com',
      model_mapping: { 'gpt-5.2': 'gpt-5.2' }
    }
    account.credentials_status = { has_api_key: true }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    // 用户未输入新 key 时，payload 不应带 api_key，由后端合并保留旧值
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).not.toHaveProperty('api_key')
  })

  it('allows saving apikey account against legacy backend without credentials_status', async () => {
    // 新前端 + 旧后端：credentials_status 缺失，但 credentials.api_key 仍是明文，应允许保存
    const account = buildAccount()
    // 显式确保没有 credentials_status
    expect(account.credentials_status).toBeUndefined()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    // 旧后端响应未脱敏，原 api_key 会随 currentCredentials 一起传回去（旧行为，等价于无操作）
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.api_key).toBe('sk-test')
  })

  it('blocks apikey save when neither credentials_status nor legacy api_key indicates existence', async () => {
    const account = buildAccount()
    account.credentials = {
      base_url: 'https://api.openai.com'
    }
    // 既没有 credentials_status 也没有旧的 api_key
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).not.toHaveBeenCalled()
  })

  it('allows saving Vertex SA account when backend redacted service_account_json but credentials_status reports it exists', async () => {
    // 新前端 + 新后端：响应已脱敏，credentials 里没有 service_account_json，credentials_status.has_service_account_json=true
    const account = buildVertexAccount()
    account.credentials = {
      project_id: 'demo-project',
      client_email: 'sa@example.iam.gserviceaccount.com',
      location: 'us-central1',
      tier_id: 'vertex'
    }
    account.credentials_status = { has_service_account_json: true }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.project_id).toBe('demo-project')
  })

  it('allows saving Vertex SA account against legacy backend without credentials_status', async () => {
    // 新前端 + 旧后端：credentials_status 缺失，但 credentials.service_account_json 仍是明文，应允许保存
    const account = buildVertexAccount()
    expect(account.credentials_status).toBeUndefined()
    expect(account.credentials.service_account_json).toBeTruthy()
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
  })

  it('shows quota limit card for Vertex SA account and keeps quota limits on save', async () => {
    const account = buildVertexAccount()
    account.extra = { quota_limit: 200, quota_daily_limit: 20, quota_used: 35.5 }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    expect(wrapper.findComponent({ name: 'QuotaLimitCard' }).exists()).toBe(true)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra?.quota_limit).toBe(200)
    expect(extra?.quota_daily_limit).toBe(20)
    expect(extra?.quota_weekly_limit).toBeUndefined()
  })

  it('blocks Vertex SA save when neither credentials_status nor legacy json indicates existence', async () => {
    const account = buildVertexAccount()
    account.credentials = {
      project_id: 'demo-project',
      client_email: 'sa@example.iam.gserviceaccount.com',
      location: 'us-central1',
      tier_id: 'vertex'
    }
    // 既没有 credentials_status 也没有旧的 service_account_json
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).not.toHaveBeenCalled()
  })

  it('keeps the redacted Vertex API key when saving without entering a new one', async () => {
    const account = buildVertexAccount()
    account.credentials = {
      auth_mode: 'apikey',
      project_id: 'demo-project',
      location: 'us-central1',
      tier_id: 'vertex'
    }
    account.credentials_status = { has_api_key: true }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    expect(wrapper.find('[data-testid="edit-vertex-api-key-input"]').exists()).toBe(true)
    await wrapper.get('[data-testid="edit-vertex-api-key-project-id-input"]').setValue('')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const credentials = updateAccountMock.mock.calls[0]?.[1]?.credentials
    expect(credentials).toMatchObject({ auth_mode: 'apikey', location: 'us-central1', tier_id: 'vertex' })
    expect(credentials).not.toHaveProperty('api_key')
    expect(credentials).not.toHaveProperty('project_id')
  })

  it('submits a rotated Vertex API key', async () => {
    const account = buildVertexAccount()
    account.credentials = { auth_mode: 'apikey', location: 'global', tier_id: 'vertex' }
    account.credentials_status = { has_api_key: true }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="edit-vertex-api-key-input"]').setValue(' rotated-key ')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.api_key).toBe('rotated-key')
  })

  it('blocks Vertex API key save when no key exists and none is entered', async () => {
    const account = buildVertexAccount()
    account.credentials = { auth_mode: 'apikey', location: 'global', tier_id: 'vertex' }
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })

    const wrapper = mountModal(account)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).not.toHaveBeenCalled()
  })

  it('loads and submits Antigravity configured project fallback', async () => {
    const account = buildAntigravityAccount('configured-project')
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const input = wrapper.get<HTMLInputElement>('[data-testid="antigravity-project-id-input"]')
    expect(input.element.value).toBe('configured-project')

    await input.setValue('  updated-project  ')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.antigravity_project_id).toBe(
      'updated-project'
    )
  })

  it('clears Antigravity configured project fallback when input is empty', async () => {
    const account = buildAntigravityAccount('configured-project')
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(account)

    const wrapper = mountModal(account)
    const input = wrapper.get<HTMLInputElement>('[data-testid="antigravity-project-id-input"]')

    await input.setValue('')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).not.toHaveProperty(
      'antigravity_project_id'
    )
  })
})

describe('EditAccountModal OpenAI 自动使用重置卡', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  it('仅对 OpenAI OAuth 母账号显示，默认关闭且阈值为 100/100', () => {
    const parent = mountModal(buildOpenAIOAuthParentAccount())
    expect(parent.find('[data-testid="auto-reset-credit-settings"]').exists()).toBe(true)
    expect((parent.get('[data-testid="auto-reset-credit-5h-threshold"]').element as HTMLInputElement).value).toBe('100')
    expect((parent.get('[data-testid="auto-reset-credit-7d-threshold"]').element as HTMLInputElement).value).toBe('100')
    expect(parent.get('[data-testid="auto-reset-credit-5h-threshold"]').attributes('disabled')).toBeDefined()
    parent.unmount()

    for (const account of [buildAccount(), buildOpenAISetupTokenAccount(), buildOpenAISparkShadowAccount()]) {
      const wrapper = mountModal(account)
      expect(wrapper.find('[data-testid="auto-reset-credit-settings"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })

  it.each([
    [75.5, 92],
    [0, 90],
    [80, 0],
    [0, 0]
  ])('保存并重新载入 5h=%s、7d=%s，且不回写运行态', async (threshold5h, threshold7d) => {
    const account = buildOpenAIOAuthParentAccount()
    account.extra = {
      codex_auto_reset_credit_state: {
        status: 'success',
        trigger_window: '5h',
        available_count: 1
      }
    }
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)

    await wrapper.get('[data-testid="auto-reset-credit-enabled"]').trigger('click')
    await wrapper.get('[data-testid="auto-reset-credit-5h-threshold"]').setValue(String(threshold5h))
    await wrapper.get('[data-testid="auto-reset-credit-7d-threshold"]').setValue(String(threshold7d))
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra).toMatchObject({
      auto_reset_credit_enabled: true,
      auto_reset_credit_5h_threshold: threshold5h / 100,
      auto_reset_credit_7d_threshold: threshold7d / 100
    })
    expect(extra).not.toHaveProperty('codex_auto_reset_credit_state')
    wrapper.unmount()

    const reopened = mountModal({ ...account, extra })
    expect((reopened.get('[data-testid="auto-reset-credit-5h-threshold"]').element as HTMLInputElement).value).toBe(String(threshold5h))
    expect((reopened.get('[data-testid="auto-reset-credit-7d-threshold"]').element as HTMLInputElement).value).toBe(String(threshold7d))
    reopened.unmount()
  })

  it.each(['', '-0.1', '0.01', '100.1'])('开启后拒绝无效阈值 %s', async (invalidThreshold) => {
    const wrapper = mountModal(buildOpenAIOAuthParentAccount())
    await wrapper.get('[data-testid="auto-reset-credit-enabled"]').trigger('click')
    await wrapper.get('[data-testid="auto-reset-credit-5h-threshold"]').setValue(invalidThreshold)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})


describe('independent account cost multiplier', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authIsSimpleMode.value = true
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
    updateAccountMock.mockResolvedValue(buildAccount())
  })
  it('defaults to 0.1 and saves independently from billing multipliers', async () => {
    const account = { ...buildAccount(), rate_multiplier: 1, group_rate_multiplier: 5 }
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-cost-multiplier"]').element.value).toBe('0.1')
    await wrapper.get('[data-testid="account-cost-multiplier"]').setValue(0.25)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent'); await flushPromises()
    expect(updateAccountMock).toHaveBeenCalledWith(1, expect.objectContaining({ rate_multiplier: 1, group_rate_multiplier: 5, extra: expect.objectContaining({ cost_multiplier: 0.25 }) }))
  })
  it('preserves an unchanged zero cost while billing rate sync stays enabled', async () => {
    const account = { ...buildAccount(), extra: { cost_multiplier: 0, upstream_billing_probe_enabled: true, upstream_billing_rate_sync_enabled: true } }
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-cost-multiplier"]').element.value).toBe('0')
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-cost-multiplier"]').element.disabled).toBe(false)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent'); await flushPromises()
    const payload = updateAccountMock.mock.calls[0][1]
    expect(payload.extra.cost_multiplier).toBeUndefined()
    expect(payload).not.toHaveProperty('rate_multiplier')
  })
  it('shows the saved upstream cost and does not resend it on unrelated edits', async () => {
    const account = { ...buildAccount(), extra: { cost_multiplier: 0.14 } }
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-cost-multiplier"]').element.value).toBe('0.14')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls.at(-1)?.[1]?.extra?.cost_multiplier).toBeUndefined()
    await wrapper.get('[data-testid="account-cost-multiplier"]').setValue(0)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls.at(-1)?.[1]?.extra?.cost_multiplier).toBe(0)
  })
  it('lets an operator disable upstream cost syncing and save the actual recharge cost', async () => {
    const account = { ...buildAccount(), extra: { cost_multiplier: 1, upstream_billing_probe_enabled: true } }
    const wrapper = mountModal(account)
    expect(wrapper.get('[data-testid="account-cost-auto-sync"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="account-cost-auto-sync"]').trigger('click')
    await wrapper.get('[data-testid="account-cost-multiplier"]').setValue(0.2)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toMatchObject({
      cost_multiplier: 0.2, cost_multiplier_auto_sync: false
    })
    expect(wrapper.get('[data-testid="upstream-billing-auto-probe"]').attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })
  it('preserves manual mode on unrelated edits and allows opting back into upstream costs', async () => {
    const account = { ...buildAccount(), extra: { cost_multiplier: 0.2, cost_multiplier_auto_sync: false } }
    const wrapper = mountModal(account)
    expect(wrapper.get('[data-testid="account-cost-auto-sync"]').attributes('aria-checked')).toBe('false')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('cost_multiplier_auto_sync')
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).not.toHaveProperty('cost_multiplier')
    await wrapper.get('[data-testid="account-cost-auto-sync"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock.mock.calls.at(-1)?.[1]?.extra?.cost_multiplier_auto_sync).toBe(true)
    wrapper.unmount()
  })
  it('rejects a negative cost before saving', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="account-cost-multiplier"]').setValue(-1)
    await wrapper.get('form#edit-account-form').trigger('submit.prevent'); await flushPromises()
    expect(updateAccountMock).not.toHaveBeenCalled()
  })
})

describe('Excel BPS default template integration', () => {
  it('preselects the saved template and persists it with unrelated account options', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { unrelated: 'keep' }
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    vi.mocked(getAutoConfig).mockResolvedValueOnce({ excel_bps: { ...defaultExcelBPSDefaults(), models: ['my-bps-model'] } } as Awaited<ReturnType<typeof getAutoConfig>>)
    const wrapper = mountModal(account)
    await wrapper.get('[data-testid="excel-bps-defaults-toggle"]').trigger('click')
    await flushPromises()
    for (const field of ['ignore-encrypted-content', 'auto-disable-on-403', 'cache-creation-as-input']) {
      expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-' + field + '"]').element.checked).toBe(true)
    }
    await wrapper.get('form#edit-account-form').trigger('submit.prevent'); await flushPromises()
    expect(updateAccountMock.mock.calls[0]?.[1]?.extra).toMatchObject({
      unrelated: 'keep', openai_excel_bps: true, openai_excel_bps_config_mode: 'defaults', openai_excel_bps_models: ['my-bps-model'],
      openai_excel_bps_ignore_encrypted_content: true, openai_excel_bps_auto_disable_on_403: true,
      openai_excel_bps_cache_creation_as_input: true
    })
    const reads = vi.mocked(getAutoConfig).mock.calls.length
    await wrapper.setProps({ account: { ...account, extra: updateAccountMock.mock.calls[0]?.[1]?.extra } })
    expect(wrapper.get('[data-testid="excel-bps-defaults-toggle"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[data-testid="excel-bps-toggle"]').attributes('aria-checked')).toBe('false')
    expect(getAutoConfig).toHaveBeenCalledTimes(reads)
  })

  it('preserves saved account settings when opening and explicitly reapplies defaults on request', async () => {
    const account = buildAccount()
    account.type = 'oauth'
    account.extra = { openai_excel_bps: true, openai_excel_bps_models: ['legacy-model'] }
    vi.mocked(getAutoConfig).mockClear().mockResolvedValueOnce({ excel_bps: defaultExcelBPSDefaults() } as Awaited<ReturnType<typeof getAutoConfig>>)
    const wrapper = mountModal(account)
    expect(getAutoConfig).not.toHaveBeenCalled()
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-ignore-encrypted-content"]').element.checked).toBe(false)
    await wrapper.get('[data-testid="excel-bps-defaults-toggle"]').trigger('click'); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="excel-bps-ignore-encrypted-content"]').element.checked).toBe(true)
  })


})

describe('EditAccountModal gemini explicit cache upstream switch', () => {
  function buildGeminiApiKeyAccount(credentials: Record<string, unknown>) {
    return {
      id: 21,
      name: 'Gemini upstream gateway',
      notes: '',
      platform: 'gemini',
      type: 'apikey',
      credentials: { api_key: 'sk-upstream', ...credentials },
      credentials_status: { has_api_key: true },
      extra: {},
      proxy_id: null,
      concurrency: 1,
      priority: 1,
      rate_multiplier: 1,
      status: 'active',
      group_ids: [],
      expires_at: null,
      auto_pause_on_expired: false
    } as any
  }

  beforeEach(() => {
    authIsSimpleMode.value = true
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  it('hides the switch for the official AI Studio address', () => {
    const wrapper = mountModal(buildGeminiApiKeyAccount({ base_url: 'https://generativelanguage.googleapis.com' }))
    expect(wrapper.find('[data-testid="explicit-cache-upstream-section"]').exists()).toBe(false)
  })

  it('loads and saves the switch and ttl limit for a custom upstream', async () => {
    const account = buildGeminiApiKeyAccount({
      base_url: 'https://upstream.example.com',
      explicit_cache_upstream: true,
      explicit_cache_upstream_max_ttl_seconds: 1800
    })
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)

    expect(wrapper.find('[data-testid="explicit-cache-upstream-section"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="explicit-cache-upstream-toggle"]').attributes('aria-checked')).toBe('true')
    const ttlInput = wrapper.get('[data-testid="explicit-cache-upstream-max-ttl"]')
    expect((ttlInput.element as HTMLInputElement).value).toBe('1800')

    await ttlInput.setValue('3600')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials).toMatchObject({
      explicit_cache_upstream: true,
      explicit_cache_upstream_max_ttl_seconds: 3600
    })
  })

  it('removes both fields when the switch is turned off', async () => {
    const account = buildGeminiApiKeyAccount({
      base_url: 'https://upstream.example.com',
      explicit_cache_upstream: true,
      explicit_cache_upstream_max_ttl_seconds: 1800
    })
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)

    await wrapper.get('[data-testid="explicit-cache-upstream-toggle"]').trigger('click')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    const credentials = updateAccountMock.mock.calls[0]?.[1]?.credentials
    expect(credentials).not.toHaveProperty('explicit_cache_upstream')
    expect(credentials).not.toHaveProperty('explicit_cache_upstream_max_ttl_seconds')
  })
})

describe('EditAccountModal temp-unschedulable rule scope', () => {
  function buildAccountWithTempUnschedRules(rules: Array<Record<string, unknown>>) {
    const account = buildAccount()
    return {
      ...account,
      credentials: {
        ...account.credentials,
        temp_unschedulable_enabled: true,
        temp_unschedulable_rules: rules
      }
    } as any
  }

  const checkedScopes = (wrapper: ReturnType<typeof mountModal>) =>
    wrapper
      .findAll('[data-testid^="temp-unsched-scope-"][aria-checked="true"]')
      .map((radio) => radio.attributes('data-testid'))

  const savedRules = () =>
    updateAccountMock.mock.calls[0]?.[1]?.credentials?.temp_unschedulable_rules

  beforeEach(() => {
    authIsSimpleMode.value = true
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  it('shows rules without scope the way the backend treats them and keeps that on save', async () => {
    const account = buildAccountWithTempUnschedRules([
      { error_code: 401, keywords: ['unauthorized'], duration_minutes: 10 },
      { error_code: 503, keywords: ['unavailable'], duration_minutes: 30 }
    ])
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)

    expect(checkedScopes(wrapper)).toEqual(['temp-unsched-scope-account', 'temp-unsched-scope-model'])

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    expect(savedRules().map((rule: { scope: string }) => rule.scope)).toEqual(['account', 'model'])
    wrapper.unmount()
  })

  it('respects an explicit scope and saves the card the operator picks', async () => {
    const account = buildAccountWithTempUnschedRules([
      { error_code: 401, keywords: ['unauthorized'], duration_minutes: 10, scope: 'model' },
      { error_code: 503, keywords: ['unavailable'], duration_minutes: 30, scope: ' Account ' }
    ])
    updateAccountMock.mockResolvedValue(account)
    const wrapper = mountModal(account)

    expect(checkedScopes(wrapper)).toEqual(['temp-unsched-scope-model', 'temp-unsched-scope-account'])

    await wrapper.findAll('[data-testid="temp-unsched-scope-model"]')[1].trigger('click')
    expect(checkedScopes(wrapper)).toEqual(['temp-unsched-scope-model', 'temp-unsched-scope-model'])

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(savedRules().map((rule: { scope: string }) => rule.scope)).toEqual(['model', 'model'])
    wrapper.unmount()
  })

  it('defaults a newly added rule to model scope', async () => {
    const wrapper = mountModal(buildAccountWithTempUnschedRules([]))

    const addRule = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.accounts.tempUnschedulable.addRule'))
    await addRule!.trigger('click')

    expect(checkedScopes(wrapper)).toEqual(['temp-unsched-scope-model'])
    wrapper.unmount()
  })
})


describe('OpenAI connection account type boundary in the actual editor', () => {
  const originalAdapter = apiClient.defaults.adapter
  const connectionRequests: string[] = []
  beforeEach(() => {
    connectionRequests.length = 0
    apiClient.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
      if (config.url?.includes('/auth-connections')) connectionRequests.push(config.url)
      return { config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, message: '', data: [] } }
    }
  })
  afterEach(() => { apiClient.defaults.adapter = originalAdapter })

  it('never mounts or fetches connections for API key accounts, including type and dialog changes', async () => {
    const account = { ...buildAccount(), credentials: { access_token: 'synthetic-misleading-oauth', api_key: 'synthetic-key' }, extra: { openai_connections_managed: true } }
    const wrapper = mountModal(account)
    await flushPromises()
    expect(wrapper.find('[data-testid="auth-connections"]').exists()).toBe(false)
    expect(connectionRequests).toEqual([])
    await wrapper.setProps({ show: false }); await wrapper.setProps({ show: true, account: { ...account, id: 43 } }); await flushPromises()
    expect(connectionRequests).toEqual([])
    await wrapper.setProps({ account: { ...account, type: 'oauth' } }); await flushPromises()
    expect(wrapper.find('[data-testid="auth-connections"]').exists()).toBe(true)
    expect(connectionRequests).toEqual(['/admin/accounts/1/auth-connections'])
    await wrapper.setProps({ account }); await flushPromises()
    expect(wrapper.find('[data-testid="auth-connections"]').exists()).toBe(false)
    await wrapper.setProps({ account: { ...account, id: 44 } }); await flushPromises()
    expect(connectionRequests).toHaveLength(1)
  })

  it('keeps OAuth connections available and excludes shadows and other platforms', async () => {
    const wrapper = mountModal(buildOpenAIOAuthParentAccount())
    await flushPromises()
    expect(wrapper.find('[data-testid="auth-connections"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="auth-connections"] option').map(o => o.attributes('value'))).toEqual(['codex', 'bps', 'siwc'])
    expect(connectionRequests).toEqual(['/admin/accounts/7/auth-connections'])
    await wrapper.setProps({ account: buildOpenAISparkShadowAccount() }); await flushPromises()
    expect(wrapper.find('[data-testid="auth-connections"]').exists()).toBe(false)
    await wrapper.setProps({ account: { ...buildAccount(), platform: 'anthropic', type: 'oauth' } }); await flushPromises()
    expect(wrapper.find('[data-testid="auth-connections"]').exists()).toBe(false)
    expect(connectionRequests).toHaveLength(1)
  })

  it('keeps the Anthropic API key Claude Code mimic switch exclusive with strip identity', async () => {
    const account = {
      ...buildAccount(),
      id: 21,
      name: 'Claude Code only relay',
      platform: 'anthropic',
      type: 'apikey',
      credentials: { api_key: 'sk-relay', base_url: 'https://relay.example.com' },
      extra: { anthropic_mimic_claude_code: true, anthropic_mimic_claude_code_deduct_tokens: true, unrelated: 'preserve' }
    } as any
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await flushPromises()

    const mimicToggle = () => wrapper.get('[data-testid="anthropic-mimic-claude-code-toggle"]')
    const stripToggle = () => wrapper.findAll('div.flex.items-center.justify-between')
      .find(row => row.text().includes('admin.accounts.anthropic.stripClaudeCodeIdentityDesc'))!
      .get('button')
    expect(mimicToggle().classes()).toContain('bg-primary-600')
    expect(stripToggle().classes()).not.toContain('bg-primary-600')

    await stripToggle().trigger('click')
    await flushPromises()
    expect(stripToggle().classes()).toContain('bg-primary-600')
    expect(mimicToggle().classes()).not.toContain('bg-primary-600')
    await wrapper.get('#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const stripExtra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(stripExtra).toMatchObject({ anthropic_strip_claude_code_identity: true, unrelated: 'preserve' })
    expect(stripExtra).not.toHaveProperty('anthropic_mimic_claude_code')
    expect(stripExtra).not.toHaveProperty('anthropic_mimic_claude_code_deduct_tokens')

    await mimicToggle().trigger('click')
    await flushPromises()
    expect(stripToggle().classes()).not.toContain('bg-primary-600')
    await wrapper.get('#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const mimicExtra = updateAccountMock.mock.calls[1]?.[1]?.extra
    expect(mimicExtra).toMatchObject({ anthropic_mimic_claude_code: true, unrelated: 'preserve' })
    expect(mimicExtra).not.toHaveProperty('anthropic_strip_claude_code_identity')
    expect(mimicExtra).not.toHaveProperty('anthropic_mimic_claude_code_deduct_tokens')
  })

  it.each([false, true])('restores and changes Anthropic estimated deduction from %s', async (deduct) => {
    const account = {
      ...buildAccount(),
      id: 23,
      platform: 'anthropic',
      type: 'apikey',
      credentials: { api_key: 'fixture-key', base_url: 'https://relay.example.com' },
      extra: { anthropic_mimic_claude_code: true, anthropic_mimic_claude_code_deduct_tokens: deduct, unrelated: 'preserve' }
    } as any
    updateAccountMock.mockReset().mockResolvedValue(account)
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
    const wrapper = mountModal(account)
    await flushPromises()
    const toggle = wrapper.get('[data-testid="anthropic-mimic-claude-code-deduct-tokens-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe(String(deduct))
    await toggle.trigger('click')
    await wrapper.get('#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    const extra = updateAccountMock.mock.calls[0]?.[1]?.extra
    expect(extra).toMatchObject({ anthropic_mimic_claude_code: true, unrelated: 'preserve' })
    if (deduct) {
      expect(extra).not.toHaveProperty('anthropic_mimic_claude_code_deduct_tokens')
    } else {
      expect(extra.anthropic_mimic_claude_code_deduct_tokens).toBe(true)
    }
  })

  it('does not restore the Claude Code mimic switch when strip identity is also stored', async () => {
    const account = {
      ...buildAccount(),
      id: 22,
      platform: 'anthropic',
      type: 'apikey',
      credentials: { api_key: 'sk-relay', base_url: 'https://relay.example.com' },
      extra: { anthropic_mimic_claude_code: true, anthropic_strip_claude_code_identity: true }
    } as any
    const wrapper = mountModal(account)
    await flushPromises()
    expect(wrapper.get('[data-testid="anthropic-mimic-claude-code-toggle"]').classes()).not.toContain('bg-primary-600')
  })
})
