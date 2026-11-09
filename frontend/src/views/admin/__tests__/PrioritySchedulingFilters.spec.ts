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
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason?: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
// Mirror /groups/all: active-only, strict platform equality, never a union.
const directory = [{ id: 11, name: 'Fixture group', platform: 'openai' }, { id: 17, name: 'Other group', platform: 'openai' }, { id: 23, name: 'Composite group', platform: 'composite' }, { id: 29, name: 'Anthropic group', platform: 'anthropic' }]
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
    if (url === '/admin/groups/all') return { data: directory.filter(group => !options?.params?.platform || group.platform === options.params.platform) }
    if (url === '/admin/priority-scheduling/models') return { data: { models: ['fixture-model', 'not-in-latest-pool'] } }
    if (url === '/admin/priority-scheduling/analysis') return { data: analysis(options.params) }
    if (url === '/admin/priority-scheduling/snapshot') return { data: { snapshot: null } }
    if (url === '/admin/priority-scheduling/sticky-bindings') return { data: { group_id: options.params.group_id, total: 0, by_account: [] } }
    throw Error(`Unexpected fixture read ${url}`)
  })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers(); vi.restoreAllMocks(); document.body.innerHTML = '' })

describe('priority explicit scope filters', () => {
  it('followup F1 offers Composite through the real strict platform catalog and retains public alias across switches', async () => {
    render(); await flushPromises()
    const catalogReads = vi.mocked(apiClient.get).mock.calls.filter(([url]) => url === '/admin/groups/all')
    expect(catalogReads.every(([, options]: any) => ['openai', 'composite'].includes(options?.params?.platform)), 'catalog must retain explicit platform admission').toBe(true)
    await wrapper!.get('[data-testid="priority-group-filter"] button').trigger('click')
    expect(document.querySelector('[role="listbox"]')?.textContent).not.toContain('Anthropic group')
    await wrapper!.get('[data-testid="priority-group-filter"] button').trigger('click')
    await choose('priority-group-filter', 'Composite group (#23)')
    await wrapper!.get('[data-testid="priority-model-filter"] button').trigger('click')
    const search = document.querySelector<HTMLInputElement>('.select-search-input')!
    search.value = 'public-alias'; search.dispatchEvent(new Event('input', { bubbles: true })); await flushPromises()
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(el => el.textContent?.includes('public-alias'))!
    expect(option).toBeTruthy(); option.click(); await flushPromises()
    await choose('priority-group-filter', 'Other group (#17)')
    await choose('priority-group-filter', 'Composite group (#23)')
    expect(reads().at(-1)?.[1]).toMatchObject({ params: { model: 'public-alias', group_id: 23, minutes: 60 } })
    expect((wrapper!.vm as any).snapshot.model).toBe('public-alias')
  })

  it('followup F2 fences a complete old R1 pair after successful save and immediately reads R2 without replacing newer edits or scope', async () => {
    render(); await flushPromises()
    await choose('priority-model-filter', 'not-in-latest-pool')
    await wrapper!.get('[data-testid="latency-window"]').setValue('15'); await flushPromises()
    const old = deferred<any>(), saved = deferred<any>(), fresh = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(old.promise)
    const oldRead = (wrapper!.vm as any).refresh(); await flushPromises()
    const oldSignal = (reads().at(-1)?.[1] as any)?.signal
    const vm = wrapper!.vm as any
    vm.draft.target_ttft_ms = 4000
    vi.mocked(apiClient.put).mockReturnValueOnce(saved.promise)
    const saving = vm.save(); await flushPromises()
    vm.draft.target_ttft_ms = 9000; vm.groups = '17'; vm.modelsText = 'new-unsaved-model'
    const original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options) => url === '/admin/priority-scheduling/analysis' ? fresh.promise : original(url, options))
    saved.resolve({ data: { ...config, target_ttft_ms: 4000 } }); await saving; await flushPromises()
    old.resolve({ data: analysis({ model: 'not-in-latest-pool', group_id: 11, minutes: 15 }, 'R1') }); await oldRead; await flushPromises()
    expect(vm.snapshot.config_revision, 'complete old R1 pair must not publish across successful save').not.toBe('R1')
    expect(reads().length, 'successful save must start a fresh analysis immediately').toBe(5)
    expect(oldSignal?.aborted, 'save barrier must cancel the old HTTP read').toBe(true)
    const freshPair = analysis({ model: 'not-in-latest-pool', group_id: 11, minutes: 15 }, 'R2')
    freshPair.latency.target_ttft_ms = 4000
    fresh.resolve({ data: freshPair }); await flushPromises()
    expect(vm.snapshot.config_revision).toBe('R2'); expect(vm.latency.config_revision).toBe('R2')
    expect(vm.draft.target_ttft_ms).toBe(9000); expect(vm.groups).toBe('17'); expect(vm.modelsText).toBe('new-unsaved-model')
    expect(vm.latencyClass(5000), 'latency color must use the captured analysis target, never the unsaved draft').toBe('text-amber-600')
    expect(reads().at(-1)?.[1]).toMatchObject({ params: { model: 'not-in-latest-pool', group_id: 11, minutes: 15 } })
    await vm.refresh(); await flushPromises()
    expect(vm.draft.target_ttft_ms).toBe(9000)
  })

  it('followup F2 failed save leaves the current read valid and preserves the draft', async () => {
    render(); await flushPromises()
    const old = deferred<any>(), saved = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(old.promise)
    const oldRead = (wrapper!.vm as any).refresh(); await flushPromises()
    const signal = (reads().at(-1)?.[1] as any)?.signal
    vi.mocked(apiClient.put).mockReturnValueOnce(saved.promise)
    const vm = wrapper!.vm as any
    vm.draft.target_ttft_ms = 8000
    const saving = vm.save(); await flushPromises()
    saved.reject(Error('fixture save rejected')); await saving; await flushPromises()
    expect(signal?.aborted).toBe(false); expect(reads()).toHaveLength(2)
    old.resolve({ data: analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }, 'R1') }); await oldRead; await flushPromises()
    expect(vm.snapshot.config_revision).toBe('R1'); expect(vm.draft.target_ttft_ms).toBe(8000)
  })

  it('followup F2 keeps the model group and window selected while PUT is pending', async () => {
    render(); await flushPromises()
    const pending = deferred<any>()
    vi.mocked(apiClient.put).mockReturnValueOnce(pending.promise)
    const saving = (wrapper!.vm as any).save(); await flushPromises()
    await choose('priority-model-filter', 'not-in-latest-pool')
    await choose('priority-group-filter', 'Composite group (#23)')
    await wrapper!.get('[data-testid="latency-window"]').setValue('15'); await flushPromises()
    const count = reads().length
    pending.resolve({ data: { ...config } }); await saving; await flushPromises()
    expect(reads()).toHaveLength(count + 1)
    expect(reads().at(-1)?.[1]).toMatchObject({ params: { model: 'not-in-latest-pool', group_id: 23, minutes: 15 } })
    const vm = wrapper!.vm as any
    expect(vm.selectedModel).toBe('not-in-latest-pool'); expect(vm.selectedGroup).toBe(23); expect(vm.latencyMinutes).toBe(15)
    expect(vm.snapshot).toMatchObject({ model: 'not-in-latest-pool', group_id: 23, window_minutes: 15 })
  })

  it.each(['scope', 'logout', 'unmount'])('followup F3 aborts the real analysis transport on %s and rejects late results', async action => {
    render(); await flushPromises()
    const old = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(old.promise)
    const read = (wrapper!.vm as any).refresh(); await flushPromises()
    const signal = (reads().at(-1)?.[1] as any)?.signal
    expect(signal).toBeInstanceOf(AbortSignal)
    if (action === 'scope') { await wrapper!.get('[data-testid="latency-window"]').setValue('15'); await flushPromises() }
    else if (action === 'logout') fixture.auth.user = null
    else { wrapper!.unmount(); wrapper = undefined }
    expect(signal.aborted).toBe(true)
    old.resolve({ data: analysis({ model: 'fixture-model', group_id: 11, minutes: 60 }, 'old') }); await read; await flushPromises()
    if (action === 'scope') expect((wrapper!.vm as any).snapshot.window_minutes).toBe(15)
  })

  it.each(['group', 'logout', 'unmount', 'save'])('followup F3 cancels the old group catalog on %s without accepting its late models', async action => {
    render(); await flushPromises()
    const pending = deferred<any>(), original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options) => url === '/admin/priority-scheduling/models' && (options as any)?.params?.group_id === 17 ? pending.promise : original(url, options))
    await choose('priority-group-filter', 'Other group (#17)')
    const catalog = vi.mocked(apiClient.get).mock.calls.filter(([url]) => url === '/admin/priority-scheduling/models').at(-1)!
    const signal = (catalog[1] as any)?.signal
    expect(signal).toBeInstanceOf(AbortSignal)
    if (action === 'group') await choose('priority-group-filter', 'Fixture group (#11)')
    else if (action === 'logout') fixture.auth.user = null
    else if (action === 'unmount') { wrapper!.unmount(); wrapper = undefined }
    else {
      vi.mocked(apiClient.put).mockResolvedValueOnce({ data: { ...config } })
      // The subsequent catalog is a distinct read after the save barrier.
      vi.mocked(apiClient.get).mockImplementation(original)
      await (wrapper!.vm as any).save(); await flushPromises()
    }
    expect(signal.aborted).toBe(true)
    pending.resolve({ data: { models: ['late-stale-model'] } }); await flushPromises()
    if (wrapper && action !== 'logout') expect((wrapper.vm as any).availableModels).not.toContain('late-stale-model')
  })

  it('followup F3 aborts pending catalog siblings when one directory read fails first', async () => {
    render(); await flushPromises()
    const pending = deferred<any>(), original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options: any) => {
      if (url === '/admin/groups/all' && options?.params?.platform === 'openai') throw Error('fixture directory failed')
      if (url === '/admin/priority-scheduling/models') return pending.promise
      return original(url, options)
    })
    await choose('priority-group-filter', 'Other group (#17)')
    const catalog = vi.mocked(apiClient.get).mock.calls.filter(([url]) => url === '/admin/priority-scheduling/models').at(-1)!
    expect((catalog[1] as any)?.signal.aborted, 'a failed directory must abort its still-pending sibling model read').toBe(true)
    expect((wrapper!.vm as any).catalogError).toBe(true)
    fixture.auth.user = null
    pending.resolve({ data: { models: ['late-orphan-model'] } }); await flushPromises()
    expect((wrapper!.vm as any).availableModels).toEqual([])
  })
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
    expect(reads().at(-1)?.[1]).toMatchObject({ params: { model: 'not-in-latest-pool', group_id: 17, minutes: 15 } })
    expect(wrapper!.get('[data-testid="priority-snapshot-at"]').text()).toContain('not-in-latest-pool')
    expect(vi.mocked(apiClient.put)).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.post)).not.toHaveBeenCalled()
  })

  it('accepts a concrete model covered by a wildcard using the real searchable Select', async () => {
    render(); await flushPromises()
    await wrapper!.get('[data-testid="priority-model-filter"] button').trigger('click')
    const search = document.querySelector<HTMLInputElement>('.select-search-input')!
    search.value = 'custom-via-wildcard'
    search.dispatchEvent(new Event('input', { bubbles: true })); await flushPromises()
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(el => el.textContent?.includes('custom-via-wildcard'))!
    expect(option).toBeTruthy(); option.click(); await flushPromises()
    expect(reads().at(-1)?.[1]).toMatchObject({ params: { model: 'custom-via-wildcard', group_id: 11, minutes: 60 } })
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
    expect(reads().every(([, options]) => JSON.stringify((options as any)?.params) === JSON.stringify({ model: 'fixture-model', group_id: 11, minutes: 60 }))).toBe(true)
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
    expect(reads().at(-1)?.[1]).toMatchObject({ params: { model: 'only-in-group-17', group_id: 17, minutes: 15 } })
  })

  it('waits for initialization before an early refresh and starts the saved group scope once', async () => {
    const pending = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    render(); await flushPromises()
    await (wrapper!.vm as any).refresh(); await flushPromises()
    expect(reads()).toHaveLength(0)
    pending.resolve({ data: { ...config } }); await flushPromises()
    expect(reads()).toHaveLength(1)
    expect(reads()[0][1]).toMatchObject({ params: { model: 'fixture-model', group_id: 11, minutes: 60 } })
  })

  it.each([{ history_status: 'error', history_error: 'fixture failure' }, { history_refreshing: true }, { config_revision: 'new' }])('refuses changed sticky cleanup when the latest exact snapshot becomes unsafe: %o', async change => {
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true)
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
    expect(confirmation).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.post).mock.calls.filter(([url]) => String(url).endsWith('/clear'))).toHaveLength(0)
  })

  it.each(['logout', 'unmount'])('discards a pending sticky preflight after %s', async action => {
    const confirmation = vi.spyOn(window, 'confirm').mockReturnValue(true)
    render(); await flushPromises()
    const latest = { ...analysis({ model: 'latest-selection', group_id: 17, minutes: 60 }).snapshot, candidates: [] }
    const original = vi.mocked(apiClient.get).getMockImplementation()!
    vi.mocked(apiClient.get).mockImplementation(async (url, options) => url === '/admin/priority-scheduling/snapshot' ? { data: { snapshot: structuredClone(latest) } } : original(url, options))
    vi.mocked(apiClient.post).mockResolvedValueOnce({ data: { before: structuredClone(latest), after: structuredClone(latest), timed_out: false, diff: [{ account_id: 42, changed: true, old_score: 1, new_score: 2, old_weight: 1, new_weight: 2, reasons: [] }] } })
    await wrapper!.get('[data-testid="recompute"]').trigger('click'); await flushPromises()
    const pending = deferred<any>()
    vi.mocked(apiClient.get).mockReturnValueOnce(pending.promise)
    await wrapper!.get('[data-testid="clear-changed-sticky"]').trigger('click'); await flushPromises()
    if (action === 'logout') fixture.auth.user = null
    else { wrapper!.unmount(); wrapper = undefined }
    pending.resolve({ data: { snapshot: latest } }); await flushPromises()
    expect(confirmation).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.post).mock.calls.filter(([url]) => String(url).endsWith('/clear'))).toHaveLength(0)
  })
})
