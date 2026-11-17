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
const admin = vi.hoisted(() => ({ user: { id: 41 }, isAdmin: true }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => admin }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
const providerRoute = '/admin/profit-operations/providers', snapshotRoute = '/admin/profit-operations/snapshot'
const sub: ProfitProvider = { provider_id: 4, provider_name: 'Relay A', site_type: 'sub2api', channel_id: 17, channel_name: 'Alpha', revision: 3, status: 'connected', observed_at: '2026-10-01T04:15:00Z', metrics: { today_spend: 60, total_spend: 60, currency: 'USD' } }
const other: ProfitProvider = { ...sub, provider_id: 9, provider_name: 'Relay B', channel_id: 28, channel_name: 'Beta', site_type: 'newapi' }
const readPage = (): ProfitProviderPage => ({ items: [{ ...sub }, { ...other }], total: 2, page: 1, page_size: 20 })
const metrics = { requests: 2, supplier_requests: 2, bill_requests: 0, estimated_requests: 0, standard_charge: 80, request_income: 100, upstream_cost: 60 as number | null, gross_profit: 40 as number | null, gross_margin: .4 as number | null, estimated_cost: 8, estimated_profit: 92, margin: .92, reconciled_cost: 40, cost_source: 'wallet_today' as const, wallet_currency: 'USD' }
const providerRow = { ...metrics, key: 'channel:17', name: 'Alpha', assignment: 'provider_url', channel_id: 17, provider_id: 4, provider_site_type: 'sub2api' as const }
function readSnapshot(scope: OperationsQuery) {
  return { scope, queried_at: scope.until, currency: 'USD', usage: { accounts: [], members: [], catalog: [], summary: { ...metrics }, rows: [{ ...providerRow }], total: 1 }, quality: { status: 'ready' }, errors: { status: 'ready' } }
}
const get = vi.mocked(apiClient.get), put = vi.mocked(apiClient.put), post = vi.mocked(apiClient.post)
let readProviders: (page: number) => Promise<ProfitProviderPage>
let snapshot = readSnapshot
let jobStatus: (url: string) => Promise<unknown>
beforeEach(() => {
  admin.user.id = 41; localStorage.clear(); vi.resetAllMocks(); vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] }); vi.setSystemTime(new Date('2026-10-20T16:30:00Z'))
  jobStatus = async () => ({ status: 'running' })
  snapshot = readSnapshot; readProviders = async () => readPage()
  get.mockImplementation(async (url, config) => {
    if (/^\/admin\/providers\/\d+\/profit-charges\/sync\//.test(url)) return { data: await jobStatus(url) }
    if (url === providerRoute) return { data: await readProviders(config?.params.page) }
    if (url === snapshotRoute) return { data: snapshot(config?.params) }
    if (url === '/admin/upstream-channels') return { data: { items: [], total: 0 } }
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

it.each(['en', 'zh'])('prices a linked channel from channel-operations spent today, not from matched bills (%s)', async lang => {
  const labels = lang === 'en' ? enProfit : zhProfit
  const w = open(lang); await flushPromises()
  const row = w.get('[data-testid="profit-drilldown"] tbody tr'), summary = w.get('[data-testid="profit-summary"]')
  for (const section of [row, summary]) {
    expect(field(section, 'upstream_cost')).toBe('60.000000'); expect(field(section, 'gross_profit')).toBe('40.000000'); expect(field(section, 'gross_margin')).toBe('40.00%')
    expect(section.get('[data-testid="cost-source"]').text()).toContain(labels.walletToday)
    expect(section.text()).not.toMatch(/尚未匹配账单|账单已匹配|No bills matched|Bills matched/)
  }
  expect(w.find('[data-testid="sync-bill"]').exists()).toBe(false)
  expect(w.text()).not.toContain(labels.syncCharges)
  expect(get.mock.calls.some(([url]) => String(url).includes('/profit-charges/'))).toBe(false)
  expect(w.get('[data-testid="profit-drilldown"] thead').text()).toContain(labels.upstream_cost)
  expect(w.text()).toContain(`${labels.compare} · ${labels.upstream}`)
  expect(w.text()).toContain(labels.formula)
  expect(row.text()).toContain(lang === 'en' ? enChannels.provider_url : zhChannels.provider_url)
  expect(get.mock.calls.some(([url]) => url === providerRoute)).toBe(false)
  await w.get('[data-testid="drill-row"]').trigger('click'); await flushPromises()
  expect(scope()).toMatchObject({ channel_id: 17, upstream: '', page: 1, dimension: 'account' })
  await w.get('input[maxlength="2048"]').setValue('https://legacy.invalid/v1'); await w.get('[data-testid="profit-filters"]').trigger('submit'); await flushPromises()
  expect(scope()).toMatchObject({ upstream: 'https://legacy.invalid/v1', channel_id: 0, page: 1 })
  expect(post).not.toHaveBeenCalled(); w.unmount()
})

it.each(['wallet_unavailable', 'wallet_not_split', 'wallet_currency'] as const)('does not invent a wallet amount when today spend cannot price the row: %s', async source => {
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) Object.assign(target, { cost_source: source, wallet_currency: 'CNY', upstream_cost: null, gross_profit: null, gross_margin: null })
    return data
  }
  const w = open('zh'); await flushPromises()
  const copy = source === 'wallet_unavailable' ? zhProfit.walletUnavailable : source === 'wallet_not_split' ? zhProfit.walletNotSplit : zhProfit.walletCurrency.replace('{currency}', 'CNY')
  for (const section of [w.get('[data-testid="profit-drilldown"] tbody tr'), w.get('[data-testid="profit-summary"]')]) {
    expect(section.get('[data-testid="cost-source"]').text()).toContain(copy)
    expect(section.text()).not.toMatch(/尚未匹配账单|账单已匹配/)
    for (const key of ['upstream_cost', 'gross_profit', 'gross_margin']) expect(field(section, key)).toBe(zhProfit.amountUnavailable)
  }
  expect(w.find('[data-testid="sync-bill"]').exists()).toBe(false)
  expect(w.get('[data-testid="profit-drilldown"] thead').text()).not.toContain(zhProfit.estimated_cost); w.unmount()
})

it('labels an unbound URL and an explicit channel without a supplier as estimates, even in a mixed total', async () => {
  snapshot = q => {
    const data = readSnapshot(q), est = { ...providerRow, requests: 1, supplier_requests: 0, bill_requests: 0, estimated_requests: 1, request_income: 4, upstream_cost: 1, gross_profit: 3, gross_margin: .75, provider_id: 0, provider_site_type: '' as const, cost_source: 'estimate' as const }
    data.usage.rows.push({ ...est, key: 'channel:30', channel_id: 30, name: 'Manual channel', assignment: 'explicit' }, { ...est, key: 'https://unbound.invalid', channel_id: 0, name: 'Unbound', assignment: 'unbound_url' })
    Object.assign(data.usage.summary, { requests: 4, supplier_requests: 2, estimated_requests: 2, request_income: 108, upstream_cost: 62, gross_profit: 46, gross_margin: 46/108, cost_source: 'mixed' }); return data
  }
  const w = open('zh'); await flushPromises()
  const rows = w.findAll('[data-testid="profit-drilldown"] tbody tr'); expect(rows).toHaveLength(3)
  for (const row of rows.slice(1)) {
    for (const label of [zhProfit.estimated_cost, zhProfit.estimated_profit, zhProfit.margin]) expect(row.text()).toContain(label)
    expect(field(row, 'gross_margin')).toBe('75.00%'); expect(row.get('[data-testid="cost-source"]').text()).toContain('1/1'); expect(row.find('[data-testid="sync-bill"]').exists()).toBe(false)
  }
  const summary = w.get('[data-testid="profit-summary"]'); expect(field(summary, 'upstream_cost')).toBe('62.000000'); expect(summary.text()).toContain(zhProfit.walletToday); expect(summary.text()).toContain('倍率估算 2/2 笔'); w.unmount()
})

it('labels even unpriceable unbound rows as estimated, without fabricating amounts', async () => {
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) Object.assign(target, { supplier_requests: 0, bill_requests: 0, estimated_requests: 1, cost_source: 'estimate', upstream_cost: null, gross_profit: null, gross_margin: null })
    return data
  }
  const w = open('zh'); await flushPromises()
  const table = w.get('[data-testid="profit-drilldown"]'); expect(table.get('thead').text()).toContain(zhProfit.estimated_cost)
  expect(table.get('tbody [data-testid="cost-source"]').text()).toContain('1/2'); expect(field(table, 'upstream_cost')).toBe(zhProfit.unknown); w.unmount()
})

it('keeps a billed zero and zero gross profit distinct from undefined margin at zero revenue', async () => {
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) Object.assign(target, { request_income: 0, upstream_cost: 0, gross_profit: 0, gross_margin: null })
    return data
  }
  const w = open(); await flushPromises(); const row = w.get('[data-testid="profit-drilldown"] tbody tr')
  expect(field(row, 'upstream_cost')).toBe('0.000000'); expect(field(row, 'gross_profit')).toBe('0.000000'); expect(field(row, 'gross_margin')).toBe(enProfit.unknown); w.unmount()
})

it('reloads the selected window without offering per-request bill sync', async () => {
  const w = open('zh'); await flushPromises()
  expect(post).not.toHaveBeenCalled()
  expect(w.find('[data-testid="sync-bill"]').exists()).toBe(false)
  expect(w.text()).not.toContain('尚未匹配账单')
  expect(w.text()).toContain(zhProfit.walletToday)
  expect(w.text()).toContain(zhProfit.refreshWallet)
  await w.get('[data-testid="yesterday-filter"]').trigger('click'); await flushPromises()
  expect(post).not.toHaveBeenCalled()
  expect(get.mock.calls.some(([url]) => String(url).includes('/profit-charges/'))).toBe(false)
  const calls = get.mock.calls.filter(([url]) => url === snapshotRoute).length
  await w.get('[data-testid="refresh-wallet"]').trigger('click'); await flushPromises()
  expect(scope()).toMatchObject({ refresh_spend: 1 })
  expect(get.mock.calls.filter(([url]) => url === snapshotRoute).length).toBe(calls + 1)
  expect(post).not.toHaveBeenCalled()
  w.unmount()
})

it('shows a matching window snapshot, including yesterday, and the last successful read', async () => {
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) Object.assign(target, { cost_source: 'wallet_window', wallet_basis: 'wallet_window', upstream_cost: 12, gross_profit: 88, gross_margin: 0.88 })
    data.usage.wallet_synced_at = '2026-11-16T16:00:00Z'
    return data
  }
  const w = open('zh'); await flushPromises()
  const row = w.get('[data-testid="profit-drilldown"] tbody tr')
  expect(row.get('[data-testid="cost-source"]').text()).toContain(zhProfit.walletWindow)
  expect(field(row, 'upstream_cost')).toBe('12.000000')
  expect(w.get('[data-testid="wallet-synced"]').text()).toContain('上游消费最近成功读取')
  expect(w.find('[data-testid="sync-bill"]').exists()).toBe(false)
  w.unmount()
})

it('still lets an admin page through channel links without requesting wallet data to render the report', async () => {
  readProviders = async page => ({ items: page === 1 ? [sub] : [other], page, page_size: 20, total: 21 })
  const w = open(); await flushPromises(); await w.get('[data-testid="open-channel-manager"]').trigger('click'); await flushPromises()
  const links = w.get('[data-testid="provider-links"]'); await button(links, enProfit.next).trigger('click'); await flushPromises()
  expect(get).toHaveBeenLastCalledWith(providerRoute, { params: { page: 2, page_size: 20 }, signal: expect.anything() }); expect(links.text()).toContain(other.provider_name); expect(links.text()).not.toContain('60 USD')
  expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(1); w.unmount()
})

it('verifies target identity and link revision, requires fresh confirmation after conflict, and reads the link back', async () => {
  const data = readPage(); readProviders = async () => data
  const w = open(); await flushPromises(); await w.get('[data-testid="open-channel-manager"]').trigger('click'); await flushPromises()
  const links = w.get('[data-testid="provider-links"]')
  async function choose() {
    await button(links.get('[data-testid="provider-link-4"]'), enProfit.linkChannel).trigger('click'); const form = links.get('fieldset')
    await form.get('input[type="number"]').setValue(42); await form.get('form').trigger('submit'); await flushPromises()
    expect(get).toHaveBeenCalledWith('/admin/upstream-channels/42'); expect(form.text()).toContain('Legacy West · legacy-west (#42)'); expect(form.text()).toContain(`Current link: ${data.items[0]!.channel_name} #${data.items[0]!.channel_id}`)
    expect(button(form, enProfit.saveLink).attributes('disabled')).toBeDefined(); return form
  }
  let form = await choose(); await form.get('input[type="checkbox"]').setValue(true); await form.get('input[type="number"]').setValue(43)
  expect(form.find('input[type="checkbox"]').exists()).toBe(false); expect(button(form, enProfit.saveLink).attributes('disabled')).toBeDefined(); expect(put).not.toHaveBeenCalled()
  await form.get('form').trigger('submit'); await flushPromises(); expect(form.get('[role="alert"]').text()).toBe(enProfit.channelNotVerified)
  await button(form, enChannels.cancel).trigger('click'); form = await choose()
  put.mockImplementationOnce(async () => { Object.assign(data.items[0]!, { channel_id: 38, channel_name: 'Changed link', revision: 4 }); throw { status: 409 } })
  await form.get('input[type="checkbox"]').setValue(true); await button(form, enProfit.saveLink).trigger('click'); await flushPromises()
  expect(put).toHaveBeenCalledWith('/admin/providers/4/profit-channel', { channel_id: 42, revision: 3, confirm: true }); expect(links.get('[role="alert"]').text()).toBe(enProfit.linkConflict)
  expect(links.find('fieldset').exists()).toBe(false); expect(links.text()).toContain('Changed link #38'); form = await choose()
  put.mockImplementationOnce(async () => { Object.assign(data.items[0]!, { channel_id: 42, channel_name: 'Legacy West', revision: 5 }); return { data: data.items[0] } })
  await form.get('input[type="checkbox"]').setValue(true); await button(form, enProfit.saveLink).trigger('click'); await flushPromises()
  expect(put).toHaveBeenLastCalledWith('/admin/providers/4/profit-channel', { channel_id: 42, revision: 4, confirm: true }); expect(links.get('[role="status"]').text()).toBe(enChannels.saved)
  expect(links.get('[data-testid="provider-link-4"]').text()).toContain('Legacy West #42'); expect(post).not.toHaveBeenCalled(); w.unmount()
})
