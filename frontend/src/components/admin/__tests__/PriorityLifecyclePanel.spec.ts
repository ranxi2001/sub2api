import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { reactive } from 'vue'
import PriorityLifecyclePanel from '../PriorityLifecyclePanel.vue'
import { getPriorityLifecycleState, getPriorityLifecycleDecisions, getPriorityLifecycleEvents, type PriorityLifecycleScope, type PriorityLifecycleState } from '@/api/admin/priorityScheduling'
const auth = vi.hoisted(() => ({ value: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth.value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/priorityScheduling', () => ({ getPriorityLifecycleState: vi.fn(), getPriorityLifecycleDecisions: vi.fn(), getPriorityLifecycleEvents: vi.fn() }))
const fixture = (scope: PriorityLifecycleScope): PriorityLifecycleState => ({
  scope: { ...scope, measurement_schema: 'ingress_alias_v1/legacy_forward', statistics_spec: 'same-population' },
  server_now: '2026-10-31T00:00:00Z', policy_revision: 'policy-v1', mode: 'shadow', last_error: '', attempted_at: '2026-10-31T00:00:00Z',
  statistics: { scope, generation: 3, watermark: 5, observed_at: '2026-10-31T00:00:00Z', window_start: '2026-10-30T23:00:00Z', window_end: '2026-10-31T00:00:00Z', policy_revision: 'policy-v1', signals: {} },
  accounts: [{ account_id: 7, account_name: 'synthetic excluded', configuration: 'ADMIN_DISABLED', admission: 'BLOCKED', reasons: ['account_not_schedulable'], content: 'VALID', freshness: 'FRESH', refresh: 'IDLE',
    signal: { samples: 20, latency_percentile: 70, selected_ttft_ms: null, p90_ttft_ms: 42500, latency_unknown_samples: 1 },
    policy: { account_id: 7, state: 'PENALIZED_SHADOW', evidence_id: 'immutable-evidence', evidence_at: '2026-10-30T23:59:00Z', expires_at: '2026-10-31T00:00:02Z', policy_revision: 'policy-v0', observed_value: 16000, threshold: 15000, percentile: 90, mode: 'stop', rule: 'shadow' } }],
})
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-31T00:00:00Z'))
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  vi.resetAllMocks()
  auth.value = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(getPriorityLifecycleState).mockImplementation(async scope => fixture(scope))
  vi.mocked(getPriorityLifecycleDecisions).mockResolvedValue({ items: [] })
  vi.mocked(getPriorityLifecycleEvents).mockResolvedValue({ items: [] })
})
afterEach(() => { vi.useRealTimers() })
async function choose(wrapper: ReturnType<typeof mount>, model: string) {
  await wrapper.get('[data-testid="lifecycle-model"]').setValue(model)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}
describe('lifecycle actual component', () => {
  it('fixes explicit scope and rejects an older scope response', async () => {
    let resolve!: (value: PriorityLifecycleState) => void
    vi.mocked(getPriorityLifecycleState).mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = mount(PriorityLifecyclePanel)
    await choose(wrapper, 'first')
    await choose(wrapper, 'second')
    resolve(fixture({ model: 'first', group_id: null, endpoint: '/v1/responses', request_type: 2 }))
    await flushPromises()
    expect(wrapper.get('[data-testid="lifecycle-scope"]').text()).toContain('second')
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).toContain('ADMIN_DISABLED')
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).toContain('P70: —')
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).not.toContain('42500')
    expect(getPriorityLifecycleState).toHaveBeenLastCalledWith({ model: 'second', group_id: null, endpoint: '/v1/responses', request_type: 2 })
    wrapper.unmount()
  })
  it('retains last-good through offline/error and resumes immediately after visibility', async () => {
    const wrapper = mount(PriorityLifecyclePanel)
    await choose(wrapper, 'stable')
    window.dispatchEvent(new Event('offline')); await flushPromises()
    expect(wrapper.get('[data-testid="lifecycle-error"]').text()).toContain('lastGood')
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).toContain('STALE')
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
    const count = vi.mocked(getPriorityLifecycleState).mock.calls.length
    await vi.advanceTimersByTimeAsync(20000)
    expect(getPriorityLifecycleState).toHaveBeenCalledTimes(count)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(getPriorityLifecycleState).toHaveBeenCalledTimes(count + 1)
    expect(wrapper.find('[data-testid="lifecycle-error"]').exists()).toBe(false)
    vi.mocked(getPriorityLifecycleState).mockRejectedValueOnce(new Error('synthetic offline'))
    await wrapper.get('[data-testid="lifecycle-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).toContain('synthetic excluded')
    expect(wrapper.get('[data-testid="lifecycle-error"]').exists()).toBe(true)
    wrapper.unmount()
  })
  it('uses server clock for display without declaring recovery, and clears on auth change', async () => {
    const wrapper = mount(PriorityLifecyclePanel)
    await choose(wrapper, 'clock')
    await vi.advanceTimersByTimeAsync(3000)
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).toContain('awaitingServer')
    expect(wrapper.get('[data-testid="lifecycle-account-7"]').text()).toContain('PENALIZED_SHADOW')
    auth.value.user = { id: 2, role: 'admin' }; await flushPromises()
    expect(wrapper.find('[data-testid="lifecycle-accounts"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
