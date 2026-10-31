import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import PrioritySchedulingView from '../PrioritySchedulingView.vue'
import { getPriorityConfig, getPrioritySnapshot, savePriorityConfig, getPriorityLatency, recomputePriorityScheduling, getPriorityStickyBindings, clearPriorityStickyBindings, type PrioritySchedulingConfig, type PriorityCandidate } from '@/api/admin/priorityScheduling'
const state = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string, params?: Record<string, unknown>) => [key, ...Object.values(params ?? {})].join(' '),
}) }))
vi.mock('@/api/admin/priorityScheduling', () => ({ getPriorityConfig: vi.fn(), getPrioritySnapshot: vi.fn(), savePriorityConfig: vi.fn(), getPriorityLatency: vi.fn(), recomputePriorityScheduling: vi.fn(), getPriorityStickyBindings: vi.fn(), clearPriorityStickyBindings: vi.fn() }))
const config: PrioritySchedulingConfig = { error_rate_weight: 25, error_rate_window_minutes: 10, error_rate_prior_samples: 5, latency_percentile: 90, latency_measurement: 'legacy_forward', recovery_exploration_percent: 0, oauth_quota_priority: false, oauth_quota_threshold: 90, balance_protocols: true, enabled: false, mode: 'balanced', group_ids: [], models: [], window_minutes: 60, min_samples: 5, target_ttft_ms: 3000, latency_penalty_mode: 'deprioritize', latency_penalty_minutes: 10, max_load_percent: 80, min_quality_percent: 90, quality_max_age_hours: 24, quality_weight: 30, latency_weight: 25, load_weight: 25, cost_weight: 20 }
beforeEach(() => {
  vi.resetAllMocks()
  state.auth = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(getPriorityConfig).mockResolvedValue({ ...config })
  vi.mocked(getPrioritySnapshot).mockResolvedValue(null)
  vi.mocked(savePriorityConfig).mockImplementation(async c => c)
  vi.mocked(getPriorityLatency).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, bucket_minutes: 5, accounts: [] })
  vi.mocked(getPriorityStickyBindings).mockResolvedValue({ group_id: 2, total: 0, by_account: [] })
  vi.mocked(recomputePriorityScheduling).mockResolvedValue({ before: null, after: null, diff: [], timed_out: false })
  vi.mocked(clearPriorityStickyBindings).mockResolvedValue({ group_id: 2, deleted: 0, remaining: 0, by_account: [] })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})
describe('priority scheduling', () => {
  it('shows scoped error evidence and unknown samples, and saves an explicit zero weight', async () => {
    const candidate: PriorityCandidate = { account_id: 1, account_name: 'Mock attempt evidence', score: 72, tier: 'eligible', reasons: [], priority: 1, concurrency: 10, load_factor: 10, load_percent: 0, waiting: 0, rate: 0.1, revenue: 0, theoretical_cost: 0, profit: null, margin: null, economics_source: 'rate', profit_samples: 0, samples: 0, p90_ttft_ms: 0, quality_passed: 1, quality_samples: 1, error_rate: 0.25, error_successes: 3, error_failures: 1, error_window_minutes: 10, error_prior_samples: 5, error_observed_at: '2026-10-22T01:02:03Z', error_endpoint: '/v1/responses', error_penalty: 0.9, base_score: 80 }
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-22T01:00:00Z', model: 'mock-model', group_id: 1, mode: 'balanced', history_ready: true, candidates: [candidate, { ...candidate, account_id: 2, error_rate: null, error_successes: 0, error_failures: 0 }, { ...candidate, account_id: 3, error_rate: 0.1, error_successes: 9, error_failures: 1 }] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    const first = wrapper.get('[data-testid="error-rate-1"]')
    expect(first.text()).toContain('25.0%')
    expect(first.text()).toContain('priorityScheduling.errorAttempts: 1 / 4')
    expect(first.text()).toContain('priorityScheduling.errorInsufficientSamples')
    expect(first.text()).toContain('priorityScheduling.errorObservedAt')
    expect(first.text()).toContain(new Date('2026-10-22T01:02:03Z').toLocaleString())
    expect(first.text()).toContain('/v1/responses')
    expect(first.text()).toContain('0.900')
    expect(first.attributes('title')).toBe('priorityScheduling.errorRateHint')
    expect(wrapper.get('[data-testid="error-rate-2"]').text()).toContain('priorityScheduling.errorNoSamples')
    expect(wrapper.get('[data-testid="error-rate-2"]').text()).not.toContain('priorityScheduling.errorAttempts')
    expect(wrapper.get('[data-testid="error-rate-2"]').text()).not.toContain('priorityScheduling.errorInsufficientSamples')
    const sufficient = wrapper.get('[data-testid="error-rate-3"]')
    expect(sufficient.text()).toContain('10.0%')
    expect(sufficient.text()).toContain('priorityScheduling.errorAttempts: 1 / 10')
    expect(sufficient.text()).not.toContain('priorityScheduling.errorInsufficientSamples')
    expect(wrapper.text()).toContain('priorityScheduling.errorLocalScope')
    const scroll = wrapper.get('[data-testid="priority-candidate-scroll"]')
    expect(scroll.classes()).toContain('overflow-x-auto')
    expect(scroll.get('table').classes()).toContain('whitespace-nowrap')
    await wrapper.get('[data-testid="error-rate-weight"]').setValue(0)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, error_rate_weight: 0 })
    wrapper.unmount()
  })

  it('defaults old error configuration to zero weight and keeps its window and prior editable', async () => {
    const legacy = { ...config }; delete legacy.error_rate_weight; delete legacy.error_rate_window_minutes; delete legacy.error_rate_prior_samples
    vi.mocked(getPriorityConfig).mockResolvedValue(legacy)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.get('[data-testid="error-rate-weight"]').element).toHaveProperty('value', '0')
    await wrapper.get('[data-testid="error_rate_window_minutes"]').setValue(20)
    await wrapper.get('[data-testid="error_rate_prior_samples"]').setValue(10)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, error_rate_weight: 0, error_rate_window_minutes: 20, error_rate_prior_samples: 10 })
    wrapper.unmount()
  })

  it('defaults an omitted latency penalty and saves an explicit duration or zero', async () => {
    const legacy = { ...config }; delete legacy.latency_penalty_minutes
    vi.mocked(getPriorityConfig).mockResolvedValue(legacy)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect((wrapper.get('[data-testid="latency_penalty_minutes"]').element as HTMLInputElement).value).toBe('10')
    await wrapper.get('[data-testid="latency_penalty_minutes"]').setValue(15)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, latency_penalty_minutes: 15 })
    await wrapper.get('[data-testid="latency_penalty_minutes"]').setValue(0)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, latency_penalty_minutes: 0 })
    wrapper.unmount()
  })

  it('shows the penalty deadline and distinguishes recovery exploration', async () => {
    const candidate: PriorityCandidate = { account_id: 1, account_name: 'Mock recovery', score: 75, tier: 'degraded', reasons: ['latency_above_target'], priority: 1, concurrency: 20, load_factor: 10000, load_percent: 0, waiting: 0, rate: 0.1, revenue: 1, theoretical_cost: 0.2, profit: 0.8, margin: 0.8, economics_source: 'usage', profit_samples: 7, samples: 7, p90_ttft_ms: 19000, quality_passed: 10, quality_samples: 10, latency_penalty_until: '2026-10-14T02:00:00Z' }
    const snapshot = { at: '2026-10-14T01:00:00Z', model: 'gpt-test', group_id: 11, mode: 'profit', history_ready: true, candidates: [candidate] }
    vi.mocked(getPrioritySnapshot).mockResolvedValue(snapshot)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.text()).toContain('priorityScheduling.latencyPenaltyUntil')
    expect(wrapper.text()).not.toContain('priorityScheduling.explorationEligible')
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ ...snapshot, candidates: [{ ...candidate, tier: 'insufficient', latency_recovery: true, exploration_eligible: true, reasons: ['latency_recovery'] }] })
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(wrapper.text()).not.toContain('priorityScheduling.latencyPenaltyUntil')
    expect(wrapper.text()).toContain('priorityScheduling.reasons.latency_recovery')
    expect(wrapper.text()).toContain('priorityScheduling.explorationEligible')
    wrapper.unmount()
  })

  it('shows the active penalty mode, soft-penalty fallback, and explicit pool fallback', async () => {
    const candidate: PriorityCandidate = { account_id: 1, account_name: 'Mock slow', score: 61.6, tier: 'degraded', reasons: ['latency_above_target'], priority: 1, concurrency: 10000, load_factor: 10000, load_percent: 0, waiting: 0, rate: 0.1, revenue: 0.41, theoretical_cost: 0.07, profit: 0.34, margin: 0.83, economics_source: 'usage', profit_samples: 11, samples: 11, p90_ttft_ms: 15884, quality_passed: 0, quality_samples: 0, latency_penalty_until: '2026-10-31T05:33:18Z' }
    const snapshot = { at: '2026-10-31T05:23:23Z', model: 'gpt-test', group_id: 11, mode: 'custom', latency_penalty_mode: 'deprioritize' as const, latency_fallback: true, history_ready: true, candidates: [candidate] }
    vi.mocked(getPrioritySnapshot).mockResolvedValue(snapshot)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.text()).toContain('priorityScheduling.latencyPenaltyMode: priorityScheduling.latencyPenaltyModes.deprioritize')
    expect(wrapper.text()).toContain('priorityScheduling.softPenalty')
    expect(wrapper.get('[data-testid="priority-latency-fallback"]').text()).toBe('priorityScheduling.latencyFallback')
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ ...snapshot, latency_penalty_mode: 'stop' })
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(wrapper.text()).toContain('priorityScheduling.latencyPenaltyModes.stop')
    expect(wrapper.text()).not.toContain('priorityScheduling.softPenalty')
    wrapper.unmount()
  })

  it('defaults legacy settings off and saves the quota cutoff and explicit disable', async () => {
    const legacy = { ...config }; delete legacy.oauth_quota_priority; delete legacy.oauth_quota_threshold
    vi.mocked(getPriorityConfig).mockResolvedValue(legacy)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect((wrapper.get('[data-testid="oauth-quota-priority"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[data-testid="oauth-quota-threshold"]').exists()).toBe(false)
    await wrapper.get('[data-testid="oauth-quota-priority"]').setValue(true)
    expect((wrapper.get('[data-testid="oauth-quota-threshold"]').element as HTMLInputElement).value).toBe('90')
    await wrapper.get('[data-testid="oauth-quota-threshold"]').setValue(85)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, oauth_quota_priority: true, oauth_quota_threshold: 85 })
    await wrapper.get('[data-testid="oauth-quota-priority"]').setValue(false)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, oauth_quota_priority: false, oauth_quota_threshold: 85 })
    wrapper.unmount()
  })

  it('can retain strict BPS preference explicitly', async () => {
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="balance-protocols"]').setValue(false)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenCalledWith({ ...config, balance_protocols: false })
    wrapper.unmount()
  })
  it('shows capacity weights, group bindings and exploration without treating scores as selections', async () => {
    const candidate: PriorityCandidate = { account_id: 1, account_name: 'Mock OAuth', oauth_quota_role: 'standby', score: 75, tier: 'insufficient', reasons: [], priority: 1, concurrency: 20, load_factor: 10000, load_percent: 0, waiting: 0, rate: 0.1, revenue: 0, theoretical_cost: 0, profit: null, margin: null, economics_source: 'unknown', profit_samples: 0, samples: 0, p90_ttft_ms: 0, quality_passed: 0, quality_samples: 0, bound_groups: 2, selection_weight: 12.345, exploration_eligible: true }
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-09-29T12:00:00Z', model: 'gpt-test', group_id: 11, mode: 'profit', selection_policy: 'capacity_first', history_ready: true, candidates: [candidate] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.text()).toContain('priorityScheduling.boundGroups: 2')
    expect(wrapper.text()).toContain('priorityScheduling.selectionWeight: 12.35')
    expect(wrapper.text()).toContain('priorityScheduling.explorationEligible')
    // Rolling upgrades can still return the previous snapshot shape.
    const legacy = { ...candidate }; delete legacy.selection_weight; delete legacy.bound_groups; delete legacy.exploration_eligible
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-09-29T12:00:00Z', model: 'gpt-test', group_id: 11, mode: 'profit', history_ready: true, candidates: [legacy] })
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(wrapper.text()).toContain('Mock OAuth')
    expect(wrapper.text()).toContain('priorityScheduling.oauthQuotaRoles.standby')
    expect(wrapper.text()).not.toContain('priorityScheduling.selectionWeight:')
    wrapper.unmount()
  })
  it('removes purchase-cost controls and saves without legacy Teams settings', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, teams: { enabled: true, cost_cny: 50 } } as PrioritySchedulingConfig)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.find('[data-testid="teams-recovery"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="teams-window-source"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenCalledWith(config)
    wrapper.unmount()
  })
  it('saves enabled strategy and exact group/model scope', async () => {
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="enabled"]').setValue(true)
    await wrapper.get('[data-testid="mode"]').setValue('profit')
    await wrapper.get('[data-testid="groups"]').setValue('5, 7, 5')
    await wrapper.get('textarea').setValue('gpt-test\ngpt-other\n')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenCalledWith({ ...config, enabled: true, mode: 'profit', group_ids: [5, 7], models: ['gpt-test', 'gpt-other'] })
    expect(wrapper.text()).toContain('priorityScheduling.saved'); wrapper.unmount()
  })
  it('rejects invalid groups without sending a write', async () => {
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="groups"]').setValue('5, invalid')
    await wrapper.get('form').trigger('submit'); expect(savePriorityConfig).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('preserves draft when score refresh fails and displays the error', async () => {
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="mode"]').setValue('custom')
    vi.mocked(getPrioritySnapshot).mockRejectedValueOnce(new Error('offline'))
    await (wrapper.vm as any).refresh(); expect(wrapper.get('[role="alert"]').text()).toContain('priorityScheduling.error')
    expect((wrapper.get('[data-testid="mode"]').element as HTMLSelectElement).value).toBe('custom'); wrapper.unmount()
  })
  it('ignores in-flight settings after logout', async () => {
    let resolve!: (c: PrioritySchedulingConfig) => void
    vi.mocked(getPriorityConfig).mockReturnValueOnce(new Promise(r => { resolve = r }))
    const wrapper = mount(PrioritySchedulingView); state.auth.user = null; resolve(config); await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false); wrapper.unmount()
  })

  it('renders latency analysis and clears sticky bindings', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, group_ids: [11] })
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] })
    vi.mocked(getPriorityLatency).mockResolvedValue({
      at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, bucket_minutes: 5,
      accounts: [{ account_id: 1, account_name: 'Mock OAuth', samples: 10, p50_ttft_ms: 500, p90_ttft_ms: 1500, p99_ttft_ms: 4000, selected_ttft_ms: 500, latency_percentile: 50, slow_samples: 1, error_samples: 2, error_rate: 0.1667, buckets: [{ account_id: 1, bucket: '2026-10-30T00:00:00Z', samples: 10, p50_ttft_ms: 500, p90_ttft_ms: 1500, p99_ttft_ms: 4000, selected_ttft_ms: 500, slow_samples: 1, error_samples: 0 }] }],
    })
    vi.mocked(getPriorityStickyBindings).mockResolvedValue({ group_id: 11, total: 3, by_account: [{ account_id: 1, count: 3 }] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.find('[data-testid="latency-panel"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('priorityScheduling.latencyPanel.title')
    expect(wrapper.text()).toContain('1500 ms')
    expect(wrapper.text()).toContain('priorityScheduling.sticky.total')
    await wrapper.get('[data-testid="sticky-clear-1"]').trigger('click'); await flushPromises()
    expect(clearPriorityStickyBindings).toHaveBeenCalledWith({ group_id: 11, account_ids: [1] })
    wrapper.unmount()
  })

  it('uses the configured percentile in every heatmap bucket', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, latency_percentile: 50 })
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] })
    vi.mocked(getPriorityLatency).mockResolvedValue({
      at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, bucket_minutes: 5, latency_percentile: 50, latency_measurement: 'attributed_v1',
      accounts: [{ account_id: 1, account_name: 'Mock OAuth', samples: 10, p50_ttft_ms: 500, p90_ttft_ms: 1500, p99_ttft_ms: 4000, selected_ttft_ms: 500, latency_percentile: 50, slow_samples: 1, error_samples: 2, error_rate: 0.1667, buckets: [{ account_id: 1, bucket: '2026-10-30T00:00:00Z', samples: 10, p50_ttft_ms: 500, p90_ttft_ms: 1500, p99_ttft_ms: 4000, selected_ttft_ms: 500, slow_samples: 1, error_samples: 0 }] }],
    })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    const rows = wrapper.get('[data-testid="latency-panel"]').findAll('tbody tr')
    const heatmap = rows.at(-1)!
    expect(heatmap.findAll('td')[1].text()).toBe('500')
    wrapper.unmount()
  })

  it('drops sticky rows as soon as the selected group changes', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, group_ids: [11] })
    vi.mocked(getPriorityStickyBindings).mockResolvedValue({ group_id: 11, total: 3, by_account: [{ account_id: 1, count: 3 }] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.find('[data-testid="sticky-clear-1"]').exists()).toBe(true)
    await wrapper.get('[data-testid="sticky-group"]').setValue('12'); await flushPromises()
    expect(wrapper.find('[data-testid="sticky-clear-1"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not let a recompute response overwrite a newer refreshed scope', async () => {
    const oldSnapshot = { at: '2026-10-30T00:00:00Z', model: 'old-model', group_id: 11, mode: 'balanced', history_ready: true, candidates: [] }
    let resolveRecompute!: (value: any) => void
    vi.mocked(getPrioritySnapshot).mockResolvedValue(oldSnapshot)
    vi.mocked(recomputePriorityScheduling).mockReturnValueOnce(new Promise(resolve => { resolveRecompute = resolve }))
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="recompute"]').trigger('click')
    vi.mocked(getPrioritySnapshot).mockResolvedValueOnce({ ...oldSnapshot, at: '2026-10-30T00:01:00Z', model: 'new-model', group_id: 12 })
    await (wrapper.vm as any).refresh(); await flushPromises()
    resolveRecompute({
      before: oldSnapshot, after: oldSnapshot, timed_out: false,
      diff: [{ account_id: 1, account_name: 'Old scope', old_rank: 2, new_rank: 1, old_score: 70, new_score: 80, old_weight: 1, new_weight: 2, old_tier: 'insufficient', new_tier: 'eligible', old_p90_ttft_ms: 5000, new_p90_ttft_ms: 1000, old_samples: 5, new_samples: 6, old_error_samples: 1, new_error_samples: 0, changed: true }],
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('new-model')
    wrapper.unmount()
  })

  it('does not expose sticky cleanup for timed out recomputation', async () => {
    vi.mocked(recomputePriorityScheduling).mockResolvedValue({
      before: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] }, after: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] }, timed_out: true,
      diff: [{ account_id: 1, account_name: 'Mock OAuth', old_rank: 2, new_rank: 1, old_score: 70, new_score: 80, old_weight: 1, new_weight: 2, old_tier: 'insufficient', new_tier: 'eligible', old_p90_ttft_ms: 5000, new_p90_ttft_ms: 1000, old_samples: 5, new_samples: 6, old_error_samples: 1, new_error_samples: 0, changed: true }],
    })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="clear-changed-sticky"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('treats a failed history recompute as an error without a diff or cleanup action', async () => {
    const failed = { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'error' as const, history_error: 'timeout', history_ready: false, candidates: [] }
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, group_ids: [11] })
    vi.mocked(getPrioritySnapshot).mockResolvedValue(failed)
    vi.mocked(recomputePriorityScheduling).mockResolvedValue({ before: failed, after: failed, timed_out: false, diff: [{ account_id: 1, account_name: 'Mock OAuth', old_rank: 2, new_rank: 1, old_score: 70, new_score: 80, old_weight: 1, new_weight: 2, old_tier: 'insufficient', new_tier: 'eligible', old_p90_ttft_ms: 5000, new_p90_ttft_ms: 1000, old_samples: 5, new_samples: 6, old_error_samples: 1, new_error_samples: 0, changed: true }] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="clear-changed-sticky"]').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('priorityScheduling.recompute.error')
    wrapper.unmount()
  })

  it('clears changed sticky rows in the recompute result scope', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, group_ids: [11] })
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] })
    vi.mocked(recomputePriorityScheduling).mockResolvedValue({
      before: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] },
      after: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] },
      timed_out: false,
      diff: [{ account_id: 1, account_name: 'Mock OAuth', old_rank: 2, new_rank: 1, old_score: 70, new_score: 80, old_weight: 1, new_weight: 2, old_tier: 'insufficient', new_tier: 'eligible', old_p90_ttft_ms: 5000, new_p90_ttft_ms: 1000, old_samples: 5, new_samples: 6, old_error_samples: 1, new_error_samples: 0, changed: true }],
    })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="sticky-group"]').setValue('12')
    await wrapper.get('[data-testid="clear-changed-sticky"]').trigger('click'); await flushPromises()
    expect(clearPriorityStickyBindings).toHaveBeenLastCalledWith({ group_id: 11, account_ids: [1] })
    wrapper.unmount()
  })

  it('shows recompute diff and clears changed accounts sticky bindings', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, group_ids: [11] })
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] })
    vi.mocked(recomputePriorityScheduling).mockResolvedValue({
      before: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] },
      after: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] }, timed_out: false,
      diff: [{ account_id: 1, account_name: 'Mock OAuth', old_rank: 2, new_rank: 1, old_score: 70, new_score: 80, old_weight: 1, new_weight: 2, old_tier: 'insufficient', new_tier: 'eligible', old_p90_ttft_ms: 5000, new_p90_ttft_ms: 1000, old_samples: 5, new_samples: 6, old_error_samples: 1, new_error_samples: 0, changed: true }],
    })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('70.0 → 80.0')
    await wrapper.get('[data-testid="clear-changed-sticky"]').trigger('click'); await flushPromises()
    expect(clearPriorityStickyBindings).toHaveBeenCalledWith({ group_id: 11, account_ids: [1] })
    wrapper.unmount()
  })

  it('reads back the recomputed group after automatically switching the sticky scope', async () => {
    vi.mocked(getPriorityConfig).mockResolvedValue({ ...config, group_ids: [12] })
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] })
    vi.mocked(getPriorityStickyBindings).mockResolvedValueOnce({ group_id: 12, total: 4, by_account: [{ account_id: 2, count: 4 }] })
    let resolveGroup11!: (value: any) => void
    vi.mocked(getPriorityStickyBindings).mockReturnValueOnce(new Promise(resolve => { resolveGroup11 = resolve }) as any)
    vi.mocked(recomputePriorityScheduling).mockResolvedValue({
      before: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] },
      after: { at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] },
      timed_out: false, diff: [],
    })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.get('[data-testid="sticky-group"]').element).toHaveProperty('value', '12')
    await wrapper.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(getPriorityStickyBindings).toHaveBeenLastCalledWith(11)
    resolveGroup11({ group_id: 11, total: 2, by_account: [{ account_id: 1, count: 2 }] }); await flushPromises()
    expect(wrapper.get('[data-testid="sticky-group"]').element).toHaveProperty('value', '11')
    expect(wrapper.find('[data-testid="sticky-scope"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="sticky-scope-account"]').text()).toContain('#1')
    expect(wrapper.find('[data-testid="sticky-clear-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="sticky-clear-2"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('supersedes an in-flight old latency window with the selected window', async () => {
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, candidates: [] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    let resolveOld!: (value: any) => void
    const oldWindow = new Promise(resolve => { resolveOld = resolve })
    const newer = { at: '2026-10-30T00:15:00Z', model: 'gpt-test', group_id: 11, bucket_minutes: 5, latency_percentile: 90, latency_measurement: 'legacy_forward', accounts: [{ account_id: 1, account_name: 'New-window account', samples: 5, p50_ttft_ms: 700, p90_ttft_ms: 900, p99_ttft_ms: 1200, selected_ttft_ms: 900, latency_percentile: 90, latency_measurement: 'legacy_forward', slow_samples: 0, error_samples: 0, error_rate: 0, buckets: [{ account_id: 1, bucket: '2026-10-30T00:15:00Z', samples: 5, p50_ttft_ms: 700, p90_ttft_ms: 900, p99_ttft_ms: 1200, selected_ttft_ms: 900, slow_samples: 0, error_samples: 0 }] }] }
    vi.mocked(getPriorityLatency).mockReset().mockReturnValueOnce(oldWindow as any).mockResolvedValueOnce(newer)
    const oldRequest = (wrapper.vm as any).loadLatency()
    await flushPromises()
    expect(getPriorityLatency).toHaveBeenCalledTimes(1)
    expect(getPriorityLatency).toHaveBeenLastCalledWith(60, 5)
    await wrapper.get('[data-testid="latency-window"]').setValue(15); await flushPromises()
    expect(getPriorityLatency).toHaveBeenCalledTimes(2)
    expect(getPriorityLatency).toHaveBeenLastCalledWith(15, 5)
    resolveOld({ at: '2026-10-30T00:00:00Z', model: 'gpt-test', group_id: 11, bucket_minutes: 5, latency_percentile: 90, latency_measurement: 'legacy_forward', accounts: [{ account_id: 1, account_name: 'Old-window account', samples: 1, p50_ttft_ms: 5000, p90_ttft_ms: 6000, p99_ttft_ms: 7000, selected_ttft_ms: 6000, slow_samples: 1, error_samples: 0, error_rate: 0, buckets: [] }] })
    await flushPromises()
    expect(wrapper.get('[data-testid="latency-panel"]').text()).toContain('New-window account')
    expect(wrapper.get('[data-testid="latency-panel"]').text()).not.toContain('Old-window account')
    await oldRequest
    wrapper.unmount()
  })
})

describe('priority history refresh', () => {
  it('distinguishes a failed refresh from an empty successful history', async () => {
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-06T00:00:00Z', evaluated_at: '2026-10-06T00:01:00Z', model: 'test', group_id: 11, mode: 'balanced', history_ready: false, history_status: 'error', history_error: 'timeout', candidates: [] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.text()).toContain('priorityScheduling.historyStates.error')
    expect(wrapper.text()).not.toContain('priorityScheduling.historyPending')
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-06T00:00:00Z', model: 'test', group_id: 11, mode: 'balanced', history_ready: true, history_status: 'ready', candidates: [] })
    await (wrapper.vm as any).refresh()
    expect(wrapper.text()).not.toContain('priorityScheduling.historyStates.error')
    expect(wrapper.get('[data-testid="priority-no-candidates"]').text()).toBe('priorityScheduling.noCandidates')
    expect(wrapper.find('[data-testid="priority-candidate-scroll"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('keeps the selection time while refreshed live error evidence and evaluation time advance', async () => {
    const candidate: PriorityCandidate = { account_id: 1, account_name: 'Read time', score: 70, tier: 'eligible', reasons: [], priority: 1, concurrency: 5, load_factor: 5, load_percent: 0, waiting: 0, rate: 0.1, revenue: 0, theoretical_cost: 0, profit: null, margin: null, economics_source: 'rate', profit_samples: 0, samples: 0, p90_ttft_ms: 0, quality_passed: 1, quality_samples: 1, error_rate: 0.5, error_successes: 2, error_failures: 2, error_window_minutes: 10, error_prior_samples: 5, error_observed_at: '2026-10-22T01:05:00Z', error_endpoint: '/v1/responses', error_penalty: 0.9, base_score: 70 }
    const at = '2026-10-22T01:00:00Z'
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at, evaluated_at: '2026-10-22T01:05:00Z', model: 'test', group_id: 1, mode: 'balanced', history_ready: true, history_status: 'ready', candidates: [candidate] })
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.get('[data-testid="priority-snapshot-at"]').text()).toContain(new Date(at).toLocaleString())
    expect(wrapper.get('[data-testid="priority-evaluated-at"]').text()).toContain(new Date('2026-10-22T01:05:00Z').toLocaleString())
    expect(wrapper.get('[data-testid="error-rate-1"]').text()).toContain('50.0%')
    expect(wrapper.get('[data-testid="error-rate-1"]').text()).toContain('priorityScheduling.errorObservedAt')
    vi.mocked(getPrioritySnapshot).mockResolvedValue({ at, evaluated_at: '2026-10-22T01:06:00Z', model: 'test', group_id: 1, mode: 'balanced', history_ready: true, history_status: 'ready', candidates: [{ ...candidate, error_rate: 0.2, error_successes: 8, error_failures: 2, error_observed_at: '2026-10-22T01:06:00Z' }] })
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(wrapper.get('[data-testid="priority-snapshot-at"]').text()).toContain(new Date(at).toLocaleString())
    expect(wrapper.get('[data-testid="priority-evaluated-at"]').text()).toContain(new Date('2026-10-22T01:06:00Z').toLocaleString())
    const evidence = wrapper.get('[data-testid="error-rate-1"]').text()
    expect(evidence).toContain('20.0%')
    expect(evidence).toContain('priorityScheduling.errorAttempts: 2 / 10')
    expect(evidence).toContain(new Date('2026-10-22T01:06:00Z').toLocaleString())
    wrapper.unmount()
  })
  it.each([true, false])('picks up history while refreshing=%s and continues bounded polling after an empty completed read', async (refreshing) => {
    vi.useFakeTimers()
    try {
      const snapshot = { at: '2026-10-06T00:00:00Z', model: 'test', group_id: 11, mode: 'balanced', history_ready: false, history_status: 'loading' as const, history_refreshing: refreshing, candidates: [] }
      vi.mocked(getPrioritySnapshot).mockResolvedValueOnce(snapshot).mockResolvedValue({ ...snapshot, history_ready: true, history_status: 'ready', history_refreshing: false })
      const wrapper = mount(PrioritySchedulingView); await flushPromises()
      await vi.advanceTimersByTimeAsync(1000); await flushPromises()
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(10000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(4)
      wrapper.unmount()
    } finally { vi.useRealTimers() }
  })
  it('bounds background reads and cancels them on logout', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(getPrioritySnapshot).mockResolvedValue({ at: '2026-10-06T00:00:00Z', model: 'test', group_id: 11, mode: 'balanced', history_ready: false, history_refreshing: true, candidates: [] })
      const wrapper = mount(PrioritySchedulingView); await flushPromises()
      await vi.advanceTimersByTimeAsync(10000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(5)
      await (wrapper.vm as any).refresh()
      state.auth.user = null
      await vi.advanceTimersByTimeAsync(10000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(6)
      expect(wrapper.text()).not.toContain('2026')
      wrapper.unmount()
    } finally { vi.useRealTimers() }
  })
  it('refreshes newly completed usage after a ready empty result and stops on unmount', async () => {
    vi.useFakeTimers()
    try {
      const value = { at: '2026-10-06T00:00:00Z', model: 'public-alias', group_id: 11, mode: 'balanced', history_ready: true, history_status: 'ready' as const, candidates: [] }
      vi.mocked(getPrioritySnapshot).mockResolvedValue(value)
      const wrapper = mount(PrioritySchedulingView); await flushPromises()
      await vi.advanceTimersByTimeAsync(4999)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1); await flushPromises()
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(2)
      wrapper.unmount()
      await vi.advanceTimersByTimeAsync(30000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(2)
    } finally { vi.useRealTimers() }
  })
  it('pauses hidden tabs and never overlaps pending snapshot requests', async () => {
    vi.useFakeTimers()
    try {
      const value = { at: '2026-10-06T00:00:00Z', model: 'test', mode: 'balanced', history_ready: true, candidates: [] }
      vi.mocked(getPrioritySnapshot).mockResolvedValue(value)
      const wrapper = mount(PrioritySchedulingView); await flushPromises()
      const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
      await vi.advanceTimersByTimeAsync(20000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(1)
      visibility.mockReturnValue('visible')
      let resolve!: (result: typeof value) => void
      vi.mocked(getPrioritySnapshot).mockImplementationOnce(() => new Promise(r => { resolve = r }))
      await vi.advanceTimersByTimeAsync(5000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(2)
      await (wrapper.vm as any).refresh()
      await vi.advanceTimersByTimeAsync(20000)
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(2)
      resolve(value); await flushPromises()
      wrapper.unmount()
    } finally { vi.restoreAllMocks(); vi.useRealTimers() }
  })

  it('discovers the first account snapshot after opening an empty page', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(getPrioritySnapshot).mockResolvedValueOnce(null).mockResolvedValue({ at: '2026-10-06T00:00:00Z', model: 'first-public-model', mode: 'balanced', history_ready: true, candidates: [] })
      const wrapper = mount(PrioritySchedulingView); await flushPromises()
      expect(wrapper.text()).not.toContain('first-public-model')
      await vi.advanceTimersByTimeAsync(5000); await flushPromises()
      expect(getPrioritySnapshot).toHaveBeenCalledTimes(2)
      expect(wrapper.text()).toContain('first-public-model')
      wrapper.unmount()
    } finally { vi.useRealTimers() }
  })

  it('defaults legacy penalty action to soft and saves either explicit action with its minutes', async () => {
    const legacy = { ...config }; delete legacy.latency_penalty_mode
    vi.mocked(getPriorityConfig).mockResolvedValue(legacy)
    const wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect((wrapper.get('[data-testid="latency-penalty-mode"]').element as HTMLSelectElement).value).toBe('deprioritize')
    await wrapper.get('[data-testid="latency-penalty-mode"]').setValue('stop')
    await wrapper.get('[data-testid="latency_penalty_minutes"]').setValue(10)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith({ ...config, latency_penalty_mode: 'stop' })
    await wrapper.get('[data-testid="latency-penalty-mode"]').setValue('deprioritize')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePriorityConfig).toHaveBeenLastCalledWith(config)
    wrapper.unmount()
  })

})
