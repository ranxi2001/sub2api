import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { AxiosHeaders, type AxiosAdapter, type InternalAxiosRequestConfig } from 'axios'
import AccountsView from '../AccountsView.vue'
import { apiClient } from '@/api/client'
import { i18n, loadLocaleMessages } from '@/i18n'
import type { AccountListItem, UpstreamBillingProbeSnapshot } from '@/types'

// Only replace the backend transport and unrelated navigation/dialogs. The
// list API, stores, countdown, merge, DataTable and declaration cell are real.
const originalAdapter = apiClient.defaults.adapter
let wrapper: VueWrapper | undefined
let rows: AccountListItem[]
let requests: InternalAxiosRequestConfig[]
let listRevision: number

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
  rate_multiplier: 1, group_ids: [], credentials: {}, extra: {},
  created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z'
} as AccountListItem)

const binding = (id: number, channelID: number, name: string) => ({
  account_id: id, account_name: `Fixture account ${id}`, deleted: false,
  channel_id: channelID, revision: channelID,
  channel: { id: channelID, name, code: `fixture-${channelID}`, remark: '', revision: 1, members: 2 }
})

const backend: AxiosAdapter = async config => {
  requests.push(config)
  let data: unknown
  switch (config.url) {
    case '/admin/accounts':
      data = { items: structuredClone(rows), total: rows.length, page: 1, page_size: 20, pages: 1 }
      break
    case '/admin/accounts/management-capabilities': data = { concurrency_upgrade_enabled: false }; break
    case '/admin/accounts/upstream-billing-probe/settings': data = { enabled: true }; break
    case '/admin/proxies/all':
    case '/admin/groups/all': data = []; break
    case '/admin/accounts/performance': data = {}; break
    case '/admin/accounts/today-stats/batch': data = { stats: {} }; break
    default: throw new Error(`Unmocked fixture backend: ${config.method} ${config.url}`)
  }
  return {
    config, status: 200, statusText: 'OK',
    headers: new AxiosHeaders({ etag: `"fixture-${listRevision}"` }),
    data: { code: 0, data }
  }
}

async function mountView() {
  wrapper = mount(AccountsView, {
    attachTo: document.body,
    global: {
      plugins: [createPinia(), i18n],
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        CreateAccountModal: true, EditAccountModal: true, BulkEditAccountModal: true,
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
})
