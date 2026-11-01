import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'

import UsageView from '../UsageView.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import UsageStatsCards from '@/components/admin/usage/UsageStatsCards.vue'
import UsageCleanupDialog from '@/components/admin/usage/UsageCleanupDialog.vue'

const { list, exportList, getStats, getSnapshotV2, getById, getModelStats, listErrorLogs, routeQuery, aoaToSheet, sheetAddAoa, saveAs, xlsxWrite, listCleanupTasks, createCleanupTask, showWarning } = vi.hoisted(() => {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
  })

  return {
    listCleanupTasks: vi.fn(),
    createCleanupTask: vi.fn(),
    showWarning: vi.fn(),
    list: vi.fn(),
		exportList: vi.fn(),
    getStats: vi.fn(),
    getSnapshotV2: vi.fn(),
    getById: vi.fn(),
    getModelStats: vi.fn(),
    listErrorLogs: vi.fn(),
    routeQuery: {} as Record<string, string>,
		aoaToSheet: vi.fn(() => ({})),
		sheetAddAoa: vi.fn(),
		saveAs: vi.fn(),
		xlsxWrite: vi.fn(() => new Uint8Array([1, 2, 3])),
  }
})

const own = vi.hoisted(() => ({
  list: vi.fn(), getStats: vi.fn(), getModelStats: vi.fn(), getSnapshotV2: vi.fn(), listErrors: vi.fn(),
  settings: { allow_user_view_error_requests: true },
}))
vi.mock('@/api/observerUsage', () => ({ observerUsageAPI: own }))

const messages: Record<string, string> = {
  'admin.dashboard.timeRange': 'Time Range',
  'admin.dashboard.day': 'Day',
  'admin.dashboard.hour': 'Hour',
  'admin.usage.failedToLoadUser': 'Failed to load user',
	'admin.usage.requestId': 'Request ID',
	'admin.usage.upstreamRequestId': 'Upstream ID',
	'latencyObservability.reasoning_first_ms': 'First reasoning event',
	'latencyObservability.visible_text_first_ms': 'First visible text',
	'latencyObservability.response_byte_first_ms': 'First response byte',
	'usage.requestedModel': 'Requested model',
	'usage.sentUpstreamModel': 'Sent upstream model',
	'usage.upstreamResponseModel': 'Upstream response model',
	'usage.upstreamModelMismatch': 'Upstream model mismatch',
	'common.yes': 'Yes',
	'common.no': 'No',
}

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      list,
      getStats,
    },
    dashboard: {
      getSnapshotV2,
      getModelStats,
    },
    users: {
      getById,
    },
  },
}))

vi.mock('@/api/admin/usage', () => ({
  adminUsageAPI: {
    listCleanupTasks, createCleanupTask,
		list: exportList,
  },
}))

vi.mock('file-saver', () => ({ saveAs }))

vi.mock('xlsx', () => ({
	utils: {
		aoa_to_sheet: aoaToSheet,
		sheet_add_aoa: sheetAddAoa,
		book_new: vi.fn(() => ({})),
		book_append_sheet: vi.fn(),
	},
	write: xlsxWrite,
}))

vi.mock('@/api/admin/ops', () => ({
  listErrorLogs,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: own.settings,
    showError: vi.fn(),
    showWarning,
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/utils/format', () => ({
  formatReasoningEffort: (value: string | null | undefined) => value ?? '-',
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
      locale: ref('en'),
    }),
  }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({
    query: routeQuery
  })
}))

const AppLayoutStub = { template: '<div><slot /></div>' }
const UsageFiltersStub = defineComponent({
  emits: ['cleanup', 'update:modelValue', 'change', 'export'],
  setup(_, { expose }) {
    const userKeyword = ref('')
    let userSearchRevision = 0
    const setUserKeyword = (email: string) => {
      userSearchRevision += 1
      userKeyword.value = email
    }
    expose({
      getUserSearchRevision: () => userSearchRevision,
      setUserKeyword,
      simulateUserInput: setUserKeyword,
    })
    return { userKeyword }
  },
  template: '<div><button data-test="cleanup-open" @click="$emit(\'cleanup\')">cleanup</button><span data-test="user-filter-label">{{ userKeyword }}</span><slot name="after-reset" /></div>',
})
const UsageTableStub = {
  props: ['columns'],
  emits: ['userClick'],
  template: '<div data-test="usage-table"><button class="user-click" @click="$emit(\'userClick\', 2)">user</button></div>',
}
const UsageConnectionsStub = { props: ['filters', 'refreshKey'], template: '<div data-test="connection-tab" />' }
const LatencyObservabilityPanelStub = {
  props: {
    startDate: String,
    endDate: String,
    model: String,
    userId: Number,
    apiKeyId: Number,
    accountId: Number,
    groupId: Number,
    refreshToken: Number,
    flat: Boolean,
    hideTitle: Boolean,
  },
  template: '<div data-test="latency-tab" />',
}
const UserTokenRankingStub = {
  emits: ['select-user'],
  template: '<div data-test="ranking"><button class="pick-user" @click="$emit(\'select-user\', 5, \'rank@test.com\')">pick</button></div>',
}
const ModelDistributionChartStub = {
  props: ['metric'],
  emits: ['update:metric'],
  template: `
    <div data-test="model-chart">
      <span class="metric">{{ metric }}</span>
      <button class="switch-metric" @click="$emit('update:metric', 'actual_cost')">switch</button>
    </div>
  `,
}
const GroupDistributionChartStub = {
  props: ['metric'],
  emits: ['update:metric'],
  template: `
    <div data-test="group-chart">
      <span class="metric">{{ metric }}</span>
      <button class="switch-metric" @click="$emit('update:metric', 'actual_cost')">switch</button>
    </div>
  `,
}

const mountRouteFilteredUsageView = (realDatePicker = false, realCleanupDialog = false) => mount(UsageView, {
  global: { stubs: {
    AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
    UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: !realCleanupDialog,
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    ConfirmDialog: { props: ['show'], emits: ['confirm'], template: '<button v-if="show" data-test="cleanup-confirm" @click="$emit(\'confirm\')">confirm</button>' },
    UserBalanceHistoryModal: true, Pagination: true, Select: true,
    DateRangePicker: !realDatePicker, Icon: true, TokenUsageTrend: true,
    ModelDistributionChart: true, GroupDistributionChart: true,
    EndpointDistributionChart: true, UserTokenRanking: true,
    LatencyObservabilityPanel: LatencyObservabilityPanelStub,
  } },
})

describe('admin UsageView route filters', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    Object.keys(routeQuery).forEach((key) => delete routeQuery[key])
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
    getById.mockReset()
  })

  afterEach(() => {
    Object.keys(routeQuery).forEach((key) => delete routeQuery[key])
    vi.useRealTimers()
  })

  it('mounts the latency panel only after selecting its tab and passes supported filters', async () => {
    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()
    expect(wrapper.find('[data-test="latency-tab"]').exists()).toBe(false)

    const vm = wrapper.vm as any
    vm.filters.model = 'gpt-5.6-sol'
    vm.filters.user_id = 42
    vm.filters.api_key_id = 8
    vm.filters.account_id = 7
    vm.filters.group_id = 3
    await wrapper.vm.$nextTick()

    const latencyTab = wrapper.findAll('[data-testid="usage-detail-tab"]').find(tab => tab.text() === 'latencyObservability.title')!
    await latencyTab.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="latency-tab"]').exists()).toBe(true)
    expect(wrapper.findComponent(LatencyObservabilityPanelStub).props()).toMatchObject({
      startDate: expect.any(String),
      endDate: expect.any(String),
      model: 'gpt-5.6-sol',
      userId: 42,
      apiKeyId: 8,
      accountId: 7,
      groupId: 3,
      flat: true,
      hideTitle: true,
    })
    wrapper.unmount()
  })

  it('shows the routed user while applying user_id to usage requests', async () => {
    routeQuery.user_id = '42'
    getById.mockResolvedValue({ id: 42, email: 'route-user@test.com' })

    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(42, true)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ user_id: 42 }), expect.anything())
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('route-user@test.com')
  })

  it('does not apply a stale routed user label after user_id changes', async () => {
    routeQuery.user_id = '42'
    let resolveLookup!: (user: { id: number; email: string }) => void
    getById.mockReturnValue(new Promise((resolve) => { resolveLookup = resolve }))

    const wrapper = mountRouteFilteredUsageView()
    await wrapper.vm.$nextTick()
    ;(wrapper.vm as any).filters.user_id = 84
    ;(wrapper.findComponent(UsageFiltersStub).vm as any).setUserKeyword('current-user@test.com')

    resolveLookup({ id: 42, email: 'stale-user@test.com' })
    await flushPromises()

    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('current-user@test.com')
  })

  it('does not overwrite newer user input when the routed user lookup succeeds', async () => {
    routeQuery.user_id = '42'
    let resolveLookup!: (user: { id: number; email: string }) => void
    getById.mockReturnValue(new Promise((resolve) => { resolveLookup = resolve }))

    const wrapper = mountRouteFilteredUsageView()
    await wrapper.vm.$nextTick()
    ;(wrapper.findComponent(UsageFiltersStub).vm as any).simulateUserInput('new-search@test.com')

    resolveLookup({ id: 42, email: 'route-user@test.com' })
    await flushPromises()

    expect((wrapper.vm as any).filters.user_id).toBe(42)
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('new-search@test.com')
  })

  it('does not overwrite newer user input when the routed user lookup fails', async () => {
    routeQuery.user_id = '42'
    let rejectLookup!: (error: Error) => void
    getById.mockReturnValue(new Promise((_, reject) => { rejectLookup = reject }))

    const wrapper = mountRouteFilteredUsageView()
    await wrapper.vm.$nextTick()
    ;(wrapper.findComponent(UsageFiltersStub).vm as any).simulateUserInput('new-search@test.com')

    rejectLookup(new Error('lookup failed'))
    await flushPromises()

    expect((wrapper.vm as any).filters.user_id).toBe(42)
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('new-search@test.com')
  })

  it('shows the routed user ID when its label lookup fails', async () => {
    routeQuery.user_id = '42'
    getById.mockRejectedValue(new Error('lookup failed'))

    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()

    expect(list).toHaveBeenCalledWith(expect.objectContaining({ user_id: 42 }), expect.anything())
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('42')
  })
})

describe('admin UsageView native compaction filter', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('propagates the filter to list/stats/model/snapshot requests and clears it on reset', async () => {
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()

    list.mockClear()
    getStats.mockClear()
    getModelStats.mockClear()
    getSnapshotV2.mockClear()

    ;(wrapper.vm as any).filters.native_compaction_v2 = true
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()

    expect((wrapper.vm as any).breakdownFilters.native_compaction_v2).toBe(true)
    expect(list).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: true }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(getModelStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))

    list.mockClear()
    getStats.mockClear()
    getModelStats.mockClear()
    getSnapshotV2.mockClear()

    ;(wrapper.vm as any).resetFilters()
    await flushPromises()

    expect((wrapper.vm as any).filters.native_compaction_v2).toBeNull()
    expect((wrapper.vm as any).breakdownFilters).not.toHaveProperty('native_compaction_v2')
    expect(list).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: null }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
    expect(getModelStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
  })
})

describe('admin UsageView distribution metric toggles', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getById.mockReset()
    getModelStats.mockReset()

    list.mockResolvedValue({
      items: [],
      total: 0,
      pages: 0,
    })
    getStats.mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({
      trend: [],
      models: [],
      groups: [],
    })
    getModelStats.mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps previous model stats visible during refresh until new data arrives', async () => {
    // 首次加载返回 A
    getModelStats.mockResolvedValueOnce({ models: [{ model: 'A', total_tokens: 10 }] })

    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, AuditLogModal: true, Pagination: true, Select: true,
        DateRangePicker: true, Icon: true, TokenUsageTrend: true,
        ModelDistributionChart: ModelDistributionChartStub, GroupDistributionChart: GroupDistributionChartStub,
        EndpointDistributionChart: true, UserTokenRanking: true,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()
    expect((wrapper.vm as any).requestedModelStats).toEqual([{ model: 'A', total_tokens: 10 }])

    // 刷新:让第二次 getModelStats 处于 pending,断言旧数据 A 仍在(不被清空成 [])
    let resolveSecond: (v: any) => void = () => {}
    getModelStats.mockReturnValueOnce(new Promise((res) => { resolveSecond = res }))
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect((wrapper.vm as any).requestedModelStats).toEqual([{ model: 'A', total_tokens: 10 }])

    // 新数据到达后替换为 B
    resolveSecond({ models: [{ model: 'B', total_tokens: 20 }] })
    await flushPromises()
    expect((wrapper.vm as any).requestedModelStats).toEqual([{ model: 'B', total_tokens: 20 }])
  })

  it('keeps model and group metric toggles independent without refetching chart data', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: true,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: ModelDistributionChartStub,
          GroupDistributionChart: GroupDistributionChartStub,
          UserTokenRanking: true,
        },
      },
    })

    vi.advanceTimersByTime(120)
    await flushPromises()

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    const now = new Date(Date.now() - 120) // The applied snapshot was captured before advancing timers.
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      start_date: yesterday.toISOString(),
      end_date: now.toISOString(),
      granularity: 'hour'
    }))

    const modelChart = wrapper.find('[data-test="model-chart"]')
    const groupChart = wrapper.find('[data-test="group-chart"]')

    expect(modelChart.find('.metric').text()).toBe('tokens')
    expect(groupChart.find('.metric').text()).toBe('tokens')

    await modelChart.find('.switch-metric').trigger('click')
    await flushPromises()

    expect(modelChart.find('.metric').text()).toBe('actual_cost')
    expect(groupChart.find('.metric').text()).toBe('tokens')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)

    await groupChart.find('.switch-metric').trigger('click')
    await flushPromises()

    expect(modelChart.find('.metric').text()).toBe('actual_cost')
    expect(groupChart.find('.metric').text()).toBe('actual_cost')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
  })
})

describe('admin UsageView request ID column visibility', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null)
    vi.mocked(localStorage.setItem).mockReset()
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps request ID hidden by default and allows enabling it from column settings', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: UsageTableStub,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          AuditLogModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: true,
          GroupDistributionChart: true,
          EndpointDistributionChart: true,
          UserTokenRanking: true,
        },
      },
    })
    await wrapper.vm.$nextTick()

    const usageTable = wrapper.findComponent(UsageTableStub)
    expect(usageTable.props('columns')).toEqual(expect.arrayContaining([
      expect.objectContaining({ key: 'content_reasoning_first_ms', label: 'First reasoning event' }),
      expect.objectContaining({ key: 'content_visible_text_first_ms', label: 'First visible text' }),
      expect.objectContaining({ key: 'content_response_byte_first_ms', label: 'First response byte' }),
    ]))
    const usageColumnKeys = usageTable.props('columns').map((column: { key: string }) => column.key)
    const latencyColumnIndex = usageColumnKeys.indexOf('latency')
    expect(usageColumnKeys.slice(latencyColumnIndex + 1, latencyColumnIndex + 4)).toEqual([
      'content_reasoning_first_ms',
      'content_visible_text_first_ms',
      'content_response_byte_first_ms',
    ])
    expect(usageTable.props('columns')).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'request_id' })]),
    )

    await wrapper.get('button[title="admin.users.columnSettings"]').trigger('click')
    const requestIdToggle = wrapper.findAll('button').find((button) => button.text() === 'Request ID')
    expect(requestIdToggle).toBeDefined()
    await requestIdToggle!.trigger('click')

    expect(usageTable.props('columns')).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'request_id', label: 'Request ID' })]),
    )
    expect(localStorage.setItem).toHaveBeenCalledWith(
      'usage-hidden-columns-version',
      'upstream-request-id-hidden-by-default',
    )

    const reasoningColumnToggle = wrapper.findAll('button').find((button) => button.text() === 'First reasoning event')
    expect(reasoningColumnToggle).toBeDefined()
    await reasoningColumnToggle!.trigger('click')
    expect(usageTable.props('columns')).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'content_reasoning_first_ms' })]),
    )
    expect(localStorage.setItem).toHaveBeenCalledWith(
      'usage-hidden-columns',
      expect.stringContaining('content_reasoning_first_ms'),
    )
  })

  it('keeps upstream ID hidden by default and allows enabling it from column settings', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: UsageTableStub,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          AuditLogModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: true,
          GroupDistributionChart: true,
          EndpointDistributionChart: true,
          UserTokenRanking: true,
        },
      },
    })
    await wrapper.vm.$nextTick()

    const usageTable = wrapper.findComponent(UsageTableStub)
    expect(usageTable.props('columns')).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'upstream_request_id' })]),
    )

    await wrapper.get('button[title="admin.users.columnSettings"]').trigger('click')
    const upstreamToggle = wrapper.findAll('button').find((button) => button.text() === 'Upstream ID')
    expect(upstreamToggle).toBeDefined()
    await upstreamToggle!.trigger('click')

    expect(usageTable.props('columns')).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'upstream_request_id', label: 'Upstream ID' })]),
    )
  })
})

describe('admin UsageView handleUserClick', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getById.mockReset()

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({ trend: [], models: [], groups: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('opens user via include_deleted when clicking a usage row user', async () => {
    getById.mockResolvedValue({ id: 2, email: 'd@test.com', deleted_at: '2026-05-28T00:00:00Z' })

    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: UsageTableStub,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          AuditLogModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: true,
          GroupDistributionChart: true,
          EndpointDistributionChart: true,
          UserTokenRanking: true,
        },
      },
    })

    vi.advanceTimersByTime(120)
    await flushPromises()

    await wrapper.find('[data-test="usage-table"] .user-click').trigger('click')
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(2, true)
  })
})

describe('admin UsageView errors tab filter forwarding', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getModelStats.mockReset()
    listErrorLogs.mockReset()

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockResolvedValue({ models: [] })
    listErrorLogs.mockResolvedValue({ items: [], total: 0, pages: 0 })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('forwards model/account_id/group_id to listErrorLogs on the errors tab', async () => {
    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, AuditLogModal: true, Pagination: true, Select: true,
        DateRangePicker: true, Icon: true, TokenUsageTrend: true,
        ModelDistributionChart: true, GroupDistributionChart: true, EndpointDistributionChart: true,
        UserTokenRanking: true, OpsErrorLogTable: true, OpsErrorDetailModal: true,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()

    // 模拟用户在过滤器里选择了模型/账户/分组
    const vm = wrapper.vm as any
    vm.filters.model = 'gpt-5.3-codex'
    vm.filters.account_id = 7
    vm.filters.group_id = 3
    await flushPromises()

    // 切换到「错误请求」标签（第二个 tab 按钮）触发 loadAdminErrors
    const tabs = wrapper.findAll('[data-testid="usage-detail-tab"]')
    await tabs[1].trigger('click')
    await flushPromises()

    expect(listErrorLogs).toHaveBeenCalledWith(expect.objectContaining({
      view: 'all',
      model: 'gpt-5.3-codex',
      account_id: 7,
      group_id: 3,
    }))
  })
})

describe('admin UsageView ranking tab', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getModelStats.mockReset()

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('mounts ranking lazily and drill-down sets user filter then jumps back to usage tab', async () => {
    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, Pagination: true, Select: true,
        DateRangePicker: true, Icon: true, TokenUsageTrend: true,
        ModelDistributionChart: true, GroupDistributionChart: true, EndpointDistributionChart: true,
        UserTokenRanking: UserTokenRankingStub,
        UsageConnections: UsageConnectionsStub, OpsErrorLogTable: true, OpsErrorDetailModal: true,
        LatencyObservabilityPanel: LatencyObservabilityPanelStub,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()

    // 懒挂载:切到排行 tab 前不渲染
    expect(wrapper.find('[data-test="ranking"]').exists()).toBe(false)

    const tabs = wrapper.findAll('[data-testid="usage-detail-tab"]')
    expect(tabs).toHaveLength(6)
    expect(wrapper.find('[data-test="connection-tab"]').exists()).toBe(false)
    await tabs[2].trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="connection-tab"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="ranking"]').exists()).toBe(false)
    await tabs[3].trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="ranking"]').exists()).toBe(true)

    // 下钻:设置 user_id、切回用量明细 tab 并按新筛选重新拉取列表
    list.mockClear()
    await wrapper.find('[data-test="ranking"] .pick-user').trigger('click')
    await flushPromises()

    expect((wrapper.vm as any).activeTab).toBe('usage')
    expect((wrapper.vm as any).filters.user_id).toBe(5)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ user_id: 5 }), expect.anything())
  })
})

describe('admin UsageView model audit export', () => {
	beforeEach(() => {
		vi.useFakeTimers()
		list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
		exportList.mockReset().mockResolvedValue({
			items: [{
				id: 1,
				created_at: '2026-08-04T00:00:00Z',
				model: 'gpt-5.6-sol',
				upstream_model: 'gpt-5.5',
				upstream_response_model: 'gpt-5.4',
				upstream_model_mismatch: true,
				request_type: 'sync',
				input_tokens: 1,
				output_tokens: 1,
				cache_read_tokens: 0,
				cache_creation_tokens: 0,
				duration_ms: 10,
			}],
			total: 1,
			pages: 1,
		})
		getStats.mockReset().mockResolvedValue({
			total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
			total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
		})
		getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
		getModelStats.mockReset().mockResolvedValue({ models: [] })
		aoaToSheet.mockClear()
		sheetAddAoa.mockClear()
		saveAs.mockClear()
		xlsxWrite.mockClear()
	})

	afterEach(() => {
		vi.useRealTimers()
	})

	it('exports requested, sent, response, and mismatch as separate admin columns', async () => {
		const wrapper = mountRouteFilteredUsageView()
		vi.advanceTimersByTime(120)
		await flushPromises()
		;(wrapper.vm as any).filters.native_compaction_v2 = true

		await (wrapper.vm as any).exportToExcel()
		await flushPromises()

		expect(exportList).toHaveBeenCalledWith(
			expect.objectContaining({ native_compaction_v2: true }),
			expect.anything()
		)

		const headers = aoaToSheet.mock.calls[0][0][0]
		expect(headers.slice(4, 8)).toEqual([
			'Requested model',
			'Sent upstream model',
			'Upstream response model',
			'Upstream model mismatch',
		])
		const row = sheetAddAoa.mock.calls[0][1][0]
		expect(row.slice(4, 8)).toEqual(['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4', 'Yes'])
		const cacheRateIndex = headers.indexOf('usage.latencyCacheRate')
		expect(cacheRateIndex).toBe(headers.indexOf('admin.usage.cacheCreationTokens') + 1)
		expect(row[cacheRateIndex]).toBe('0%')
		expect(saveAs).toHaveBeenCalledTimes(1)
	})
})


describe('observer own usage view', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    Object.keys(routeQuery).forEach(key => delete routeQuery[key])
    own.settings.allow_user_view_error_requests = true
    own.list.mockResolvedValue({ items: [], total: 0 })
    own.getStats.mockResolvedValue({ total_requests: 2, total_account_cost: 3 })
    own.getModelStats.mockResolvedValue({ models: [] })
    own.getSnapshotV2.mockResolvedValue({ trend: [], groups: [] })
    own.listErrors.mockResolvedValue({ items: [], total: 0 })
  })
  afterEach(() => { vi.useRealTimers() })
  const mountObserver = () => mount(UsageView, {
    props: { observerMode: true },
    global: { stubs: {
      AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
      UsageTable: UsageTableStub, UsageExportProgress: true, UsageCleanupDialog: true,
      UserBalanceHistoryModal: true, Pagination: true, Select: true, DateRangePicker: true,
      Icon: true, TokenUsageTrend: true, ModelDistributionChart: true, GroupDistributionChart: true,
      EndpointDistributionChart: true, UserTokenRanking: true, OpsErrorLogTable: true, OpsErrorDetailModal: true,
      LatencyObservabilityPanel: LatencyObservabilityPanelStub,
    } },
  })
  it('uses own APIs for filters, charts and export, without admin actions', async () => {
    routeQuery.user_id = '999'
    const wrapper = mountObserver()
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(own.list).toHaveBeenCalledWith(expect.objectContaining({ user_id: undefined }), expect.anything())
    expect(own.getStats).toHaveBeenCalled()
    expect(own.getModelStats).toHaveBeenCalled()
    expect(own.getSnapshotV2).toHaveBeenCalled()
    expect(wrapper.find('usage-cleanup-dialog-stub').exists()).toBe(false)
    expect(wrapper.find('user-balance-history-modal-stub').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('usage.tabs.ranking')
    expect(wrapper.text()).not.toContain('connections.title')
    expect(wrapper.text()).not.toContain('latencyObservability.title')
    for (const name of ['model-distribution-chart-stub', 'group-distribution-chart-stub', 'endpoint-distribution-chart-stub']) {
      expect(wrapper.findComponent(name).props('enableBreakdown')).toBe(false)
    }
    await wrapper.find('.user-click').trigger('click')
    const filter = wrapper.findComponent(UsageFiltersStub)
    filter.vm.$emit('update:modelValue', { account_id: 7, group_id: 8, api_key_id: 9, model: 'gpt', start_date: '2026-09-01', end_date: '2026-09-02' })
    filter.vm.$emit('change')
    await flushPromises()
    expect(own.list).toHaveBeenLastCalledWith(expect.objectContaining({ account_id: 7, group_id: 8, api_key_id: 9 }), expect.anything())
    filter.vm.$emit('cleanup')
    filter.vm.$emit('export')
    await flushPromises()
    expect(own.list).toHaveBeenLastCalledWith(expect.objectContaining({ page_size: 100, exact_total: true, account_id: 7 }), expect.anything())
    expect(saveAs).toHaveBeenCalled()
    const errorTab = wrapper.findAll('[data-testid="usage-detail-tab"]').find(tab => tab.text() === 'usage.tabs.errors')!
    await errorTab.trigger('click')
    await flushPromises()
    expect(own.listErrors).toHaveBeenCalledWith(expect.objectContaining({ start_date: '2026-09-01', account_id: 7, group_id: 8 }))
    for (const fn of [list, exportList, getStats, getModelStats, getSnapshotV2, getById, listErrorLogs]) expect(fn).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('honors the user error visibility switch', async () => {
    own.settings.allow_user_view_error_requests = false
    const wrapper = mountObserver()
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(wrapper.findAll('[data-testid="usage-detail-tab"]')).toHaveLength(1)
    expect(own.listErrors).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('admin UsageView rolling 24 hour requests', () => {
  let wrapper: ReturnType<typeof mountRouteFilteredUsageView> | undefined
  const now = new Date('2026-10-01T00:01:02.123+09:00')

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(now)
    Object.keys(routeQuery).forEach(key => delete routeQuery[key])
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    exportList.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({ total_requests: 2, total_cost: 12, total_actual_cost: 3, total_account_cost: 5 })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], groups: [] })
    listErrorLogs.mockReset().mockResolvedValue({ items: [], total: 0 })
  })
  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    Object.keys(routeQuery).forEach(key => delete routeQuery[key])
    vi.useRealTimers()
  })

  const expectRequests = (end: Date) => {
    const range = { start_date: new Date(end.getTime() - 86_400_000).toISOString(), end_date: end.toISOString() }
    for (const request of [list, getStats, getModelStats, getSnapshotV2]) {
      expect(request.mock.lastCall?.[0]).toMatchObject(range)
      const params = request.mock.lastCall![0]
      expect(Date.parse(params.end_date) - Date.parse(params.start_date)).toBe(86_400_000)
    }
    expect(getSnapshotV2.mock.lastCall?.[0].granularity).toBe('hour')
    return range
  }

  it.each([
    ['rolling', undefined, undefined],
    ['routed exact', '2026-09-30T00:00:00Z', '2026-10-01T00:00:00Z'],
    ['mixed', '2026-09-30', '2026-10-01T00:00:00Z'],
  ])('blocks %s cleanup until calendar dates are explicitly selected', async (_, start, end) => {
    if (start) routeQuery.start_date = start
    if (end) routeQuery.end_date = end
    listCleanupTasks.mockReset().mockResolvedValue({ items: [], total: 0 })
    createCleanupTask.mockReset()
    showWarning.mockClear()
    wrapper = mountRouteFilteredUsageView(true, true)
    await flushPromises()
    await wrapper.get('[data-test="cleanup-open"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').some(b => b.text() === 'admin.usage.cleanup.submit')).toBe(false)
    expect(wrapper.find('[data-test="cleanup-confirm"]').exists()).toBe(false)
    expect(listCleanupTasks).not.toHaveBeenCalled()
    expect(createCleanupTask).not.toHaveBeenCalled()
    expect(showWarning).toHaveBeenCalledWith('admin.usage.cleanup.calendarRangeRequired')
  })

  it('submits unchanged natural dates after an explicit picker selection', async () => {
    listCleanupTasks.mockReset().mockResolvedValue({ items: [], total: 0 })
    createCleanupTask.mockReset().mockResolvedValue({ id: 1 })
    wrapper = mountRouteFilteredUsageView(true, true)
    await flushPromises()
    await wrapper.get('[data-test="cleanup-open"]').trigger('click')
    const picker = wrapper.findComponent(DateRangePicker)
    await picker.get('.date-picker-trigger').trigger('click')
    await picker.findAll('.date-picker-preset').find(b => b.text() === 'dates.today')!.trigger('click')
    await picker.get('.date-picker-apply').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="cleanup-open"]').trigger('click')
    await flushPromises()
    const dialog = wrapper.findComponent(UsageCleanupDialog)
    const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
    expect(dialog.props()).toMatchObject({ show: true, startDate: today, endDate: today })
    await dialog.findAll('button').find(b => b.text() === 'admin.usage.cleanup.submit')!.trigger('click')
    await wrapper.get('[data-test="cleanup-confirm"]').trigger('click')
    await flushPromises()
    expect(createCleanupTask).toHaveBeenCalledOnce()
    expect(createCleanupTask).toHaveBeenCalledWith({ start_date: today, end_date: today, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone })
  })

  it('uses the same precise cross-midnight window for list, cards and charts on initial load', async () => {
    wrapper = mountRouteFilteredUsageView(true)
    vi.advanceTimersByTime(120)
    await flushPromises()
    expectRequests(now)
    expect(wrapper.findComponent(DateRangePicker).get('.date-picker-trigger').text()).toContain('dates.last24Hours')
    expect(wrapper.findComponent(UsageStatsCards).props('stats')).toMatchObject({
      total_actual_cost: 3, total_cost: 12, total_account_cost: 5
    })
  })

  it('refreshes the rolling window once and keeps pagination/export on that applied snapshot', async () => {
    wrapper = mountRouteFilteredUsageView()
    await flushPromises()
    const refreshedAt = new Date('2026-10-02T00:00:00.456+09:00')
    vi.setSystemTime(refreshedAt)
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    const range = expectRequests(refreshedAt)
    vi.setSystemTime(new Date('2026-10-02T00:05:00.789+09:00'))
    ;(wrapper.vm as any).handlePageChange(2)
    await flushPromises()
    expect(list.mock.lastCall?.[0]).toMatchObject({ ...range, page: 2 })
    await (wrapper.vm as any).exportToExcel()
    expect(exportList.mock.lastCall?.[0]).toMatchObject(range)
    expect((wrapper.vm as any).connectionFilters).toMatchObject({ start_time: range.start_date, end_time: range.end_date })
    ;(wrapper.vm as any).switchTab('errors')
    await flushPromises()
    expect(listErrorLogs.mock.lastCall?.[0]).toMatchObject({ start_time: range.start_date, end_time: range.end_date })
  })

  it('applies the actual picker selection after midnight and resets back to rolling time', async () => {
    routeQuery.start_date = '2026-09-20'
    routeQuery.end_date = '2026-09-25'
    wrapper = mountRouteFilteredUsageView(true)
    await flushPromises()
    const picker = wrapper.findComponent(DateRangePicker)
    await picker.get('.date-picker-trigger').trigger('click')
    await picker.findAll('.date-picker-preset').find(b => b.text() === 'dates.last24Hours')!.trigger('click')
    await picker.get('.date-picker-apply').trigger('click')
    await flushPromises()
    expectRequests(now)
    const resetAt = new Date('2026-10-02T00:00:00.789+09:00')
    vi.setSystemTime(resetAt)
    ;(wrapper.vm as any).resetFilters()
    await flushPromises()
    expectRequests(resetAt)
  })

  it('preserves manual/routed calendar dates on refresh', async () => {
    routeQuery.start_date = '2026-09-30'
    routeQuery.end_date = '2026-10-01'
    wrapper = mountRouteFilteredUsageView(true)
    await flushPromises()
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    for (const request of [list, getStats, getModelStats, getSnapshotV2]) {
      expect(request.mock.lastCall?.[0]).toMatchObject({ start_date: '2026-09-30', end_date: '2026-10-01' })
    }
    expect(wrapper.findComponent(DateRangePicker).get('.date-picker-trigger').text()).not.toContain('dates.last24Hours')
  })
})
