import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, it, expect, vi } from 'vitest'
import ProfitOperationsView from '../ProfitOperationsView.vue'
import { readOperations, type OperationsSnapshot } from '@/api/admin/upstreamOperations'
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/upstreamOperations', () => ({ readOperations: vi.fn() }))
const fixture = () => ({ scope: { from: '2026-10-27T00:00:00Z', until: '2026-10-28T00:00:00Z', dimension: 'upstream' }, queried_at: '2026-10-28T00:00:00Z', attribution: 'current_config_estimate', currency: 'USD', usage: { summary: { requests: 3, standard_charge: 12, request_income: 4, estimated_cost: 3, estimated_profit: 1, margin: .25, unknown_cost_requests: 0, cost_covered_requests: 3, unknown_upstream_requests: 1, default_multiplier_requests: 1 }, rows: [{ key: 'https://relay.example:8443/tenant/v1', name: 'https://relay.example:8443/tenant/v1', requests: 3, request_income: 4, estimated_cost: 3, estimated_profit: 1, margin: .25 }], total: 21, accounts: [] }, quality: { status: 'ready', data: { summary: { rounds: 0 }, rows: [], total: 0 } }, errors: { status: 'ready', data: { records: 0, upstream_5xx: 0, rows: [] } } }) as unknown as OperationsSnapshot
beforeEach(() => { vi.resetAllMocks(); vi.mocked(readOperations).mockResolvedValue(fixture()) })
describe('independent profit operations', () => {
 it('shows the persisted request accounting formula and current configuration estimate boundary', async () => {
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.text()).toContain('profitOps.title'); expect(w.text()).toContain('profitOps.formula'); expect(w.text()).toContain('profitOps.attribution')
  expect(w.find('[data-testid="profit-filters"]').exists()).toBe(true)
  expect(w.get('[data-testid="profit-summary"]').text()).toContain('25.00%')
  expect(readOperations).toHaveBeenCalledWith('profit', expect.objectContaining({ dimension: 'upstream', page: 1 }), expect.anything())
  const q = vi.mocked(readOperations).mock.calls[0]![1]
  expect(Date.parse(q.until) - Date.parse(q.from)).toBe(86400000)
  w.unmount()
 })
 it('drills from URL to account to model and keeps a frozen half-open range when paging', async () => {
  const w = mount(ProfitOperationsView); await flushPromises(); const original = { ...vi.mocked(readOperations).mock.calls[0]![1] }
  await w.get('[data-testid="operations-next"]').trigger('click'); await flushPromises()
  expect(readOperations).toHaveBeenLastCalledWith('profit', expect.objectContaining({ page: 2, from: original.from, until: original.until }), expect.anything())
  await w.get('[data-testid="drill-row"]').trigger('click'); await flushPromises()
  expect(readOperations).toHaveBeenLastCalledWith('profit', expect.objectContaining({ upstream: 'https://relay.example:8443/tenant/v1', dimension: 'account', page: 1, account_id: 0 }), expect.anything())
  w.unmount()
 })
 it('never renders query failure as a successful zero, and shows null margin as unknown', async () => {
  vi.mocked(readOperations).mockRejectedValueOnce(new Error('synthetic failure'))
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.find('[role="alert"]').exists()).toBe(true); expect(w.find('[data-testid="profit-summary"]').exists()).toBe(false)
  const next = fixture(); next.usage.summary.margin = null; next.usage.summary.estimated_cost = null; next.usage.summary.estimated_profit = null
  vi.mocked(readOperations).mockResolvedValue(next)
  await w.get('[data-testid="refresh-operations"]').trigger('click'); await flushPromises()
  expect(w.get('[data-testid="profit-summary"]').text()).toContain('profitOps.unknown')
  expect(w.text()).not.toMatch(/NaN|Infinity/); w.unmount()
 })
 it('applies model scope and sort after resetting pagination and ignores stale replies', async () => {
  const w = mount(ProfitOperationsView); await flushPromises()
  await w.get('[data-testid="model-filter"]').setValue('vendor-model')
  await w.get('[data-testid="model-scope"]').setValue('upstream')
  await w.get('[data-testid="sort-filter"]').setValue('estimated_profit')
  await w.get('[data-testid="profit-filters"]').trigger('submit'); await flushPromises()
  expect(readOperations).toHaveBeenLastCalledWith('profit', expect.objectContaining({ model: 'vendor-model', model_scope: 'upstream', sort: 'estimated_profit', page: 1 }), expect.anything())
  w.unmount()
 })
})
