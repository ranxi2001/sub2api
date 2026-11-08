import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import PrioritySchedulingView from '../PrioritySchedulingView.vue'
import * as api from '@/api/admin/priorityScheduling'

const state = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/priorityScheduling', () => ({ getPriorityConfig: vi.fn(), getPrioritySnapshot: vi.fn(), savePriorityConfig: vi.fn(), getPriorityLatency: vi.fn(), recomputePriorityScheduling: vi.fn(), getPriorityStickyBindings: vi.fn(), clearPriorityStickyBindings: vi.fn() }))

const candidate = (id: number): api.PriorityCandidate => ({ account_id: id, account_name: `Fixture account ${id}`, score: 80, tier: 'eligible', reasons: [], priority: 1, concurrency: 10, load_factor: 10, load_percent: 20, waiting: 0, rate: 0.5, revenue: 1, theoretical_cost: 0.2, profit: 0.8, margin: 0.8, economics_source: 'usage', profit_samples: 20, samples: 20, p90_ttft_ms: 1500, quality_passed: 20, quality_samples: 20 })
const snapshot = (count = 65): api.PrioritySnapshot => ({ at: '2026-11-08T00:00:00Z', model: 'fixture-model', group_id: 11, mode: 'balanced', history_status: 'ready', history_ready: true, config_revision: 'fixture-v1', candidates: Array.from({ length: count }, (_, i) => candidate(i + 1)) })
const series = (count = 65): api.PriorityLatencySeries => ({ at: '2026-11-08T00:00:00Z', model: 'fixture-model', group_id: 11, bucket_minutes: 5, accounts: Array.from({ length: count }, (_, i) => ({ account_id: i + 1, account_name: `Fixture latency ${i + 1}`, samples: 20, p50_ttft_ms: 500, p90_ttft_ms: 1500 + i, p99_ttft_ms: 3000, error_samples: 1, slow_samples: 1, error_rate: 0.05, buckets: [{ account_id: i + 1, bucket: '2026-11-08T00:00:00Z', samples: 20, p50_ttft_ms: 500, p90_ttft_ms: 1500 + i, p99_ttft_ms: 3000, error_samples: 1, slow_samples: 1 }] })) })
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
