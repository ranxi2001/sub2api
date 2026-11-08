import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import PrioritySchedulingView from '../PrioritySchedulingView.vue'
import DataTable from '@/components/common/DataTable.vue'
import * as api from '@/api/admin/priorityScheduling'

const state = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/priorityScheduling', () => ({ getPriorityConfig: vi.fn(), getPrioritySnapshot: vi.fn(), savePriorityConfig: vi.fn(), getPriorityLatency: vi.fn(), recomputePriorityScheduling: vi.fn(), getPriorityStickyBindings: vi.fn(), clearPriorityStickyBindings: vi.fn() }))

const candidate = (id: number): api.PriorityCandidate => ({ account_id: id, account_name: `Fixture account ${id}`, score: 80, tier: 'eligible', reasons: [], priority: 1, concurrency: 10, load_factor: 10, load_percent: 20, waiting: 0, rate: 0.5, revenue: 1, theoretical_cost: 0.2, profit: 0.8, margin: 0.8, economics_source: 'usage', profit_samples: 20, samples: 20, p90_ttft_ms: 1500, quality_passed: 20, quality_samples: 20 })
const snapshot = (count = 65): api.PrioritySnapshot => ({ at: '2026-11-08T00:00:00Z', model: 'fixture-model', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, config_revision: 'fixture-v1', candidates: Array.from({ length: count }, (_, i) => candidate(i + 1)) })
const series = (count = 65): api.PriorityLatencySeries => ({ at: '2026-11-08T00:00:00Z', model: 'fixture-model', group_id: 11, bucket_minutes: 5, config_revision: 'fixture-v1', accounts: Array.from({ length: count }, (_, i) => ({ account_id: i + 1, account_name: `Fixture latency ${i + 1}`, samples: 20, p50_ttft_ms: 500, p90_ttft_ms: 1500 + i, p99_ttft_ms: 3000, error_samples: 1, slow_samples: 1, error_rate: 0.05, buckets: [{ account_id: i + 1, bucket: '2026-11-08T00:00:00Z', samples: 20, p50_ttft_ms: 500, p90_ttft_ms: 1500 + i, p99_ttft_ms: 3000, error_samples: 1, slow_samples: 1 }] })) })
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason?: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => {
  vi.resetAllMocks()
  state.auth = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(api.getPrioritySnapshot).mockResolvedValue(snapshot())
  vi.mocked(api.getPriorityLatency).mockResolvedValue(series())
  vi.mocked(api.getPriorityStickyBindings).mockResolvedValue({ group_id: 2, total: 0, by_account: [] })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })

describe('priority reading stability', () => {
  it('retains latency accounts while a same-scope refresh is pending', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    // Works on both the expanded baseline and the default-collapsed candidate.
    const toggle = wrapper.find('[data-testid="latency-toggle"]')
    if (toggle.exists()) await toggle.trigger('click')
    const panel = wrapper.get('[data-testid="latency-panel"]')
    expect(panel.text()).toContain('Fixture latency 1')
    const pending = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityLatency).mockReturnValueOnce(pending.promise)
    const read = (wrapper.vm as any).loadLatency(); await flushPromises()
    expect(panel.text(), 'same-scope refresh must retain readable latency accounts').toContain('Fixture latency 1')
    pending.resolve(series()); await read; await flushPromises()
  })
})


describe('priority paginated reading and isolation', () => {
  it('defaults latency collapsed and paginates both real tables with synchronized buckets', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.get('[data-testid="latency-toggle"]').attributes('aria-expanded')).toBe('false')
    await wrapper.get('[data-testid="latency-toggle"]').trigger('click')
    const tables = wrapper.findAllComponents(DataTable)
    expect(tables).toHaveLength(3)
    const pager = (id: string) => wrapper!.getComponent(`[data-testid="${id}-pagination"]`)
    for (const id of ['candidate', 'latency']) {
      pager(id).vm.$emit('update:pageSize', 10); await flushPromises()
      pager(id).vm.$emit('update:page', 3); await flushPromises()
      expect(pager(id).props('page')).toBe(3)
      expect(pager(id).props('pageSize')).toBe(10)
    }
    const latencyTable = wrapper.getComponent('[data-testid="latency-table"]')
    const bucketTable = wrapper.getComponent('[data-testid="latency-buckets"]')
    expect(latencyTable.props('data').map((r: any) => r.account_id)).toEqual(bucketTable.props('data').map((r: any) => r.account_id))
    expect(latencyTable.props('data')).toHaveLength(10)
    expect(wrapper.getComponent('[data-testid="candidate-table"]').props('data')[0].account_id).toBe(21)
    vi.mocked(api.getPrioritySnapshot).mockResolvedValue({ ...snapshot(), at: '2026-11-08T00:01:00Z', candidates: [...snapshot().candidates].reverse().map(r => ({ ...r, score: 1 })) })
    vi.mocked(api.getPriorityLatency).mockResolvedValue({ ...series(), accounts: [...series().accounts].reverse().map(r => ({ ...r, p90_ttft_ms: 99 })) })
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(pager('candidate').props('page')).toBe(3)
    expect(pager('latency').props('pageSize')).toBe(10)
    expect(wrapper.getComponent('[data-testid="candidate-table"]').props('data')[0].account_id).toBe(21)
    expect(latencyTable.props('data')[0].account_id).toBe(21)
    expect(wrapper.get('[data-testid="latency-toggle"]').attributes('aria-expanded')).toBe('true')
    vi.mocked(api.getPrioritySnapshot).mockResolvedValue(snapshot(3))
    vi.mocked(api.getPriorityLatency).mockResolvedValue(series(3))
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(pager('candidate').props('page')).toBe(1)
    expect(pager('latency').props('page')).toBe(1)
    expect(pager('latency').props('pageSize')).toBe(10)
  })

  it('keeps content on failure and recovers without overwriting an unsaved draft', async () => {
    vi.mocked(api.getPriorityConfig).mockResolvedValue({ enabled: false, mode: 'balanced', group_ids: [], models: [] } as any)
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="enabled"]').setValue(true)
    await wrapper.get('[data-testid="latency-toggle"]').trigger('click')
    vi.mocked(api.getPriorityLatency).mockRejectedValueOnce(new Error('offline'))
    await (wrapper.vm as any).loadLatency(); await flushPromises()
    expect(wrapper.get('[data-testid="latency-table"]').text()).toContain('Fixture latency 1')
    expect(wrapper.get('[data-testid="latency-status"]').text()).toContain('priorityScheduling.reading.stale')
    vi.mocked(api.getPrioritySnapshot).mockRejectedValueOnce(new Error('offline'))
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(wrapper.get('[data-testid="candidate-table"]').text()).toContain('Fixture account 1')
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(wrapper.get('[data-testid="enabled"]').element).toHaveProperty('checked', true)
    expect(api.getPriorityConfig).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-testid="latency-status"]').text()).not.toContain('priorityScheduling.reading.stale')
  })

  it.each([
    { model: 'other-model' }, { group_id: 12 }, { config_revision: 'fixture-v2' },
    { latency_percentile: 50 }, { latency_measurement: 'attributed_v1' }, { window_minutes: 180 },
  ])('isolates late latency when semantics change: %o', async change => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const pending = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityLatency).mockReturnValueOnce(pending.promise)
    const old = (wrapper.vm as any).loadLatency(); await flushPromises()
    vi.mocked(api.getPrioritySnapshot).mockResolvedValue({ ...snapshot(0), ...change })
    const current = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityLatency).mockReturnValueOnce(current.promise)
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect((wrapper.vm as any).latency).toBeNull()
    pending.resolve(series()); await old; await flushPromises()
    expect((wrapper.vm as any).latency).toBeNull()
    const next = { ...series(0), model: change.model ?? 'fixture-model', group_id: change.group_id ?? 11, ...change }
    current.resolve(next); await flushPromises()
    expect((wrapper.vm as any).latency?.accounts).toEqual([])
  })

  it('clears old window immediately and ignores its late response', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const old = deferred<api.PriorityLatencySeries>(), current = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityLatency).mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const read = (wrapper.vm as any).loadLatency(); await flushPromises()
    await wrapper.get('[data-testid="latency-window"]').setValue(15)
    expect((wrapper.vm as any).latency).toBeNull()
    old.resolve(series()); await read; await flushPromises()
    expect((wrapper.vm as any).latency).toBeNull()
    current.resolve(series(0)); await flushPromises()
    expect((wrapper.vm as any).latency?.accounts).toEqual([])
  })

  it('clears an empty snapshot and never accepts the prior latency response', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const old = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityLatency).mockReturnValueOnce(old.promise)
    const read = (wrapper.vm as any).loadLatency()
    vi.mocked(api.getPrioritySnapshot).mockResolvedValue(null)
    await (wrapper.vm as any).refresh(); await flushPromises()
    old.resolve(series()); await read; await flushPromises()
    expect((wrapper.vm as any).latency).toBeNull()
    expect(wrapper.find('[data-testid="candidate-table"]').exists()).toBe(false)
  })

  it.each(['logout', 'unmount'])('does not restart requests after delayed config and %s', async action => {
    vi.useFakeTimers()
    const pending = deferred<api.PrioritySchedulingConfig>()
    vi.mocked(api.getPriorityConfig).mockReturnValueOnce(pending.promise)
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    if (action === 'logout') state.auth.user = null
    else { wrapper.unmount(); wrapper = undefined }
    await flushPromises()
    const reads = [vi.mocked(api.getPrioritySnapshot).mock.calls.length, vi.mocked(api.getPriorityLatency).mock.calls.length, vi.mocked(api.getPriorityStickyBindings).mock.calls.length]
    pending.resolve({ group_ids: [11] } as any); await flushPromises()
    await vi.advanceTimersByTimeAsync(30000); await flushPromises()
    expect([vi.mocked(api.getPrioritySnapshot).mock.calls.length, vi.mocked(api.getPriorityLatency).mock.calls.length, vi.mocked(api.getPriorityStickyBindings).mock.calls.length]).toEqual(reads)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['at', 'config_revision', 'candidates'])('rejects recompute identity mismatch in %s', async field => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const before = snapshot(), after = { ...before, [field]: field === 'candidates' ? [candidate(999)] : 'changed' }
    vi.mocked(api.recomputePriorityScheduling).mockResolvedValue({ before, after, diff: [], timed_out: false })
    await (wrapper.vm as any).recompute(); await flushPromises()
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="clear-changed-sticky"]').exists()).toBe(false)
  })
})


describe('slow latency polling', () => {
  it('lets a same-scope latency response finish across multiple snapshot polls', async () => {
    vi.useFakeTimers()
    const pending = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityLatency).mockReturnValue(pending.promise)
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    await vi.advanceTimersByTimeAsync(11000); await flushPromises()
    expect(api.getPrioritySnapshot).toHaveBeenCalledTimes(3)
    expect(api.getPriorityLatency).toHaveBeenCalledTimes(1)
    pending.resolve(series()); await flushPromises()
    expect((wrapper.vm as any).latency.accounts).toHaveLength(65)
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(api.getPriorityLatency).toHaveBeenCalledTimes(2)
  })
})


describe('latency response semantic identity', () => {
  it('rejects latency from a new configuration before the snapshot poll observes it', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const previous = (wrapper.vm as any).latency
    vi.mocked(api.getPriorityLatency).mockResolvedValue({ ...series(), config_revision: 'fixture-v2', accounts: [] })
    await (wrapper.vm as any).loadLatency(); await flushPromises()
    expect((wrapper.vm as any).latency).toEqual(previous)
    expect((wrapper.vm as any).latencyError).toBe('priorityScheduling.latencyPanel.error')
  })
})


describe('snapshot first read and mutation safety', () => {
  it('distinguishes a pending first snapshot from a successful empty snapshot', async () => {
    const pending = deferred<api.PrioritySnapshot | null>()
    vi.mocked(api.getPrioritySnapshot).mockReturnValueOnce(pending.promise)
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.get('[data-testid="snapshot-initial-state"]').text()).toBe('priorityScheduling.loading')
    pending.resolve(null); await flushPromises()
    expect(wrapper.get('[data-testid="snapshot-initial-state"]').text()).toBe('priorityScheduling.empty')
    expect(api.getPriorityLatency).not.toHaveBeenCalled()
  })

  it('invalidates a pending recompute on timestamp-only polling even though reading scope stays stable', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const pending = deferred<api.PrioritySchedulingRecomputeResult>(), before = snapshot()
    vi.mocked(api.recomputePriorityScheduling).mockReturnValueOnce(pending.promise)
    const recompute = (wrapper.vm as any).recompute(); await flushPromises()
    vi.mocked(api.getPrioritySnapshot).mockResolvedValue({ ...before, at: '2026-11-08T00:01:00Z' })
    await (wrapper.vm as any).refresh(); await flushPromises()
    pending.resolve({ before, after: before, diff: [], timed_out: false }); await recompute; await flushPromises()
    expect((wrapper.vm as any).snapshot.at).toBe('2026-11-08T00:01:00Z')
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(false)
    expect(wrapper.getComponent('[data-testid="candidate-table"]').props('data')[0].account_id).toBe(1)
  })

  it('does not clear sticky bindings when confirmation is cancelled', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    vi.mocked(api.getPriorityStickyBindings).mockResolvedValue({ group_id: 2, total: 3, by_account: [{ account_id: 1, count: 3 }] })
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    await wrapper.get('[data-testid="sticky-clear-1"]').trigger('click'); await flushPromises()
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('priorityScheduling.sticky.confirmAccount'))
    expect(api.clearPriorityStickyBindings).not.toHaveBeenCalled()
  })
})
