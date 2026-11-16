import { flushPromises, mount, type DOMWrapper } from '@vue/test-utils'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import ProfitOperationsView from '../ProfitOperationsView.vue'
import { apiClient } from '@/api/client'
import type { OperationsQuery, ProfitProvider, ProfitProviderPage } from '@/api/admin/profitOperations'
import enProfit from '@/i18n/locales/en/profitOps'
import enProvider from '@/i18n/locales/en/matureProviders'
import enChannels from '@/i18n/locales/en/upstreamEntities'
import zhProfit from '@/i18n/locales/zh/profitOps'
import zhProvider from '@/i18n/locales/zh/matureProviders'
import zhChannels from '@/i18n/locales/zh/upstreamEntities'

vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 }, isAdmin: true }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
const providerRoute = '/admin/profit-operations/providers', snapshotRoute = '/admin/profit-operations/snapshot'
const sub: ProfitProvider = { provider_id: 4, provider_name: 'Relay A', site_type: 'sub2api', channel_id: 17, channel_name: 'Alpha', revision: 3, status: 'connected', observed_at: '2026-10-01T04:15:00Z', metrics: { today_spend: 0, total_spend: null, currency: 'USD' } }
const other: ProfitProvider = { ...sub, provider_id: 9, provider_name: 'No local usage', channel_id: 28, channel_name: 'Beta', site_type: 'newapi', status: 'unknown', observed_at: null, metrics: { today_spend: null, total_spend: null, currency: '' } }
const readPage = (): ProfitProviderPage => ({ items: [{ ...sub }, { ...other }], total: 2, page: 1, page_size: 20 })
const metrics = { requests: 2, request_income: 7, estimated_cost: 4, estimated_profit: 3, reconciled_cost: null as number | null, reconciled_profit: null as number | null, reconciled_requests: 1, covered_reconciled_cost: 0 as number | null, reported_usage_requests: 2, reported_usage_cost: 1.5 as number | null }
function readSnapshot(scope: OperationsQuery) {
  return { scope, queried_at: scope.until, currency: 'USD', usage: { accounts: [], members: [], catalog: [], summary: { ...metrics }, rows: [{ ...metrics, key: 'channel:17', name: 'Alpha', assignment: 'provider_import', channel_id: 17 }], total: 1 }, quality: { status: 'ready' }, errors: { status: 'ready' } }
}
const get = vi.mocked(apiClient.get), put = vi.mocked(apiClient.put), post = vi.mocked(apiClient.post)
let readProviders: (page: number) => Promise<ProfitProviderPage>
let snapshot = readSnapshot
beforeEach(() => {
  vi.resetAllMocks(); vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date('2026-10-20T16:30:00Z'))
  snapshot = readSnapshot; readProviders = async () => readPage()
  get.mockImplementation(async (url, config) => {
    if (url === providerRoute) return { data: await readProviders(config?.params.page) }
    if (url === snapshotRoute) return { data: snapshot(config?.params) }
    if (url === '/admin/upstream-channels/42') return { data: { id: 42, name: 'Legacy West', code: 'legacy-west', revision: 8, members: 5 } }
    throw new Error(`Unexpected GET: ${url}`)
  })
})
afterEach(() => vi.useRealTimers())
function open(locale = 'en') {
  const i18n = createI18n({ legacy: false, locale, missingWarn: false, fallbackWarn: false, messages: {
    en: { profitOps: enProfit, upstreamEntities: enChannels, matureProviders: enProvider },
    zh: { profitOps: zhProfit, upstreamEntities: zhChannels, matureProviders: zhProvider }
  } })
  return mount(ProfitOperationsView, { global: { plugins: [createPinia(), i18n] } })
}
function button(node: DOMWrapper<Element>, text: string) { const btn = node.findAll('button').find(item => item.text() === text); if (!btn) throw new Error(`Button not found: ${text}`); return btn }
const scope = () => get.mock.calls.filter(([url]) => url === snapshotRoute).at(-1)?.[1]?.params as OperationsQuery
const field = (el: DOMWrapper<Element>, key: string) => el.get(`[data-field="${key}"]`).text()

it.each(['en', 'zh'])('keeps wallet observations separate from filtered accounting and reconciled coverage (%s)', async lang => {
  const labels = lang === 'en' ? enProfit : zhProfit
  const w = open(lang); await flushPromises()
  const wallets = w.get('[data-testid="profit-providers"]'), a = wallets.get('[data-testid="wallet-4"]'), b = wallets.get('[data-testid="wallet-9"]')
  expect(a.text()).toContain('0 USD'); expect(a.text()).toContain(labels.unknown); expect(a.text()).toContain(labels.olderReading); expect(a.get('time').text()).toBe(sub.observed_at)
  expect(b.text()).toContain(other.provider_name); expect(b.text()).not.toContain(labels.syncCharges)
  expect(wallets.get('table').findAll('tbody tr')).toHaveLength(2)
  expect(wallets.text()).toContain(labels.walletNote)
  const summary = w.get('[data-testid="profit-summary"]')
  expect(field(summary, 'reconciled_cost')).toBe(labels.unknown); expect(field(summary, 'reconciled_profit')).toBe(labels.unknown)
  expect(field(summary, 'reconciled_requests')).toBe('1 / 2'); expect(field(summary, 'covered_reconciled_cost')).toBe('0')
  expect(field(summary, 'reported_usage_cost')).toBe('1.5'); expect(field(summary, 'reported_usage_requests')).toBe('2 / 2')
  const row = w.get('[data-testid="drill-row"]').element.closest('tr')!
  expect(row.textContent).toContain(lang === 'en' ? enChannels.provider_import : zhChannels.provider_import)
  expect(row.querySelector('[data-field="reconciled_profit"]')?.textContent).toBe(labels.unknown)
  expect(w.findAll('[data-testid="drill-row"]')).toHaveLength(1)
  await button(b, 'Beta #28').trigger('click'); await flushPromises()
  expect(scope()).toMatchObject({ channel_id: 28, upstream: '', account_id: 0, page: 1, dimension: 'account' })
  await w.get('input[maxlength="2048"]').setValue('https://legacy.example/v1'); await w.get('[data-testid="profit-filters"]').trigger('submit'); await flushPromises()
  expect(scope()).toMatchObject({ upstream: 'https://legacy.example/v1', channel_id: 0, page: 1 })
  expect(get.mock.calls.filter(([url]) => url === providerRoute)).toHaveLength(1)
  expect(post).not.toHaveBeenCalled(); expect(put).not.toHaveBeenCalled(); w.unmount()
})

it('distinguishes fully matched zero debit from no evidence, and never fills gaps from estimates or reported usage', async () => {
  snapshot = q => {
    const data = readSnapshot(q)
    Object.assign(data.usage.summary, { reconciled_requests: 2, reconciled_cost: 0, reconciled_profit: 7 })
    Object.assign(data.usage.rows[0]!, { reconciled_requests: 0, reconciled_cost: 55, reconciled_profit: 56, covered_reconciled_cost: null, reported_usage_requests: 0, reported_usage_cost: null })
    return data
  }
  const w = open(); await flushPromises()
  const summary = w.get('[data-testid="profit-summary"]'), row = w.get('[data-testid="drill-row"]').element.closest('tr')!
  expect(field(summary, 'reconciled_cost')).toBe('0'); expect(field(summary, 'reconciled_profit')).toBe('7')
  for (const key of ['reconciled_cost', 'reconciled_profit', 'reported_usage_cost', 'covered_reconciled_cost']) expect(row.querySelector(`[data-field="${key}"]`)?.textContent).toBe('Unknown')
  expect(row.querySelector('[data-field="reconciled_requests"]')?.textContent).toBe('0 / 2'); w.unmount()
})

it('shows shared wallet spend of 60 separately from 40 in local debits and 60 in request profit', async () => {
  readProviders = async () => ({ ...readPage(), items: [{ ...sub, metrics: { today_spend: 60, total_spend: 60, currency: 'USD' } }] })
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) {
      Object.assign(target, { request_income: 100, reported_usage_cost: 45, covered_reconciled_cost: 40, reconciled_requests: 2, reconciled_cost: 40, reconciled_profit: 60 })
    }
    return data
  }
  const w = open(); await flushPromises()
  expect(w.get('[data-testid="wallet-4"]').text()).toContain('60 USD')
  const summary = w.get('[data-testid="profit-summary"]')
  expect(field(summary, 'reported_usage_cost')).toBe('45')
  expect(field(summary, 'reconciled_cost')).toBe('40')
  expect(field(summary, 'reconciled_profit')).toBe('60')
  expect(field(summary, 'reconciled_requests')).toBe('2 / 2')
  w.unmount()
})

it('does not turn a pending or failed wallet list into zero; paginates without rereading the report', async () => {
  let reject!: (cause: Error) => void
  readProviders = () => new Promise((_, r) => { reject = r })
  const w = open(); await flushPromises()
  const wallets = w.get('[data-testid="profit-providers"]')
  expect(wallets.get('[role="status"]').text()).toBe(enProvider.loading); expect(wallets.find('table').exists()).toBe(false)
  reject(new Error('fixture failure')); await flushPromises()
  expect(wallets.get('[role="alert"]').text()).toBe(enProfit.walletLoadFailed); expect(wallets.text()).not.toContain(enProfit.providersEmpty)
  readProviders = async page => ({ items: page === 1 ? [sub] : [other], page, page_size: 20, total: 21 })
  await button(wallets, enProvider.refreshList).trigger('click'); await flushPromises()
  await button(wallets, enProfit.next).trigger('click'); await flushPromises()
  expect(get).toHaveBeenLastCalledWith(providerRoute, { params: { page: 2, page_size: 20 }, signal: expect.anything() })
  expect(wallets.find('[data-testid="wallet-4"]').exists()).toBe(false); expect(wallets.get('[data-testid="wallet-9"]').text()).toContain(other.provider_name)
  expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(1); w.unmount()
})

it('sends only the applied dates to Sub2API following an explicit click; shows local match counts and blocks double clicks', async () => {
  const w = open(); await flushPromises()
  await w.get('[data-testid="yesterday-filter"]').trigger('click'); await flushPromises()
  const { from, until } = scope()
  await w.get('input[type="datetime-local"]').setValue('2026-10-01T00:00')
  expect(post).not.toHaveBeenCalled()
  let done!: (v: { data: { matched: number; unmatched: number } }) => void
  post.mockImplementationOnce(() => new Promise(resolve => { done = resolve }))
  const wallets = w.get('[data-testid="profit-providers"]'), act = button(wallets.get('[data-testid="wallet-4"]'), enProfit.syncCharges)
  await act.trigger('click'); await act.trigger('click')
  expect(post).toHaveBeenCalledTimes(1); expect(post).toHaveBeenCalledWith('/admin/providers/4/profit-charges/sync', { from, until }, { timeout: 55_000 })
  expect(wallets.text()).not.toContain('0 matched')
  done({ data: { matched: 0, unmatched: 3 } }); await flushPromises()
  expect(wallets.get('[role="status"]').text()).toContain('Relay A: 0 matched, 3 unmatched'); expect(wallets.get('[role="status"]').text()).toContain(until)
  expect(scope()).toMatchObject({ from, until }); expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(3)
  post.mockRejectedValueOnce(new Error('fixture unavailable'))
  await act.trigger('click'); await flushPromises(); expect(wallets.get('[role="alert"]').text()).toContain('Could not confirm the sync'); expect(wallets.text()).not.toContain('0 matched')
  w.unmount()
})

it('verifies target identity and link revision, requires new confirmation after conflict, and reads the link back', async () => {
  const data = readPage(); readProviders = async () => data
  const w = open(); await flushPromises()
  const wallets = w.get('[data-testid="profit-providers"]')
  async function choose() {
    await button(wallets.get('[data-testid="wallet-4"]'), enProfit.linkChannel).trigger('click')
    const form = wallets.get('fieldset')
    await form.get('input[type="number"]').setValue(42); await form.get('form').trigger('submit'); await flushPromises()
    expect(get).toHaveBeenCalledWith('/admin/upstream-channels/42')
    expect(form.text()).toContain('Legacy West · legacy-west (#42)'); expect(form.text()).toContain(`Current link: ${data.items[0]!.channel_name} #${data.items[0]!.channel_id}`)
    expect(button(form, enProfit.saveLink).attributes('disabled')).toBeDefined(); return form
  }
  let form = await choose(); await form.get('input[type="checkbox"]').setValue(true)
  await form.get('input[type="number"]').setValue(43)
  expect(form.find('input[type="checkbox"]').exists()).toBe(false); expect(button(form, enProfit.saveLink).attributes('disabled')).toBeDefined(); expect(put).not.toHaveBeenCalled()
  await form.get('form').trigger('submit'); await flushPromises(); expect(form.get('[role="alert"]').text()).toBe(enProfit.channelNotVerified)
  await button(form, enChannels.cancel).trigger('click'); form = await choose()
  put.mockImplementationOnce(async () => { Object.assign(data.items[0]!, { channel_id: 38, channel_name: 'Changed link', revision: 4 }); throw { status: 409 } })
  await form.get('input[type="checkbox"]').setValue(true); await button(form, enProfit.saveLink).trigger('click'); await flushPromises()
  expect(put).toHaveBeenCalledWith('/admin/providers/4/profit-channel', { channel_id: 42, revision: 3, confirm: true }); expect(wallets.get('[role="alert"]').text()).toBe(enProfit.linkConflict)
  expect(wallets.find('fieldset').exists()).toBe(false); expect(wallets.text()).toContain('Changed link #38')
  form = await choose(); expect(put).toHaveBeenCalledTimes(1)
  put.mockImplementationOnce(async () => { Object.assign(data.items[0]!, { channel_id: 42, channel_name: 'Legacy West', revision: 5 }); return { data: data.items[0] } })
  await form.get('input[type="checkbox"]').setValue(true); await button(form, enProfit.saveLink).trigger('click'); await flushPromises()
  expect(put).toHaveBeenLastCalledWith('/admin/providers/4/profit-channel', { channel_id: 42, revision: 4, confirm: true }); expect(wallets.get('[role="status"]').text()).toBe(enChannels.saved)
  expect(wallets.get('[data-testid="wallet-4"]').text()).toContain('Legacy West #42'); expect(post).not.toHaveBeenCalled(); w.unmount()
})
