import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import PrioritySchedulingView from '../PrioritySchedulingView.vue'
import { apiClient } from '@/api/client'
import type { PriorityAnalysis } from '@/api/admin/priorityScheduling'

const fixture = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => fixture.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }))
const config = { enabled: false, mode: 'balanced', group_ids: [11], models: ['fixture-model'] }
function analysis(params: any, revision = 'fixture-v1'): PriorityAnalysis {
  const common = { at: '2026-11-09T00:00:00Z', model: params.model, group_id: params.group_id ?? null, config_revision: revision, latency_percentile: 70, latency_measurement: 'legacy_forward' }
  const rows = Array.from({ length: 65 }, (_, i) => ({ account_id: i + 1, account_name: `Fixture ${params.model} ${i + 1}`, score: 80, tier: 'insufficient', reasons: [], priority: 1, samples: 0, p90_ttft_ms: 0, quality_samples: 0 }))
  return { snapshot: { ...common, mode: 'balanced', history_status: 'ready', history_ready: true, window_minutes: params.minutes, candidates: rows as any }, latency: { ...common, bucket_minutes: 5, accounts: rows.map(row => ({ ...row, p50_ttft_ms: 0, p99_ttft_ms: 0, slow_samples: 0, error_samples: 0, error_rate: 0, buckets: [] })) as any } }
}
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }
let wrapper: ReturnType<typeof mount> | undefined
function render() { wrapper = mount(PrioritySchedulingView, { attachTo: document.body }); return wrapper }
async function choose(id: string, label: string) {
  await wrapper!.get(`[data-testid="${id}"] button`).trigger('click')
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(el => el.textContent?.trim() === label)
  expect(option, `real Select option ${label}`).toBeTruthy(); option!.click(); await flushPromises()
}
const reads = () => vi.mocked(apiClient.get).mock.calls.filter(([url]) => url === '/admin/priority-scheduling/analysis')
beforeEach(() => {
  vi.resetAllMocks()
  fixture.auth = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(apiClient.get).mockImplementation(async (url, options: any) => {
    if (url === '/admin/priority-scheduling/config') return { data: { ...config } }
    if (url === '/admin/groups/all') return { data: [{ id: 11, name: 'Fixture group' }, { id: 17, name: 'Other group' }] }
    if (url === '/admin/priority-scheduling/models') return { data: { models: ['fixture-model', 'not-in-latest-pool'] } }
    if (url === '/admin/priority-scheduling/analysis') return { data: analysis(options.params) }
    if (url === '/admin/priority-scheduling/snapshot') return { data: { snapshot: null } }
    if (url === '/admin/priority-scheduling/sticky-bindings') return { data: { group_id: options.params.group_id, total: 0, by_account: [] } }
    throw Error(`Unexpected fixture read ${url}`)
  })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers(); document.body.innerHTML = '' })

describe('priority explicit scope filters', () => {
  it('offers explicit model and group filters independently of the latest pool', async () => {
    render(); await flushPromises()
    expect(wrapper!.find('[data-testid="priority-model-filter"]').exists(), 'administrators must be able to select the analysis model').toBe(true)
    expect(wrapper!.find('[data-testid="priority-group-filter"]').exists(), 'administrators must be able to select the analysis group').toBe(true)
  })

  it('sends real HTTP scope parameters after model, group and window selection', async () => {
    render(); await flushPromises()
    await choose('priority-model-filter', 'not-in-latest-pool')
    await choose('priority-group-filter', 'Other group (#17)')
    await wrapper!.get('[data-testid="latency-window"]').setValue('15'); await flushPromises()
    expect(reads().at(-1)?.[1]).toEqual({ params: { model: 'not-in-latest-pool', group_id: 17, minutes: 15 } })
    expect(wrapper!.get('[data-testid="priority-snapshot-at"]').text()).toContain('not-in-latest-pool')
    expect(vi.mocked(apiClient.put)).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.post)).not.toHaveBeenCalled()
  })

  it('polls the chosen scope without reading the recent pool and retains pagination, expansion and draft', async () => {
    vi.useFakeTimers(); render(); await flushPromises()
    await wrapper!.get('[data-testid="enabled"]').setValue(true)
    await wrapper!.get('[data-testid="latency-toggle"]').trigger('click')
    for (const name of ['candidate', 'latency']) {
      const pager = wrapper!.getComponent(`[data-testid="${name}-pagination"]`)
      pager.vm.$emit('update:pageSize', 10); pager.vm.$emit('update:page', 3)
    }
    await flushPromises()
    await vi.advanceTimersByTimeAsync(15000); await flushPromises()
    expect(reads()).toHaveLength(4)
    expect(reads().every(([, options]) => JSON.stringify(options) === JSON.stringify({ params: { model: 'fixture-model', group_id: 11, minutes: 60 } }))).toBe(true)
    expect(vi.mocked(apiClient.get).mock.calls.some(([url]) => url === '/admin/priority-scheduling/snapshot')).toBe(false)
    for (const name of ['candidate', 'latency']) {
      expect(wrapper!.getComponent(`[data-testid="${name}-pagination"]`).props()).toMatchObject({ page: 3, pageSize: 10 })
    }
    expect(wrapper!.get('[data-testid="latency-toggle"]').attributes('aria-expanded')).toBe('true')
    expect(wrapper!.get('[data-testid="enabled"]').element).toHaveProperty('checked', true)
  })

  it('keeps same-scope data during pending/error and recovers', async () => {
    render(); await flushPromises()
    const pending = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    const refresh = (wrapper!.vm as any).refresh(); await flushPromises()
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('Fixture fixture-model 1')
    pending.resolve({ data: analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }) }); await refresh; await flushPromises()
    vi.mocked(apiClient.get).mockRejectedValueOnce(new Error('fixture database failure'))
    await (wrapper!.vm as any).refresh(); await flushPromises()
    expect(wrapper!.get('[data-testid="candidate-table"]').text()).toContain('Fixture fixture-model 1')
    expect(wrapper!.get('[data-testid="latency-status"]').text()).toContain('priorityScheduling.reading.stale')
    await (wrapper!.vm as any).refresh(); await flushPromises()
    expect(wrapper!.get('[data-testid="latency-status"]').text()).not.toContain('priorityScheduling.reading.stale')
  })

  it('isolates old scope and window responses arriving after the new selection', async () => {
    render(); await flushPromises()
    const old = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(old.promise)
    const refresh = (wrapper!.vm as any).refresh(); await flushPromises()
    await choose('priority-model-filter', 'not-in-latest-pool')
    await wrapper!.get('[data-testid="latency-window"]').setValue('15'); await flushPromises()
    old.resolve({ data: analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }) }); await refresh; await flushPromises()
    expect(wrapper!.get('[data-testid="priority-snapshot-at"]').text()).toContain('not-in-latest-pool')
    expect((wrapper!.vm as any).snapshot.window_minutes).toBe(15)
  })

  it.each([{ config_revision: 'old-revision' }, { group_id: 17 }, { latency_percentile: 50 }, { latency_measurement: 'attributed_v1' }])('rejects inconsistent response semantics %o without mixing the two tables', async change => {
    render(); await flushPromises()
    const bad = analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }, 'new-revision')
    Object.assign(bad.latency, change)
    vi.mocked(apiClient.get).mockResolvedValueOnce({ data: bad })
    await (wrapper!.vm as any).refresh(); await flushPromises()
    expect((wrapper!.vm as any).snapshot.config_revision).toBe('fixture-v1')
    expect((wrapper!.vm as any).latency.config_revision).toBe('fixture-v1')
    expect(wrapper!.find('[role="alert"]').exists()).toBe(true)
  })

  it('discards late data and never restarts polling after logout or unmount', async () => {
    vi.useFakeTimers(); render(); await flushPromises()
    const old = deferred<any>(); vi.mocked(apiClient.get).mockReturnValueOnce(old.promise)
    const refresh = (wrapper!.vm as any).refresh(); await flushPromises()
    fixture.auth.user = null
    old.resolve({ data: analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }) }); await refresh; await flushPromises()
    expect(wrapper!.find('[data-testid="candidate-table"]').exists()).toBe(false)
    const count = vi.mocked(apiClient.get).mock.calls.length
    wrapper!.unmount(); wrapper = undefined
    await vi.advanceTimersByTimeAsync(30000)
    expect(vi.mocked(apiClient.get)).toHaveBeenCalledTimes(count)
  })

  it('distinguishes first-read failure from a successful empty sample set', async () => {
    const original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options) => {
      if (url === '/admin/priority-scheduling/analysis') throw Error('fixture history failure')
      return original(url, options)
    })
    render(); await flushPromises()
    await wrapper!.get('[data-testid="latency-toggle"]').trigger('click')
    expect(wrapper!.get('[data-testid="latency-toggle"]').text()).toContain('—')
    expect(wrapper!.get('[data-testid="latency-panel"]').text()).not.toContain('priorityScheduling.latencyPanel.empty')
    expect(wrapper!.get('[data-testid="latency-panel"]').text()).toContain('priorityScheduling.latencyPanel.error')
  })

  it('finishes a group catalog read after a window change without changing the selected model', async () => {
    render(); await flushPromises()
    const pending = deferred<any>(), original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options) => url === '/admin/priority-scheduling/models' && (options as any)?.params?.group_id === 17 ? pending.promise : original(url, options))
    await choose('priority-group-filter', 'Other group (#17)')
    await wrapper!.get('[data-testid="latency-window"]').setValue('15'); await flushPromises()
    pending.resolve({ data: { models: ['only-in-group-17'] } }); await flushPromises()
    expect((wrapper!.vm as any).selectedModel).toBe('fixture-model')
    await choose('priority-model-filter', 'only-in-group-17')
    expect(reads().at(-1)?.[1]).toEqual({ params: { model: 'only-in-group-17', group_id: 17, minutes: 15 } })
  })

  it('waits for initialization before an early refresh and starts the saved group scope once', async () => {
    const pending = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    render(); await flushPromises()
    await (wrapper!.vm as any).refresh(); await flushPromises()
    expect(reads()).toHaveLength(0)
    pending.resolve({ data: { ...config } }); await flushPromises()
    expect(reads()).toHaveLength(1)
    expect(reads()[0][1]).toEqual({ params: { model: 'fixture-model', group_id: 11, minutes: 60 } })
  })

  it.each([{ history_status: 'error', history_error: 'fixture failure' }, { history_refreshing: true }, { config_revision: 'new' }])('refuses changed sticky cleanup when the latest exact snapshot becomes unsafe: %o', async change => {
    render(); await flushPromises()
    const latest = { ...analysis({ model: 'latest-selection', group_id: 17, minutes: 60 }).snapshot, candidates: [] }
    const original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options) => url === '/admin/priority-scheduling/snapshot' ? { data: { snapshot: latest } } : original(url, options))
    vi.mocked(apiClient.post).mockResolvedValueOnce({ data: { before: structuredClone(latest), after: structuredClone(latest), timed_out: false, diff: [{ account_id: 42, changed: true, old_score: 1, new_score: 2, old_weight: 1, new_weight: 2, reasons: [] }] } })
    await wrapper!.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    expect(wrapper!.find('[data-testid="clear-changed-sticky"]').exists()).toBe(true)
    expect((wrapper!.vm as any).snapshot.model).toBe('fixture-model')
    Object.assign(latest, change)
    await wrapper!.get('[data-testid="clear-changed-sticky"]').trigger('click'); await flushPromises()
    expect(vi.mocked(apiClient.post).mock.calls.filter(([url]) => String(url).endsWith('/clear'))).toHaveLength(0)
  })
})
