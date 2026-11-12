import { afterEach, beforeEach, expect, vi } from 'vitest'
import { reactive } from 'vue'
import { createPinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import PrioritySchedulingView from '../PrioritySchedulingView.vue'
import { apiClient } from '@/api/client'
import type { PriorityAnalysis, PriorityCandidate, PrioritySchedulingConfig } from '@/api/admin/priorityScheduling'
import { emptyPerformanceConfig, type AccountPerformance } from '@/types/accountPerformance'

// The mock reads auth only when the component mounts, after beforeEach.
// Export ordinary state: Vitest does not allow exporting vi.hoisted variables.
export const fixture = { auth: null as any }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => fixture.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => [key, ...Object.values(params ?? {})].join(' ') }) }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }))
export const client = apiClient
export const settle = flushPromises
export const config: PrioritySchedulingConfig = { enabled: true, mode: 'balanced', group_ids: [], models: ['fixture-model'], window_minutes: 60, min_samples: 5, target_ttft_ms: 15000, target_tps: 30, max_load_percent: 80, min_quality_percent: 90, quality_max_age_hours: 24, quality_weight: 30, latency_weight: 25, load_weight: 25, cost_weight: 20, error_rate_weight: 25 }
export const directory = [{ id: 11, name: 'MiMo', platform: 'openai' }, { id: 17, name: 'GPT Standard', platform: 'openai' }, { id: 23, name: 'Composite', platform: 'composite' }]
export function performance(id = 1): AccountPerformance {
  return { account_id: id, avg_tps: 25, avg_cache_percent: 86.8, cache_samples: 20, avg_ttft_ms: 4000, degradation_percent: 5, tps_samples: 20, ttft_samples: 20, quality_samples: 20, config: emptyPerformanceConfig(), revision: 1, paused: false, pause_generation: 0, paused_at: null, reason: '', window_start: '2026-11-11T00:00:00Z', window_end: '2026-11-11T01:00:00Z', upstream_error: { source: 'process_local_http_responses', failures: 1, total: 200, percent: .5, observed_since: '2026-11-11T00:00:00Z', window_complete: true } }
}
export function analysis(params: any, count = 3, revision = 'v1'): PriorityAnalysis {
  const common = { at: '2026-11-11T01:00:00Z', model: params.model, group_id: params.group_id ?? null, config_revision: revision }
  const candidates = Array.from({ length: count }, (_, i): PriorityCandidate => ({ account_id: i + 1, account_name: `Fixture ${i + 1}`, score: 75, base_score: 76, error_penalty: .9875, tier: 'eligible', reasons: [], priority: 1, concurrency: 50, load_factor: 1, load_percent: null, waiting: 0, rate: .1, revenue: 5, theoretical_cost: 1, profit: 4, margin: .8, economics_source: 'usage', profit_samples: 20, samples: 20, p90_ttft_ms: 0, quality_samples: 20, quality_passed: 19, performance: performance(i + 1) }))
  return { snapshot: { ...common, mode: 'balanced', performance_source: 'account_performance', selection_policy: 'read_only_analysis', history_ready: true, history_status: 'ready', window_minutes: params.minutes, candidates }, latency: { ...common, bucket_minutes: 5, accounts: [] } }
}
export function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason?: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
export let wrapper: ReturnType<typeof mount> | undefined
export function render() { wrapper = mount(PrioritySchedulingView, { attachTo: document.body, global: { plugins: [createPinia()] } }); return wrapper }
export function unmount() { wrapper?.unmount(); wrapper = undefined }
export const reads = () => vi.mocked(apiClient.get).mock.calls.filter(([url]) => url === '/admin/priority-scheduling/analysis')
export async function choose(id: string, label: string) {
  await wrapper!.get(`[data-testid="${id}"] button`).trigger('click')
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(el => el.textContent?.trim() === label)
  expect(option).toBeTruthy(); option!.click(); await flushPromises()
}
export function overrideGet(handler: (url: string, options: any) => any) {
  const original = vi.mocked(apiClient.get).getMockImplementation()!
  vi.mocked(apiClient.get).mockImplementation(async (url, options) => handler(url, options) ?? original(url, options))
}
beforeEach(() => {
  vi.resetAllMocks()
  fixture.auth = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(apiClient.get).mockImplementation(async (url, options: any) => {
    if (url === '/admin/priority-scheduling/config') return { data: structuredClone(config) }
    if (url === '/admin/groups/all') return { data: directory.filter(group => group.platform === options?.params?.platform) }
    if (url === '/admin/priority-scheduling/models') return { data: { models: ['fixture-model', 'another-model'] } }
    if (url === '/admin/priority-scheduling/analysis') return { data: analysis(options.params) }
    if (url === '/admin/priority-scheduling/snapshot') return { data: { snapshot: null } }
    if (url === '/admin/priority-scheduling/sticky-bindings') return { data: { group_id: options.params.group_id, total: 0, by_account: [] } }
    throw Error(`Unexpected fixture read ${url}`)
  })
  vi.mocked(apiClient.put).mockImplementation(async (_url, body) => ({ data: body }))
})
afterEach(() => { unmount(); vi.useRealTimers(); vi.restoreAllMocks(); document.body.innerHTML = '' })
