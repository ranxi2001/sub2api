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
vi.mock('@/api/admin/priorityScheduling', () => ({ getPriorityAnalysis: vi.fn(), getPriorityModels: async () => ['fixture-model','other-model'], getPriorityConfig: vi.fn(), getPrioritySnapshot: vi.fn(), savePriorityConfig: vi.fn(), getPriorityLatency: vi.fn(), recomputePriorityScheduling: vi.fn(), getPriorityStickyBindings: vi.fn(), clearPriorityStickyBindings: vi.fn() }))

vi.mock('@/api/admin/groups', () => ({ getAll: async () => ([{ id: 11, name: 'Fixture' }]), getModelAllowlistCandidates: async () => (['fixture-model','other-model']) }))

const candidate = (id: number): api.PriorityCandidate => ({ account_id: id, account_name: `Fixture account ${id}`, score: 80, tier: 'eligible', reasons: [], priority: 1, concurrency: 10, load_factor: 10, load_percent: 20, waiting: 0, rate: 0.5, revenue: 1, theoretical_cost: 0.2, profit: 0.8, margin: 0.8, economics_source: 'usage', profit_samples: 20, samples: 20, p90_ttft_ms: 1500, quality_passed: 20, quality_samples: 20 })
const snapshot = (count = 65): api.PrioritySnapshot => ({ at: '2026-11-08T00:00:00Z', model: 'fixture-model', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, window_minutes: 60, config_revision: 'fixture-v1', candidates: Array.from({ length: count }, (_, i) => candidate(i + 1)) })
const series = (count = 65): api.PriorityLatencySeries => ({ at: '2026-11-08T00:00:00Z', model: 'fixture-model', group_id: 11, bucket_minutes: 5, config_revision: 'fixture-v1', accounts: Array.from({ length: count }, (_, i) => ({ account_id: i + 1, account_name: `Fixture latency ${i + 1}`, samples: 20, p50_ttft_ms: 500, p90_ttft_ms: 1500 + i, p99_ttft_ms: 3000, error_samples: 1, slow_samples: 1, error_rate: 0.05, buckets: [{ account_id: i + 1, bucket: '2026-11-08T00:00:00Z', samples: 20, p50_ttft_ms: 500, p90_ttft_ms: 1500 + i, p99_ttft_ms: 3000, error_samples: 1, slow_samples: 1 }] })) })
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason?: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(api.getPriorityConfig).mockResolvedValue({ enabled: false, mode: 'balanced', group_ids: [11], models: ['fixture-model'] } as any)
  vi.mocked(api.getPriorityAnalysis).mockImplementation(async q => ({ snapshot: { ...snapshot(), model: q.model, group_id: q.group_id ?? null, window_minutes: q.minutes }, latency: { ...series(), model: q.model, group_id: q.group_id ?? null } }))
  state.auth = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(api.getPrioritySnapshot).mockResolvedValue(snapshot())
  vi.mocked(api.getPriorityLatency).mockResolvedValue(series())
  vi.mocked(api.getPriorityStickyBindings).mockResolvedValue({ group_id: 2, total: 0, by_account: [] })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })

describe('priority reading stability', () => {
  it('retains latency accounts while a same-scope refresh is pending', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    const toggle = wrapper.find('[data-testid="latency-toggle"]')
    if (toggle.exists()) await toggle.trigger('click')
    const panel = wrapper.get('[data-testid="latency-panel"]')
    expect(panel.text()).toContain('Fixture latency 1')
    const pending = deferred<api.PriorityAnalysis>(), legacy = deferred<api.PriorityLatencySeries>()
    vi.mocked(api.getPriorityAnalysis).mockReturnValueOnce(pending.promise)
    vi.mocked(api.getPriorityLatency).mockReturnValueOnce(legacy.promise)
    const read = (wrapper.vm as any).loadLatency(); await flushPromises()
    expect(panel.text(), 'same-scope refresh must retain readable latency accounts').toContain('Fixture latency 1')
    pending.resolve({ snapshot: snapshot(), latency: series() }); legacy.resolve(series()); await read; await flushPromises()
  })

  it('defaults collapsed, paginates both real tables and keeps the matching bucket page', async () => {
    wrapper = mount(PrioritySchedulingView); await flushPromises()
    expect(wrapper.get('[data-testid="latency-toggle"]').attributes('aria-expanded')).toBe('false')
    expect(wrapper.findAllComponents(DataTable)).toHaveLength(3)
    await wrapper.get('[data-testid="latency-toggle"]').trigger('click')
    for (const name of ['candidate', 'latency']) {
      const pager = wrapper.getComponent(`[data-testid="${name}-pagination"]`)
      pager.vm.$emit('update:pageSize', 10); pager.vm.$emit('update:page', 3)
    }
    await flushPromises()
    const rows = (id: string) => wrapper!.getComponent(`[data-testid="${id}"]`).props('data').map((r: any) => r.account_id)
    expect(rows('latency-table')).toEqual(rows('latency-buckets'))
    expect(rows('candidate-table')).toEqual(Array.from({length:10},(_,i)=>i+21))
    vi.mocked(api.getPriorityAnalysis).mockResolvedValue({ snapshot: { ...snapshot(), at: '2026-11-09T01:00:00Z', candidates: [...snapshot().candidates].reverse() }, latency: { ...series(), accounts: [...series().accounts].reverse() } })
    await (wrapper.vm as any).refresh(); await flushPromises()
    expect(rows('candidate-table')).toEqual(Array.from({length:10},(_,i)=>i+21))
    expect(rows('latency-table')).toEqual(rows('latency-buckets'))
    for (const name of ['candidate', 'latency']) expect(wrapper.getComponent(`[data-testid="${name}-pagination"]`).props()).toMatchObject({page:3,pageSize:10})
    vi.mocked(api.getPriorityAnalysis).mockResolvedValue({snapshot:snapshot(3),latency:series(3)})
    await (wrapper.vm as any).refresh(); await flushPromises()
    for (const name of ['candidate', 'latency']) expect(wrapper.getComponent(`[data-testid="${name}-pagination"]`).props()).toMatchObject({page:1,pageSize:10})
    expect(wrapper.get('[data-testid="latency-toggle"]').attributes('aria-expanded')).toBe('true')
  })

  it('keeps content on failure and recovers without overwriting an unsaved draft', async () => {
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    await wrapper.get('[data-testid="enabled"]').setValue(true)
    vi.mocked(api.getPriorityAnalysis).mockRejectedValueOnce(Error('fixture unavailable'))
    await (wrapper.vm as any).refresh();await flushPromises()
    expect(wrapper.get('[data-testid="candidate-table"]').text()).toContain('Fixture account 1')
    expect(wrapper.get('[data-testid="latency-status"]').text()).toContain('priorityScheduling.reading.stale')
    await (wrapper.vm as any).refresh();await flushPromises()
    expect(wrapper.get('[data-testid="enabled"]').element).toHaveProperty('checked',true)
    expect(api.getPriorityConfig).toHaveBeenCalledTimes(1)
  })

  it.each([{config_revision:'v2'},{latency_percentile:50},{latency_measurement:'attributed_v1'}])('accepts an internally consistent new saved configuration %o',async change=>{
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    vi.mocked(api.getPriorityAnalysis).mockResolvedValue({snapshot:{...snapshot(0),...change},latency:{...series(0),...change}})
    await (wrapper.vm as any).refresh();await flushPromises()
    expect((wrapper.vm as any).latency.accounts).toEqual([])
    expect((wrapper.vm as any).snapshot).toMatchObject(change)
  })

  it.each([{model:'unexpected'},{group_id:99},{config_revision:'unexpected'},{latency_percentile:50},{latency_measurement:'attributed_v1'}])('rejects mismatched response semantics %o',async change=>{
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    const previous=(wrapper.vm as any).latency
    vi.mocked(api.getPriorityAnalysis).mockResolvedValue({snapshot:snapshot(),latency:{...series(0),...change}})
    await (wrapper.vm as any).refresh();await flushPromises()
    expect((wrapper.vm as any).latency).toEqual(previous)
    expect((wrapper.vm as any).latencyError).toBe('priorityScheduling.latencyPanel.error')
  })

  it('supersedes an old scope/window response after explicit selection',async()=>{
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    const pending=deferred<api.PriorityAnalysis>()
    vi.mocked(api.getPriorityAnalysis).mockReturnValueOnce(pending.promise)
    const old=(wrapper.vm as any).refresh();await flushPromises()
    await wrapper.get('[data-testid="latency-window"]').setValue(15);await flushPromises()
    pending.resolve({snapshot:snapshot(),latency:series()});await old;await flushPromises()
    expect((wrapper.vm as any).snapshot.window_minutes).toBe(15)
  })

  it('finishes a slow combined read without overlapping or starving it',async()=>{
    vi.useFakeTimers();const pending=deferred<api.PriorityAnalysis>()
    vi.mocked(api.getPriorityAnalysis).mockReturnValueOnce(pending.promise)
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    await vi.advanceTimersByTimeAsync(11000);await flushPromises()
    expect(api.getPriorityAnalysis).toHaveBeenCalledTimes(1)
    pending.resolve({snapshot:snapshot(),latency:series()});await flushPromises()
    expect((wrapper.vm as any).latency.accounts).toHaveLength(65)
    await vi.advanceTimersByTimeAsync(5000);await flushPromises()
    expect(api.getPriorityAnalysis).toHaveBeenCalledTimes(2)
  })

  it.each(['logout','unmount'])('does not restart after delayed config and %s',async action=>{
    vi.useFakeTimers();const pending=deferred<api.PrioritySchedulingConfig>()
    vi.mocked(api.getPriorityConfig).mockReturnValueOnce(pending.promise)
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    if(action==='logout')state.auth.user=null;else{wrapper.unmount();wrapper=undefined}
    pending.resolve({group_ids:[11]} as any);await flushPromises();await vi.advanceTimersByTimeAsync(30000)
    expect(api.getPriorityAnalysis).not.toHaveBeenCalled();expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['at','config_revision','candidates'])('rejects a recompute identity mismatch in %s',async field=>{
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    const before=snapshot(),after={...before,[field]:field==='candidates'?[candidate(999)]:'changed'}
    vi.mocked(api.recomputePriorityScheduling).mockResolvedValue({before,after,diff:[],timed_out:false})
    await (wrapper.vm as any).recompute();await flushPromises()
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="clear-changed-sticky"]').exists()).toBe(false)
  })

  it('invalidates pending recompute on polling without changing chosen scope',async()=>{
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    const pending=deferred<api.PrioritySchedulingRecomputeResult>(),before=snapshot()
    vi.mocked(api.recomputePriorityScheduling).mockReturnValueOnce(pending.promise)
    const operation=(wrapper.vm as any).recompute();await flushPromises()
    await (wrapper.vm as any).refresh();await flushPromises()
    pending.resolve({before,after:before,diff:[],timed_out:false});await operation;await flushPromises()
    expect(wrapper.find('[data-testid="recompute-diff"]').exists()).toBe(false)
    expect((wrapper.vm as any).snapshot.model).toBe('fixture-model')
  })

  it('does not clear sticky bindings when confirmation is cancelled',async()=>{
    vi.spyOn(window,'confirm').mockReturnValue(false)
    vi.mocked(api.getPriorityStickyBindings).mockResolvedValue({group_id:11,total:3,by_account:[{account_id:1,count:3}]})
    wrapper=mount(PrioritySchedulingView);await flushPromises()
    await wrapper.get('[data-testid="sticky-clear-1"]').trigger('click');await flushPromises()
    expect(window.confirm).toHaveBeenCalled();expect(api.clearPriorityStickyBindings).not.toHaveBeenCalled()
  })
})
