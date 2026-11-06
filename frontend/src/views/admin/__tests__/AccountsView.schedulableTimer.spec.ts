import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  setSchedulable,
  bulkUpdate,
  batchDelete,
  showError,
  showSuccess,
  getProbeSettings,
  getCapabilities
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  setSchedulable: vi.fn(),
  bulkUpdate: vi.fn(),
  batchDelete: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  getProbeSettings: vi.fn(),
  getCapabilities: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getManagementCapabilities: getCapabilities,
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings: getProbeSettings,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      setSchedulable,
      bulkUpdate,
      batchDelete
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
    token: 'test-token', isAdmin: true
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

// Render selection and scheduling controls so tests exercise the actual click handlers.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <div v-for="row in data" :key="row.id" :data-test="'scheduling-' + row.id">
        <slot name="cell-select" :row="row" />
        <slot name="cell-schedulable" :row="row" />
      </div>
    </div>
  `
}

function mountView() {
  return mount(AccountsView, {
    global: {
      plugins: [createPinia()],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        HelpTooltip: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
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
        // Keep timer actions separate so scheduling tests click the real list switch.
        SchedulableTimerControl: {
          name: 'SchedulableTimerControl',
          props: ['accountId', 'accountName', 'schedulable', 'switching', 'canManageTimers', 'refreshKey'],
          emits: ['toggle', 'executed'],
          template: '<div data-test="timer-control"><button @click="$emit(\'toggle\')">dialog-immediate-action</button></div>'
        },
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })
}

const baseAccount = {
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  concurrency: 1,
  priority: 0,
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z'
}


let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  getProbeSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
  getCapabilities.mockResolvedValue({ web_search_enabled: false, account_quota_notify_enabled: false })
  localStorage.clear()
  listAccounts.mockResolvedValue({ items: [{ ...baseAccount, id: 7, name: 'timer-fixture' }], total: 1, page: 1, page_size: 20, pages: 1 })
  listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
  getBatchTodayStats.mockResolvedValue({ stats: {} })
  getAllProxies.mockResolvedValue([])
  getAllGroups.mockResolvedValue([])
  setSchedulable.mockResolvedValue({ ...baseAccount, id: 7, schedulable: false })
  bulkUpdate.mockResolvedValue({ success: 1, failed: 0, success_ids: [7], failed_ids: [] })
})
afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks() })
function bulkButton(key: string) {
  return wrapper.findComponent({ name: 'AccountBulkActionsBar' }).findAll('button')
    .find(button => button.text() === `admin.accounts.bulkActions.${key}`)!
}

async function mountWithSelection() {
  listAccounts.mockResolvedValue({
    items: [
      { ...baseAccount, id: 7, name: 'selected-on' },
      { ...baseAccount, id: 9, name: 'unselected' },
      { ...baseAccount, id: 11, name: 'selected-off', schedulable: false }
    ],
    total: 3, page: 1, page_size: 20, pages: 1
  })
  wrapper = mountView()
  await flushPromises()
  await wrapper.get('[data-test="scheduling-7"] input[type="checkbox"]').setValue(true)
  await wrapper.get('[data-test="scheduling-11"] input[type="checkbox"]').setValue(true)
}

function schedulingButton(id: number) {
  return wrapper.get(`[data-test="scheduling-${id}"] button[role="switch"]`)
}

function selectedIds() {
  return wrapper.findComponent({ name: 'AccountBulkActionsBar' }).props('selectedIds')
}

describe('account immediate scheduling switch and timer control', () => {
  it('keeps the timer control above an independent scheduling switch', async () => {
    wrapper = mountView()
    await flushPromises()
    const control = wrapper.findComponent({ name: 'SchedulableTimerControl' })
    expect(control.props()).toMatchObject({ accountId: 7, schedulable: true, canManageTimers: true, switching: false })
    const toggle = schedulingButton(7)
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.element.previousElementSibling).toBe(control.element)
  })
  it.each([true, false])('directly switches schedulable=%s through the original API and refreshes plans', async (schedulable) => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    listAccounts.mockResolvedValue({ items: [{ ...baseAccount, id: 7, name: 'timer-fixture', schedulable }], total: 1, page: 1, page_size: 20, pages: 1 })
    setSchedulable.mockResolvedValue({ ...baseAccount, id: 7, schedulable: !schedulable })
    wrapper = mountView()
    await flushPromises()
    await schedulingButton(7).trigger('click')
    await flushPromises()

    expect(confirm).not.toHaveBeenCalled()
    expect(setSchedulable).toHaveBeenCalledTimes(1)
    expect(setSchedulable).toHaveBeenCalledWith(7, !schedulable)
    expect(schedulingButton(7).attributes('title')).toBe(`admin.accounts.schedulable${schedulable ? 'Disabled' : 'Enabled'}`)
    expect(wrapper.findComponent({ name: 'SchedulableTimerControl' }).props('refreshKey')).toBe(1)
    expect(showError).not.toHaveBeenCalled()
  })

  it('keeps the loading guard and restores the switch with error feedback when the single API rejects', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const errorLog = vi.spyOn(console, 'error').mockImplementation(() => {})
    let rejectRequest!: (reason: Error) => void
    setSchedulable.mockImplementationOnce(() => new Promise((_, reject) => { rejectRequest = reject }))
    wrapper = mountView()
    await flushPromises()
    await schedulingButton(7).trigger('click')

    expect(confirm).not.toHaveBeenCalled()
    expect(setSchedulable).toHaveBeenCalledTimes(1)
    expect(setSchedulable).toHaveBeenCalledWith(7, false)
    expect(schedulingButton(7).attributes('disabled')).toBeDefined()
    await schedulingButton(7).trigger('click')
    expect(setSchedulable).toHaveBeenCalledTimes(1)
    const error = new Error('offline fixture failure')
    rejectRequest(error)
    await flushPromises()

    expect(schedulingButton(7).attributes('disabled')).toBeUndefined()
    expect(schedulingButton(7).attributes('title')).toBe('admin.accounts.schedulableEnabled')
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToToggleSchedulable')
    expect(errorLog).toHaveBeenCalledWith('Failed to toggle schedulable:', error)
    expect(wrapper.findComponent({ name: 'SchedulableTimerControl' }).props('refreshKey')).toBe(0)
  })

  it.each([7, 9])('keeps both accounts guarded until their own request finishes when account %s resolves first', async (firstId) => {
    const resolvers = new Map<number, (value: { id: number; schedulable: boolean }) => void>()
    setSchedulable.mockImplementation((id: number) => new Promise(resolve => { resolvers.set(id, resolve) }))
    listAccounts.mockResolvedValue({
      items: [
        { ...baseAccount, id: 7, name: 'first-account' },
        { ...baseAccount, id: 9, name: 'second-account' }
      ],
      total: 2, page: 1, page_size: 20, pages: 1
    })
    wrapper = mountView()
    await flushPromises()
    const timerControl = (id: number) => wrapper.get(`[data-test="scheduling-${id}"]`).findComponent({ name: 'SchedulableTimerControl' })

    await schedulingButton(7).trigger('click')
    await schedulingButton(9).trigger('click')

    expect(setSchedulable.mock.calls).toEqual([[7, false], [9, false]])
    for (const id of [7, 9]) {
      expect(schedulingButton(id).attributes('disabled')).toBeDefined()
      expect(timerControl(id).props('switching')).toBe(true)
    }

    resolvers.get(firstId)!({ id: firstId, schedulable: false })
    await flushPromises()
    const pendingId = firstId === 7 ? 9 : 7
    expect(schedulingButton(firstId).attributes('disabled')).toBeUndefined()
    expect(schedulingButton(firstId).attributes('aria-checked')).toBe('false')
    expect(timerControl(firstId).props('switching')).toBe(false)
    expect(schedulingButton(pendingId).attributes('disabled')).toBeDefined()
    expect(schedulingButton(pendingId).attributes('aria-checked')).toBe('true')
    expect(timerControl(pendingId).props('switching')).toBe(true)
    timerControl(pendingId).vm.$emit('toggle')
    await flushPromises()
    expect(setSchedulable).toHaveBeenCalledTimes(2)

    resolvers.get(pendingId)!({ id: pendingId, schedulable: false })
    await flushPromises()
    for (const id of [7, 9]) {
      expect(schedulingButton(id).attributes('disabled')).toBeUndefined()
      expect(schedulingButton(id).attributes('aria-checked')).toBe('false')
      expect(timerControl(id).props('switching')).toBe(false)
    }
    expect(timerControl(7).props('refreshKey')).toBe(2)
    expect(showError).not.toHaveBeenCalled()
  })

  it('ignores repeated actions for an account until its pending request finishes', async () => {
    let resolveRequest!: (value: { schedulable: boolean }) => void
    setSchedulable.mockImplementationOnce(() => new Promise(resolve => { resolveRequest = resolve }))
    wrapper = mountView()
    await flushPromises()
    const timerControl = wrapper.findComponent({ name: 'SchedulableTimerControl' })

    // Two events before Vue updates the disabled controls must still issue one request.
    timerControl.vm.$emit('toggle')
    timerControl.vm.$emit('toggle')
    await flushPromises()
    await schedulingButton(7).trigger('click')
    expect(setSchedulable).toHaveBeenCalledTimes(1)
    expect(setSchedulable).toHaveBeenCalledWith(7, false)

    resolveRequest({ schedulable: false })
    await flushPromises()
    expect(schedulingButton(7).attributes('disabled')).toBeUndefined()
    await schedulingButton(7).trigger('click')
    await flushPromises()
    expect(setSchedulable.mock.calls).toEqual([[7, false], [7, true]])
  })

  it.each([true, false])('directly applies batch schedulable=%s to every selected account, including unchanged values', async (schedulable) => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    bulkUpdate.mockResolvedValue({ success: 2, failed: 0, success_ids: [7, 11], failed_ids: [] })
    await mountWithSelection()
    await bulkButton(schedulable ? 'enableScheduling' : 'disableScheduling').trigger('click')
    await flushPromises()

    expect(confirm).not.toHaveBeenCalled()
    expect(bulkUpdate).toHaveBeenCalledTimes(1)
    expect(bulkUpdate).toHaveBeenCalledWith([7, 11], { schedulable })
    expect(setSchedulable).not.toHaveBeenCalled()
    for (const id of [7, 11]) {
      expect(schedulingButton(id).attributes('title')).toBe(`admin.accounts.schedulable${schedulable ? 'Enabled' : 'Disabled'}`)
    }
    expect(schedulingButton(9).attributes('title')).toBe('admin.accounts.schedulableEnabled')
    expect(selectedIds()).toEqual([])
    expect(showSuccess).toHaveBeenCalledTimes(1)
    expect(showSuccess).toHaveBeenCalledWith(`admin.accounts.bulkSchedulable${schedulable ? 'Enabled' : 'Disabled'}`)
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.findAllComponents({ name: 'SchedulableTimerControl' }).map(control => control.props('refreshKey'))).toEqual([1, 1, 1])
  })

  it('keeps failed batch targets selected and only updates successful targets on partial failure', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    bulkUpdate.mockResolvedValue({ success: 1, failed: 1, success_ids: [7], failed_ids: [11] })
    await mountWithSelection()
    await bulkButton('enableScheduling').trigger('click')
    await flushPromises()

    expect(confirm).not.toHaveBeenCalled()
    expect(bulkUpdate).toHaveBeenCalledTimes(1)
    expect(bulkUpdate).toHaveBeenCalledWith([7, 11], { schedulable: true })
    expect(schedulingButton(7).attributes('title')).toBe('admin.accounts.schedulableEnabled')
    expect(schedulingButton(11).attributes('title')).toBe('admin.accounts.schedulableDisabled')
    expect(schedulingButton(9).attributes('title')).toBe('admin.accounts.schedulableEnabled')
    expect(selectedIds()).toEqual([11])
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('admin.accounts.bulkSchedulablePartial')
    expect(showSuccess).not.toHaveBeenCalled()
  })

  it('retains batch error feedback, selection and account state when the API rejects', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const errorLog = vi.spyOn(console, 'error').mockImplementation(() => {})
    const error = new Error('offline fixture failure')
    bulkUpdate.mockRejectedValueOnce(error)
    await mountWithSelection()
    await bulkButton('disableScheduling').trigger('click')
    await flushPromises()

    expect(confirm).not.toHaveBeenCalled()
    expect(bulkUpdate).toHaveBeenCalledTimes(1)
    expect(bulkUpdate).toHaveBeenCalledWith([7, 11], { schedulable: false })
    expect(selectedIds()).toEqual([7, 11])
    expect(schedulingButton(7).attributes('title')).toBe('admin.accounts.schedulableEnabled')
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('common.error')
    expect(errorLog).toHaveBeenCalledWith('Failed to bulk toggle schedulable:', error)
    expect(wrapper.findComponent({ name: 'SchedulableTimerControl' }).props('refreshKey')).toBe(0)
  })

  it('still requires confirmation for deleting accounts', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    await mountWithSelection()
    await bulkButton('delete').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm).toHaveBeenCalledWith('admin.accounts.bulkActions.confirmDelete')
    expect(batchDelete).not.toHaveBeenCalled()
    expect(selectedIds()).toEqual([7, 11])
  })
})
