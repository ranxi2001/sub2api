import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getUpstreamBillingRatesWithEtag,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings,
  getAllProxies,
  getAllGroups,
  probeUpstreamBilling,
  probeUpstreamBillingBatch,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getUpstreamBillingRatesWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  probeUpstreamBilling: vi.fn(),
  probeUpstreamBillingBatch: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: vi.fn().mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false }),
      list: listAccounts,
      listWithEtag,
      getUpstreamBillingRatesWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      probeUpstreamBilling,
      probeUpstreamBillingBatch,
      toggleSchedulable: vi.fn()
    },
    proxies: {
      getAll: getAllProxies
    },
    groups: {
      getAll: getAllGroups
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token'
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <span v-for="column in columns" :key="column.key" data-test="column-key">{{ column.key }}</span>
      <div v-for="row in data" :key="row.id">
        <div data-test="select-row"><slot name="cell-select" :row="row" /></div>
        <slot name="cell-created_at" :value="row.created_at" :row="row" />
        <div data-test="account-rate"><slot name="cell-rate_multiplier" :row="row" /></div>
      </div>
    </div>
  `
}

const ProbeDataTableStub = {
  props: ['data', 'loading'],
  template: `
    <div>
      <div v-if="loading" data-test="table-loading">loading</div>
      <div v-for="row in loading ? [] : data" :key="row.id" :data-account-id="row.id">
        <div data-test="select-row"><slot name="cell-select" :row="row" /></div>
        <div data-test="account-rate"><slot name="cell-rate_multiplier" :row="row" /></div>
        <span data-test="account-cost">{{ row.extra?.cost_multiplier }}</span>
        <slot name="cell-upstream_billing_rate" :row="row" />
      </div>
    </div>
  `
}

const AccountBulkActionsBarStub = {
  props: ['selectedIds'],
  emits: ['edit-filtered', 'probe-upstream-billing'],
  template: `
    <div>
      <button data-test="edit-filtered" @click="$emit('edit-filtered')">edit filtered</button>
      <button data-test="probe-upstream-billing" @click="$emit('probe-upstream-billing')">probe</button>
    </div>
  `
}

const PaginationStub = {
  emits: ['update:page'],
  template: '<button data-test="next-page" @click="$emit(\'update:page\', 2)">next</button>'
}

const BulkEditAccountModalStub = {
  props: ['show', 'target', 'filteredPreview'],
  template: '<div data-test="bulk-edit-modal" :data-show="String(show)" :data-target-mode="target?.mode ?? \'\'" :data-filter-preview="String(filteredPreview)" :data-target-ids="JSON.stringify(target?.accountIds ?? [])"></div>'
}

const makeProbeAccount = (id: number, rate = 0.25) => ({
  id, name: 'account-' + id, platform: 'openai', type: 'apikey',
  status: 'active', schedulable: true, rate_multiplier: rate,
  extra: { upstream_billing_probe_enabled: true, cost_multiplier: 0.1 },
  created_at: '2026-07-13T00:00:00Z', updated_at: '2026-07-13T00:00:00Z'
})

const probeSnapshot = {
  status: 'ok', data: { effective_rate_multiplier: 0.065 },
  synced_rate_multiplier: 0.065, last_attempt_at: '2026-07-13T00:01:00Z'
}

const probePage = (ids: number[], total = ids.length, page = 1) => ({
  items: ids.map(id => makeProbeAccount(id)), total, page, page_size: 20, pages: Math.ceil(total / 20)
})

const mockRatePage = (ids: number[], total = ids.length) => {
  getUpstreamBillingRatesWithEtag.mockResolvedValue({
    notModified: false, etag: 'rates-etag',
    data: { items: ids.map(account_id => ({ account_id, snapshot: probeSnapshot, cost_multiplier: 0.065 })), total, page: 1, page_size: 20 }
  })
}

const FilterValuesStub = {
 props: ['filters'],
 emits: ['update:filters', 'change'],
 template: `<button data-test="persist-filters" @click="$emit('update:filters', { ...filters, platform: 'gemini', status: 'error' }); $emit('change')">filter</button>`
}

const mountProbeView = (stubs: { BulkEditAccountModal?: typeof BulkEditAccountModalStub; AccountTableFilters?: typeof FilterValuesStub } = {}) => mount(AccountsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      DataTable: ProbeDataTableStub, Pagination: PaginationStub,
      AccountBulkActionsBar: AccountBulkActionsBarStub, AccountTableActions: true, AccountTableFilters: true,
      AccountActionMenu: true, ConfirmDialog: true, ImportDataModal: true, ReAuthAccountModal: true,
      AccountTestModal: true, AccountStatsModal: true, ScheduledTestsPanel: true, SyncFromCrsModal: true,
      TempUnschedStatusModal: true, ErrorPassthroughRulesModal: true, TLSFingerprintProfilesModal: true,
      CreateAccountModal: true, EditAccountModal: true, BulkEditAccountModal: true,
      PlatformTypeBadge: true, AccountCapacityCell: true, AccountStatusIndicator: true,
      AccountTodayStatsCell: true, AccountGroupsCell: true, AccountUsageCell: true, Icon: true,
      ...stubs
    }
  }
})

describe('admin AccountsView bulk edit scope', () => {
  beforeEach(() => {
    localStorage.clear()

    listAccounts.mockReset()
    listWithEtag.mockReset()
    getUpstreamBillingRatesWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getUpstreamBillingProbeSettings.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    probeUpstreamBilling.mockReset()
    probeUpstreamBillingBatch.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listAccounts.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null
    })
    getUpstreamBillingRatesWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    probeUpstreamBilling.mockResolvedValue({})
    probeUpstreamBillingBatch.mockResolvedValue([])
  })

  it('restores saved platform and status in the initial account request', async () => {
    localStorage.setItem('account-filter-values', JSON.stringify({ platform: 'gemini', status: 'rate_limited' }))
    const wrapper = mountProbeView()
    await flushPromises()
    expect(listAccounts.mock.calls[0]?.[2]).toEqual(expect.objectContaining({ platform: 'gemini', status: 'rate_limited' }))
    wrapper.unmount()
  })

  it.each(['{', 'null', '[]', '42', '{"platform":"unknown","status":"removed"}'])(
    'ignores malformed or unsupported saved filters: %s', async raw => {
      localStorage.setItem('account-filter-values', raw)
      const wrapper = mountProbeView()
      await flushPromises()
      expect(listAccounts.mock.calls[0]?.[2]).toEqual(expect.objectContaining({ platform: '', status: '' }))
      wrapper.unmount()
    }
  )

  it('persists actual filter events and restores them after remount', async () => {
    const wrapper = mountProbeView({ AccountTableFilters: FilterValuesStub })
    await flushPromises()
    await wrapper.get('[data-test="persist-filters"]').trigger('click')
    await flushPromises()
    expect(JSON.parse(localStorage.getItem('account-filter-values')!)).toEqual({ platform: 'gemini', status: 'error' })
    wrapper.unmount()
    listAccounts.mockClear()
    const restored = mountProbeView()
    await flushPromises()
    expect(listAccounts.mock.calls[0]?.[2]).toEqual(expect.objectContaining({ platform: 'gemini', status: 'error' }))
    restored.unmount()
  })

  it('keeps filtering available when browser storage writes fail', async () => {
    const original = Storage.prototype.setItem
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (this: Storage, key, value) {
      if (key === 'account-filter-values') throw new Error('storage unavailable')
      return original.call(this, key, value)
    })
    const wrapper = mountProbeView({ AccountTableFilters: FilterValuesStub })
    try {
      await flushPromises()
      await wrapper.get('[data-test="persist-filters"]').trigger('click')
      await flushPromises()
      expect(wrapper.findComponent(FilterValuesStub).props('filters')).toEqual(expect.objectContaining({ platform: 'gemini', status: 'error' }))
    } finally {
      wrapper.unmount()
      spy.mockRestore()
    }
  })

  it('does not reload an unmounted filter view after its debounce expires', async () => {
    vi.useFakeTimers()
    const wrapper = mountProbeView({ AccountTableFilters: FilterValuesStub })
    try {
      await flushPromises()
      expect(listAccounts).toHaveBeenCalledTimes(1)
      await wrapper.get('[data-test="persist-filters"]').trigger('click')
      wrapper.unmount()
      await vi.advanceTimersByTimeAsync(301)
      await flushPromises()
      expect(listAccounts).toHaveBeenCalledTimes(1)
    } finally {
      wrapper.unmount()
      vi.useRealTimers()
    }
  })

  it('freezes filtered results as explicit IDs before opening bulk edit', async () => {
    listAccounts.mockResolvedValue(probePage([7, 11]))
    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
          AccountTableFilters: { template: '<div></div>' },
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountActionMenu: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-show')).toBe('true')
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-target-mode')).toBe('selected')
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-filter-preview')).toBe('true')
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-target-ids')).toBe('[7,11]')
  })

  it('passes only the explicitly checked IDs across pages to the upstream action', async () => {
    listAccounts.mockImplementation(async (page: number) =>
      page === 2 ? probePage([21, 22], 22, 2) : probePage([7, 8], 22, 1))
    const wrapper = mountProbeView({ BulkEditAccountModal: BulkEditAccountModalStub })
    await flushPromises()
    await wrapper.get('[data-account-id="7"] [data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-account-id="22"] [data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    const modal = wrapper.get('[data-test="bulk-edit-modal"]')
    expect(modal.attributes('data-filter-preview')).toBe('false')
    expect(modal.attributes('data-target-ids')).toBe('[7,22]')
    await wrapper.get('[data-account-id="21"] [data-test="select-row"] input').trigger('change')
    expect(modal.attributes('data-target-ids')).toBe('[7,22]')
    wrapper.unmount()
  })

  it('keeps checked OAuth IDs when a stale filtered-edit action fires', async () => {
    const page = probePage([7, 11, 13])
    page.items[0] = { ...page.items[0], type: 'oauth' }
    // Initial mounting uses list(); listWithEtag() only serves incremental refreshes.
    listAccounts.mockResolvedValue(page)
    const wrapper = mountProbeView({ BulkEditAccountModal: BulkEditAccountModalStub })
    await flushPromises()
    expect(wrapper.findAll('[data-account-id]').map(row => row.attributes('data-account-id'))).toEqual(['7', '11', '13'])
    await wrapper.get('[data-account-id="7"] [data-test="select-row"] input').trigger('change')
    const callsBeforeEdit = listAccounts.mock.calls.length
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-show')).toBe('true')
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-target-ids')).toBe('[7]')
    expect(listAccounts).toHaveBeenCalledTimes(callsBeforeEdit)
  })

  it('does not open bulk edit for an empty or incomplete filtered preview', async () => {
    const wrapper = mountProbeView({ BulkEditAccountModal: BulkEditAccountModalStub })
    await flushPromises()
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-show')).toBe('false')
    expect(showError).toHaveBeenCalledWith('admin.accounts.bulkEdit.noSelection')

    listAccounts.mockResolvedValue(probePage([7], 2))
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-show')).toBe('false')
    expect(showError).toHaveBeenCalledWith('admin.accounts.bulkActions.selectAllFailed')
  })

  it('renders the created_at column by default', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'test-account',
          platform: 'anthropic',
          type: 'oauth',
          status: 'active',
          schedulable: true,
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        }
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
          AccountTableFilters: { template: '<div></div>' },
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountActionMenu: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()

    const columnKeys = wrapper.findAll('[data-test="column-key"]').map(node => node.text())
    expect(columnKeys).toContain('created_at')
    const columns = wrapper.getComponent(DataTableStub).props('columns') as Array<{ key: string; label: string; sortable: boolean }>
    expect(columns.find(column => column.key === 'created_at')).toMatchObject({
      label: 'admin.accounts.columns.createdAt',
      sortable: true
    })
  })

  it('passes the loaded global probe state to every upstream billing cell', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'upstream',
          platform: 'openai',
          type: 'apikey',
          status: 'active',
          schedulable: true,
          created_at: '2026-07-13T00:00:00Z',
          updated_at: '2026-07-13T00:00:00Z'
        }
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getUpstreamBillingProbeSettings.mockResolvedValue({ enabled: false, interval_minutes: 30 })

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: {
            props: ['data'],
            template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-upstream_billing_rate" :row="row" /></div></div>'
          },
          UpstreamBillingRateCell: {
            props: ['globalProbeEnabled'],
            template: '<span data-test="upstream-billing-cell" :data-global-enabled="String(globalProbeEnabled)"></span>'
          },
          Pagination: true,
          ConfirmDialog: true,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountBulkActionsBar: true,
          AccountActionMenu: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: true,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()

    expect(getUpstreamBillingProbeSettings).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="upstream-billing-cell"]').attributes('data-global-enabled')).toBe('false')
  })

  it('submits selected account IDs from every page for backend eligibility checks', async () => {
    const account = (id: number) => ({
      id,
      name: `account-${id}`,
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    })
    listAccounts
      .mockResolvedValueOnce({ items: [account(7)], total: 2, page: 1, page_size: 1, pages: 2 })
      .mockResolvedValueOnce({ items: [account(11)], total: 2, page: 2, page_size: 1, pages: 2 })

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /><slot name="pagination" /></div>' },
          DataTable: DataTableStub,
          Pagination: PaginationStub,
          ConfirmDialog: true,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountActionMenu: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()
    await wrapper.get('[data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="probe-upstream-billing"]').trigger('click')
    await flushPromises()

    expect(probeUpstreamBillingBatch).toHaveBeenCalledWith([7, 11])
  })

  it('updates the current page locally after a batch probe', async () => {
    const account = (id: number, rateMultiplier: number) => ({
      id,
      name: `account-${id}`,
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      rate_multiplier: rateMultiplier,
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    })
    listAccounts
      .mockResolvedValueOnce({ items: [account(7, 0.25)], total: 2, page: 1, page_size: 1, pages: 2 })
      .mockResolvedValueOnce({ items: [account(11, 0.25)], total: 2, page: 2, page_size: 1, pages: 2 })
      .mockResolvedValueOnce({ items: [account(11, 0.065)], total: 2, page: 2, page_size: 1, pages: 2 })
    probeUpstreamBillingBatch.mockResolvedValue([
      {
        account_id: 11,
        snapshot: {
          status: 'ok',
          data: { effective_rate_multiplier: 0.065 },
          synced_rate_multiplier: 0.065,
          last_attempt_at: '2026-07-13T00:00:00Z',
          next_probe_at: '2026-07-13T00:30:00Z'
        }
      }
    ])

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /><slot name="pagination" /></div>' },
          DataTable: DataTableStub,
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountActionMenu: true,
          Pagination: PaginationStub,
          ConfirmDialog: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="probe-upstream-billing"]').trigger('click')
    await flushPromises()

    expect(probeUpstreamBillingBatch).toHaveBeenCalledWith([11])
    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="account-rate"]').text()).toBe('0.065x')
  })

  it('does not report a successful batch probe as failed when reconciliation is skipped', async () => {
    const account = {
      id: 7,
      name: 'account-7',
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      rate_multiplier: 0.25,
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    }
    listAccounts
      .mockResolvedValueOnce({ items: [account], total: 1, page: 1, page_size: 20, pages: 1 })
    probeUpstreamBillingBatch.mockResolvedValue([
      {
        account_id: 7,
        snapshot: {
          status: 'ok',
          data: { effective_rate_multiplier: 0.065 },
          last_attempt_at: '2026-07-13T00:00:00Z',
          next_probe_at: '2026-07-13T00:30:00Z'
        }
      }
    ])
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: DataTableStub,
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountActionMenu: true,
          Pagination: true,
          ConfirmDialog: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()
    await wrapper.get('[data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="probe-upstream-billing"]').trigger('click')
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.upstreamBilling.batchCompleted')
    consoleError.mockRestore()
  })

  it('updates the account row after a successful single-account probe', async () => {
    const account = (rateMultiplier: number) => ({
      id: 7,
      name: 'account-7',
      platform: 'openai',
      type: 'apikey',
      status: 'active',
      schedulable: true,
      rate_multiplier: rateMultiplier,
      extra: { upstream_billing_probe_enabled: true },
      created_at: '2026-07-13T00:00:00Z',
      updated_at: '2026-07-13T00:00:00Z'
    })
    listAccounts
      .mockResolvedValueOnce({ items: [account(0.25)], total: 1, page: 1, page_size: 20, pages: 1 })
      .mockResolvedValueOnce({ items: [account(0.065)], total: 1, page: 1, page_size: 20, pages: 1 })
    probeUpstreamBilling.mockResolvedValue({
      account_id: 7,
      snapshot: {
        status: 'ok',
        data: { effective_rate_multiplier: 0.065 },
        synced_rate_multiplier: 0.065,
        last_attempt_at: '2026-07-13T00:00:00Z',
        next_probe_at: '2026-07-13T00:30:00Z'
      }
    })

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: ProbeDataTableStub,
          AccountBulkActionsBar: true,
          AccountTableActions: true,
          AccountTableFilters: true,
          AccountActionMenu: true,
          Pagination: true,
          ConfirmDialog: true,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: true,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()

    expect(probeUpstreamBilling).toHaveBeenCalledWith(7)
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="account-rate"]').text()).toBe('0.065x')
  })

  it.each(['last_used_at', 'name', 'status'])('refreshes saved cost under %s sorting without reloading the table', async (sortBy) => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: sortBy, order: 'desc' }))
    listAccounts.mockResolvedValue(probePage([7, 11]))
    probeUpstreamBilling.mockResolvedValue({ account_id: 7, snapshot: probeSnapshot })
    mockRatePage([7, 11])
    const wrapper = mountProbeView()
    await flushPromises()
    const row = wrapper.get('[data-account-id="11"]').element
    const statsCalls = getBatchTodayStats.mock.calls.length

    await wrapper.get('[data-account-id="7"] [data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()

    expect(getUpstreamBillingRatesWithEtag).toHaveBeenCalledTimes(1)
    expect(getUpstreamBillingRatesWithEtag).toHaveBeenCalledWith(
      1, 20, expect.objectContaining({ sort_by: sortBy, sort_order: 'desc' }),
      expect.objectContaining({ etag: null, signal: expect.any(AbortSignal) })
    )
    expect(wrapper.get('[data-account-id="7"] [data-test="account-cost"]').text()).toBe('0.065')
    expect(wrapper.findAll('[data-account-id]').map(item => item.attributes('data-account-id'))).toEqual(['7', '11'])
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(getBatchTodayStats).toHaveBeenCalledTimes(statsCalls)
    expect(wrapper.get('[data-account-id="11"]').element).toBe(row)
    expect(wrapper.get('[data-account-id="7"] [data-test="account-rate"]').text()).toBe('0.065x')
    wrapper.unmount()
  })

  it.each(['upstream_billing_rate', 'rate_multiplier'])('reorders the existing %s page without remounting rows', async (sortBy) => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: sortBy, order: 'asc' }))
    listAccounts.mockResolvedValue(probePage([7, 11]))
    probeUpstreamBilling.mockResolvedValue({ account_id: 7, snapshot: probeSnapshot })
    mockRatePage([11, 7])
    const wrapper = mountProbeView()
    await flushPromises()
    const row = wrapper.get('[data-account-id="11"]').element

    await wrapper.get('[data-account-id="7"] [data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-account-id]').map(item => item.attributes('data-account-id'))).toEqual(['11', '7'])
    expect(wrapper.get('[data-account-id="11"]').element).toBe(row)
    wrapper.unmount()
  })

  it('keeps the table mounted while fetching rows that cross a rate-sorted page boundary', async () => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'upstream_billing_rate', order: 'asc' }))
    listAccounts.mockResolvedValueOnce(probePage([7, 11], 40))
    let finishPage!: (value: ReturnType<typeof probePage>) => void
    listAccounts.mockImplementationOnce(() => new Promise(resolve => { finishPage = resolve }))
    probeUpstreamBilling.mockResolvedValue({ account_id: 7, snapshot: probeSnapshot })
    mockRatePage([11, 13], 41)
    const wrapper = mountProbeView()
    await flushPromises()
    const row = wrapper.get('[data-account-id="11"]').element
    const statsCalls = getBatchTodayStats.mock.calls.length

    await wrapper.get('[data-account-id="7"] [data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="table-loading"]').exists()).toBe(false)
    expect(wrapper.get('[data-account-id="11"]').element).toBe(row)
    expect(listAccounts).toHaveBeenLastCalledWith(1, 20, expect.objectContaining({ lite: '1', sort_by: 'upstream_billing_rate' }), expect.objectContaining({ signal: expect.any(AbortSignal) }))

    finishPage(probePage([11, 13], 41))
    await flushPromises()
    expect(wrapper.findAll('[data-account-id]').map(item => item.attributes('data-account-id'))).toEqual(['11', '13'])
    expect(wrapper.get('[data-account-id="11"]').element).toBe(row)
    expect(getBatchTodayStats).toHaveBeenCalledTimes(statsCalls)
    wrapper.unmount()
  })

  it('ignores a canceled rate-page response even after navigating back to the same page', async () => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'upstream_billing_rate', order: 'asc' }))
    listAccounts.mockResolvedValueOnce(probePage([7, 11], 40))
    let finishPage!: (value: ReturnType<typeof probePage>) => void
    listAccounts.mockImplementationOnce(() => new Promise(resolve => { finishPage = resolve }))
    listAccounts.mockResolvedValueOnce(probePage([21, 22], 40, 2))
    listAccounts.mockResolvedValueOnce(probePage([17, 19], 40))
    probeUpstreamBilling.mockResolvedValue({ account_id: 7, snapshot: probeSnapshot })
    mockRatePage([11, 13], 40)
    const wrapper = mountProbeView()
    await flushPromises()
    await wrapper.get('[data-account-id="7"] [data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()
    const signal = listAccounts.mock.calls[1][3].signal as AbortSignal
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(wrapper.findAll('[data-account-id]').map(item => item.attributes('data-account-id'))).toEqual(['21', '22'])
    wrapper.findComponent(PaginationStub).vm.$emit('update:page', 1)
    await flushPromises()
    finishPage(probePage([11, 13], 40))
    await flushPromises()
    expect(wrapper.findAll('[data-account-id]').map(item => item.attributes('data-account-id'))).toEqual(['17', '19'])
    wrapper.unmount()
  })

  it('retains the successful probe and mounted rows when the silent page read fails', async () => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'upstream_billing_rate', order: 'asc' }))
    listAccounts.mockResolvedValueOnce(probePage([7, 11]))
    const error = new Error('page read failed')
    listAccounts.mockRejectedValueOnce(error)
    probeUpstreamBilling.mockResolvedValue({ account_id: 7, snapshot: probeSnapshot })
    mockRatePage([11, 13])
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountProbeView()
    try {
      await flushPromises()
      const row = wrapper.get('[data-account-id="11"]').element
      await wrapper.get('[data-account-id="7"] [data-testid="upstream-billing-probe"]').trigger('click')
      await flushPromises()
      expect(wrapper.get('[data-account-id="11"]').element).toBe(row)
      expect(wrapper.get('[data-account-id="7"] [data-test="account-rate"]').text()).toBe('0.065x')
      expect(wrapper.find('[data-test="table-loading"]').exists()).toBe(false)
      expect(wrapper.get('[data-account-id="7"] [data-testid="upstream-billing-probe"]').attributes('disabled')).toBeUndefined()
      expect(consoleError).toHaveBeenCalledWith('Failed to refresh upstream billing rates:', error)
    } finally {
      wrapper.unmount()
      consoleError.mockRestore()
    }
  })

  it('refreshes saved cost after a batch probe without reloading recent-use rows', async () => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'last_used_at', order: 'desc' }))
    listAccounts.mockResolvedValue(probePage([7, 11]))
    probeUpstreamBillingBatch.mockResolvedValue([{ account_id: 7, snapshot: probeSnapshot }])
    mockRatePage([7, 11])
    const wrapper = mountProbeView()
    await flushPromises()
    const row = wrapper.get('[data-account-id="11"]').element
    const statsCalls = getBatchTodayStats.mock.calls.length
    await wrapper.get('[data-account-id="7"] [data-test="select-row"] input').trigger('change')
    await wrapper.get('[data-test="probe-upstream-billing"]').trigger('click')
    await flushPromises()
    expect(probeUpstreamBillingBatch).toHaveBeenCalledWith([7])
    expect(getUpstreamBillingRatesWithEtag).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-account-id="7"] [data-test="account-cost"]').text()).toBe('0.065')
    expect(wrapper.get('[data-account-id="11"]').element).toBe(row)
    expect(wrapper.findAll('[data-account-id]').map(item => item.attributes('data-account-id'))).toEqual(['7', '11'])
    expect(getBatchTodayStats).toHaveBeenCalledTimes(statsCalls)
    expect(listAccounts).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
