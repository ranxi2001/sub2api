import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { createPinia } from 'pinia'
import { config, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import SecurityAuditNav from '@/components/admin/security/SecurityAuditNav.vue'
import type { DOMWrapper, VueWrapper } from '@vue/test-utils'

import RiskControlView from '../RiskControlView.vue'
import type { ContentModerationAPIKeyStatus, ContentModerationConfig, UpdateContentModerationConfig } from '@/api/admin/riskControl'

const {
  getConfig,
  updateConfig,
  getStatus,
  listLogs,
  getGroups,
  getProxies,
  testAPIKeys,
  showError,
  showSuccess,
  listModerationUsers,
} = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
  getStatus: vi.fn(),
  listLogs: vi.fn(),
  getGroups: vi.fn(),
  getProxies: vi.fn(),
  testAPIKeys: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  listModerationUsers: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    riskControl: {
      getConfig,
      updateConfig,
      getStatus,
      listLogs,
      testAPIKeys,
      deleteFlaggedHash: vi.fn(),
      clearFlaggedHashes: vi.fn(),
      unbanUser: vi.fn(),
    },
    groups: {
      getAll: getGroups,
    },
    proxies: {
      getAll: getProxies,
    },
  },
}))

// Exercise the real summary while keeping its API calls offline.
vi.mock('@/api/admin/riskControl', () => ({
  listModerationUsers,
  getModerationUser: vi.fn(),
  manageModerationUser: vi.fn(),
}))
config.global.stubs.RouterLink = RouterLinkStub
config.global.plugins = [...(config.global.plugins ?? []), createPinia()]

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_err: unknown, fallback: string) => fallback,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.riskControl.preBlockAPIKeyLoadSummary') {
          return `同步并发 ${params?.active} / 可用 Key ${params?.available}，累计 ${params?.total} 次，worker：${params?.workerActive} / ${params?.workerTotal}`
        }
        return key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`))
      },
    }),
  }
})

const baseConfig = (): ContentModerationConfig => ({
  enabled: true,
  mode: 'pre_block',
  base_url: 'https://api.openai.com',
  model: 'omni-moderation-latest',
  proxy_id: null,
  api_key_configured: false,
  api_key_masked: '',
  api_key_count: 0,
  api_key_masks: [],
  api_key_statuses: [],
  timeout_ms: 3000,
  sample_rate: 100,
  all_groups: true,
  group_ids: [],
  record_non_hits: false,
  worker_count: 4,
  queue_size: 32768,
  block_status: 403,
  block_message: '内容审计命中风险规则，请调整输入后重试',
  email_on_hit: true,
  auto_ban_enabled: true,
  ban_threshold: 10,
  violation_window_hours: 720,
  retry_count: 2,
  hit_retention_days: 180,
  non_hit_retention_days: 3,
  pre_hash_check_enabled: false,
  blocked_keywords: [],
  keyword_blocking_mode: 'keyword_and_api',
  thresholds: {
    harassment: 0.98,
    sexual: 0.65,
  },
  model_filter: {
    type: 'all',
    models: [],
  },
})

const runtimeStatus = () => ({
  enabled: true,
  risk_control_enabled: true,
  mode: 'pre_block',
  worker_count: 4,
  max_workers: 32,
  active_workers: 0,
  idle_workers: 4,
  queue_size: 32768,
  queue_length: 0,
  queue_usage_percent: 0,
  enqueued: 0,
  dropped: 0,
  processed: 0,
  errors: 0,
  pre_block_active: 0,
  pre_block_checked: 0,
  pre_block_allowed: 0,
  pre_block_blocked: 0,
  pre_block_errors: 0,
  pre_block_avg_latency_ms: 0,
  pre_block_api_key_active: 0,
  pre_block_api_key_available_count: 0,
  pre_block_api_key_total_calls: 0,
  pre_block_api_key_loads: [],
  api_key_statuses: [],
  flagged_hash_count: 0,
  last_cleanup_deleted_hit: 0,
  last_cleanup_deleted_non_hit: 0,
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})
const ModelWhitelistSelectorStub = defineComponent({
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    const onInput = (event: Event) => {
      const value = (event.target as HTMLInputElement).value
      emit(
        'update:modelValue',
        value
          .split(/[,\n]/)
          .map((item) => item.trim())
          .filter(Boolean)
      )
    }
    return () =>
      h('input', {
        'data-test': 'model-filter-input',
        value: (props.modelValue as string[]).join('\n'),
        onInput,
      })
  },
})

function findButtonByText(wrapper: VueWrapper, text: string): DOMWrapper<HTMLButtonElement> {
  const button = wrapper.findAll<HTMLButtonElement>('button').find((item) => item.text().includes(text))
  if (!button) {
    throw new Error(`button not found: ${text}`)
  }
  return button
}

describe('admin RiskControlView', () => {
  beforeEach(() => {
    listModerationUsers.mockReset()
    listModerationUsers.mockResolvedValue({ items: [], total: 0 })
    getConfig.mockReset()
    updateConfig.mockReset()
    getStatus.mockReset()
    listLogs.mockReset()
    getGroups.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    testAPIKeys.mockReset()

    getConfig.mockResolvedValue(baseConfig())
    getStatus.mockResolvedValue(runtimeStatus())
    listLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    getGroups.mockResolvedValue([])
    getProxies.mockResolvedValue([])
    updateConfig.mockImplementation(async (payload: UpdateContentModerationConfig) => ({
      ...baseConfig(),
      ...payload,
      model_filter: payload.model_filter ?? baseConfig().model_filter,
      api_key_configured: false,
      api_key_masked: '',
      api_key_count: 0,
      api_key_masks: [],
      api_key_statuses: [],
    }))
  })

  it('delegates content moderation navigation with the current route highlighted', async () => {
    const wrapper = mount(RiskControlView, { attachTo: document.body, global: { stubs: { AppLayout: AppLayoutStub, Icon: true } } })
    await flushPromises()
    expect(wrapper.getComponent(SecurityAuditNav).props('active')).toBe('/admin/risk-control')
    wrapper.unmount()
  })

  it('mounts and queries the user list only on first expansion and retains drafts across repeated collapse', async () => {
    const wrapper = mount(RiskControlView, { attachTo: document.body, global: { stubs: { AppLayout: AppLayoutStub, Icon: true } } })
    await flushPromises()
    const toggle = wrapper.get<HTMLButtonElement>('[data-test="moderation-users-toggle"]')
    expect(toggle.element.tagName).toBe('BUTTON') // Native Enter/Space activation.
    expect(toggle.attributes('type')).toBe('button')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-test="moderation-user-summary"]').exists()).toBe(false)
    expect(listModerationUsers).not.toHaveBeenCalled()
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-expanded')).toBe('true')
    const region = wrapper.get(`#${toggle.attributes('aria-controls')}`)
    expect(region.attributes('aria-labelledby')).toBe(toggle.attributes('id'))
    const summary = wrapper.get('[data-test="moderation-user-summary"]')
    const originalNode = summary.element
    await summary.get('input').setValue('306') // Unsubmitted search draft.
    for (let i = 0; i < 3; i++) {
      await toggle.trigger('click')
      expect(toggle.attributes('aria-expanded')).toBe('false')
      expect(summary.isVisible()).toBe(false)
      await toggle.trigger('click'); await flushPromises()
      expect(summary.isVisible()).toBe(true)
      expect(summary.element).toBe(originalNode)
      expect(summary.get('input').element).toHaveProperty('value', '306')
    }
    expect(listModerationUsers).toHaveBeenCalledTimes(1)
    expect(updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('saves each key URL inline with readback, cancellation, validation and errors', async () => {
    const rows = ['row-a', 'row-b'].map((key_hash, i) => ({
      index: i + 1, key_hash, masked: '********fixture', status: 'frozen' as const,
      failure_count: 1, success_count: 0, last_error: 'typesafe API status 401',
      last_latency_ms: 10, last_http_status: 401, last_tested: false, configured: true,
      base_url: `https://relay-${i}.example/v1`,
    }))
    let persisted = { ...baseConfig(), engine: 'typesafe' as const, api_key_statuses: rows }
    getConfig.mockImplementation(async () => structuredClone(persisted))
    getStatus.mockResolvedValue({ ...runtimeStatus(), engine: 'typesafe', api_key_statuses: rows })
    updateConfig.mockImplementation(async (payload) => {
      const change = payload.engine_configs.typesafe.key_url_updates[0]
      persisted = { ...persisted, api_key_statuses: persisted.api_key_statuses.map(row => row.key_hash === change.key_hash ? { ...row, base_url: change.base_url.replace(/\/$/, '') } : row) }
      return structuredClone(persisted)
    })
    const wrapper = mount(RiskControlView, { global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    const row = (id: string) => wrapper.get(`[data-key-url="${id}"]`)
    expect(row('row-a').text()).toContain(rows[0].base_url)
    await row('row-a').get('[data-test="key-url-edit"]').trigger('click')
    await row('row-a').get('input').setValue('https://cancelled.example')
    await row('row-a').get('[data-test="key-url-cancel"]').trigger('click')
    expect(updateConfig).not.toHaveBeenCalled()
    await row('row-a').get('[data-test="key-url-edit"]').trigger('click')
    await row('row-a').get('input').setValue('')
    await row('row-a').get('[data-test="key-url-save"]').trigger('click')
    expect(row('row-a').get('[role="alert"]').exists()).toBe(true)
    expect(updateConfig).not.toHaveBeenCalled()
    for (const id of ['row-a', 'row-b']) {
      if (id === 'row-b') await row(id).get('[data-test="key-url-edit"]').trigger('click')
      await row(id).get('input').setValue(`https://${id}.example/v1/`)
      await row(id).get('[data-test="key-url-save"]').trigger('click')
      expect(row(id).get('[data-test="key-url-save"]').attributes('disabled')).toBeDefined()
      await flushPromises()
      expect(row(id).text()).toContain(`https://${id}.example/v1`)
    }
    expect(updateConfig).toHaveBeenNthCalledWith(1, { engine_configs: { typesafe: { key_url_updates: [{ key_hash: 'row-a', base_url: 'https://row-a.example/v1/', expected_base_url: rows[0].base_url }] } } })
    expect(row('row-a').text()).toContain('https://row-a.example/v1')
    expect(getConfig.mock.calls.length).toBeGreaterThanOrEqual(3)
    updateConfig.mockRejectedValueOnce({ response: { data: { message: 'fixture save failure' } } })
    await row('row-a').get('[data-test="key-url-edit"]').trigger('click')
    await row('row-a').get('input').setValue('https://failed.example')
    await row('row-a').get('[data-test="key-url-save"]').trigger('click')
    await flushPromises()
    expect(row('row-a').get('[role="alert"]').text()).toContain('admin.riskControl.keyUrlSaveFailed')
    expect(row('row-b').text()).toContain('https://row-b.example/v1')
    // A write without successful readback must not show a success confirmation.
    getConfig.mockRejectedValueOnce(new Error('fixture readback failure'))
    await row('row-a').get('[data-test="key-url-save"]').trigger('click')
    await flushPromises()
    expect(row('row-a').get('[role="alert"]').exists()).toBe(true)
    expect(row('row-a').find('[role="status"]').exists()).toBe(false)
    expect(row('row-a').find('input').exists()).toBe(true)
    getConfig.mockResolvedValueOnce({ ...persisted, api_key_statuses: rows })
    await row('row-a').get('[data-test="key-url-save"]').trigger('click')
    await flushPromises()
    expect(row('row-a').get('[role="alert"]').exists()).toBe(true)
    expect(row('row-a').find('[role="status"]').exists()).toBe(false)
    await row('row-a').get('[data-test="key-url-cancel"]').trigger('click')
    wrapper.unmount()
  })

  it.each(['openai', 'typesafe'] as const)('shows the selected draft engine keys while %s remains active', async (activeEngine) => {
    const openaiKey: ContentModerationAPIKeyStatus = {
      index: 1, key_hash: 'openai-hash', masked: '********oa01', status: 'frozen',
      failure_count: 1, success_count: 0, last_error: '', last_latency_ms: 100,
      last_http_status: 429, last_tested: false, configured: true,
    }
    const typesafeKey: ContentModerationAPIKeyStatus = { ...openaiKey, key_hash: 'typesafe-hash', masked: '********ts02', status: 'ok', failure_count: 0, success_count: 1, last_http_status: 200 }
    const profiles = {
      openai: { ...baseConfig(), engine: 'openai' as const, api_key_configured: true, api_key_count: 1, api_key_statuses: [openaiKey] },
      typesafe: { ...baseConfig(), engine: 'typesafe' as const, api_key_configured: true, api_key_count: 1, api_key_statuses: [typesafeKey] },
    }
    const otherEngine = activeEngine === 'openai' ? 'typesafe' : 'openai'
    getConfig.mockResolvedValue({ ...profiles[activeEngine], engine_configs: profiles })
    getStatus.mockResolvedValue({ ...runtimeStatus(), engine: activeEngine, api_key_statuses: profiles[activeEngine].api_key_statuses })
    const wrapper = mount(RiskControlView, { global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    const engine = () => wrapper.findComponent('[data-test="audit-engine-select"]')
    const keyPanel = () => wrapper.get('[data-test="audit-key-statuses"]')
    expect(keyPanel().text()).toContain(profiles[activeEngine].api_key_statuses[0].masked)
    engine().vm.$emit('update:modelValue', otherEngine)
    await flushPromises()
    expect(keyPanel().text()).toContain(profiles[otherEngine].api_key_statuses[0].masked)
    expect(keyPanel().text()).not.toContain(profiles[activeEngine].api_key_statuses[0].masked)
    expect(keyPanel().text()).toContain(otherEngine === 'openai' ? 'admin.riskControl.apiKeyStatusFrozen' : 'admin.riskControl.apiKeyStatusOk')
    await findButtonByText(wrapper, 'admin.riskControl.refreshStatus').trigger('click')
    await flushPromises()
    expect(keyPanel().text()).toContain(profiles[otherEngine].api_key_statuses[0].masked)
    expect(keyPanel().text()).not.toContain(profiles[activeEngine].api_key_statuses[0].masked)
    engine().vm.$emit('update:modelValue', activeEngine)
    await flushPromises()
    expect(keyPanel().text()).toContain(profiles[activeEngine].api_key_statuses[0].masked)
    expect(keyPanel().text()).not.toContain(profiles[otherEngine].api_key_statuses[0].masked)
    expect(updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows an empty key panel for an unconfigured draft engine', async () => {
    getStatus.mockResolvedValue({ ...runtimeStatus(), api_key_statuses: [{ masked: '********oa01', configured: true, status: 'ok' }] })
    const wrapper = mount(RiskControlView, { global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    wrapper.findComponent('[data-test="audit-engine-select"]').vm.$emit('update:modelValue', 'typesafe')
    await flushPromises()
    expect(wrapper.get('[data-test="audit-key-statuses"]').text()).toContain('admin.riskControl.apiKeyHealthEmpty')
    expect(wrapper.get('[data-test="audit-key-statuses"]').text()).not.toContain('********oa01')
    expect(updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('edits the TypeSafe URL from key availability and retains it after save and reload', async () => {
    const frozenKey: ContentModerationAPIKeyStatus = {
      index: 1, key_hash: 'fixture-hash', masked: '********ts01', status: 'frozen',
      failure_count: 1, success_count: 0, last_error: 'typesafe API status 401',
      last_latency_ms: 10, last_http_status: 401, last_tested: true, configured: true,
      frozen_until: '2099-01-01T00:00:00Z',
    }
    let saved: ContentModerationConfig = {
      ...baseConfig(), engine: 'typesafe', base_url: 'https://old.example/v1', model: 'jev-latest',
      api_key_configured: true, api_key_count: 1, api_key_statuses: [frozenKey],
    }
    getConfig.mockImplementation(async () => saved)
    getStatus.mockResolvedValue({ ...runtimeStatus(), engine: 'typesafe', api_key_statuses: [frozenKey] })
    updateConfig.mockImplementation(async (payload: UpdateContentModerationConfig) => {
      saved = { ...saved, base_url: payload.engine_configs!.typesafe!.base_url! }
      return saved
    })
    const createWrapper = () => mount(RiskControlView, { attachTo: document.body, global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    let wrapper = createWrapper()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    expect(wrapper.get('[data-test="audit-key-statuses"]').text()).toContain('401')
    await wrapper.get('[data-test="audit-edit-base-url"]').trigger('click')
    expect(document.activeElement).toBe(wrapper.get('[data-test="audit-base-url"]').element)
    expect(wrapper.get('[data-test="audit-base-url-hint"]').text()).toContain('admin.riskControl.typeSafeBaseUrlHint')
    await wrapper.get('[data-test="audit-base-url"]').setValue('https://new.example/relay/v1/')
    wrapper.findComponent('[data-test="audit-engine-select"]').vm.$emit('update:modelValue', 'openai')
    await flushPromises()
    wrapper.findComponent('[data-test="audit-engine-select"]').vm.$emit('update:modelValue', 'typesafe')
    await flushPromises()
    expect((wrapper.get('[data-test="audit-base-url"]').element as HTMLInputElement).value).toBe('https://new.example/relay/v1/')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()
    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({ engine_configs: expect.objectContaining({
      typesafe: expect.objectContaining({ base_url: 'https://new.example/relay/v1/', clear_api_key: false, delete_api_key_hashes: [] }),
    }) }))
    wrapper.unmount()
    wrapper = createWrapper()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    expect((wrapper.get('[data-test="audit-base-url"]').element as HTMLInputElement).value).toBe('https://new.example/relay/v1/')
    expect(wrapper.get('[data-test="audit-key-statuses"]').text()).toContain('admin.riskControl.apiKeyStatusFrozen')
    expect(testAPIKeys).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('preserves both engine drafts and saves thresholds independently', async () => {
    const wrapper = mount(RiskControlView, { global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await wrapper.get('[data-test="audit-base-url"]').setValue('https://openai-edited.example')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    await wrapper.get('[data-test="risk-threshold-sexual"]').setValue('73')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.basic').trigger('click')
    const engine = () => wrapper.findComponent('[data-test="audit-engine-select"]')
    engine().vm.$emit('update:modelValue', 'typesafe')
    await flushPromises()
    expect((wrapper.get('[data-test="audit-model"]').element as HTMLInputElement).value).toBe('jev-latest')
    await wrapper.get('[data-test="audit-base-url"]').setValue('https://typesafe-edited.example')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    expect((wrapper.get('[data-test="risk-threshold-sexual"]').element as HTMLInputElement).value).toBe('65')
    await wrapper.get('[data-test="risk-threshold-sexual"]').setValue('91')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.basic').trigger('click')
    engine().vm.$emit('update:modelValue', 'openai')
    await flushPromises()
    expect((wrapper.get('[data-test="audit-base-url"]').element as HTMLInputElement).value).toBe('https://openai-edited.example')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()
    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({ engine: 'openai', engine_configs: {
      openai: expect.objectContaining({ base_url: 'https://openai-edited.example', thresholds: expect.objectContaining({ sexual: 0.73 }) }),
      typesafe: expect.objectContaining({ base_url: 'https://typesafe-edited.example', thresholds: expect.objectContaining({ sexual: 0.91 }) }),
    } }))
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('tests the draft engine without switching the active engine', async () => {
    testAPIKeys.mockResolvedValue({ items: [{ status: 'ok', configured: false }], image_count: 0 })
    const wrapper = mount(RiskControlView, { global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    wrapper.findComponent('[data-test="audit-engine-select"]').vm.$emit('update:modelValue', 'typesafe')
    await flushPromises()
    await wrapper.get('textarea[autocomplete="new-password"]').setValue('test-only-key')
    await findButtonByText(wrapper, 'admin.riskControl.testInputApiKeys').trigger('click')
    await flushPromises()
    expect(testAPIKeys).toHaveBeenCalledWith(expect.objectContaining({ engine: 'typesafe', api_keys: ['test-only-key'], model: 'jev-latest', thresholds: expect.objectContaining({ sexual: 0.65 }) }))
    expect(updateConfig).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="active-audit-engine"]').text()).not.toContain('TypeSafe AI')
    wrapper.unmount()
  })

  it.each(['openai', 'typesafe'] as const)('restores the shared defaults for %s without replacing saved thresholds on load', async (engine) => {
    getConfig.mockResolvedValue({ ...baseConfig(), engine, thresholds: { sexual: 0.8, harassment: 0.8 } })
    const wrapper = mount(RiskControlView, { global: { stubs: { AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true, Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true } } })
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    expect((wrapper.get('[data-test="risk-threshold-sexual"]').element as HTMLInputElement).value).toBe('80')
    await findButtonByText(wrapper, 'admin.riskControl.riskThresholdReset').trigger('click')
    const defaults = {
      harassment: 98, 'harassment/threatening': 90, hate: 65, 'hate/threatening': 65,
      illicit: 95, 'illicit/violent': 95, 'self-harm': 65, 'self-harm/intent': 85,
      'self-harm/instructions': 65, sexual: 65, 'sexual/minors': 65, violence: 95, 'violence/graphic': 95,
    }
    for (const [category, value] of Object.entries(defaults)) {
      expect((wrapper.get(`[data-test="risk-threshold-${category}"]`).element as HTMLInputElement).value).toBe(String(value))
    }
    if (engine === 'typesafe') expect(wrapper.text()).toContain('admin.riskControl.typeSafeThresholds')
    expect(updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('saves the selected model filter mode and models', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.modelFilterInclude').trigger('click')
    await wrapper.get('[data-test="model-filter-input"]').setValue('gpt-5.5, gpt-5.4')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      model_filter: {
        type: 'include',
        models: ['gpt-5.5', 'gpt-5.4'],
      },
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('submits edited risk control thresholds when saving moderation config', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    await wrapper.get('[data-test="risk-threshold-sexual"]').setValue('72')
    await wrapper.get('[data-test="risk-threshold-harassment"]').setValue('99')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      thresholds: expect.objectContaining({
        sexual: 0.72,
        harassment: 0.99,
      }),
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('describes worker runtime as async audit and pre-block record processing', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      mode: 'observe',
      processed: 12,
      queue_length: 2,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.workerStatusHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('12')
    expect(wrapper.text()).toContain('2 / 32,768')
  })

  it('shows pre-block synchronous moderation metrics separately from worker queue', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      pre_block_active: 2,
      pre_block_checked: 128,
      pre_block_allowed: 120,
      pre_block_blocked: 8,
      pre_block_errors: 1,
      pre_block_avg_latency_ms: 86,
      pre_block_api_key_active: 2,
      pre_block_api_key_available_count: 2,
      pre_block_api_key_total_calls: 128,
      active_workers: 3,
      worker_count: 7,
      pre_block_api_key_loads: [
        {
          index: 0,
          key_hash: 'hash-one',
          masked: 'sk-...one',
          status: 'ok',
          active: 1,
          total: 72,
          success: 70,
          errors: 2,
          avg_latency_ms: 84,
          last_latency_ms: 80,
          last_http_status: 200,
        },
        {
          index: 1,
          key_hash: 'hash-two',
          masked: 'sk-...two',
          status: 'ok',
          active: 1,
          total: 56,
          success: 56,
          errors: 0,
          avg_latency_ms: 90,
          last_latency_ms: 92,
          last_http_status: 200,
        },
      ],
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.workerStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('128')
    expect(wrapper.text()).toContain('120')
    expect(wrapper.text()).toContain('8')
    expect(wrapper.text()).toContain('86 ms')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockAPIKeyLoad')
    expect(wrapper.text()).toContain('sk-...one')
    expect(wrapper.text()).toContain('sk-...two')
    expect(wrapper.text()).toContain('72')
    expect(wrapper.text()).toContain('56')
    expect(wrapper.text()).toContain('同步并发 2 / 可用 Key 2，累计 128 次，worker：3 / 7')

    const runtimeCards = wrapper.get('[data-test="pre-block-runtime-cards"]')
    const syncCard = wrapper.get('[data-test="pre-block-sync-card"]')
    const apiKeyLoadCard = wrapper.get('[data-test="pre-block-api-key-load-card"]')

    expect(runtimeCards.classes()).toEqual(expect.arrayContaining([
      'grid',
      'grid-cols-1',
      'xl:grid-cols-[minmax(0,520px)_minmax(0,1fr)]',
    ]))
    expect(syncCard.element.parentElement).toBe(runtimeCards.element)
    expect(apiKeyLoadCard.element.parentElement).toBe(runtimeCards.element)
    expect(syncCard.classes()).toContain('card')
    expect(apiKeyLoadCard.classes()).toContain('card')
    expect(syncCard.get('h2').text()).toBe('admin.riskControl.preBlockSyncStatus')
    expect(syncCard.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(apiKeyLoadCard.get('h2').text()).toBe('admin.riskControl.preBlockAPIKeyLoad')
    expect(apiKeyLoadCard.text()).toContain('admin.riskControl.preBlockAPIKeyLoadHint')
    expect(wrapper.get('[data-test="pre-block-api-key-load-list"]').classes()).toEqual(expect.arrayContaining([
      'max-h-[280px]',
      'overflow-y-auto',
    ]))
  })
})

describe('TypeSafe optional category contract', () => {
  const mountView = () => mount(RiskControlView, { global: { stubs: {
    AppLayout: AppLayoutStub, BaseDialog: BaseDialogStub, Icon: true, Select: true,
    Toggle: true, Pagination: true, ModelWhitelistSelector: ModelWhitelistSelectorStub, ProxySelector: true,
  } } })

  beforeEach(() => {
    vi.clearAllMocks()
    listModerationUsers.mockResolvedValue({ items: [], total: 0 })
    getConfig.mockResolvedValue(baseConfig())
    getStatus.mockResolvedValue(runtimeStatus())
    listLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    getGroups.mockResolvedValue([])
    getProxies.mockResolvedValue([])
  })

  it('defaults off, preserves both drafts and reloads saved category switches and thresholds', async () => {
    let stored = { ...baseConfig(), engine: 'openai' as const } as ContentModerationConfig
    updateConfig.mockImplementation(async (payload: UpdateContentModerationConfig) => {
      const profiles = Object.fromEntries(Object.entries(payload.engine_configs!).map(([engine, profile]) => [engine, { ...baseConfig(), ...profile, engine }]))
      stored = { ...baseConfig(), ...profiles[payload.engine!], engine: payload.engine!, engine_configs: profiles } as ContentModerationConfig
      return stored
    })
    const wrapper = mountView()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    expect(wrapper.find('[data-test="risk-enabled-jailbreak"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-test^="risk-threshold-"]')).toHaveLength(13)
    await findButtonByText(wrapper, 'admin.riskControl.tabs.basic').trigger('click')
    const engine = () => wrapper.findComponent('[data-test="audit-engine-select"]')
    engine().vm.$emit('update:modelValue', 'typesafe')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    expect(wrapper.findAll('[data-test^="risk-threshold-"]')).toHaveLength(15)
    expect(wrapper.get<HTMLInputElement>('[data-test="risk-enabled-jailbreak"]').element.checked).toBe(false)
    expect(wrapper.get<HTMLInputElement>('[data-test="risk-enabled-control_bypass"]').element.checked).toBe(false)
    expect(wrapper.get<HTMLInputElement>('[data-test="risk-threshold-jailbreak"]').element.disabled).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-test="risk-threshold-jailbreak"]').element.value).toBe('95')
    await wrapper.get('[data-test="risk-enabled-jailbreak"]').setValue(true)
    await wrapper.get('[data-test="risk-threshold-jailbreak"]').setValue('80')
    await wrapper.get('[data-test="risk-enabled-control_bypass"]').setValue(true)
    await wrapper.get('[data-test="risk-threshold-control_bypass"]').setValue('91')
    await wrapper.get('[data-test="risk-enabled-control_bypass"]').setValue(false)
    await findButtonByText(wrapper, 'admin.riskControl.tabs.basic').trigger('click')
    engine().vm.$emit('update:modelValue', 'openai')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()
    const payload = updateConfig.mock.calls[0]![0] as UpdateContentModerationConfig
    expect(Object.keys(payload.engine_configs!.openai!.thresholds!)).toHaveLength(13)
    expect(payload.engine_configs!.openai!.category_settings).toEqual({})
    expect(payload.engine_configs!.openai!.thresholds).not.toHaveProperty('jailbreak')
    expect(payload.engine_configs!.typesafe!.category_settings).toEqual({ jailbreak: { enabled: true }, control_bypass: { enabled: false } })
    expect(payload.engine_configs!.typesafe!.thresholds).toMatchObject({ jailbreak: 0.80, control_bypass: 0.91 })
    wrapper.unmount()
    getConfig.mockResolvedValue(stored)
    const reloaded = mountView()
    await flushPromises()
    await findButtonByText(reloaded, 'admin.riskControl.openSettings').trigger('click')
    reloaded.findComponent('[data-test="audit-engine-select"]').vm.$emit('update:modelValue', 'typesafe')
    await flushPromises()
    await findButtonByText(reloaded, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    expect(reloaded.get<HTMLInputElement>('[data-test="risk-enabled-jailbreak"]').element.checked).toBe(true)
    expect(reloaded.get<HTMLInputElement>('[data-test="risk-enabled-control_bypass"]').element.checked).toBe(false)
    expect(reloaded.get<HTMLInputElement>('[data-test="risk-threshold-jailbreak"]').element.value).toBe('80')
    expect(reloaded.get<HTMLInputElement>('[data-test="risk-threshold-control_bypass"]').element.value).toBe('91')
    reloaded.unmount()
  })

  it('sends draft enablement to the test API and distinguishes disabled scores of one', async () => {
    getConfig.mockResolvedValue({ ...baseConfig(), engine: 'typesafe', category_settings: { jailbreak: { enabled: true }, control_bypass: { enabled: false } }, thresholds: { jailbreak: 0.80, control_bypass: 0.80 } })
    testAPIKeys.mockResolvedValue({ items: [], image_count: 0, audit_result: {
      engine_meta: { engine: 'typesafe', model: 'jev-fixture', rules_version: 'content-audit-15-zh-v2', skipped_images: 0 },
      flagged: true, highest_category: 'control_bypass', highest_score: 1, composite_score: 1,
      category_scores: { jailbreak: 0.80, control_bypass: 1 }, thresholds: { jailbreak: 0.80, control_bypass: 0.80 },
      category_settings: { jailbreak: { enabled: true }, control_bypass: { enabled: false } }, matched_categories: ['jailbreak'],
    } })
    const wrapper = mountView()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await wrapper.get('textarea[autocomplete="new-password"]').setValue('offline-fixture')
    await findButtonByText(wrapper, 'admin.riskControl.testInputApiKeys').trigger('click')
    await flushPromises()
    expect(testAPIKeys).toHaveBeenCalledWith(expect.objectContaining({ category_settings: { jailbreak: { enabled: true }, control_bypass: { enabled: false } }, thresholds: expect.objectContaining({ jailbreak: 0.80 }) }))
    expect(wrapper.text()).toContain('content-audit-15-zh-v2')
    expect(wrapper.text()).toContain('jailbreak · admin.riskControl.categoryHit')
    expect(wrapper.text()).toContain('control_bypass · admin.riskControl.categoryDisabled')
    wrapper.unmount()
  })

  it('renders log scores using the saved enablement and thresholds', async () => {
    listLogs.mockResolvedValue({ total: 1, page: 1, page_size: 20, pages: 1, items: [{
      id: 554, created_at: '2026-10-24T00:00:00Z', action: 'block', mode: 'pre_block', flagged: true,
      highest_category: 'control_bypass', highest_score: 1, input_excerpt: 'synthetic fixture',
      category_scores: { jailbreak: 0.80, control_bypass: 1 }, threshold_snapshot: { jailbreak: 0.80, control_bypass: 0.80 },
      engine_meta: { engine: 'typesafe', model: 'jev-fixture', rules_version: 'content-audit-15-zh-v2', skipped_images: 0,
        category_settings: { jailbreak: { enabled: true }, control_bypass: { enabled: false } }, matched_categories: ['jailbreak'] },
    }] })
    const wrapper = mountView()
    await flushPromises()
    await findButtonByText(wrapper, 'synthetic fixture').trigger('click')
    expect(wrapper.get('[data-test="audit-engine-meta"]').text()).toContain('content-audit-15-zh-v2')
    expect(wrapper.get('[data-test="audit-score-jailbreak"]').text()).toContain('admin.riskControl.categoryHit')
    expect(wrapper.get('[data-test="audit-score-jailbreak"]').text()).toContain('80.0% / 80.0%')
    expect(wrapper.get('[data-test="audit-score-control_bypass"]').text()).toContain('admin.riskControl.categoryDisabled')
    wrapper.unmount()
  })
})
