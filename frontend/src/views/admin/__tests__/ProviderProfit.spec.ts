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
const metrics = { requests: 2, supplier_requests: 2, bill_requests: 2, estimated_requests: 0, standard_charge: 80, request_income: 100, upstream_cost: 45 as number | null, gross_profit: 55 as number | null, gross_margin: .55 as number | null, estimated_cost: 8, estimated_profit: 92, margin: .92, reconciled_cost: 40 }
const providerRow = { ...metrics, key: 'channel:17', name: 'Alpha', assignment: 'provider_url', channel_id: 17, provider_id: 4, provider_site_type: 'sub2api' }
function readSnapshot(scope: OperationsQuery) {
  return { scope, queried_at: scope.until, currency: 'USD', usage: { accounts: [], members: [], catalog: [], summary: { ...metrics }, rows: [{ ...providerRow }], total: 1 }, quality: { status: 'ready' }, errors: { status: 'ready' } }
}
const get = vi.mocked(apiClient.get), put = vi.mocked(apiClient.put), post = vi.mocked(apiClient.post)
let readProviders: (page: number) => Promise<ProfitProviderPage>
let snapshot = readSnapshot
const jobRoute = '/admin/providers/4/profit-charges/sync'
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

it.each(['en', 'zh'])('shows billed cost rather than wallet spend, confirmed debit or estimate in the table and summary (%s)', async lang => {
  const labels = lang === 'en' ? enProfit : zhProfit
  const w = open(lang); await flushPromises()
  const row = w.get('[data-testid="profit-drilldown"] tbody tr'), summary = w.get('[data-testid="profit-summary"]')
  for (const section of [row, summary]) {
    expect(field(section, 'upstream_cost')).toBe('45.000000'); expect(field(section, 'gross_profit')).toBe('55.000000'); expect(field(section, 'gross_margin')).toBe('55.00%')
    expect(section.get('[data-testid="cost-source"]').text()).toContain('2/2')
  }
  expect(w.get('[data-testid="profit-drilldown"] thead').text()).toContain(labels.upstream_cost)
  expect(w.text()).toContain(`${labels.compare} · ${labels.upstream}`)
  expect(w.text()).not.toMatch(/供应商钱包最近读数|上游费用凭据|Latest provider wallet readings|Upstream charge evidence/)
  expect(row.text()).toContain(lang === 'en' ? enChannels.provider_url : zhChannels.provider_url)
  expect(get.mock.calls.some(([url]) => url === providerRoute)).toBe(false)
  await w.get('[data-testid="drill-row"]').trigger('click'); await flushPromises()
  expect(scope()).toMatchObject({ channel_id: 17, upstream: '', page: 1, dimension: 'account' })
  await w.get('input[maxlength="2048"]').setValue('https://legacy.invalid/v1'); await w.get('[data-testid="profit-filters"]').trigger('submit'); await flushPromises()
  expect(scope()).toMatchObject({ upstream: 'https://legacy.invalid/v1', channel_id: 0, page: 1 })
  expect(post).not.toHaveBeenCalled(); w.unmount()
})

it.each([0, 1])('never substitutes an estimate for a linked supplier with %i of 2 bills', async bills => {
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) Object.assign(target, { bill_requests: bills, upstream_cost: null, gross_profit: null, gross_margin: null })
    return data
  }
  const w = open('zh'); await flushPromises()
  for (const section of [w.get('[data-testid="profit-drilldown"] tbody tr'), w.get('[data-testid="profit-summary"]')]) {
    expect(section.get('[data-testid="cost-source"]').text()).toContain(bills ? '账单已匹配 1/2 笔' : '尚未匹配账单（0/2 笔）')
    expect(section.text()).not.toMatch(/待同步|未同步|漏账/)
    for (const key of ['upstream_cost', 'gross_profit', 'gross_margin']) expect(field(section, key)).toBe(bills ? zhProfit.incomplete : zhProfit.pending)
  }
  expect(w.get('[data-testid="profit-drilldown"] thead').text()).not.toContain(zhProfit.estimated_cost); w.unmount()
})

it('labels an unbound URL and an explicit channel without a supplier as estimates, even in a mixed total', async () => {
  snapshot = q => {
    const data = readSnapshot(q), est = { ...providerRow, requests: 1, supplier_requests: 0, bill_requests: 0, estimated_requests: 1, request_income: 4, upstream_cost: 1, gross_profit: 3, gross_margin: .75, provider_id: 0, provider_site_type: '' }
    data.usage.rows.push({ ...est, key: 'channel:30', channel_id: 30, name: 'Manual channel', assignment: 'explicit' }, { ...est, key: 'https://unbound.invalid', channel_id: 0, name: 'Unbound', assignment: 'unbound_url' })
    Object.assign(data.usage.summary, { requests: 4, estimated_requests: 2, request_income: 108, upstream_cost: 47, gross_profit: 61, gross_margin: 61/108 }); return data
  }
  const w = open('zh'); await flushPromises()
  const rows = w.findAll('[data-testid="profit-drilldown"] tbody tr'); expect(rows).toHaveLength(3)
  for (const row of rows.slice(1)) {
    for (const label of [zhProfit.estimated_cost, zhProfit.estimated_profit, zhProfit.margin]) expect(row.text()).toContain(label)
    expect(field(row, 'gross_margin')).toBe('75.00%'); expect(row.get('[data-testid="cost-source"]').text()).toContain('1/1'); expect(row.find('[data-testid="sync-bill"]').exists()).toBe(false)
  }
  const summary = w.get('[data-testid="profit-summary"]'); expect(field(summary, 'upstream_cost')).toBe('47.000000'); expect(summary.text()).toContain('上游账单 2/2 笔 · 倍率估算 2/2 笔'); w.unmount()
})

it('labels even unpriceable unbound rows as estimated, without fabricating amounts', async () => {
  snapshot = q => {
    const data = readSnapshot(q)
    for (const target of [data.usage.summary, ...data.usage.rows]) Object.assign(target, { supplier_requests: 0, bill_requests: 0, estimated_requests: 1, upstream_cost: null, gross_profit: null, gross_margin: null })
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

it('syncs only an eligible Sub2API row using the applied dates, prevents double clicks and rereads the report', async () => {
  snapshot = q => { const data = readSnapshot(q); data.usage.rows.push({ ...providerRow, key: 'channel:28', name: 'Beta', channel_id: 28, provider_id: 9, provider_site_type: 'newapi', bill_requests: 0, upstream_cost: null, gross_profit: null, gross_margin: null }); return data }
  const w = open(); await flushPromises(); await w.get('[data-testid="yesterday-filter"]').trigger('click'); await flushPromises()
  const { from, until } = scope(); await w.get('input[type="datetime-local"]').setValue('2026-10-01T00:00'); expect(post).not.toHaveBeenCalled()
  expect(w.findAll('[data-testid="sync-bill"]')).toHaveLength(1); expect(w.text()).toContain(enProfit.syncUnsupported)
  let accepted!: (v: { data: { job_id: string } }) => void
  post.mockImplementationOnce(() => new Promise(resolve => { accepted = resolve }))
  const act = w.get('[data-testid="sync-bill"]'); await act.trigger('click'); await act.trigger('click')
  expect(post).toHaveBeenCalledTimes(1); expect(post).toHaveBeenCalledWith(jobRoute, { from, until }, { params: { background: true } })
  accepted({ data: { job_id: 'fixture-id' } }); await flushPromises()
  expect(get).toHaveBeenCalledWith(`${jobRoute}/fixture-id`, { signal: expect.anything() })
  expect(act.attributes('disabled')).toBeDefined(); expect(w.get('[role="status"]').text()).toContain('background')
  expect(w.get('[data-testid="refresh-operations"]').attributes('disabled')).toBeUndefined()
  let finish!: (result: unknown) => void
  jobStatus = () => new Promise(resolve => { finish = resolve })
  await vi.advanceTimersByTimeAsync(60_000)
  expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/fixture-id`)).toHaveLength(2) // no overlapping polls
  finish({ status: 'succeeded', matched: 0, unmatched: 3 }); await flushPromises()
  expect(w.get('[role="status"]').text()).toContain('Alpha: 0 matched, 3 unmatched'); expect(w.get('[role="status"]').text()).toContain(until)
  expect(scope()).toMatchObject({ from, until }); expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(3)
  expect(w.get('[data-testid="sync-bill"]').attributes('disabled')).toBeUndefined(); expect(post).toHaveBeenCalledTimes(1); w.unmount()
})

it.each(['upstream', 'timeout', 'conflict', 'simple_mode', 'evidence_budget', 'internal', 'Bearer fake-private-text'] as const)('shows only local advice after a late job failure: %s', async reason => {
  const w = open('zh'); await flushPromises()
  post.mockResolvedValue({ data: { job_id: 'late-failure' } })
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(w.get('[role="status"]').text()).toBe(zhProfit.syncing)
  jobStatus = async () => ({ status: 'failed', reason, message: 'fake-private-text', matched: 1234 })
  await vi.advanceTimersByTimeAsync(60_000); await flushPromises()
  expect(w.get('[role="alert"]').text()).toBe(zhProfit.syncFailure[reason === 'Bearer fake-private-text' ? 'internal' : reason])
  expect(w.text()).not.toMatch(/fake-private-text|1234/); expect(w.find('[role="status"]').exists()).toBe(false)
  expect(post).toHaveBeenCalledTimes(1); expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(1)
  w.unmount()
})

it('retries a lost status read with GET only and treats 404 as expired', async () => {
  const w = open(); await flushPromises()
  post.mockResolvedValue({ data: { job_id: 'interrupted' } })
  jobStatus = async () => { throw { status: 503, message: 'fake-private-text' } }
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toBe(enProfit.syncDisconnected)
  expect(w.get('[data-testid="sync-bill"]').text()).toBe(enProfit.syncCheck)
  jobStatus = async () => { throw { status: 404 } }
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toBe(enProfit.syncExpired); expect(w.get('[data-testid="sync-bill"]').text()).toBe(enProfit.syncCharges)
  expect(post).toHaveBeenCalledTimes(1); expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/interrupted`)).toHaveLength(2)
  expect(w.text()).not.toContain('fake-private-text'); w.unmount()
  const reopened = open(); await flushPromises(); expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/interrupted`)).toHaveLength(2); reopened.unmount()
})

it('shows actionable admission errors without echoing the response or polling', async () => {
  const w = open(); await flushPromises()
  post.mockRejectedValueOnce({ status: 429, message: 'fake-private-text' })
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toBe(enProfit.syncBusy)
  post.mockRejectedValueOnce({ status: 503, message: 'fake-private-text' })
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toBe(enProfit.syncUnconfirmed); expect(w.text()).not.toContain('fake-private-text')
  expect(get.mock.calls.filter(([url]) => url.includes('/profit-charges/'))).toHaveLength(0); w.unmount()
})

it('resumes the saved job and exact dates on reopen without trusting a late reply after unmount', async () => {
  const w = open(); await flushPromises()
  const { from, until } = scope()
  let finish!: (result: unknown) => void
  jobStatus = () => new Promise(resolve => { finish = resolve })
  post.mockResolvedValueOnce({ data: { job_id: 'reopen', private: 'fake-private-text' } })
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  const signal = get.mock.calls.find(([url]) => url === `${jobRoute}/reopen`)![1]!.signal!
  expect(JSON.stringify(localStorage)).not.toContain('fake-private-text'); w.unmount(); expect(signal.aborted).toBe(true)
  finish({ status: 'succeeded', matched: 77, unmatched: 88 }); await flushPromises(); await vi.advanceTimersByTimeAsync(60_000)
  expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(1)
  jobStatus = async () => ({ status: 'running' })
  admin.user.id = 42
  const otherAdmin = open(); await flushPromises()
  expect(otherAdmin.find('[role="status"]').exists()).toBe(false); expect(scope().until).not.toBe(until)
  expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/reopen`)).toHaveLength(1); otherAdmin.unmount()
  admin.user.id = 41
  const reopened = open(); await flushPromises()
  expect(scope()).toMatchObject({ from, until }); expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/reopen`)).toHaveLength(2)
  expect(reopened.get('[role="status"]').text()).toBe(enProfit.syncing); expect(reopened.text()).not.toContain('77 matched')
  reopened.unmount(); await vi.advanceTimersByTimeAsync(60_000)
  expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/reopen`)).toHaveLength(2); expect(post).toHaveBeenCalledTimes(1)
})

it('keeps an admission in flight across navigation and polls only from the remounted view', async () => {
  const w = open(); await flushPromises()
  let accept!: (result: { data: { job_id: string } }) => void
  post.mockImplementationOnce(() => new Promise(resolve => { accept = resolve }))
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises(); w.unmount()
  const reopened = open(); await flushPromises(); await reopened.get('[data-testid="sync-bill"]').trigger('click')
  expect(post).toHaveBeenCalledTimes(1)
  accept({ data: { job_id: 'accepted-after-leaving' } }); await flushPromises()
  expect(reopened.get('[role="status"]').text()).toBe(enProfit.syncing)
  expect(get.mock.calls.filter(([url]) => url === `${jobRoute}/accepted-after-leaving`)).toHaveLength(1); reopened.unmount()
})

it('does not start a sync on reload if the admission response was lost; retry uses the original dates', async () => {
  const w = open(); await flushPromises()
  const { from, until } = scope()
  post.mockRejectedValueOnce({ status: 0 })
  await w.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises(); w.unmount()
  await vi.advanceTimersByTimeAsync(60_000)
  const reopened = open(); await flushPromises()
  expect(scope()).toMatchObject({ from, until }); expect(reopened.get('[role="alert"]').text()).toBe(enProfit.syncUnconfirmed)
  expect(post).toHaveBeenCalledTimes(1)
  post.mockResolvedValueOnce({ data: { job_id: 'reused' } })
  jobStatus = async () => ({ status: 'succeeded' })
  await reopened.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(post.mock.calls[1]).toEqual(post.mock.calls[0]); expect(reopened.get('[role="status"]').text()).toBe(enProfit.syncComplete)
  expect(reopened.text()).not.toContain('0 matched'); reopened.unmount()
})

it('never puts a late status in another provider row or date window', async () => {
  snapshot = q => { const data = readSnapshot(q); data.usage.rows.push({ ...providerRow, key: 'channel:28', channel_id: 28, provider_id: 9, name: 'Beta' }); return data }
  const w = open(); await flushPromises()
  const acceptA = { data: { job_id: 'a' } }, acceptB = { data: { job_id: 'b' } }
  post.mockResolvedValueOnce(acceptA).mockResolvedValueOnce(acceptB)
  let finishA!: (v: unknown) => void, finishB!: (v: unknown) => void
  jobStatus = url => new Promise(resolve => { if (url === `${jobRoute}/a`) finishA = resolve; else finishB = resolve })
  const rows = w.findAll('[data-testid="profit-drilldown"] tbody tr')
  await rows[0]!.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  expect(rows[1]!.find('[role="status"]').exists()).toBe(false)
  await rows[1]!.get('[data-testid="sync-bill"]').trigger('click'); await flushPromises()
  finishB({ status: 'failed', reason: 'conflict' }); await flushPromises()
  expect(rows[1]!.get('[role="alert"]').text()).toBe(enProfit.syncFailure.conflict)
  expect(rows[0]!.find('[role="alert"]').exists()).toBe(false)
  await w.get('[data-testid="yesterday-filter"]').trigger('click'); await flushPromises()
  finishA({ status: 'succeeded', matched: 111, unmatched: 222 }); await flushPromises()
  expect(w.find('[role="status"]').exists()).toBe(false); expect(w.find('[role="alert"]').exists()).toBe(false)
  expect(w.text()).not.toContain('111 matched'); expect(get.mock.calls.filter(([url]) => url === snapshotRoute)).toHaveLength(2)
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
