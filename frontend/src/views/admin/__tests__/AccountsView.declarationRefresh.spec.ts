import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { AxiosHeaders, type AxiosAdapter, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios'
import AccountsView from '../AccountsView.vue'
import { apiClient } from '@/api/client'
import { i18n, loadLocaleMessages } from '@/i18n'
import type { AccountListItem, UpstreamBillingProbeSnapshot } from '@/types'
import DataTable from '@/components/common/DataTable.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/types'

// Only replace the backend transport and unrelated navigation/dialogs. The
// list API, stores, countdown, merge, DataTable and declaration cell are real.
const originalAdapter = apiClient.defaults.adapter
let wrapper: VueWrapper | undefined
let rows: AccountListItem[]
let requests: InternalAxiosRequestConfig[]
let listRevision: number
let overrides: Map<string, AxiosAdapter>
let total: number

const pageData = () => ({ items: structuredClone(rows), total, page: 1, page_size: 20, pages: Math.ceil(total / 20) })
const reply = (config: InternalAxiosRequestConfig, data: unknown, status = 200): AxiosResponse => ({
  config, status, statusText: status === 304 ? 'Not Modified' : 'OK',
  headers: new AxiosHeaders({ etag: `"fixture-${listRevision}"` }),
  data: status === 304 ? null : { code: 0, data }
})

function holdNext(path: string, data: unknown) {
  let resolve!: (value: AxiosResponse) => void
  let config!: InternalAxiosRequestConfig
  const pending = new Promise<AxiosResponse>(done => { resolve = done })
  overrides.set(path, request => {
    config = request
    overrides.delete(path)
    return pending
  })
  return { complete: () => resolve(reply(config, data)), config: () => config }
}

const snapshot = (rate: number): UpstreamBillingProbeSnapshot => ({
  status: 'ok',
  received_at: '2026-11-04T00:00:00Z',
  fresh_until: '2026-11-05T00:00:00Z',
  last_attempt_at: '2026-11-04T00:00:00Z',
  next_probe_at: '2026-11-04T00:10:00Z',
  data: {
    object: 'sub2api.key_billing', schema_version: 1, billing_scope: 'token',
    resolved_rate_multiplier: rate, effective_rate_multiplier: rate,
    group_rate_multiplier: rate, peak_rate_enabled: false,
    observed_at: '2026-11-04T00:00:00Z'
  }
})

const account = (id: number): AccountListItem => ({
  id, name: `Fixture account ${id}`, platform: 'openai', type: 'apikey',
  status: 'active', schedulable: true, concurrency: 2, priority: 1,
  rate_multiplier: 1, group_ids: [], credentials: {}, extra: {}, proxy_id: null,
  created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z'
} as AccountListItem)

const binding = (id: number, channelID: number, name: string) => ({
  account_id: id, account_name: `Fixture account ${id}`, deleted: false,
  channel_id: channelID, revision: channelID,
  channel: { id: channelID, name, code: `fixture-${channelID}`, remark: '', revision: 1, members: 2 }
})

const backend: AxiosAdapter = async config => {
  requests.push(config)
  const override = overrides.get(config.url ?? '')
  if (override) return override(config)
  let data: unknown
  switch (config.url) {
    case '/admin/accounts':
      data = pageData()
      break
    case '/admin/accounts/management-capabilities': data = { concurrency_upgrade_enabled: false }; break
    case '/admin/accounts/upstream-billing-probe/settings': data = { enabled: true }; break
    case '/admin/proxies/all':
    case '/admin/tls-fingerprint-profiles':
    case '/admin/groups/all': data = []; break
    case '/admin/accounts/performance': data = {}; break
    case '/admin/accounts/today-stats/batch': data = { stats: {} }; break
    default:
      if (/\/accounts\/\d+\/schedulable-timers$/.test(config.url ?? '')) {
        data = { tasks: [], history: [], server_now: new Date().toISOString(), poll_seconds: 30, open_grace_seconds: 900, max_delay_minutes: 100 }
      } else throw new Error(`Unmocked fixture backend: ${config.method} ${config.url}`)
  }
  return reply(config, data)
}

async function mountView(admin = false, realEdit = false) {
  const pinia = createPinia()
  if (admin) useAuthStore(pinia).user = { id: 1, role: 'admin' } as User
  wrapper = mount(AccountsView, {
    attachTo: document.body,
    global: {
      plugins: [pinia, i18n],
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        CreateAccountModal: true, EditAccountModal: !realEdit, BulkEditAccountModal: true,
        SyncFromCrsModal: true, TempUnschedStatusModal: true, ImportDataModal: true,
        ReAuthAccountModal: true, AccountTestModal: true, AccountStatsModal: true,
        IQTestModal: true, ScheduledTestsPanel: true, ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true, NewAPIUpstreamConfigDialog: true,
        BulkExport2FADialog: true, TotpStepUpDialog: true
      }
    }
  })
  await flushPromises()
  return wrapper
}

async function tick() {
  // Preserve the existing countdown: a five-second selection fires at 6s.
  await vi.advanceTimersByTimeAsync(6_000)
  await flushPromises()
}

async function toggleAutoRefresh(view: VueWrapper) {
  await view.get('button[title="自动刷新"]').trigger('click')
  await view.findAll('button').find(button => button.text() === i18n.global.t('admin.accounts.enableAutoRefresh'))!.trigger('click')
  // Close the real dropdown so subsequent automatic ticks are eligible.
  await view.get('button[title="自动刷新"]').trigger('click')
}

describe('AccountsView automatic declaration refresh', () => {
  beforeAll(async () => {
    await loadLocaleMessages('zh')
    i18n.global.locale.value = 'zh'
  })

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-04T00:01:00Z'))
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.clear()
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    localStorage.setItem('account-hidden-columns', JSON.stringify([
      'usage', 'today_stats', 'proxy', 'notes', 'scheduler_score', 'rate_multiplier'
    ]))
    rows = [account(41), account(42)]
    requests = []
    listRevision = 1
    total = 2
    overrides = new Map()
    apiClient.defaults.adapter = backend
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    apiClient.defaults.adapter = originalAdapter
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('renders binding A → B → unbound/null from automatic list responses without changing account updated_at', async () => {
    rows = rows.map(row => ({ ...row, upstream_channel_binding: binding(row.id, 1, 'Fixture A') }))
    const view = await mountView()
    expect(view.findAll('[data-testid="account-current-upstream"]').map(link => link.text()))
      .toEqual(['Fixture A · fixture-1', 'Fixture A · fixture-1'])

    rows = rows.map(row => ({ ...row, upstream_channel_binding: binding(row.id, 2, 'Fixture B') }))
    listRevision++
    await tick()
    expect.soft(view.findAll('[data-testid="account-current-upstream"]').map(link => link.text()))
      .toEqual(['Fixture B · fixture-2', 'Fixture B · fixture-2'])

    // Server's unbound record (channel absent), then omitted binding/null.
    rows = rows.map(row => ({ ...row, upstream_channel_binding: {
      account_id: row.id, account_name: row.name, deleted: false, channel_id: 0, revision: 3
    } }))
    listRevision++
    await tick()
    expect.soft(view.findAll('[data-testid="account-current-upstream"]').map(link => link.text()))
      .toEqual([i18n.global.t('upstreamEntities.unbound'), i18n.global.t('upstreamEntities.unbound')])
    rows = rows.map(({ upstream_channel_binding: _binding, ...row }) => row as AccountListItem)
    listRevision++
    await tick()
    expect.soft(view.findAll('[data-testid="account-current-upstream"]')).toHaveLength(0)

    const lists = requests.filter(request => request.url === '/admin/accounts')
    expect(lists).toHaveLength(4)
    expect(lists.every(request => request.params.lite === '1' && request.params.page === 1)).toBe(true)
    expect(lists[2].headers.get('If-None-Match')).toBe('"fixture-2"')
  })

  it('renders declaration A → B → null, including status, from one list snapshot even with unchanged updated_at', async () => {
    // Defensive response-contract case: normal probe writers advance updated_at.
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.5) } }))
    const view = await mountView()
    expect(view.text()).toContain('上游渠道声明')
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.50x', '0.50x'])

    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: { ...snapshot(0.8), status: 'failed' } } }))
    listRevision++
    await tick()
    expect.soft(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.80x', '0.80x'])
    expect.soft(view.text()).toContain('失败')

    // Exercise raw HTTP null even though the client type models omission.
    rows = rows.map(row => ({ ...row, extra: {
      upstream_billing_probe: null
    } as unknown as AccountListItem['extra'] }))
    listRevision++
    await tick()
    expect.soft(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['未探测', '未探测'])
    expect.soft(view.text()).not.toContain('0.80x')
    expect(requests.filter(request => request.url === '/admin/accounts')).toHaveLength(3)
    expect(requests.some(request => /probe$|upstream-billing-rates|\/accounts\/\d+$/.test(request.url ?? ''))).toBe(false)
  })

  it('does not start the dormant five-minute reader while an automatic list is pending', async () => {
    const view = await mountView()
    const pending = holdNext('/admin/accounts', pageData())
    await tick()
    await vi.advanceTimersByTimeAsync(300_000)
    await flushPromises()
    // useIntervalFn(..., { immediate: false }) is never resumed for this reader.
    // Do not fabricate an unreachable five-minute response race in the test.
    expect(requests.filter(request => request.url === '/admin/accounts')).toHaveLength(2)
    expect(requests.filter(request => request.url === '/admin/accounts/upstream-billing-rates')).toHaveLength(0)
    expect(view.findAll('[data-row-id]')).toHaveLength(2)
    pending.complete()
    await flushPromises()
  })

  it('ignores an old automatic list after a newer forced billing snapshot has reached the DOM', async () => {
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.5) } }))
    const view = await mountView()
    const oldList = holdNext('/admin/accounts', pageData())
    await tick()
    overrides.set('/admin/accounts/41/upstream-billing-probe', async config => reply(config, { account_id: 41, snapshot: snapshot(0.8) }))
    overrides.set('/admin/accounts/upstream-billing-rates', async config => reply(config, {
      items: rows.map(row => ({ account_id: row.id, cost_multiplier: 1, snapshot: snapshot(0.8) })), total, page: 1, page_size: 20
    }))
    await view.get('[data-row-id="41"] [data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.80x', '0.80x'])
    oldList.complete()
    await flushPromises()
    expect.soft(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.80x', '0.80x'])
    expect.soft(oldList.config().signal?.aborted).toBe(true)
  })

  it('aborts and discards an in-flight automatic response when switched off, then resumes cleanly', async () => {
    const view = await mountView()
    rows = rows.map(row => ({ ...row, upstream_channel_binding: binding(row.id, 2, 'late off') }))
    const pending = holdNext('/admin/accounts', pageData())
    await tick()
    await toggleAutoRefresh(view)
    expect.soft(pending.config().signal?.aborted).toBe(true)
    pending.complete()
    await flushPromises()
    expect.soft(view.findAll('[data-testid="account-current-upstream"]')).toHaveLength(0)
    await tick()
    expect(requests.filter(request => request.url === '/admin/accounts')).toHaveLength(2)
    await toggleAutoRefresh(view)
    await tick()
    expect(view.findAll('[data-testid="account-current-upstream"]').map(link => link.text())).toEqual(['late off · fixture-2', 'late off · fixture-2'])
  })

  it('aborts an automatic list on teardown and prevents late state writes and follow-up stats', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['usage', 'proxy', 'notes', 'scheduler_score', 'rate_multiplier']))
    const view = await mountView()
    const table = view.getComponent(DataTable)
    const initialRows = table.props('data')
    rows = rows.map(row => ({ ...row, upstream_channel_binding: binding(row.id, 2, 'late teardown') }))
    const pending = holdNext('/admin/accounts', pageData())
    await tick()
    const statsBefore = requests.filter(request => request.url === '/admin/accounts/today-stats/batch').length
    view.unmount()
    wrapper = undefined
    expect.soft(pending.config().signal?.aborted).toBe(true)
    pending.complete()
    await flushPromises()
    expect.soft(table.props('data')).toEqual(initialRows)
    expect.soft(requests.filter(request => request.url === '/admin/accounts/today-stats/batch')).toHaveLength(statsBefore)
    const count = requests.length
    await vi.advanceTimersByTimeAsync(300_000)
    await flushPromises()
    expect(requests).toHaveLength(count)
  })

  it('ignores an older compact read after a newer automatic list response', async () => {
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.5) } }))
    const view = await mountView()
    overrides.set('/admin/accounts/41/upstream-billing-probe', async config => reply(config, { account_id: 41, snapshot: snapshot(0.6) }))
    const compact = holdNext('/admin/accounts/upstream-billing-rates', {
      items: rows.map(row => ({ account_id: row.id, cost_multiplier: 1, snapshot: snapshot(0.6) })), total, page: 1, page_size: 20
    })
    await view.get('[data-row-id="41"] [data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.9) } }))
    await vi.advanceTimersByTimeAsync(21_000)
    await flushPromises()
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.90x', '0.90x'])
    expect(compact.config().signal?.aborted).toBe(true)
    compact.complete()
    await flushPromises()
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.90x', '0.90x'])
  })

  it('keeps newer pending request ownership when the canceled request finally completes', async () => {
    const view = await mountView()
    const first = holdNext('/admin/accounts', pageData())
    await tick()
    await toggleAutoRefresh(view)
    await toggleAutoRefresh(view)
    const second = holdNext('/admin/accounts', pageData())
    await tick()
    first.complete()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(18_000)
    await flushPromises()
    expect(requests.filter(request => request.url === '/admin/accounts')).toHaveLength(3)
    expect(second.config().signal?.aborted).toBe(false)
    second.complete()
    await flushPromises()
  })

  it('retains lastgood on failed refresh, retries without overlap and handles 304', async () => {
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.5) } }))
    const view = await mountView()
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    overrides.set('/admin/accounts', async () => { throw new Error('fictional network failure') })
    await tick()
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.50x', '0.50x'])
    expect(error).toHaveBeenCalled()
    overrides.delete('/admin/accounts')
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.7) } }))
    await tick()
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.70x', '0.70x'])
    overrides.set('/admin/accounts', async config => reply(config, null, 304))
    await tick()
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.70x', '0.70x'])
  })

  it('does not request automatic lists while disabled initially', async () => {
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: false, interval_seconds: 5 }))
    await mountView()
    await vi.advanceTimersByTimeAsync(300_000)
    await flushPromises()
    expect(requests.filter(request => request.url === '/admin/accounts')).toHaveLength(1)
    expect(requests.some(request => request.url === '/admin/accounts/upstream-billing-rates')).toBe(false)
  })

  it('preserves filters, capacity sort, page size and the new page when an old page returns late', async () => {
    localStorage.setItem('account-filter-values', JSON.stringify({ platform: 'openai', status: 'active' }))
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'capacity', order: 'desc' }))
    total = 60
    const view = await mountView()
    const pending = holdNext('/admin/accounts', pageData())
    await tick()
    rows = [account(61), account(62)]
    await view.get('button[aria-label="' + i18n.global.t('pagination.next') + '"]').trigger('click')
    await flushPromises()
    expect(view.findAll('[data-row-id]').map(row => row.attributes('data-row-id'))).toEqual(['61', '62'])
    pending.complete()
    await flushPromises()
    expect(view.findAll('[data-row-id]').map(row => row.attributes('data-row-id'))).toEqual(['61', '62'])
    await tick()
    const last = requests.filter(request => request.url === '/admin/accounts').at(-1)!
    expect(last.params).toMatchObject({ page: 2, page_size: 20, platform: 'openai', status: 'active', sort_by: 'current_concurrency', sort_order: 'desc', lite: '1' })
    expect(last.headers.get('If-None-Match')).toBeUndefined()
  })

  it('updates homepage URL and clears known URL and provider when absent', async () => {
    rows = rows.map(row => ({ ...row, credentials: { base_url: 'https://fixture-a.invalid/v1' } }))
    const view = await mountView(true)
    expect(view.findAll('a[href="https://fixture-a.invalid"]').length).toBe(2)
    rows = rows.map(row => ({ ...row, credentials: { base_url: 'https://fixture-b.invalid/v1' }, extra: { upstream_billing_provider: 'new_api' } }))
    await tick()
    expect(view.findAll('a[href="https://fixture-a.invalid"]')).toHaveLength(0)
    expect(view.findAll('a[href="https://fixture-b.invalid"]')).toHaveLength(2)
    expect(view.findAll('[data-testid="upstream-billing-configure"]')).toHaveLength(2)
    rows = rows.map(row => ({ ...row, credentials: {}, extra: {} }))
    await tick()
    expect(view.findAll('a[href="https://fixture-b.invalid"]')).toHaveLength(0)
    expect(view.findAll('[data-testid="upstream-billing-configure"]')).toHaveLength(0)
  })

  it('updates probe and rate-sync switches in real tooltip and indicator DOM', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['usage', 'today_stats', 'proxy', 'notes', 'scheduler_score']))
    const view = await mountView()
    const tooltip = view.findAllComponents(HelpTooltip).find(component => component.attributes('data-testid') === 'upstream-billing-details')!
    await tooltip.get('div').trigger('mouseenter')
    const probeState = () => [...document.querySelectorAll('[data-testid="upstream-billing-probe-state"]')].map(node => node.textContent)
    expect(probeState()).toHaveLength(2)
    expect(probeState().every(text => text?.includes(i18n.global.t('admin.accounts.upstreamBilling.disabled')))).toBe(true)
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe_enabled: true, upstream_billing_rate_sync_enabled: true } }))
    await tick()
    expect(probeState().every(text => text?.includes(i18n.global.t('admin.accounts.upstreamBilling.enabled')))).toBe(true)
    expect(view.findAll('[data-testid="account-rate-sync-indicator"]')).toHaveLength(2)
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe_enabled: false, upstream_billing_rate_sync_enabled: false } }))
    await tick()
    expect(probeState().every(text => text?.includes(i18n.global.t('admin.accounts.upstreamBilling.disabled')))).toBe(true)
    expect(view.findAll('[data-testid="account-rate-sync-indicator"]')).toHaveLength(0)
  })

  it('preserves selection and expanded group details while the declaration changes', async () => {
    const multi = (rate: number): UpstreamBillingProbeSnapshot => ({ ...snapshot(rate), data: {
      ...snapshot(rate).data!, schema_version: 2, billing_mode: 'multi_group',
      groups: [{ ...snapshot(rate).data!, group_id: 1, group_name: 'Fictional group', platform: 'openai', status: 'active' }]
    } })
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: multi(0.5) } }))
    const view = await mountView()
    const checkbox = view.get('[data-row-id="41"] input[type="checkbox"]')
    await checkbox.setValue(true)
    const details = view.get('[data-row-id="41"] details').element as HTMLDetailsElement
    details.open = true
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: multi(0.8) } }))
    await tick()
    expect((view.get('[data-row-id="41"] input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)
    expect(view.get('[data-row-id="41"] details').element).toBe(details)
    expect(details.open).toBe(true)
    expect(view.findAll('[data-testid="upstream-billing-rate"]').map(cell => cell.text())).toEqual(['0.80x', '0.80x'])
  })

  it('preserves an open real edit draft when an automatic list was already in flight', async () => {
    const view = await mountView(false, true)
    rows = rows.map(row => ({ ...row, extra: { upstream_billing_probe: snapshot(0.8) } }))
    const pending = holdNext('/admin/accounts', pageData())
    await tick()
    overrides.set('/admin/accounts/41', async config => reply(config, account(41)))
    await view.get('[data-row-id="41"]').findAll('button').find(button => button.text() === i18n.global.t('common.edit'))!.trigger('click')
    await flushPromises()
    const input = document.querySelector<HTMLInputElement>('[data-tour="edit-account-form-name"]')!
    expect(input).not.toBeNull()
    input.value = 'Unsaved fictional edit'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    pending.complete()
    await flushPromises()
    expect.soft(input.value).toBe('Unsaved fictional edit')
    expect.soft(pending.config().signal?.aborted).toBe(true)
    const before = requests.filter(request => request.url === '/admin/accounts').length
    await vi.advanceTimersByTimeAsync(18_000)
    await flushPromises()
    expect(requests.filter(request => request.url === '/admin/accounts')).toHaveLength(before)
  })
})
