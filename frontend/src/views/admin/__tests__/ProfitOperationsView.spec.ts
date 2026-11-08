vi.mock('@/api/admin/upstreamChannels', () => ({ listUpstreamChannels: vi.fn(), getUpstreamChannel: vi.fn(), saveUpstreamChannel: vi.fn(), deleteUpstreamChannel: vi.fn(), getChannelMembers: vi.fn(), bindChannelAccounts: vi.fn(), readChannelBindings: vi.fn() }))
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import ProfitOperationsView from '../ProfitOperationsView.vue'
import { readProfitOperations, type OperationsSnapshot } from '@/api/admin/profitOperations'
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/profitOperations', () => ({ readProfitOperations: vi.fn() }))
const fixture = () => ({ scope: { from: '2026-10-27T00:00:00Z', until: '2026-10-28T00:00:00Z', dimension: 'upstream' }, queried_at: '2026-10-28T00:00:00Z', attribution: 'current_config_estimate', currency: 'USD', usage: { summary: { requests: 3, standard_charge: 12, request_income: 4, estimated_cost: 3, estimated_profit: 1, margin: .25, unknown_cost_requests: 0, cost_covered_requests: 3, unknown_upstream_requests: 1, default_multiplier_requests: 1 }, rows: [{ key: 'https://relay.example:8443/tenant/v1', name: 'https://relay.example:8443/tenant/v1', requests: 3, request_income: 4, estimated_cost: 3, estimated_profit: 1, margin: .25 }], total: 21, accounts: [] }, quality: { status: 'ready', data: { summary: { rounds: 0 }, rows: [], total: 0 } }, errors: { status: 'ready', data: { records: 0, upstream_5xx: 0, rows: [] } } }) as unknown as OperationsSnapshot
beforeEach(() => { setActivePinia(createPinia()); localStorage.clear(); vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date(2026, 10, 5, 17, 18, 42)); vi.resetAllMocks(); vi.mocked(readProfitOperations).mockResolvedValue(fixture()) })
afterEach(() => vi.useRealTimers())
describe('independent profit operations', () => {
 it('shows the persisted request accounting formula and current configuration estimate boundary', async () => {
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.text()).toContain('profitOps.title'); expect(w.text()).toContain('profitOps.formula'); expect(w.text()).toContain('profitOps.attribution')
  expect(w.find('[data-testid="profit-filters"]').exists()).toBe(true)
  expect(w.get('[data-testid="profit-summary"]').text()).toContain('25.00%')
  expect(readProfitOperations).toHaveBeenCalledWith(expect.objectContaining({ dimension: 'upstream', page: 1 }), expect.anything())
  const q = vi.mocked(readProfitOperations).mock.calls[0]![0]
  expect(q.from).toBe(new Date(2026, 10, 5, 0, 0, 0).toISOString())
  expect(q.until).toBe(new Date(2026, 10, 5, 17, 18, 42).toISOString())
  expect(w.get('input[type="datetime-local"]').element).toHaveProperty('value', '2026-11-05T00:00')
  w.unmount()
 })
 it('drills from URL to account to model and keeps a frozen half-open range when paging', async () => {
  const w = mount(ProfitOperationsView); await flushPromises(); const original = { ...vi.mocked(readProfitOperations).mock.calls[0]![0] }
  await w.get('[data-testid="operations-next"]').trigger('click'); await flushPromises()
  expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2, from: original.from, until: original.until }), expect.anything())
  await w.get('[data-testid="drill-row"]').trigger('click'); await flushPromises()
  expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ upstream: 'https://relay.example:8443/tenant/v1', dimension: 'account', page: 1, account_id: 0 }), expect.anything())
  w.unmount()
 })
 it('never renders query failure as a successful zero, and shows null margin as unknown', async () => {
  vi.mocked(readProfitOperations).mockRejectedValueOnce(new Error('synthetic failure'))
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.find('[role="alert"]').exists()).toBe(true); expect(w.find('[data-testid="profit-summary"]').exists()).toBe(false)
  const next = fixture(); next.usage.summary.margin = null; next.usage.summary.estimated_cost = null; next.usage.summary.estimated_profit = null
  vi.mocked(readProfitOperations).mockResolvedValue(next)
  await w.get('[data-testid="refresh-operations"]').trigger('click'); await flushPromises()
  expect(w.get('[data-testid="profit-summary"]').text()).toContain('profitOps.unknown')
  expect(w.text()).not.toMatch(/NaN|Infinity/); w.unmount()
 })
 it.each([
  { status: 504, reason: 'PROFIT_OPERATIONS_TIMEOUT' },
  { status: 504, message: 'synthetic proxy timeout' },
  { status: 0, code: 'ECONNABORTED' },
  { status: 0, code: 'ETIMEDOUT' },
 ])('distinguishes a timeout from an empty result and allows retry: %j', async (failure) => {
  vi.mocked(readProfitOperations).mockRejectedValueOnce(failure)
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.get('[role="alert"]').text()).toBe('profitOps.queryTimeout')
  expect(w.find('[data-testid="profit-summary"]').exists()).toBe(false)
  expect(w.find('[role="status"]').exists()).toBe(false)
  await w.get('[data-testid="refresh-operations"]').trigger('click'); await flushPromises()
  expect(w.find('[role="alert"]').exists()).toBe(false)
  expect(w.get('[data-testid="profit-summary"]').text()).toContain('25.00%')
  w.unmount()
 })
 it('keeps a pending aggregation visible and cancels it when leaving the page', async () => {
  let rejectPending!: (reason: unknown) => void
  vi.mocked(readProfitOperations).mockImplementationOnce(() => new Promise((_, reject) => { rejectPending = reject }))
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.get('[role="status"]').text()).toBe('profitOps.loading')
  expect(w.find('[role="alert"]').exists()).toBe(false)
  expect(w.find('[data-testid="profit-summary"]').exists()).toBe(false)
  for (const preset of ['today', 'yesterday', 'thisWeek', 'lastWeek', 'last24h', 'last48h', 'thisMonth', 'lastMonth']) {
   expect(w.get(`[data-testid="${preset}-filter"]`).attributes('disabled')).toBeDefined()
  }
  const signal = vi.mocked(readProfitOperations).mock.calls[0]![1]!
  expect(signal.aborted).toBe(false)
  w.unmount()
  expect(signal.aborted).toBe(true)
  rejectPending({ code: 'ERR_CANCELED' }); await flushPromises()
  expect(readProfitOperations).toHaveBeenCalledTimes(1)
 })
 it('applies model scope and sort after resetting pagination and ignores stale replies', async () => {
  const w = mount(ProfitOperationsView); await flushPromises()
  await w.get('[data-testid="model-filter"]').setValue('vendor-model')
  await w.get('[data-testid="model-scope"]').setValue('upstream')
  await w.get('[data-testid="sort-filter"]').setValue('estimated_profit')
  await w.get('[data-testid="profit-filters"]').trigger('submit'); await flushPromises()
  expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ model: 'vendor-model', model_scope: 'upstream', sort: 'estimated_profit', page: 1 }), expect.anything())
  w.unmount()
 })
})

it('does not overwrite the latest filters with an earlier request response', async () => {
 let resolveOld: ((v: OperationsSnapshot) => void) | undefined
 vi.mocked(readProfitOperations).mockImplementationOnce(() => new Promise(resolve => { resolveOld=resolve }))
 const w=mount(ProfitOperationsView); await flushPromises()
 await w.get('[data-testid="model-filter"]').setValue('new-model')
 const latest=fixture();latest.usage.summary.requests=77;vi.mocked(readProfitOperations).mockResolvedValue(latest)
 await w.get('[data-testid="profit-filters"]').trigger('submit');await flushPromises()
 expect(w.get('[data-testid="profit-summary"]').text()).toContain('77')
 const stale=fixture();stale.usage.summary.requests=999;resolveOld!(stale);await flushPromises()
 expect(w.get('[data-testid="profit-summary"]').text()).toContain('77');expect(w.get('[data-testid="profit-summary"]').text()).not.toContain('999')
 w.unmount()
})


it('applies today at click time after crossing midnight, retaining filters and resetting pagination', async () => {
 const w = mount(ProfitOperationsView); await flushPromises()
 await w.get('[data-testid="operations-next"]').trigger('click'); await flushPromises()
 await w.get('[data-testid="model-filter"]').setValue('fixture-model')
 vi.setSystemTime(new Date(2026, 10, 6, 0, 18, 42))
 await w.get('[data-testid="today-filter"]').trigger('click'); await flushPromises()
 const range = vi.mocked(readProfitOperations).mock.lastCall![0]
 expect(range).toMatchObject({ from: new Date(2026, 10, 6).toISOString(), until: new Date(2026, 10, 6, 0, 18, 42).toISOString(), model: 'fixture-model', page: 1 })
 const inputs = w.findAll('input[type="datetime-local"]')
 expect(inputs[0]!.element).toHaveProperty('value', '2026-11-06T00:00')
 expect(inputs[1]!.element).toHaveProperty('value', '2026-11-06T00:18')
 vi.setSystemTime(new Date(2026, 10, 6, 1))
 await w.get('[data-testid="operations-next"]').trigger('click'); await flushPromises()
 expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ from: range.from, until: range.until, page: 2 }), expect.anything())
 w.unmount()
})

it('keeps the last 24 hours shortcut as a rolling window', async () => {
 const w = mount(ProfitOperationsView); await flushPromises()
 await w.get('[data-testid="last24h-filter"]').trigger('click'); await flushPromises()
 const range = vi.mocked(readProfitOperations).mock.lastCall![0]
 expect(Date.parse(range.until) - Date.parse(range.from)).toBe(86400000)
 expect(range.until).toBe(new Date().toISOString())
 expect(range.page).toBe(1)
 w.unmount()
})

it.each([
 ['yesterday', new Date(2026, 10, 7), new Date(2026, 10, 8)],
 ['thisWeek', new Date(2026, 10, 2), new Date(2026, 10, 8, 15, 8, 42)],
 ['lastWeek', new Date(2026, 9, 26), new Date(2026, 10, 2)],
 ['last48h', new Date(2026, 10, 6, 15, 8, 42), new Date(2026, 10, 8, 15, 8, 42)],
 ['thisMonth', new Date(2026, 10, 1), new Date(2026, 10, 8, 15, 8, 42)],
 ['lastMonth', new Date(2026, 9, 1), new Date(2026, 10, 1)],
] as const)('applies %s at click time, retaining filters and freezing the half-open window for pagination', async (preset, start, end) => {
 const w = mount(ProfitOperationsView); await flushPromises()
 await w.get('[data-testid="operations-next"]').trigger('click'); await flushPromises()
 const numeric = w.findAll('input[type="number"]')
 await numeric[0]!.setValue(42)
 await numeric[1]!.setValue(7)
 await w.get('[data-testid="model-filter"]').setValue('fixture-model')
 await w.get('[data-testid="model-scope"]').setValue('upstream')
 await w.get('[data-testid="sort-filter"]').setValue('estimated_profit')
 vi.setSystemTime(new Date(2026, 10, 8, 15, 8, 42))
 await w.get(`[data-testid="${preset}-filter"]`).trigger('click'); await flushPromises()
 const range = vi.mocked(readProfitOperations).mock.lastCall![0]
 expect(range).toMatchObject({ from: start.toISOString(), until: end.toISOString(), account_id: 42, group_id: 7, model: 'fixture-model', model_scope: 'upstream', sort: 'estimated_profit', page: 1 })
 const local = (date: Date) => new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
 const inputs = w.findAll('input[type="datetime-local"]')
 expect(inputs[0]!.element).toHaveProperty('value', local(start))
 expect(inputs[1]!.element).toHaveProperty('value', local(end))
 vi.setSystemTime(new Date(2026, 10, 9, 1))
 await w.get('[data-testid="operations-next"]').trigger('click'); await flushPromises()
 expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ from: range.from, until: range.until, page: 2 }), expect.anything())
 w.unmount()
})

it.each([
 ['yesterday', new Date(2027, 0, 1, 15, 8), new Date(2026, 11, 31), new Date(2027, 0, 1)],
 ['thisWeek', new Date(2027, 0, 1, 15, 8), new Date(2026, 11, 28), new Date(2027, 0, 1, 15, 8)],
 ['lastWeek', new Date(2026, 10, 9, 15, 8), new Date(2026, 10, 2), new Date(2026, 10, 9)],
 ['lastMonth', new Date(2027, 0, 1, 15, 8), new Date(2026, 11, 1), new Date(2027, 0, 1)],
 ['lastMonth', new Date(2024, 2, 31, 15, 8), new Date(2024, 1, 1), new Date(2024, 2, 1)],
 ['lastMonth', new Date(2026, 2, 31, 15, 8), new Date(2026, 1, 1), new Date(2026, 2, 1)],
 ['lastMonth', new Date(2026, 4, 31, 15, 8), new Date(2026, 3, 1), new Date(2026, 4, 1)],
 ['lastMonth', new Date(2026, 10, 5, 15, 8), new Date(2026, 9, 1), new Date(2026, 10, 1)],
] as const)('handles calendar boundaries for %s at %s', async (preset, now, start, end) => {
 vi.setSystemTime(now)
 const w = mount(ProfitOperationsView); await flushPromises()
 await w.get(`[data-testid="${preset}-filter"]`).trigger('click'); await flushPromises()
 expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ from: start.toISOString(), until: end.toISOString(), page: 1 }), expect.anything())
 // Reapplying the displayed month must also accept the repeated DST hour.
 await w.get('[data-testid="profit-filters"]').trigger('submit'); await flushPromises()
 expect(w.find('[role="alert"]').exists()).toBe(false)
 expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({ from: start.toISOString(), until: end.toISOString() }), expect.anything())
 w.unmount()
})

it.each([
 ['yesterday', new Date(2026, 2, 30, 15, 8), new Date(2026, 2, 29), new Date(2026, 2, 30)],
 ['yesterday', new Date(2026, 9, 26, 15, 8), new Date(2026, 9, 25), new Date(2026, 9, 26)],
 ['last48h', new Date(2026, 2, 30, 15, 8)],
 ['last48h', new Date(2026, 9, 26, 15, 8)],
] as const)('keeps local calendar days and rolling hours distinct across DST: %s at %s', async (preset, now, ...bounds) => {
 vi.setSystemTime(now)
 const w = mount(ProfitOperationsView); await flushPromises()
 await w.get(`[data-testid="${preset}-filter"]`).trigger('click'); await flushPromises()
 const range = vi.mocked(readProfitOperations).mock.lastCall![0]
 if (preset === 'yesterday') {
  expect(range.from).toBe(bounds[0]!.toISOString())
  expect(range.until).toBe(bounds[1]!.toISOString())
 } else {
  expect(Date.parse(range.until) - Date.parse(range.from)).toBe(48 * 3600000)
  expect(range.until).toBe(now.toISOString())
 }
 w.unmount()
})

it('announces today independently and persists dismissal without applying filters', async () => {
 const notices = useNewFeatureNoticeStore()
 const previous = NEW_FEATURE_NOTICES.protocolFeatureSwitches
 const current = NEW_FEATURE_NOTICES.profitOperationsToday
 notices.dismiss(41, previous)
 const w = mount(ProfitOperationsView); await flushPromises()
 const badge = w.findAllComponents(NewFeatureBadge).find(b => b.props('noticeId') === current)!
 expect(badge.find('button').exists()).toBe(true)
 const requests = vi.mocked(readProfitOperations).mock.calls.length
 await badge.get('button').trigger('click')
 expect(readProfitOperations).toHaveBeenCalledTimes(requests)
 expect(notices.isDismissed(41, current)).toBe(true)
 w.unmount()
 // A fresh store simulates refreshing or signing in again in the same browser.
 setActivePinia(createPinia())
 const refreshed = useNewFeatureNoticeStore()
 expect(refreshed.isDismissed(41, previous)).toBe(true)
 expect(refreshed.isDismissed(41, current)).toBe(true)
 expect(refreshed.isDismissed(41, 'future-profit-feature-fixture')).toBe(false)
 const reopened = mount(ProfitOperationsView); await flushPromises()
 const oldBadge = reopened.findAllComponents(NewFeatureBadge).find(b => b.props('noticeId') === current)!
 const rangeBadge = reopened.findAllComponents(NewFeatureBadge).find(b => b.props('noticeId') === NEW_FEATURE_NOTICES.profitOperationsTimeRanges)!
 expect(oldBadge.find('button').exists()).toBe(false)
 expect(rangeBadge.find('button').exists()).toBe(true)
 expect(reopened.find('[data-testid="today-filter"]').exists()).toBe(true)
 const requestsAfterReopen = vi.mocked(readProfitOperations).mock.calls.length
 await rangeBadge.get('button').trigger('click')
 expect(readProfitOperations).toHaveBeenCalledTimes(requestsAfterReopen)
 expect(useNewFeatureNoticeStore().isDismissed(41, NEW_FEATURE_NOTICES.profitOperationsTimeRanges)).toBe(true)
 reopened.unmount()
})
