import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AIOpsView from '../AIOpsView.vue'
import type { AIOpsRun, AIOpsStatus } from '@/api/admin/aiOps'
import en from '@/i18n/locales/en/aiOps'
import zh from '@/i18n/locales/zh/aiOps'

const { api, auth } = vi.hoisted(() => ({
  api: { status: vi.fn(), list: vi.fn(), get: vi.fn(), create: vi.fn() },
  auth: { isAdmin: true, user: { id: 41 } },
}))
vi.mock('@/api/admin/aiOps', () => ({ aiOpsAPI: api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))

const ready: AIOpsStatus = { enabled: true, configured: true, mode: 'observe', running: false, group_id: 2, window_minutes: 15, min_interval_seconds: 300, automatic_interval_seconds: 0, daily_run_limit: 24 }
function run(id = 'run-a', status: AIOpsRun['status'] = 'completed'): AIOpsRun {
  return {
    id, status, created_at: '2026-11-05T00:15:00Z', group_id: 2,
    window: { start: '2026-11-05T00:00:00Z', end: '2026-11-05T00:15:00Z' },
    evidence: [{ id: 'capacity-1', source: 'account_capacity', status: 'ok', collected_at: '2026-11-05T00:15:00Z', metrics: { inflight: 3, concurrency: 10 } }],
    ...(status === 'completed' ? { result: { summary: `Summary ${id}`, findings: [], proposals: [], limitations: ['Small sample'] } } : {}),
  }
}

function mountView(locale: 'en' | 'zh' = 'en') {
  return mount(AIOpsView, { global: {
    plugins: [createI18n({ legacy: false, locale, messages: { en: { aiOps: en }, zh: { aiOps: zh } } })],
    stubs: { AppLayout: { template: '<main><slot /></main>' }, SmartOpsNav: true, NewFeatureBadge: true, AIOpsControls: true },
  } })
}
let wrapper: ReturnType<typeof mountView> | undefined
beforeEach(() => {
  vi.useFakeTimers()
  auth.isAdmin = true
  for (const fn of Object.values(api)) fn.mockReset()
  api.status.mockResolvedValue({ ...ready })
  api.list.mockResolvedValue({ items: [], total: 0 })
  api.get.mockImplementation(async (id: string) => run(id))
  api.create.mockResolvedValue(run('new-run', 'queued'))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })

describe('AI operations observe-only page', () => {
  it.each(['en', 'zh'] as const)('shows a disabled deployment in %s without creating a run', async locale => {
    api.status.mockResolvedValue({ ...ready, enabled: false, configured: false, group_id: null })
    wrapper = mountView(locale); await flushPromises()
    expect(wrapper.get('h1').text()).toBe((locale === 'zh' ? zh : en).title)
    expect(wrapper.get('[data-testid="ai-ops-disabled"]').text()).toBe((locale === 'zh' ? zh : en).disabled)
    expect(wrapper.find('[data-testid="ai-ops-start"]').exists()).toBe(false)
    expect(api.create).not.toHaveBeenCalled()
  })

  it('does not issue requests for an observer or a regular user', async () => {
    auth.isAdmin = false
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain(en.unavailable)
    for (const fn of Object.values(api)) expect(fn).not.toHaveBeenCalled()
  })

  it.each([
    ['en', 0], ['en', 900], ['en', 90], ['zh', 0], ['zh', 900], ['zh', 90],
  ] as const)('displays the configured automatic diagnostic interval in %s (%i seconds)', async (locale, seconds) => {
    api.status.mockResolvedValue({ ...ready, automatic_interval_seconds: seconds })
    wrapper = mountView(locale); await flushPromises()
    const messages = locale === 'zh' ? zh : en
    const expected = seconds === 0 ? messages.automaticDisabled
      : seconds % 60 === 0 ? messages.automaticMinutes.replace('{count}', String(seconds / 60))
      : messages.automaticSeconds.replace('{count}', String(seconds))
    expect(wrapper.get('[data-testid="ai-ops-automatic"]').text()).toBe(expected)
    expect(wrapper.text()).toContain(messages.costConsent)
    expect(api.create).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(10000)
    expect(api.create).not.toHaveBeenCalled()
  })

  it('requires an explicit cost acknowledgement and click before starting once', async () => {
    wrapper = mountView(); await flushPromises()
    expect(api.create).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="ai-ops-start"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-ops-cost-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-ops-start"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="ai-ops-start"]').trigger('click'); await flushPromises()
    expect(api.create).toHaveBeenCalledOnce()
    expect(api.create).toHaveBeenCalledWith(expect.any(AbortSignal))
    expect((wrapper.get('[data-testid="ai-ops-cost-consent"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.text()).toContain(en.noChanges)
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(api.create).toHaveBeenCalledOnce()
  })

  it('blocks starting while the service is not ready or another run is active', async () => {
    api.status.mockResolvedValue({ ...ready, configured: false })
    wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain(en.notConfigured)
    expect(wrapper.find('[data-testid="ai-ops-start"]').exists()).toBe(false)
    api.status.mockResolvedValue({ ...ready, running: true })
    await wrapper.get('[data-testid="ai-ops-refresh"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-ops-cost-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-ops-start"]').attributes('disabled')).toBeDefined()
    expect(api.create).not.toHaveBeenCalled()
  })

  it('polls only the selected run and stops when the run completes', async () => {
    api.list.mockResolvedValue({ items: [run('run-a', 'collecting')], total: 1 })
    api.get.mockResolvedValueOnce(run('run-a', 'collecting')).mockResolvedValue(run('run-a'))
    wrapper = mountView(); await flushPromises()
    expect(api.get).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.get('[data-testid="ai-ops-summary"]').text()).toBe('Summary run-a')
    const count = api.get.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.get).toHaveBeenCalledTimes(count)
    expect(api.create).not.toHaveBeenCalled()
  })

  it('stops refreshing on request failure and preserves the last observed state', async () => {
    api.list.mockResolvedValue({ items: [run('run-a', 'analyzing')], total: 1 })
    api.get.mockResolvedValueOnce(run('run-a', 'analyzing')).mockRejectedValue(new Error('provider URL must not be shown'))
    wrapper = mountView(); await flushPromises()
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.text()).toContain(en.pollingFailed)
    expect(wrapper.text()).not.toContain('provider URL must not be shown')
    const count = api.get.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.get).toHaveBeenCalledTimes(count)
    expect(wrapper.get('[data-testid="ai-ops-resume-polling"]').exists()).toBe(true)
  })

  it('lets the user stop refreshing without pretending to cancel the run', async () => {
    api.list.mockResolvedValue({ items: [run('run-a', 'analyzing')], total: 1 })
    api.get.mockResolvedValue(run('run-a', 'analyzing'))
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="ai-ops-stop-polling"]').trigger('click')
    expect(wrapper.text()).toContain(en.pollingStopped)
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.get).toHaveBeenCalledOnce()
    await wrapper.get('[data-testid="ai-ops-resume-polling"]').trigger('click')
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.get).toHaveBeenCalledTimes(2)
  })

  it('bounds polling even when the service keeps reporting an active run', async () => {
    api.list.mockResolvedValue({ items: [run('run-a', 'analyzing')], total: 1 })
    api.get.mockResolvedValue(run('run-a', 'analyzing'))
    wrapper = mountView(); await flushPromises()
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000); await flushPromises()
    expect(wrapper.text()).toContain(en.pollingExpired)
    const count = api.get.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.get).toHaveBeenCalledTimes(count)
  })

  it('ignores an old response after the user selects another record', async () => {
    let resolveOld!: (value: AIOpsRun) => void
    api.list.mockResolvedValue({ items: [run('run-a'), run('run-b')], total: 2 })
    api.get.mockImplementation((id: string) => id === 'run-a' ? new Promise<AIOpsRun>(resolve => { resolveOld = resolve }) : Promise.resolve(run(id)))
    wrapper = mountView(); await flushPromises()
    const oldSignal = api.get.mock.calls[0][1] as AbortSignal
    await wrapper.findAll('[data-testid="ai-ops-history-run"]')[1].trigger('click'); await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolveOld(run('run-a')); await flushPromises()
    expect(wrapper.get('[data-testid="ai-ops-summary"]').text()).toBe('Summary run-b')
    expect(api.create).not.toHaveBeenCalled()
  })

  it('aborts pending reads and leaves no polling requests after unmount', async () => {
    api.list.mockResolvedValue({ items: [run('run-a', 'analyzing')], total: 1 })
    api.get.mockResolvedValueOnce(run('run-a', 'analyzing')).mockImplementation(() => new Promise(() => {}))
    wrapper = mountView(); await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    const signal = api.get.mock.calls[1][1] as AbortSignal
    wrapper.unmount(); wrapper = undefined
    expect(signal.aborted).toBe(true)
    const count = api.get.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.get).toHaveBeenCalledTimes(count)
  })

  it('requires a history refresh after an ambiguous creation failure', async () => {
    api.create.mockRejectedValue(new Error('network timeout'))
    wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="ai-ops-cost-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-ops-start"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(en.startFailed)
    expect(wrapper.get('[data-testid="ai-ops-start"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-ops-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="ai-ops-start"]').attributes('disabled')).toBeUndefined()
    expect(api.create).toHaveBeenCalledOnce()
  })

  it('renders model output and evidence as escaped text with no apply control', async () => {
    const report = run()
    const hostile = '<img src=x onerror="alert(1)">'
    report.result = {
      summary: hostile,
      findings: [{ severity: 'warning', category: 'capacity', description: hostile, recommendation: hostile, evidence_ids: ['capacity-1'], account_ids: [3] }],
      proposals: [{ action: 'set_concurrency', account_id: 3, proposed_value: 8, reason: hostile, evidence_ids: ['capacity-1'], executable: false }],
      limitations: [hostile],
    }
    report.evidence[0].metrics = { text: hostile }
    api.list.mockResolvedValue({ items: [report], total: 1 })
    api.get.mockResolvedValue(report)
    wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="ai-ops-summary"]').text()).toBe(hostile)
    expect(wrapper.get('[data-testid="ai-ops-proposal"]').text()).toContain(en.notExecuted)
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-ops-proposal"]').find('button').exists()).toBe(false)
    expect(api.create).not.toHaveBeenCalled()
  })
})
