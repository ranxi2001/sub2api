import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import AccountsView from '../AccountsView.vue'
import UpstreamBillingRateCell from '@/components/account/UpstreamBillingRateCell.vue'
import AccountActionMenu from '@/components/admin/account/AccountActionMenu.vue'

const {
  listAccounts,
  listWithEtag,
  getById,
  getPerformance,
  getBatchTodayStats,
  getManagementCapabilities,
  getUpstreamBillingProbeSettings,
  getUpstreamBillingRatesWithEtag,
  probeUpstreamBilling,
  getAllProxies,
  getAllGroups,
  refreshCredentials,
  showError,
  showWarning
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getById: vi.fn(),
  getPerformance: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getManagementCapabilities: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getUpstreamBillingRatesWithEtag: vi.fn(),
  probeUpstreamBilling: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  refreshCredentials: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities,
      list: listAccounts,
      getById,
      getPerformance,
      listWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings,
      getUpstreamBillingRatesWithEtag,
      probeUpstreamBilling,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
      refreshCredentials
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showWarning, showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] }, loading: Boolean, columns: { type: Array, default: () => [] } },
  emits: ['sort'],
  template: `
    <div>
      <template v-if="!loading"><div v-for="row in data" :key="row.id" :data-account-name="row.name">
        <slot name="cell-groups" :row="row" />
        <slot name="cell-capacity" :row="row" />
        <slot name="cell-performance" :row="row" />
        <slot name="cell-upstream_billing_rate" :row="row" />
        <slot name="cell-actions" :row="row" />
      </div></template>
    </div>
  `
})

const AccountGroupsCellStub = defineComponent({
  props: { groups: { type: Array, default: () => [] } },
  template: '<span data-test="account-groups">{{ groups.map(group => group.name).join(",") }}</span>'
})

const EditAccountModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="edit-account">{{ show ? account?.name : "" }}</div>'
})

const AccountTestModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="test-account">{{ show ? account?.name : "" }}</div>'
})

const AccountStatsModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="stats-account">{{ show ? account?.name : "" }}</div>'
})

function mountView(stubActionMenu = true) {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: { template: `<div><button data-test="manual-refresh" @click="$emit('refresh')">refresh</button><slot name="after" /></div>` },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: stubActionMenu,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: AccountTestModalStub,
        AccountStatsModal: AccountStatsModalStub,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: EditAccountModalStub,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: false,
        AccountPerformanceCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: AccountGroupsCellStub,
        AccountUsageCell: true,
        UpstreamBillingRateCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: stubActionMenu
      }
    }
  })
}

const listRow = {
  id: 42,
  name: 'compact row',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 2,
  priority: 1,
  group_ids: [7],
  extra: { auto_config_concurrency: { concurrency: 2, successes: 1, required: 20, maximum: 100, step: 1 } },
  credentials: {}
}

const fullAccount = {
  ...listRow,
  groups: [{ id: 7, name: 'codex', platform: 'openai' }],
  account_groups: [{ account_id: 42, group_id: 7 }],
  credentials: { api_key: 'redacted' },
  extra: { detail_only: true }
}

describe('admin AccountsView lite account list', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'compact-etag', data: null })
    getById.mockReset().mockResolvedValue(fullAccount)
    getPerformance.mockReset().mockResolvedValue({})
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getManagementCapabilities.mockReset().mockResolvedValue({ concurrency_upgrade_enabled: false })
    getUpstreamBillingProbeSettings.mockReset().mockResolvedValue({ enabled: true })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([{ id: 7, name: 'codex', platform: 'openai' }])
    getUpstreamBillingRatesWithEtag.mockReset().mockResolvedValue({ notModified: true, data: null })
    probeUpstreamBilling.mockReset()
    refreshCredentials.mockReset()
    showError.mockReset()
    showWarning.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('refreshes the saved cost after probing even with unchanged snapshot and default sorting', async () => {
    const snapshot = { status: 'ok', data: { effective_rate_multiplier: 0.14 } }
    const row = { ...listRow, type: 'apikey', extra: { cost_multiplier: 0.1, upstream_billing_probe: snapshot } }
    listAccounts.mockResolvedValue({ items: [row], total: 1, page: 1, page_size: 20, pages: 1 })
    probeUpstreamBilling.mockResolvedValue({ account_id: row.id, snapshot })
    getUpstreamBillingRatesWithEtag.mockResolvedValue({ notModified: false, data: {
      items: [{ account_id: row.id, snapshot, cost_multiplier: 0.14 }], total: 1, page: 1, page_size: 20
    } })
    const wrapper = mountView()
    await flushPromises()
    wrapper.findComponent(UpstreamBillingRateCell).vm.$emit('probe')
    await flushPromises()
    expect(probeUpstreamBilling).toHaveBeenCalledWith(row.id)
    expect(getUpstreamBillingRatesWithEtag).toHaveBeenCalled()
    expect(wrapper.findComponent(UpstreamBillingRateCell).props('account').extra?.cost_multiplier).toBe(0.14)
    wrapper.unmount()
  })

  it('refreshes performance evidence every 30 seconds independently of unchanged list ETags', async () => {
    vi.useFakeTimers()
    const wrapper = mountView()
    await flushPromises()
    expect(getPerformance).toHaveBeenCalledWith([42], expect.any(AbortSignal))
    getPerformance.mockClear()
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(getPerformance).toHaveBeenCalledTimes(1)
    expect(getPerformance).toHaveBeenCalledWith([42], expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('keeps lite=1 on the initial list request', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledWith(
      1,
      20,
      expect.objectContaining({ lite: '1' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    wrapper.unmount()
  })

  it('sorts capacity by current concurrency and resets the page while retaining filters', async () => {
    localStorage.setItem('account-filter-values', JSON.stringify({ platform: 'openai', status: 'active' }))
    listAccounts.mockResolvedValue({ items: [listRow], total: 60, page: 1, page_size: 20, pages: 3 })
    const wrapper = mountView()
    await flushPromises()
    const table = wrapper.getComponent(DataTableStub)
    expect(table.props('columns')).toContainEqual(expect.objectContaining({ key: 'capacity', sortable: true }))

    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()
    expect(listAccounts.mock.lastCall?.[0]).toBe(2)
    table.vm.$emit('sort', 'capacity', 'desc')
    await flushPromises()
    expect(listAccounts).toHaveBeenLastCalledWith(1, 20, expect.objectContaining({
      sort_by: 'current_concurrency', sort_order: 'desc', platform: 'openai', status: 'active', lite: '1'
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    table.vm.$emit('sort', 'capacity', 'asc')
    await flushPromises()
    expect(listAccounts.mock.lastCall?.[2]).toMatchObject({ sort_by: 'current_concurrency', sort_order: 'asc' })
    wrapper.unmount()
  })

  it('refreshes concurrency counts together with order after a billing probe', async () => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'capacity', order: 'desc' }))
    const leader = { ...listRow, id: 99, current_concurrency: 9, concurrency: 100 }
    const follower = { ...listRow, current_concurrency: 2 }
    listAccounts.mockResolvedValue({ items: [leader, follower], total: 2, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountView()
    await flushPromises()
    const refreshed = [{ ...follower, current_concurrency: 10 }, { ...leader, current_concurrency: 1 }]
    listAccounts.mockResolvedValue({ items: refreshed, total: 2, page: 1, page_size: 20, pages: 1 })
    getUpstreamBillingRatesWithEtag.mockResolvedValue({ notModified: false, data: {
      items: [{ account_id: 42, snapshot: null }, { account_id: 99, snapshot: null }],
      total: 2, page: 1, page_size: 20
    } })
    probeUpstreamBilling.mockResolvedValue({ account_id: 42, snapshot: { status: 'ok', data: { effective_rate_multiplier: 0.14 } } })
    wrapper.findAllComponents(UpstreamBillingRateCell)[1].vm.$emit('probe')
    await flushPromises()
    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(wrapper.getComponent(DataTableStub).props('data')).toEqual(refreshed)
    wrapper.unmount()
  })

  it('restores capacity sorting and uses server order on automatic refresh across page boundaries', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'capacity', order: 'desc' }))
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const refreshed = { ...listRow, id: 99, name: 'refreshed leader', current_concurrency: 9, concurrency: 100 }
    listWithEtag.mockResolvedValue({ notModified: false, etag: 'fresh-concurrency', data: {
      items: [refreshed], total: 1, page: 1, page_size: 20, pages: 1
    } })
    const wrapper = mountView()
    await flushPromises()
    expect(listAccounts.mock.lastCall?.[2]).toMatchObject({ sort_by: 'current_concurrency', sort_order: 'desc' })
    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()
    expect(listWithEtag).toHaveBeenCalledWith(1, 20, expect.objectContaining({
      sort_by: 'current_concurrency', sort_order: 'desc', lite: '1'
    }), expect.objectContaining({ etag: null }))
    expect(wrapper.getComponent(DataTableStub).props('data')).toEqual([refreshed])
    wrapper.unmount()
  })

  it('ignores an older automatic refresh after capacity sort changes', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    let completeRefresh!: (value: unknown) => void
    listWithEtag.mockImplementation(() => new Promise(resolve => { completeRefresh = resolve }))
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    const sortedRow = { ...listRow, id: 99, name: 'new sort leader' }
    listAccounts.mockResolvedValue({ items: [sortedRow], total: 1, page: 1, page_size: 20, pages: 1 })
    wrapper.getComponent(DataTableStub).vm.$emit('sort', 'capacity', 'desc')
    await flushPromises()
    completeRefresh({ notModified: false, etag: 'old-sort', data: {
      items: [listRow], total: 1, page: 1, page_size: 20, pages: 1
    } })
    await flushPromises()
    expect(wrapper.getComponent(DataTableStub).props('data')).toEqual([sortedRow])
    wrapper.unmount()
  })

  it('hides stored upgrade progress until the global switch is enabled', async () => {
    let resolveCapabilities!: (value: { concurrency_upgrade_enabled: boolean }) => void
    getManagementCapabilities.mockReturnValueOnce(new Promise(resolve => { resolveCapabilities = resolve }))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('progress').exists()).toBe(false)

    resolveCapabilities({ concurrency_upgrade_enabled: true })
    await flushPromises()
    expect(wrapper.get('progress').attributes()).toMatchObject({ value: '1', max: '20' })
    wrapper.unmount()
  })

  it.each([false, undefined])('hides historical progress when the upgrade flag is %s', async enabled => {
    getManagementCapabilities.mockResolvedValue({ concurrency_upgrade_enabled: enabled })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('progress').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('autoConfig.tier')
    expect(wrapper.text()).not.toContain('autoConfig.progress')
    wrapper.unmount()
  })

  it('hides upgrade progress after disabling even when the account ETag is unchanged', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    getManagementCapabilities.mockResolvedValue({ concurrency_upgrade_enabled: true })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('progress').exists()).toBe(true)

    getManagementCapabilities.mockResolvedValue({ concurrency_upgrade_enabled: false })
    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()
    expect(listWithEtag).toHaveBeenCalled()
    expect(wrapper.find('progress').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('autoConfig.tier')
    wrapper.unmount()
  })

  it('hides upgrade progress if the switch cannot be read', async () => {
    getManagementCapabilities.mockRejectedValue(new Error('settings unavailable'))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-account-name]').exists()).toBe(true)
    expect(wrapper.find('progress').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps existing rows mounted and busy while a manual list refresh is pending', async () => {
    const wrapper = mountView()
    await flushPromises()
    const row = wrapper.get('[data-account-name="compact row"]').element
    let finish: (value: unknown) => void = () => {}
    listAccounts.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await wrapper.get('[data-test="manual-refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(DataTableStub).props('loading')).toBe(false)
    expect(wrapper.get('[data-account-name="compact row"]').element).toBe(row)
    expect(wrapper.find('[aria-busy="true"]').attributes()).toHaveProperty('inert')
    finish({ items: [{ ...listRow, name: 'updated row' }], total: 1, page: 1, page_size: 20, pages: 1 })
    await flushPromises()
    expect(wrapper.get('[data-account-name="updated row"]').element).toBe(row)
    expect(wrapper.find('[aria-busy="true"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('still shows initial loading when no rows are available', async () => {
    let finish: (value: unknown) => void = () => {}
    listAccounts.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.getComponent(DataTableStub).props('loading')).toBe(true)
    finish({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    await flushPromises()
    wrapper.unmount()
  })

  it('maps group_ids through the group catalog for the table cell', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="account-groups"]').text()).toBe('codex')
    wrapper.unmount()
  })

  it('keeps the action menu open during internal scrolling but closes it on table scrolling', async () => {
    const wrapper = mountView(false)
    await flushPromises()

    const trigger = wrapper.findAll('button').find(button => button.text() === 'common.more')!
    await trigger.trigger('click')
    const menu = new DOMWrapper(document.body.querySelector('.action-menu-content')!)
    menu.element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(true)

    menu.get('button').element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(true)

    wrapper.getComponent(DataTableStub).element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(false)
    wrapper.unmount()
  })

  it('keeps lite=1 on automatic ETag refreshes', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()

    expect(listWithEtag).toHaveBeenCalledWith(
      1,
      20,
      expect.objectContaining({ lite: '1' }),
      expect.objectContaining({ etag: null })
    )
    wrapper.unmount()
  })

  it('loads the full account by id before opening edit, test, and stats actions', async () => {
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find(button => button.text().includes('common.edit'))
    expect(editButton).toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-test="edit-account"]').text()).toBe('compact row')

    const menu = wrapper.findComponent(AccountActionMenu)
    menu.vm.$emit('test', listRow)
    await flushPromises()
    expect(getById).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="test-account"]').text()).toBe('compact row')

    menu.vm.$emit('stats', listRow)
    await flushPromises()
    expect(getById).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[data-test="stats-account"]').text()).toBe('compact row')
    wrapper.unmount()
  })

  it('shows the warning and patches the account after a partial Antigravity refresh', async () => {
    refreshCredentials.mockResolvedValue({
      account: { ...fullAccount, name: 'refreshed account' },
      message: 'Token refreshed, but project_id is temporarily unavailable',
      warning: 'missing_project_id_temporary'
    })
    const wrapper = mountView(false)
    await flushPromises()

    wrapper.findComponent(AccountActionMenu).vm.$emit('refresh-token', listRow)
    await flushPromises()

    expect(refreshCredentials).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-account-name]').attributes('data-account-name')).toBe('refreshed account')
    expect(showWarning).toHaveBeenCalledWith('Token refreshed, but project_id is temporarily unavailable')
    wrapper.unmount()
  })

  it('shows an error and keeps the modal closed when detail loading fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    getById.mockRejectedValueOnce(new Error('detail failed'))
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find(button => button.text().includes('common.edit'))
    await editButton!.trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('detail failed')
    expect(wrapper.get('[data-test="edit-account"]').text()).toBe('')
    consoleError.mockRestore()
    wrapper.unmount()
  })
})
