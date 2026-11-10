import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { mount, flushPromises } from '@vue/test-utils'
import AutoConfigView from '../AutoConfigView.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { getAutoConfig, saveAutoConfig, type AutoConfig } from '@/api/admin/autoConfig'
import { listQualityPlans } from '@/api/admin/accountQuality'
import { getAll, list as listGroups } from '@/api/admin/groups'
import { list as listAccounts } from '@/api/admin/accounts'
import type { Group } from '@/types'
import { defaultModelBillingConfig } from '@/utils/modelBilling'
import { defaultExcelBPSDefaults } from '@/utils/excelBPSDefaults'
import { defaultOAuthModelMappings } from '@/utils/oauthModelMappings'
import { defaultOAuthDegradationConfig } from '@/utils/oauthDegradation'
import { defaultOpenAITwoFAAdmissionConfig } from '@/utils/openaiTwoFAAdmission'
import { defaultOpenAIAPIKeyFallbackConfig, type OpenAIAPIKeyFallbackStatus } from '@/utils/openaiAPIKeyFallback'
vi.mock('@/components/account/ModelWhitelistSelector.vue', () => ({ default: { props: ['modelValue'], template: '<div data-testid="model-selection">{{ modelValue.join(", ") }}</div>' } }))
const state = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/AutoConfigHistory.vue', () => ({ default: { props: ['refreshKey'], template: '<section data-testid="history" :data-refresh="refreshKey" />' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn() } }))
vi.mock('@/api/admin/autoConfig', async importOriginal => ({ ...await importOriginal<typeof import('@/api/admin/autoConfig')>(), getAutoConfig: vi.fn(), saveAutoConfig: vi.fn() }))
vi.mock('@/api/admin/accountQuality', () => ({ listQualityPlans: vi.fn() }))
vi.mock('@/api/admin/groups', () => ({ getAll: vi.fn(), list: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ list: vi.fn() }))
const config: AutoConfig = { openai_apikey_fallback: defaultOpenAIAPIKeyFallbackConfig(), degradation_action: defaultOAuthDegradationConfig(), openai_ws_mode: 'ctx_pool', openai_ws_sse_acceleration: false, openai_totp_rotate_on_429: false, openai_disable_scheduling_on_429: false, openai_disable_scheduling_on_429_7d_threshold_percent: 98, openai_two_fa_admission: defaultOpenAITwoFAAdmissionConfig(), model_billing: defaultModelBillingConfig(), model_mappings: defaultOAuthModelMappings('openai'), excel_bps: defaultExcelBPSDefaults(), enabled: false, platform: 'openai', priority: 50, load_factor: 1, concurrency: 3, cost_multiplier: 0.07, group_ids: [], upgrade_enabled: false, upgrade_group_ids: [], successes_per_step: 20, upgrade_step: 1, max_concurrency: 100, cooldown_seconds: 60, revision: '' }
beforeEach(() => {
 localStorage.clear(); setActivePinia(createPinia())
 vi.resetAllMocks(); vi.mocked(listAccounts).mockResolvedValue({ items: [], page: 1, page_size: 100, pages: 0, total: 0 }); vi.mocked(listQualityPlans).mockResolvedValue([]); state.auth = reactive({ user: { id: 1, role: 'admin' } })
 vi.mocked(listGroups).mockImplementation(async () => ({ items: await getAll(), page: 1, page_size: 100, pages: 1, total: 2 }) as any)
 vi.mocked(getAutoConfig).mockResolvedValue(structuredClone(config)); vi.mocked(saveAutoConfig).mockImplementation(async c => c)
 vi.mocked(getAll).mockResolvedValue([{ id: 5, name: 'OpenAI test', platform: 'openai', status: 'active' }, { id: 6, name: 'Anthropic test', platform: 'anthropic', status: 'active' }] as Group[])
})
describe('AutoConfigView', () => {
 it('places fallback handling beside degradation handling and saves its controls independently of initial configuration', async () => {
  vi.mocked(listAccounts).mockResolvedValueOnce({ items: [{ id: 71, name: 'Fallback fixture', platform: 'openai', type: 'apikey', status: 'active', schedulable: false, parent_account_id: null }] as any, page: 1, page_size: 100, pages: 1, total: 1 })
  const w = mount(AutoConfigView); await flushPromises()
  const panels = w.get('[data-testid="oauth-handling-panels"]')
  expect(panels.classes()).toContain('lg:grid-cols-2')
  expect(panels.find('[data-testid="degradation-action"]').exists()).toBe(true)
  expect(panels.find('[data-testid="apikey-fallback"]').exists()).toBe(true)
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-enabled"]').element.checked).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-threshold"]').element.value).toBe('95')
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-window"]').element.value).toBe('5')
  await w.get('[data-testid="platform"]').setValue('anthropic')
  await w.get('[data-testid="apikey-fallback-enabled"]').setValue(true)
  await w.get('[data-testid="apikey-fallback-account-71"]').setValue(true)
  await w.get('[data-testid="apikey-fallback-metric"]').setValue('health_score')
  await w.get('[data-testid="apikey-fallback-threshold"]').setValue('82.5')
  await w.get('[data-testid="apikey-fallback-window"]').setValue('1440')
  await w.get('form').trigger('submit'); await flushPromises()
  const saved: AutoConfig = { ...config, platform: 'anthropic', selected_models: null, quality_rule: null, model_mappings: [], openai_apikey_fallback: { enabled: true, account_ids: [71], metric: 'health_score', threshold: 82.5, window_minutes: 1440 } }
  expect(saveAutoConfig).toHaveBeenCalledWith(saved)
  expect(w.get<HTMLInputElement>('[data-testid="initial-enabled"]').element.checked).toBe(false)
  w.unmount()
  vi.mocked(getAutoConfig).mockResolvedValueOnce(saved)
  vi.mocked(listAccounts).mockResolvedValueOnce({ items: [{ id: 71, name: 'Fallback fixture', platform: 'openai', type: 'apikey', status: 'active', schedulable: false }] as any, page: 1, page_size: 100, pages: 1, total: 1 })
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLSelectElement>('[data-testid="apikey-fallback-metric"]').element.value).toBe('health_score')
  expect(reloaded.get<HTMLInputElement>('[data-testid="apikey-fallback-threshold"]').element.value).toBe('82.5')
  expect(reloaded.get<HTMLInputElement>('[data-testid="apikey-fallback-window"]').element.value).toBe('1440')
  expect(reloaded.get<HTMLInputElement>('[data-testid="apikey-fallback-account-71"]').element.checked).toBe(true)
  reloaded.unmount()
 })
 it('loads all fallback account pages and excludes OAuth, other platforms, inactive accounts, shadows and expired or invalid expiry timestamps', async () => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_apikey_fallback: { ...defaultOpenAIAPIKeyFallbackConfig(), account_ids: [77] } })
  vi.mocked(listAccounts).mockResolvedValueOnce({ items: [
   { id: 71, name: 'First key', platform: 'openai', type: 'apikey', status: 'active', schedulable: false },
   { id: 72, name: 'OAuth fixture', platform: 'openai', type: 'oauth', status: 'active' },
   { id: 73, name: 'Shadow fixture', platform: 'openai', type: 'apikey', status: 'active', parent_account_id: 71 }
  ] as any, page: 1, page_size: 100, pages: 2, total: 9 })
  vi.mocked(listAccounts).mockResolvedValueOnce({ items: [
   { id: 74, name: 'Other platform', platform: 'anthropic', type: 'apikey', status: 'active' },
   { id: 75, name: 'Inactive fixture', platform: 'openai', type: 'apikey', status: 'inactive' },
   { id: 76, name: 'Second key', platform: 'openai', type: 'apikey', status: 'active', schedulable: true, expires_at: Date.now() / 1000 + 3600 },
   { id: 77, name: 'Expired key', platform: 'openai', type: 'apikey', status: 'active', schedulable: false, expires_at: 1 },
   { id: 78, name: 'Invalid expiry', platform: 'openai', type: 'apikey', status: 'active', schedulable: false, expires_at: 'invalid' },
   { id: 79, name: 'Non-finite expiry', platform: 'openai', type: 'apikey', status: 'active', schedulable: false, expires_at: Number.POSITIVE_INFINITY }
  ] as any, page: 2, page_size: 100, pages: 2, total: 9 })
  const w = mount(AutoConfigView); await flushPromises()
  expect(listAccounts).toHaveBeenNthCalledWith(1, 1, 100, { platform: 'openai', type: 'apikey', status: 'active', lite: '1', sort_by: 'id', sort_order: 'asc' })
  expect(listAccounts).toHaveBeenNthCalledWith(2, 2, 100, { platform: 'openai', type: 'apikey', status: 'active', lite: '1', sort_by: 'id', sort_order: 'asc' })
  expect(w.findAll('[data-testid^="apikey-fallback-account-"]').map(input => input.attributes('data-testid'))).toEqual(['apikey-fallback-account-71', 'apikey-fallback-account-76'])
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-unavailable-77"]').element.checked).toBe(true)
  w.unmount()
 })
 it('preserves unavailable selected account IDs, blocks enabling them and still allows the rule to be disabled', async () => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_apikey_fallback: { ...defaultOpenAIAPIKeyFallbackConfig(), enabled: true, account_ids: [999] } })
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-unavailable-999"]').element.checked).toBe(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('autoConfig.apikeyFallback.unavailableHint')
  await w.get('[data-testid="apikey-fallback-enabled"]').setValue(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, openai_apikey_fallback: { ...defaultOpenAIAPIKeyFallbackConfig(), account_ids: [999] } })
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-unavailable-999"]').element.checked).toBe(true)
  w.unmount()
 })
 it('keeps selections on an incomplete account response and can retry before enabling fallback', async () => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_apikey_fallback: { ...defaultOpenAIAPIKeyFallbackConfig(), enabled: true, account_ids: [71] } })
  vi.mocked(listAccounts).mockResolvedValueOnce({ items: [], page: 1, page_size: 100, pages: 2, total: 1 })
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get('[data-testid="apikey-fallback-accounts-error"]').text()).toContain('autoConfig.apikeyFallback.accountsFailed')
  expect(w.get('[data-testid="apikey-fallback-retained-ids"]').text()).toContain('#71')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  vi.mocked(listAccounts).mockResolvedValueOnce({ items: [{ id: 71, name: 'Fallback fixture', platform: 'openai', type: 'apikey', status: 'active', schedulable: false }] as any, page: 1, page_size: 100, pages: 1, total: 1 })
  await w.get('[data-testid="apikey-fallback-reload-accounts"]').trigger('click'); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-account-71"]').element.checked).toBe(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalled()
  w.unmount()
 })
 it.each([
  { metric: 'sla', threshold: -1, window_minutes: 5 },
  { metric: 'health_score', threshold: 100.1, window_minutes: 5 },
  { metric: 'sla', threshold: Number.NaN, window_minutes: 5 },
  { metric: 'sla', threshold: 95, window_minutes: 0 },
  { metric: 'sla', threshold: 95, window_minutes: 1441 },
  { metric: 'sla', threshold: 95, window_minutes: 1.5 }
 ] as const)('rejects invalid fallback metric controls $metric / $threshold / $window_minutes', async controls => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_apikey_fallback: { ...defaultOpenAIAPIKeyFallbackConfig(), ...controls } })
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('autoConfig.apikeyFallback.invalid')
  w.unmount()
 })
 it('defaults legacy fallback settings to off and requires a target before enabling', async () => {
  const { openai_apikey_fallback: _fallback, ...legacy } = config
  vi.mocked(getAutoConfig).mockResolvedValueOnce(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-enabled"]').element.checked).toBe(false)
  await w.get('[data-testid="apikey-fallback-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('autoConfig.apikeyFallback.chooseAccounts')
  await w.get('[data-testid="apikey-fallback-enabled"]').setValue(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(config)
  w.unmount()
 })
 it('refreshes read-only fallback status without overwriting the draft or including status in saves', async () => {
  const status: OpenAIAPIKeyFallbackStatus = { last_checked_at: '2026-11-10T15:00:00Z', last_metric_checked_at: '2026-11-10T14:59:30Z', oauth_account_count: 5, oauth_available_count: 0, metric: 'sla', metric_value: 91, samples: 100, window_minutes: 5, last_trigger_reason: 'metric_below_threshold', last_enabled_at: '2026-11-10T14:59:00Z' }
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_apikey_fallback_status: status })
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get('[data-testid="apikey-fallback-status"]').text()).toContain('91.00%')
  expect(w.get('[data-testid="apikey-fallback-metric-checked-at"]').text()).toContain('2026-11-10T14:59:30Z')
  expect(w.get('[data-testid="apikey-fallback-metric-checked-at"]').text()).toContain('autoConfig.apikeyFallback.lastMetricChecked')
  await w.get('[data-testid="apikey-fallback-threshold"]').setValue('88')
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_apikey_fallback_status: { ...status, last_checked_at: '2026-11-10T15:00:15Z', metric: 'health_score', metric_value: 86, samples: 50, window_minutes: 15 } })
  await w.get('[data-testid="apikey-fallback-refresh-status"]').trigger('click'); await flushPromises()
  expect(w.get('[data-testid="apikey-fallback-status"]').text()).toContain('86.00')
  expect(w.get('[data-testid="apikey-fallback-status"]').text()).toContain('autoConfig.apikeyFallback.metrics.health_score')
  expect(w.get('[data-testid="apikey-fallback-status"]').text()).toContain('86.00autoConfig.apikeyFallback.scoreUnit')
  expect(w.get('[data-testid="apikey-fallback-status"]').text()).not.toContain('86.00%')
  expect(w.get<HTMLSelectElement>('[data-testid="apikey-fallback-metric"]').element.value).toBe('sla')
  expect(w.get('[data-testid="apikey-fallback-metric-checked-at"]').text()).toContain('2026-11-10T14:59:30Z')
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-threshold"]').element.value).toBe('88')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, openai_apikey_fallback: { ...defaultOpenAIAPIKeyFallbackConfig(), threshold: 88 } })
  expect(vi.mocked(saveAutoConfig).mock.lastCall![0]).not.toHaveProperty('openai_apikey_fallback_status')
  w.unmount()
 })
 it('registers an independent fallback notice and preserves its dismissal across remounts', async () => {
  const oldId = NEW_FEATURE_NOTICES.oauthDegradationActions, id = NEW_FEATURE_NOTICES.openaiAPIKeyFallback
  useNewFeatureNoticeStore().dismiss(1, oldId)
  const w = mount(AutoConfigView); await flushPromises()
  const badge = w.get('[data-testid="apikey-fallback"] [data-testid="new-feature-badge"]')
  const second = mount(NewFeatureBadge, { props: { noticeId: id, userId: 1 } })
  await badge.trigger('click')
  expect(second.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="apikey-fallback-enabled"]').element.checked).toBe(false)
  expect(saveAutoConfig).not.toHaveBeenCalled()
  w.unmount(); second.unmount(); setActivePinia(createPinia())
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.find('[data-testid="apikey-fallback"] [data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(useNewFeatureNoticeStore().isDismissed(1, oldId)).toBe(true)
  const future = mount(NewFeatureBadge, { props: { noticeId: 'future-fallback-test', userId: 1 } })
  expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  const otherAdmin = mount(NewFeatureBadge, { props: { noticeId: id, userId: 2 } })
  expect(otherAdmin.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  reloaded.unmount(); future.unmount(); otherAdmin.unmount()
 })
 it('preserves degradation actions when saving WS settings and preserves WS settings when saving degradation actions', async () => {
  const degradation = { ...defaultOAuthDegradationConfig(), enabled: true, threshold_percent: 42, limit_concurrency: false, limit_models: false, limit_groups: true, group_ids: [5] }
  const initial: AutoConfig = { ...config, openai_ws_mode: 'off', degradation_action: degradation }
  vi.mocked(getAutoConfig).mockResolvedValueOnce(initial)
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="initial-ws-mode-select"]').setValue('http_bridge')
  await w.get('[data-testid="initial-ws-sse-acceleration-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  const wsSaved = JSON.parse(JSON.stringify(vi.mocked(saveAutoConfig).mock.lastCall![0])) as AutoConfig
  expect(wsSaved).toEqual({ ...initial, openai_ws_mode: 'http_bridge', openai_ws_sse_acceleration: true })
  w.unmount()

  vi.mocked(getAutoConfig).mockResolvedValueOnce(structuredClone(wsSaved))
  const actions = mount(AutoConfigView); await flushPromises()
  expect(actions.get<HTMLInputElement>('[data-testid="degradation-limit-groups"]').element.checked).toBe(true)
  expect(actions.get<HTMLInputElement>('[data-testid="degradation-group-5"]').element.checked).toBe(true)
  await actions.get('[data-testid="degradation-limit-concurrency"]').setValue(true)
  await actions.get('[data-testid="degradation-limit-groups"]').setValue(false)
  await actions.get('[data-testid="degradation-concurrency"]').setValue('7')
  await actions.get('[data-testid="degradation-threshold"]').setValue('63.5')
  await actions.get('form').trigger('submit'); await flushPromises()
  const actionsSaved = JSON.parse(JSON.stringify(vi.mocked(saveAutoConfig).mock.lastCall![0])) as AutoConfig
  expect(actionsSaved).toEqual({ ...wsSaved, degradation_action: { ...degradation, limit_concurrency: true, limit_groups: false, target_concurrency: 7, threshold_percent: 63.5 } })
  actions.unmount()

  vi.mocked(getAutoConfig).mockResolvedValueOnce(structuredClone(actionsSaved))
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLSelectElement>('[data-testid="initial-ws-mode-select"]').element.value).toBe('http_bridge')
  expect(reloaded.get<HTMLInputElement>('[data-testid="initial-ws-sse-acceleration-enabled"]').element.checked).toBe(true)
  expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-limit-concurrency"]').element.checked).toBe(true)
  expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-limit-models"]').element.checked).toBe(false)
  expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-limit-groups"]').element.checked).toBe(false)
  expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-concurrency"]').element.value).toBe('7')
  expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-threshold"]').element.value).toBe('63.5')
  reloaded.unmount()
 })
 it('defaults missing initial WS settings to context pool and unchecked acceleration before explicitly saving them', async () => {
  const { openai_ws_mode: _mode, openai_ws_sse_acceleration: _acceleration, ...legacy } = config
  vi.mocked(getAutoConfig).mockResolvedValueOnce(legacy)
  vi.mocked(saveAutoConfig).mockResolvedValueOnce(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  const mode = '[data-testid="initial-ws-mode-select"]', acceleration = '[data-testid="initial-ws-sse-acceleration-enabled"]'
  expect(w.get<HTMLSelectElement>(mode).element.value).toBe('ctx_pool')
  expect(w.get<HTMLInputElement>(acceleration).element.checked).toBe(false)
  expect(w.get(mode).findAll('option').map(option => option.element.value)).toEqual(['off', 'ctx_pool', 'passthrough', 'http_bridge'])
  expect(w.get(mode).element.closest('section')).toBe(w.get('[data-testid="initial-enabled"]').element.closest('section'))
  expect(w.get(acceleration).element.closest('section')).toBe(w.get('[data-testid="initial-enabled"]').element.closest('section'))
  expect(saveAutoConfig).not.toHaveBeenCalled()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(config)
  expect(w.get<HTMLSelectElement>(mode).element.value).toBe('ctx_pool')
  expect(w.get<HTMLInputElement>(acceleration).element.checked).toBe(false)
  w.unmount()
 })
 it.each([
  { mode: 'off', acceleration: false },
  { mode: 'off', acceleration: true },
  { mode: 'ctx_pool', acceleration: false },
  { mode: 'ctx_pool', acceleration: true },
  { mode: 'passthrough', acceleration: false },
  { mode: 'passthrough', acceleration: true },
  { mode: 'http_bridge', acceleration: false },
  { mode: 'http_bridge', acceleration: true }
 ] as const)('saves and reloads initial WS mode $mode with acceleration=$acceleration independently of 429 settings', async ({ mode, acceleration }) => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="initial-enabled"]').setValue(true)
  await w.get('[data-testid="initial-group-5"]').setValue(true)
  await w.get('[data-testid="initial-ws-mode-select"]').setValue(mode)
  await w.get('[data-testid="initial-ws-sse-acceleration-enabled"]').setValue(acceleration)
  await w.get('[data-testid="totp-rotate-on-429"]').setValue(true)
  await w.get('[data-testid="disable-scheduling-on-429"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  const saved: AutoConfig = { ...config, enabled: true, group_ids: [5], openai_ws_mode: mode, openai_ws_sse_acceleration: acceleration, openai_totp_rotate_on_429: true, openai_disable_scheduling_on_429: true }
  expect(saveAutoConfig).toHaveBeenCalledWith(saved)
  expect(w.get<HTMLSelectElement>('[data-testid="initial-ws-mode-select"]').element.value).toBe(mode)
  expect(w.get<HTMLInputElement>('[data-testid="initial-ws-sse-acceleration-enabled"]').element.checked).toBe(acceleration)
  w.unmount()
  vi.mocked(getAutoConfig).mockResolvedValueOnce(saved)
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLSelectElement>('[data-testid="initial-ws-mode-select"]').element.value).toBe(mode)
  expect(reloaded.get<HTMLInputElement>('[data-testid="initial-ws-sse-acceleration-enabled"]').element.checked).toBe(acceleration)
  expect(reloaded.get<HTMLInputElement>('[data-testid="totp-rotate-on-429"]').element.checked).toBe(true)
  expect(reloaded.get<HTMLInputElement>('[data-testid="disable-scheduling-on-429"]').element.checked).toBe(true)
  const hint = { off: null, ctx_pool: 'wsModeCtxPoolHint', passthrough: 'wsModePassthroughHint', http_bridge: 'wsModeHttpBridgeHint' }[mode]
  if (hint) expect(reloaded.get('[data-testid="initial-ws-mode"]').text()).toContain('admin.accounts.openai.' + hint)
  reloaded.unmount()
 })
 it('hides initial WS settings for other platforms and resets them before returning to OpenAI', async () => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_ws_mode: 'passthrough', openai_ws_sse_acceleration: true })
  const w = mount(AutoConfigView); await flushPromises()
  for (const platform of ['anthropic', 'gemini', 'antigravity', 'grok']) {
   await w.get('[data-testid="platform"]').setValue(platform)
   expect(w.find('[data-testid="initial-ws-mode"]').exists()).toBe(false)
   expect(w.find('[data-testid="initial-ws-sse-acceleration"]').exists()).toBe(false)
   await w.get('form').trigger('submit'); await flushPromises()
   expect(saveAutoConfig).toHaveBeenLastCalledWith({ ...config, platform, selected_models: null, quality_rule: null, model_mappings: defaultOAuthModelMappings(platform) })
  }
  await w.get('[data-testid="platform"]').setValue('openai')
  expect(w.get<HTMLSelectElement>('[data-testid="initial-ws-mode-select"]').element.value).toBe('ctx_pool')
  expect(w.get<HTMLInputElement>('[data-testid="initial-ws-sse-acceleration-enabled"]').element.checked).toBe(false)
  w.unmount()
 })
 it('shares the initial WS notice between both controls, preserves old dismissals and remembers its dismissal per admin', async () => {
  const id = NEW_FEATURE_NOTICES.oauthInitialWSSettings, oldId = NEW_FEATURE_NOTICES.oauthInitialDisableScheduling429
  useNewFeatureNoticeStore().dismiss(1, oldId)
  const w = mount(AutoConfigView); await flushPromises()
  const wsNotices = (view: typeof w) => view.findAllComponents(NewFeatureBadge).filter(badge => badge.props('noticeId') === id)
  expect(wsNotices(w)).toHaveLength(2)
  expect(wsNotices(w).every(badge => badge.find('[data-testid="new-feature-badge"]').exists())).toBe(true)
  expect(w.findAllComponents(NewFeatureBadge).find(badge => badge.props('noticeId') === oldId)!.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  await wsNotices(w)[0]!.get('[data-testid="new-feature-badge"]').trigger('click')
  expect(wsNotices(w).every(badge => !badge.find('[data-testid="new-feature-badge"]').exists())).toBe(true)
  expect(w.get<HTMLSelectElement>('[data-testid="initial-ws-mode-select"]').element.value).toBe('ctx_pool')
  expect(w.get<HTMLInputElement>('[data-testid="initial-ws-sse-acceleration-enabled"]').element.checked).toBe(false)
  expect(saveAutoConfig).not.toHaveBeenCalled()
  w.unmount()
  setActivePinia(createPinia())
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(wsNotices(reloaded).every(badge => !badge.find('[data-testid="new-feature-badge"]').exists())).toBe(true)
  expect(useNewFeatureNoticeStore().isDismissed(1, oldId)).toBe(true)
  const future = mount(NewFeatureBadge, { props: { noticeId: 'future-ws-announcement-test', userId: 1 } })
  expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  const otherAdmin = mount(NewFeatureBadge, { props: { noticeId: id, userId: 2 } })
  expect(otherAdmin.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  reloaded.unmount(); future.unmount(); otherAdmin.unmount()
 })
 it('keeps the new 429 notice independent of old dismissals and persists its dismissal', async () => {
  const id = NEW_FEATURE_NOTICES.oauthInitialTOTPRotation429
  const oldId = NEW_FEATURE_NOTICES.protocolFeatureSwitches
  useNewFeatureNoticeStore().dismiss(1, oldId)
  const w = mount(AutoConfigView); await flushPromises()
  const rotationNotice = (view: typeof w) => view.findAllComponents(NewFeatureBadge).find(badge => badge.props('noticeId') === id)!
  const other = mount(NewFeatureBadge, { props: { noticeId: id, userId: 1 } })
  expect(rotationNotice(w).find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  expect(other.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  await rotationNotice(w).get('[data-testid="new-feature-badge"]').trigger('click')
  expect(rotationNotice(w).find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(w.get('[data-testid="initial-model-selection"]').find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  expect(other.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="totp-rotate-on-429"]').element.checked).toBe(false)
  w.unmount(); other.unmount()
  setActivePinia(createPinia())
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(rotationNotice(reloaded).find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(useNewFeatureNoticeStore().isDismissed(1, oldId)).toBe(true)
  const future = mount(NewFeatureBadge, { props: { noticeId: 'future-announcement-test', userId: 1 } })
  expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  reloaded.unmount(); future.unmount()
 })
 it('saves the 429 2FA opt-in with initial OAuth configuration and reloads it', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  const checkbox = '[data-testid="totp-rotate-on-429"]'
  expect(w.get<HTMLInputElement>(checkbox).element.checked).toBe(false)
  expect(w.get(checkbox).element.closest('section')).toBe(w.get('[data-testid="initial-enabled"]').element.closest('section'))
  await w.get('[data-testid="initial-enabled"]').setValue(true)
  await w.get('[data-testid="initial-group-5"]').setValue(true)
  await w.get(checkbox).setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, enabled: true, group_ids: [5], openai_totp_rotate_on_429: true })
  expect(w.get<HTMLInputElement>(checkbox).element.checked).toBe(true)
  w.unmount()
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_totp_rotate_on_429: true })
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLInputElement>(checkbox).element.checked).toBe(true)
  await reloaded.get('[data-testid="platform"]').setValue('anthropic')
  expect(reloaded.find(checkbox).exists()).toBe(false)
  await reloaded.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenLastCalledWith({ ...config, platform: 'anthropic', selected_models: null, quality_rule: null, model_mappings: [] })
  reloaded.unmount()
 })
 it('keeps the 429 2FA option off for legacy settings', async () => {
  const { openai_totp_rotate_on_429: _rotation, ...legacy } = config
  vi.mocked(getAutoConfig).mockResolvedValueOnce(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="totp-rotate-on-429"]').element.checked).toBe(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(config)
  w.unmount()
 })
 it('shows the scheduling notice after the 2FA notice was dismissed and remembers its own dismissal', async () => {
  const id = NEW_FEATURE_NOTICES.oauthInitialDisableScheduling429
  const oldId = NEW_FEATURE_NOTICES.oauthInitialTOTPRotation429
  useNewFeatureNoticeStore().dismiss(1, oldId)
  const w = mount(AutoConfigView); await flushPromises()
  const schedulingNotice = (view: typeof w) => view.findAllComponents(NewFeatureBadge).find(badge => badge.props('noticeId') === id)!
  const rotationNotice = (view: typeof w) => view.findAllComponents(NewFeatureBadge).find(badge => badge.props('noticeId') === oldId)!
  const threshold = '[data-testid="disable-scheduling-on-429-7d-threshold"]'
  expect(w.get(threshold).element.closest('fieldset')).toBe(schedulingNotice(w).element.closest('fieldset'))
  const other = mount(NewFeatureBadge, { props: { noticeId: id, userId: 1 } })
  expect(schedulingNotice(w).find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  expect(rotationNotice(w).find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  await schedulingNotice(w).get('[data-testid="new-feature-badge"]').trigger('click')
  expect(schedulingNotice(w).find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(other.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="disable-scheduling-on-429"]').element.checked).toBe(false)
  expect(w.get<HTMLInputElement>(threshold).element.value).toBe('98')
  w.unmount(); other.unmount()
  setActivePinia(createPinia())
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(schedulingNotice(reloaded).find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(rotationNotice(reloaded).find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(reloaded.get('[data-testid="initial-model-selection"]').find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  const future = mount(NewFeatureBadge, { props: { noticeId: 'future-scheduling-announcement-test', userId: 1 } })
  expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  reloaded.unmount(); future.unmount()
 })
 it.each([
  { disable: true, rotate: false },
  { disable: false, rotate: true },
  { disable: true, rotate: true },
  { disable: false, rotate: false }
 ])('saves scheduling and 2FA independently (disable=$disable, rotate=$rotate)', async ({ disable, rotate }) => {
  const w = mount(AutoConfigView); await flushPromises()
  const checkbox = '[data-testid="disable-scheduling-on-429"]'
  expect(w.get<HTMLInputElement>(checkbox).element.checked).toBe(false)
  expect(w.get(checkbox).element.closest('section')).toBe(w.get('[data-testid="initial-enabled"]').element.closest('section'))
  await w.get('[data-testid="initial-enabled"]').setValue(true)
  await w.get('[data-testid="initial-group-5"]').setValue(true)
  await w.get(checkbox).setValue(disable)
  await w.get('[data-testid="totp-rotate-on-429"]').setValue(rotate)
  await w.get('form').trigger('submit'); await flushPromises()
  const saved = { ...config, enabled: true, group_ids: [5], openai_disable_scheduling_on_429: disable, openai_totp_rotate_on_429: rotate }
  expect(saveAutoConfig).toHaveBeenCalledWith(saved)
  expect(w.get<HTMLInputElement>(checkbox).element.checked).toBe(disable)
  w.unmount()
  vi.mocked(getAutoConfig).mockResolvedValueOnce(saved)
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLInputElement>(checkbox).element.checked).toBe(disable)
  expect(reloaded.get<HTMLInputElement>('[data-testid="totp-rotate-on-429"]').element.checked).toBe(rotate)
  reloaded.unmount()
 })
 it('resets both OpenAI 429 options and their weekly threshold when changing platforms', async () => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_disable_scheduling_on_429: true, openai_totp_rotate_on_429: true, openai_disable_scheduling_on_429_7d_threshold_percent: 97.5 })
  const w = mount(AutoConfigView); await flushPromises()
  const scheduling = '[data-testid="disable-scheduling-on-429"]', rotation = '[data-testid="totp-rotate-on-429"]', threshold = '[data-testid="disable-scheduling-on-429-7d-threshold"]'
  await w.get('[data-testid="platform"]').setValue('anthropic')
  expect(w.find(scheduling).exists()).toBe(false)
  expect(w.find(rotation).exists()).toBe(false)
  expect(w.find(threshold).exists()).toBe(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenLastCalledWith({ ...config, platform: 'anthropic', selected_models: null, quality_rule: null, model_mappings: [] })
  await w.get('[data-testid="platform"]').setValue('openai')
  expect(w.get<HTMLInputElement>(scheduling).element.checked).toBe(false)
  expect(w.get<HTMLInputElement>(rotation).element.checked).toBe(false)
  expect(w.get<HTMLInputElement>(threshold).element.value).toBe('98')
  expect(w.get<HTMLInputElement>(threshold).element.disabled).toBe(true)
  w.unmount()
 })
 it('keeps automatic scheduling shutdown off when loading or saving legacy settings', async () => {
  const { openai_disable_scheduling_on_429: _scheduling, openai_disable_scheduling_on_429_7d_threshold_percent: _threshold, ...legacy } = config
  vi.mocked(getAutoConfig).mockResolvedValueOnce(legacy)
  vi.mocked(saveAutoConfig).mockResolvedValueOnce(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  const checkbox = '[data-testid="disable-scheduling-on-429"]'
  expect(w.get<HTMLInputElement>(checkbox).element.checked).toBe(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(config)
  expect(w.get<HTMLInputElement>(checkbox).element.checked).toBe(false)
  w.unmount()
 })
 it('defaults the weekly shutdown threshold to 98 and enables editing only with the switch', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  const threshold = '[data-testid="disable-scheduling-on-429-7d-threshold"]'
  expect(w.get<HTMLInputElement>(threshold).element.value).toBe('98')
  expect(w.get<HTMLInputElement>(threshold).element.disabled).toBe(true)
  expect(w.get(threshold).attributes()).toEqual(expect.objectContaining({ min: '0', max: '100', step: 'any', required: '' }))
  await w.get('[data-testid="disable-scheduling-on-429"]').setValue(true)
  expect(w.get<HTMLInputElement>(threshold).element.disabled).toBe(false)
  await w.get(threshold).setValue('97.25')
  await w.get('[data-testid="disable-scheduling-on-429"]').setValue(false)
  expect(w.get<HTMLInputElement>(threshold).element.disabled).toBe(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, openai_disable_scheduling_on_429_7d_threshold_percent: 97.25 })
  w.unmount()
 })
 it.each([0, 97.5, 98, 100])('saves and reloads weekly shutdown threshold %s independently of 2FA', async threshold => {
  const w = mount(AutoConfigView); await flushPromises()
  const input = '[data-testid="disable-scheduling-on-429-7d-threshold"]'
  await w.get('[data-testid="disable-scheduling-on-429"]').setValue(true)
  await w.get('[data-testid="totp-rotate-on-429"]').setValue(true)
  await w.get(input).setValue(threshold)
  await w.get('form').trigger('submit'); await flushPromises()
  const saved = { ...config, openai_disable_scheduling_on_429: true, openai_totp_rotate_on_429: true, openai_disable_scheduling_on_429_7d_threshold_percent: threshold }
  expect(saveAutoConfig).toHaveBeenCalledWith(saved)
  expect(w.get<HTMLInputElement>(input).element.value).toBe(String(threshold))
  w.unmount()
  vi.mocked(getAutoConfig).mockResolvedValueOnce(saved)
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLInputElement>(input).element.value).toBe(String(threshold))
  expect(reloaded.get<HTMLInputElement>('[data-testid="disable-scheduling-on-429"]').element.checked).toBe(true)
  expect(reloaded.get<HTMLInputElement>('[data-testid="totp-rotate-on-429"]').element.checked).toBe(true)
  reloaded.unmount()
 })
 it('uses 98 for existing opt-in settings without a weekly threshold on load and save', async () => {
  const { openai_disable_scheduling_on_429_7d_threshold_percent: _threshold, ...withoutThreshold } = config
  const legacy = { ...withoutThreshold, openai_disable_scheduling_on_429: true }
  vi.mocked(getAutoConfig).mockResolvedValueOnce(legacy)
  vi.mocked(saveAutoConfig).mockResolvedValueOnce(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  const input = '[data-testid="disable-scheduling-on-429-7d-threshold"]'
  expect(w.get<HTMLInputElement>(input).element.value).toBe('98')
  expect(w.get<HTMLInputElement>(input).element.disabled).toBe(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, openai_disable_scheduling_on_429: true })
  expect(w.get<HTMLInputElement>(input).element.value).toBe('98')
  expect(w.get<HTMLInputElement>('[data-testid="disable-scheduling-on-429"]').element.checked).toBe(true)
  w.unmount()
 })
 it.each([-1, 100.01, ''])('rejects invalid weekly shutdown threshold input %s', async threshold => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="disable-scheduling-on-429"]').setValue(true)
  await w.get('[data-testid="disable-scheduling-on-429-7d-threshold"]').setValue(threshold)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('autoConfig.disableScheduling429.invalidThreshold')
  w.unmount()
 })
 it.each([Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])('rejects a non-finite loaded weekly shutdown threshold (%s)', async threshold => {
  vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...config, openai_disable_scheduling_on_429_7d_threshold_percent: threshold })
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('autoConfig.disableScheduling429.invalidThreshold')
  w.unmount()
 })
 it('starts with the Luna 10x rule disabled and saves model billing independently', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-enabled"]').element.checked).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-model-0"]').element.value).toBe('gpt-6-luna*')
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-multiplier-0"]').element.value).toBe('10')
  await w.get('[data-testid="model-billing-enabled"]').setValue(true)
  await w.get('[data-testid="model-billing-multiplier-0"]').setValue(2.5)
  await w.get('[data-testid="model-billing-add"]').trigger('click')
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-multiplier-1"]').element.value).toBe('10')
  await w.get('[data-testid="model-billing-model-1"]').setValue(' custom-mini ')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, model_billing: { enabled: true, rules: [{ model: 'gpt-6-luna*', multiplier: 2.5, group_ids: [] }, { model: 'custom-mini', multiplier: 10, group_ids: [] }] } })
  w.unmount()
 })
 it.each([0, -1, 1001, ''])('rejects invalid model multipliers (%s)', async multiplier => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="model-billing-multiplier-0"]').setValue(multiplier)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('modelBilling.invalid')
  w.unmount()
 })
 it('requires a rule when enabled and rejects duplicate patterns', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="model-billing-enabled"]').setValue(true)
  await w.get('[data-testid="model-billing-add"]').trigger('click')
  await w.get('[data-testid="model-billing-model-1"]').setValue(' GPT-6-LUNA* ')
  await w.get('form').trigger('submit'); expect(saveAutoConfig).not.toHaveBeenCalled()
  await w.get('[data-testid="model-billing-remove-1"]').trigger('click')
  await w.get('[data-testid="model-billing-remove-0"]').trigger('click')
  await w.get('form').trigger('submit'); expect(saveAutoConfig).not.toHaveBeenCalled()
  await w.get('[data-testid="model-billing-enabled"]').setValue(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, model_billing: { enabled: false, rules: [] } })
  w.unmount()
 })

 it('edits model mappings inside initial OAuth configuration using its existing switch and platform', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.find('[data-testid="mapping-enabled"]').exists()).toBe(false)
  expect(w.find('[data-testid="mapping-platform"]').exists()).toBe(false)
  expect(w.get('[data-testid="initial-model-mappings"]').element.closest('section')).toBe(w.get('[data-testid="initial-enabled"]').element.closest('section'))
  expect(w.get<HTMLInputElement>('[data-testid="mapping-from-0"]').element.value).toBe('gpt-5.4')
  expect(w.get<HTMLInputElement>('[data-testid="mapping-to-0"]').element.value).toBe('gpt-5.5')
  await w.get('[data-testid="initial-enabled"]').setValue(true)
  await w.get('[data-testid="initial-group-5"]').setValue(true)
  await w.get('[data-testid="mapping-to-0"]').setValue(' custom-model ')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, enabled: true, group_ids: [5], model_mappings: [{ from: 'gpt-5.4', to: 'custom-model' }] })
  w.unmount()
 })
 it('validates duplicates and wildcard targets, and allows removing every mapping', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="mapping-add"]').trigger('click')
  await w.get('[data-testid="mapping-from-1"]').setValue(' gpt-5.4 ')
  await w.get('[data-testid="mapping-to-1"]').setValue('gpt-other')
  await w.get('form').trigger('submit')
  expect(w.get('[role="alert"]').text()).toContain('mapping.duplicate')
  await w.get('[data-testid="mapping-remove-1"]').trigger('click')
  await w.get('[data-testid="mapping-to-0"]').setValue('gpt-*')
  await w.get('form').trigger('submit')
  expect(w.get('[role="alert"]').text()).toContain('mapping.invalid')
  expect(saveAutoConfig).not.toHaveBeenCalled()
  await w.get('[data-testid="mapping-remove-0"]').trigger('click')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, model_mappings: [] })
  expect(w.find('[data-testid="mapping-from-0"]').exists()).toBe(false)
  w.unmount()
 })
 it('clears incompatible mappings when the existing platform selection changes', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="platform"]').setValue('anthropic')
  expect(w.find('[data-testid="mapping-from-0"]').exists()).toBe(false)
  await w.get('[data-testid="mapping-add"]').trigger('click')
  await w.get('[data-testid="mapping-from-0"]').setValue('claude-*')
  await w.get('[data-testid="mapping-to-0"]').setValue('claude-example')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, selected_models: null, quality_rule: null, platform: 'anthropic', model_mappings: [{ from: 'claude-*', to: 'claude-example' }] })
  w.unmount()
 })
 it('saves model mappings and billing rules together without resetting either', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="mapping-to-0"]').setValue('gpt-6-luna')
  await w.get('[data-testid="model-billing-enabled"]').setValue(true)
  await w.get('[data-testid="model-billing-multiplier-0"]').setValue(12.5)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenLastCalledWith({
    ...config,
    model_mappings: [{ from: 'gpt-5.4', to: 'gpt-6-luna' }],
    model_billing: { enabled: true, rules: [{ model: 'gpt-6-luna*', multiplier: 12.5, group_ids: [] }] }
  })
  expect(w.get<HTMLInputElement>('[data-testid="mapping-to-0"]').element.value).toBe('gpt-6-luna')
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-multiplier-0"]').element.value).toBe('12.5')
  w.unmount()
 })
 it('saves only the reusable BPS template without any account activation switch', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.find('[data-testid="bps-initialize_enabled"]').exists()).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="bps-ignore_encrypted_content"]').element.checked).toBe(true)
  await w.get('[data-testid="bps-ignore_encrypted_content"]').setValue(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, excel_bps: { ...config.excel_bps, ignore_encrypted_content: false } }); w.unmount()
 })
 it('restores the recommended template options', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="bps-ignore_encrypted_content"]').setValue(false)
  await w.get('[data-testid="bps-reset-options"]').trigger('click')
  expect(w.get<HTMLInputElement>('[data-testid="bps-ignore_encrypted_content"]').element.checked).toBe(true); w.unmount()
 })
 it('keeps recovery dependent on automatic 403 disabling', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="bps-auto_recover_on_403"]').setValue(true)
  await w.get('[data-testid="bps-auto_disable_on_403"]').setValue(false)
  const recovery = w.get<HTMLInputElement>('[data-testid="bps-auto_recover_on_403"]')
  expect(recovery.element.disabled).toBe(true); expect(recovery.element.checked).toBe(false); w.unmount()
 })
 it('fills compatible defaults when loading legacy settings', async () => {
  const { excel_bps: _bps, model_billing: _billing, model_mappings: _mapping, ...legacy } = config
  vi.mocked(getAutoConfig).mockResolvedValueOnce(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(config); w.unmount()
 })

 it('saves initial OAuth values without enabling upgrades', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="initial-enabled"]').setValue(true); await w.get('[data-testid="initial-group-5"]').setValue(true)
  await w.get('[data-testid="priority"]').setValue(0); await w.get('[data-testid="load_factor"]').setValue(90); await w.get('[data-testid="concurrency"]').setValue(7)
  expect(w.find('[data-testid="initial-group-6"]').exists()).toBe(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(w.get('[data-testid="history"]').attributes('data-refresh')).toBe('1')
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, enabled: true, priority: 0, load_factor: 90, concurrency: 7, group_ids: [5] }); w.unmount()
 })
 it('enables upgrades independently and validates scope', async () => {
  const w = mount(AutoConfigView); await flushPromises(); await w.get('[data-testid="upgrade-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); expect(saveAutoConfig).not.toHaveBeenCalled()
  await w.get('[data-testid="upgrade-group-6"]').setValue(true); await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, upgrade_enabled: true, upgrade_group_ids: [6] }); w.unmount()
 })
 it('clears incompatible initial groups when platform changes', async () => {
  const w = mount(AutoConfigView); await flushPromises(); await w.get('[data-testid="initial-group-5"]').setValue(true)
  await w.get('[data-testid="platform"]').setValue('anthropic'); await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith({ ...config, selected_models: null, quality_rule: null, platform: 'anthropic', model_mappings: [] }); w.unmount()
 })
 it('does not allow saving before a successful load and supports retry', async () => {
  vi.mocked(getAutoConfig).mockRejectedValueOnce(new Error('offline')); const w = mount(AutoConfigView); await flushPromises()
  expect(w.find('form').exists()).toBe(false); await w.get('button').trigger('click'); await flushPromises(); expect(w.find('form').exists()).toBe(true); w.unmount()
 })
 it('preserves draft after a save error', async () => {
  const w = mount(AutoConfigView); await flushPromises(); vi.mocked(saveAutoConfig).mockRejectedValueOnce(new Error('offline'))
  await w.get('[data-testid="concurrency"]').setValue(12); await w.get('form').trigger('submit'); await flushPromises()
  expect(w.get('[data-testid="history"]').attributes('data-refresh')).toBe('0')
  expect((w.get('[data-testid="concurrency"]').element as HTMLInputElement).value).toBe('12'); expect(w.get('[role="alert"]').text()).toContain('saveFailed'); w.unmount()
 })
 it('discards a pending settings response after logout', async () => {
  let resolve!: (v: AutoConfig) => void; vi.mocked(getAutoConfig).mockReturnValueOnce(new Promise(r => { resolve = r }))
  const w = mount(AutoConfigView); state.auth.user = null; resolve(config); await flushPromises(); expect(w.find('form').exists()).toBe(false); w.unmount()
 })
})

const qualityPlan = { id: 81, account_id: 21, account_name: 'Source account', model_id: 'gpt-5.4', cron_expression: '*/5 * * * *', enabled: true, max_results: 100, pelican_config: { question_kind: 'state_probe', parallel_count: 1, reasoning_effort: 'high', quality: { action: 'observe_only' } } } as any
it('copies a selected quality rule without account identity and clears it on platform change', async () => {
 vi.mocked(listQualityPlans).mockResolvedValue([structuredClone(qualityPlan)])
 const w = mount(AutoConfigView); await flushPromises()
 await w.get('[data-testid="quality-rule-select"]').setValue('81')
 await w.get('form').trigger('submit'); await flushPromises()
 const saved = vi.mocked(saveAutoConfig).mock.calls[0][0].quality_rule!
 expect(saved).toEqual({ model_id: 'gpt-5.4', cron_expression: '*/5 * * * *', enabled: true, max_results: 100, pelican_config: qualityPlan.pelican_config })
 expect(saved).not.toHaveProperty('account_id')
 expect(w.get('[data-testid="quality-rule-summary"]').text()).toContain('gpt-5.4')
 await w.get('[data-testid="platform"]').setValue('anthropic')
 expect(w.find('[data-testid="quality-rule-select"] option[value="81"]').exists()).toBe(false)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(vi.mocked(saveAutoConfig).mock.calls[1][0].quality_rule).toBeNull()
 w.unmount()
})
it('preserves the saved quality copy if listing fails and allows opting out', async () => {
 const { model_id, cron_expression, enabled, max_results, pelican_config } = qualityPlan
 const quality_rule = { model_id, cron_expression, enabled, max_results, pelican_config }
 vi.mocked(getAutoConfig).mockResolvedValue({ ...structuredClone(config), quality_rule })
 vi.mocked(listQualityPlans).mockRejectedValue(new Error('offline'))
 const w = mount(AutoConfigView); await flushPromises()
 expect(w.get('[data-testid="quality-rule-summary"]').text()).toContain('gpt-5.4')
 await w.get('form').trigger('submit'); await flushPromises()
 expect(vi.mocked(saveAutoConfig).mock.calls[0][0].quality_rule).toEqual(quality_rule)
 await w.get('[data-testid="quality-rule-select"]').setValue('')
 await w.get('form').trigger('submit'); await flushPromises()
 expect(vi.mocked(saveAutoConfig).mock.calls[1][0].quality_rule).toBeNull()
 w.unmount()
})


describe('new 2FA admission configuration', () => {
 it('saves independently and validates active OpenAI isolation groups', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="admission-enabled"]').element.checked).toBe(false)
  await w.get('[data-testid="admission-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  await w.get('[data-testid="admission-group"]').setValue('5')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, openai_two_fa_admission: { ...defaultOpenAITwoFAAdmissionConfig(), enabled: true, isolation_group_id: 5 } }))
  w.unmount()
 })
 it('fills defaults for older settings without changing the other feature switches', async () => {
  const legacy = { ...config }; delete legacy.openai_two_fa_admission
  vi.mocked(getAutoConfig).mockResolvedValue(legacy)
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="admission-model"]').element.value).toBe('gpt-6.1-sol')
  expect(w.get<HTMLInputElement>('[data-testid="admission-pass_count"]').element.value).toBe('6')
  expect(w.get<HTMLInputElement>('[data-testid="admission-wait_seconds"]').element.value).toBe('60')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(saveAutoConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, upgrade_enabled: false, openai_two_fa_admission: defaultOpenAITwoFAAdmissionConfig() }))
  w.unmount()
 })
})

describe('per-rule model billing scopes', () => {
 it('loads, edits independently, saves, reloads and explicitly clears to all groups', async () => {
  const scoped = { ...structuredClone(config), model_billing: { enabled: true, rules: [
   { model: 'gpt-6-luna*', multiplier: 10, group_ids: [5] },
   { model: 'claude-*', multiplier: 3, group_ids: [6] }
  ] } }
  vi.mocked(getAutoConfig).mockResolvedValue(scoped)
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-group-0-5"]').element.checked).toBe(true)
  expect(w.get<HTMLInputElement>('[data-testid="model-billing-group-1-6"]').element.checked).toBe(true)
  await w.get('[data-testid="model-billing-group-0-6"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  const saved: AutoConfig = JSON.parse(JSON.stringify(vi.mocked(saveAutoConfig).mock.calls[0][0]))
  expect(saved.model_billing?.rules.map(r => r.group_ids)).toEqual([[5,6],[6]])
  expect(saved.openai_two_fa_admission).toEqual(config.openai_two_fa_admission)
  expect(saved.excel_bps).toEqual(config.excel_bps)
  expect(saved.model_mappings).toEqual(config.model_mappings)
  w.unmount()
  vi.mocked(getAutoConfig).mockResolvedValue(saved)
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLInputElement>('[data-testid="model-billing-group-0-6"]').element.checked).toBe(true)
  await reloaded.get('[data-testid="model-billing-clear-groups-0"]').trigger('click')
  expect(reloaded.get('[data-testid="model-billing-scope-0"]').text()).toContain('modelBilling.allGroups')
  await reloaded.get('form').trigger('submit'); await flushPromises()
  expect(vi.mocked(saveAutoConfig).mock.lastCall?.[0].model_billing?.rules.map(r => r.group_ids)).toEqual([[],[6]])
  reloaded.unmount()
 })
 it('discards unsaved scope changes when leaving the page', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="model-billing-group-0-5"]').setValue(true)
  w.unmount()
  expect(saveAutoConfig).not.toHaveBeenCalled()
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLInputElement>('[data-testid="model-billing-group-0-5"]').element.checked).toBe(false)
  reloaded.unmount()
 })
 it('retains missing selected IDs with a warning when saving unrelated settings', async () => {
  vi.mocked(getAutoConfig).mockResolvedValue({ ...structuredClone(config), model_billing: { enabled:true,rules:[{model:'luna',multiplier:10,group_ids:[999]}] } })
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get('[data-testid="model-billing-unavailable-0-999"]').text()).toContain('#999')
  await w.get('[data-testid="concurrency"]').setValue(9)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(vi.mocked(saveAutoConfig).mock.lastCall?.[0].model_billing?.rules[0].group_ids).toEqual([999])
  w.unmount()
 })
})

it('keeps scopes during a group page failure, retries all pages and preserves IDs absent from the list', async () => {
 vi.mocked(getAutoConfig).mockResolvedValue({ ...structuredClone(config), model_billing: { enabled: true, rules: [{model:'luna',multiplier:10,group_ids:[5,999]}] } })
 vi.mocked(listGroups).mockResolvedValueOnce({items:[{id:5,name:'page one',platform:'openai',status:'active'}],page:1,page_size:100,pages:2,total:2} as any)
 vi.mocked(listGroups).mockRejectedValueOnce(new Error('page two offline'))
 const w = mount(AutoConfigView); await flushPromises()
 expect(w.get('[data-testid="model-billing-groups-error"]').exists()).toBe(true)
 expect(w.get<HTMLButtonElement>('[data-testid="model-billing-clear-groups-0"]').element.closest('fieldset')?.disabled).toBe(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(vi.mocked(saveAutoConfig).mock.lastCall?.[0].model_billing?.rules[0].group_ids).toEqual([5,999])
 vi.mocked(listGroups).mockResolvedValueOnce({items:[{id:5,name:'page one',platform:'openai',status:'active'}],page:1,page_size:100,pages:2,total:2} as any)
 vi.mocked(listGroups).mockResolvedValueOnce({items:[{id:6,name:'page two',platform:'anthropic',status:'active'}],page:2,page_size:100,pages:2,total:2} as any)
 await w.get('[data-testid="model-billing-groups-retry"]').trigger('click'); await flushPromises()
 expect(w.find('[data-testid="model-billing-groups-error"]').exists()).toBe(false)
 expect(w.get('[data-testid="model-billing-group-0-6"]').exists()).toBe(true)
 expect(w.get('[data-testid="model-billing-unavailable-0-999"]').text()).toContain('#999')
 expect(listGroups).toHaveBeenLastCalledWith(2,100,expect.objectContaining({status:'active'}))
 w.unmount()
})


describe('initial model selection real page interactions', () => {
 it('selects platform models, excludes Luna, persists and reloads, and distinguishes none from unset', async () => {
  const w = mount(AutoConfigView); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="model-selection-enabled"]').element.checked).toBe(false)
  await w.get('[data-testid="model-selection-enabled"]').setValue(true)
  expect(w.get<HTMLInputElement>('[data-model="gpt-6-luna"]').element.checked).toBe(true)
  await w.get('[data-model="gpt-6-luna"]').setValue(false)
  await w.get('form').trigger('submit'); await flushPromises()
  let saved = vi.mocked(saveAutoConfig).mock.calls.at(-1)![0]
  expect(saved.selected_models).toContain('gpt-6.1-sol')
  expect(saved.selected_models).not.toContain('gpt-6-luna')
  expect(saved.model_billing).toEqual(config.model_billing)
  vi.mocked(getAutoConfig).mockResolvedValue(JSON.parse(JSON.stringify(saved))); w.unmount()
  const reloaded = mount(AutoConfigView); await flushPromises()
  expect(reloaded.get<HTMLInputElement>('[data-model="gpt-6-luna"]').element.checked).toBe(false)
  await reloaded.get('[data-testid="model-selection-none"]').trigger('click')
  expect(reloaded.get('[data-testid="model-selection-empty"]').text()).toContain('autoConfig.selection.empty')
  await reloaded.get('form').trigger('submit'); await flushPromises()
  saved = vi.mocked(saveAutoConfig).mock.calls.at(-1)![0]; expect(saved.selected_models).toEqual([])
  await reloaded.get('[data-testid="model-selection-enabled"]').setValue(false)
  await reloaded.get('form').trigger('submit'); await flushPromises()
  expect(vi.mocked(saveAutoConfig).mock.calls.at(-1)![0].selected_models).toBeNull()
  reloaded.unmount()
 })
 it('clears platform-specific selection and offers the real next-platform catalog', async () => {
  vi.mocked(getAutoConfig).mockResolvedValue({ ...structuredClone(config), selected_models: ['gpt-6.1-sol'] })
  const w = mount(AutoConfigView); await flushPromises()
  await w.get('[data-testid="platform"]').setValue('anthropic')
  expect(w.get<HTMLInputElement>('[data-testid="model-selection-enabled"]').element.checked).toBe(false)
  await w.get('[data-testid="model-selection-enabled"]').setValue(true)
  expect(w.find('[data-model="gpt-6-luna"]').exists()).toBe(false)
  expect(w.find('[data-model="claude-sonnet-4-6"]').exists()).toBe(true)
  await w.get('[data-testid="model-selection-custom"]').setValue(' custom-model ')
  await w.get('[data-testid="model-selection-add"]').trigger('click')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(vi.mocked(saveAutoConfig).mock.calls.at(-1)![0].selected_models).toContain('custom-model')
  w.unmount()
 })
})
