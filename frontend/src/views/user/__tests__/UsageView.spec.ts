import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import { apiClient } from '@/api/client'
import { listMyErrorRequests as requestUserErrors } from '@/api/usage'
import UsageView from '../UsageView.vue'
import UsageApiGuide from '@/components/usage/UsageApiGuide.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import TrafficPanel from '@/components/usage/TrafficPanel.vue'
import UserErrorRequestsTable from '@/components/user/UserErrorRequestsTable.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import UsageTable from '@/components/admin/usage/UsageTable.vue'
import ExportProgressDialog from '@/components/common/ExportProgressDialog.vue'
import { EXPORT_PAGE_SIZE } from '@/utils/pagedExport'

const {
  query,
  getStats,
  getDashboardModels,
  getDashboardSnapshotV2,
  listMyErrorRequests,
  getTrafficKeys,
  list,
  getAvailable,
  showError,
  showWarning,
  showSuccess,
  showInfo,
} = vi.hoisted(() => ({
  query: vi.fn(),
  getStats: vi.fn(),
  getDashboardModels: vi.fn(),
  getDashboardSnapshotV2: vi.fn(),
  listMyErrorRequests: vi.fn(),
  getTrafficKeys: vi.fn(),
  list: vi.fn(),
  getAvailable: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn(),
}))

const messages: Record<string, string> = {
  'admin.dashboard.timeRange': 'Time range',
  'admin.dashboard.granularity': 'Granularity',
  'admin.dashboard.day': 'Day',
  'admin.dashboard.hour': 'Hour',
  'admin.users.columnSettings': 'Columns',
  'admin.usage.group': 'Group',
  'admin.usage.billingType': 'Billing type',
  'admin.usage.billingMode': 'Billing mode',
  'admin.usage.allTypes': 'All types',
  'admin.usage.allBillingTypes': 'All billing types',
  'admin.usage.billingTypeBalance': 'Balance',
  'admin.usage.billingTypeSubscription': 'Subscription',
  'admin.usage.allBillingModes': 'All billing modes',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per request',
  'admin.usage.billingModeImage': 'Image',
  'admin.usage.allGroups': 'All groups',
  'admin.usage.allModels': 'All models',
  'usage.allApiKeys': 'All API Keys',
  'usage.errors.allKeys': 'All API Keys',
  'usage.errors.allStatuses': 'All status codes',
  'usage.errors.allCategories': 'All categories',
  'usage.errors.categories.transport_error': 'Transport error',
  'usage.errors.http200StreamFailure': '200 — Stream failed after headers',
  'usage.tabs.usage': 'Usage records',
  'usage.tabs.errors': 'Error records',
  'usage.apiKeyFilter': 'API Key',
  'usage.model': 'Model',
  'usage.type': 'Type',
  'usage.ws': 'WS',
  'usage.stream': 'Stream',
  'usage.sync': 'Sync',
  'usage.compactionFilter': 'Request Kind',
  'usage.allCompactionTypes': 'All Requests',
  'usage.compactionOnly': 'Compaction Only',
  'usage.exporting': 'Exporting',
  'usage.exportCsv': 'Export CSV',
  'usage.failedToLoad': 'Failed to load',
  'usage.noDataToExport': 'No data',
  'usage.preparingExport': 'Preparing export',
  'usage.exportSuccess': 'Export success',
  'usage.exportFailed': 'Export failed',
  'common.refresh': 'Refresh',
  'common.reset': 'Reset',
}

vi.mock('@/api', () => ({
  usageAPI: {
    query,
    getStats,
    getDashboardModels,
    getDashboardSnapshotV2,
    listMyErrorRequests,
  },
  keysAPI: {
    list,
  },
  userGroupsAPI: {
    getAvailable,
  },
}))

vi.mock('@/api/traffic', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/traffic')>(),
  getTrafficKeys,
  getTraffic: vi.fn().mockResolvedValue({
    period: 'day', start: '2026-03-08T00:00:00Z', end: '2026-03-09T00:00:00Z',
    requests: 0, upload_bytes: 0, download_bytes: 0, total_bytes: 0, items: [], trend: [],
  }),
}))

const appStoreState = vi.hoisted(() => ({
  isAdmin: false,
  cachedPublicSettings: { allow_user_view_error_requests: true } as Record<string, unknown>,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAdmin: appStoreState.isAdmin, user: { id: 41 } }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError, showWarning, showSuccess, showInfo,
    get cachedPublicSettings() {
      return appStoreState.cachedPublicSettings
    },
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const simpleStub = { template: '<div><slot /></div>' }
const chartStub = { template: '<div />' }

function pendingPage() {
  type Page = { items: { id: number }[]; total: number }
  let resolve!: (page: Page) => void
  let reject!: (error: Error) => void
  const promise = new Promise<Page>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const usageLog = {
  id: 1,
  request_id: 'req-user-export',
  actual_cost: 0.092883,
  total_cost: 0.092883,
  rate_multiplier: 1,
  service_tier: 'priority',
  input_cost: 0.020285,
  output_cost: 0.00303,
  cache_creation_cost: 0.000001,
  cache_read_cost: 0.069568,
  input_tokens: 4057,
  output_tokens: 101,
  cache_creation_tokens: 4,
  cache_read_tokens: 278272,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  image_count: 0,
  image_size: null,
  first_token_ms: 12,
  duration_ms: 345,
  created_at: '2026-03-08T00:00:00Z',
  model: 'gpt-5.4',
  reasoning_effort: null,
  ip_address: '203.0.113.10',
  api_key: { name: 'demo-key' },
  billing_mode: 'token',
  request_type: 'sync',
  stream: false,
  native_compaction_v2: false,
}

function mountUsageView(realSelect = false) {
  return mount(UsageView, {
    attachTo: realSelect ? document.body : undefined,
    global: {
      stubs: {
        AppLayout: simpleStub,
        Pagination: true,
        Select: !realSelect,
        DateRangePicker: true,
        Icon: true,
        UsageStatsCards: chartStub,
        UsageTable: { ...chartStub, props: ['columns', 'data', 'loading'] },
        UserErrorRequestsTable: true,
        UsageApiGuide: true,
        NewFeatureBadge: true,
        ModelDistributionChart: chartStub,
        GroupDistributionChart: chartStub,
        EndpointDistributionChart: chartStub,
        TokenUsageTrend: chartStub,
      },
    },
  })
}

describe('user UsageView', () => {
  beforeEach(() => {
    appStoreState.isAdmin = false
    appStoreState.cachedPublicSettings = { allow_user_view_error_requests: true }
    query.mockReset()
    getStats.mockReset()
    getDashboardModels.mockReset()
    getDashboardSnapshotV2.mockReset()
    listMyErrorRequests.mockReset()
    getTrafficKeys.mockReset().mockResolvedValue([])
    list.mockReset()
    getAvailable.mockReset()
    showError.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()
    showInfo.mockReset()

    query.mockResolvedValue({ items: [usageLog], total: 1, pages: 1 })
    getStats.mockResolvedValue({
      total_requests: 1,
      total_input_tokens: 10,
      total_output_tokens: 20,
      total_cache_tokens: 0,
      total_tokens: 30,
      total_cost: 0.1,
      total_actual_cost: 0.08,
      average_duration_ms: 12,
      endpoints: [],
      upstream_endpoints: [],
      endpoint_paths: [],
    })
    getDashboardModels.mockResolvedValue({
      models: [{ model: 'gpt-5.4', requests: 1, input_tokens: 10, output_tokens: 20, cache_creation_tokens: 0, cache_read_tokens: 0, total_tokens: 30, cost: 0.1, actual_cost: 0.08 }],
      start_date: '2026-03-08',
      end_date: '2026-03-08',
    })
    getDashboardSnapshotV2.mockResolvedValue({
      generated_at: '2026-03-08T00:00:00Z',
      start_date: '2026-03-08',
      end_date: '2026-03-08',
      granularity: 'hour',
      trend: [],
      groups: [],
    })
    listMyErrorRequests.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    list.mockResolvedValue({ items: [{ id: 1, name: 'demo-key' }], total: 1, page: 1, page_size: 100, pages: 1 })
    getAvailable.mockResolvedValue([{ id: 1, name: 'default' }])
  })

  it('selects HTTP200 transport errors through the actual error-tab controls and submits both API filters', async () => {
    const wrapper = mountUsageView(true)
    try {
      await flushPromises()
      await wrapper.findAll('button').find(button => button.text() === 'Error records')!.trigger('click')
      await flushPromises()
      const selects = wrapper.findAllComponents(Select)
      const category = selects.find(select => (select.props('options') as SelectOption[]).some(option => option.value === 'transport_error'))!
      await category.get('button').trigger('click')
      await flushPromises()
      const transport = Array.from(document.querySelectorAll<HTMLElement>('[role="option"]')).find(option => option.textContent?.trim() === 'Transport error')!
      transport.click()
      await flushPromises()

      const status = selects.find(select => (select.props('options') as SelectOption[]).some(option => option.value === 502))!
      expect(status.props('options'), 'HTTP200 must be selectable from the real error status control').toContainEqual({ value: 200, label: '200 — Stream failed after headers' })
      await status.get('button').trigger('click')
      await flushPromises()
      const http200 = Array.from(document.querySelectorAll<HTMLElement>('[role="option"]')).find(option => option.textContent?.trim() === '200 — Stream failed after headers')!
      http200.click()
      await flushPromises()
      expect(listMyErrorRequests).toHaveBeenLastCalledWith(expect.objectContaining({ category: 'transport_error', status_code: 200, page: 1 }), { signal: expect.any(AbortSignal) })

      await status.get('button').trigger('click')
      await flushPromises()
      Array.from(document.querySelectorAll<HTMLElement>('[role="option"]')).find(option => option.textContent?.trim() === '502')!.click()
      await flushPromises()
      expect(listMyErrorRequests).toHaveBeenLastCalledWith(expect.objectContaining({ category: 'transport_error', status_code: 502 }), { signal: expect.any(AbortSignal) })
      await wrapper.findAll('button').find(button => button.text() === 'Usage records')!.trigger('click')
      expect(query.mock.calls.every(([params]) => params.status_code === undefined && params.category === undefined)).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  it.each([false, true])('opens the fourth tab without loading data (admin=%s)', async (admin) => {
    appStoreState.isAdmin = admin
    const wrapper = mountUsageView()
    await flushPromises()
    const tabs = wrapper.findAll('button.tab')
    expect(tabs).toHaveLength(4)
    expect(tabs[3].text()).toBe('usageApi.title')
    const badge = wrapper.findComponent(NewFeatureBadge)
    expect(badge.exists()).toBe(admin)
    if (admin) expect(badge.props()).toMatchObject({ noticeId: NEW_FEATURE_NOTICES.usageRecordsApi, userId: 41 })
    const calls = query.mock.calls.length
    const statCalls = getStats.mock.calls.length
    await tabs[3].trigger('click')
    expect(wrapper.findComponent(UsageApiGuide).exists()).toBe(true)
    expect(wrapper.findComponent(UsageTable).exists()).toBe(false)
    expect(wrapper.findComponent(DateRangePicker).exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'UsageStatsCards' }).exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Export CSV')
    expect(query).toHaveBeenCalledTimes(calls)
    expect(getStats).toHaveBeenCalledTimes(statCalls)
    await tabs[0].trigger('click')
    expect(wrapper.findComponent(UsageTable).exists()).toBe(true)
    wrapper.unmount()
  })

  it('stops pending pagination and ignores stale errors when the guide opens', async () => {
    let finishKeys!: (page: unknown) => void
    let failLogs!: (reason: Error) => void
    let failErrors!: (reason: Error) => void
    list.mockReturnValueOnce(new Promise(resolve => { finishKeys = resolve }))
    query.mockReturnValueOnce(new Promise((_, reject) => { failLogs = reject }))
    listMyErrorRequests.mockReturnValueOnce(new Promise((_, reject) => { failErrors = reject }))
    const wrapper = mountUsageView()
    const tabs = wrapper.findAll('button.tab')
    await tabs[1].trigger('click')
    await tabs[3].trigger('click')
    expect(wrapper.findComponent(UsageApiGuide).exists()).toBe(true)
    expect(list.mock.calls[0][3].signal.aborted).toBe(true)
    expect(query.mock.calls[0][1].signal.aborted).toBe(true)
    expect(listMyErrorRequests.mock.calls[0][1].signal.aborted).toBe(true)
    finishKeys({ items: [{ id: 1 }], pages: 3 })
    failLogs(new Error('old usage request'))
    failErrors(new Error('old error request'))
    await flushPromises()
    expect(list).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()
    await tabs[0].trigger('click')
    await flushPromises()
    expect(list).toHaveBeenCalledTimes(2)
    expect(query).toHaveBeenCalledTimes(2)
    expect(wrapper.findComponent(UsageTable).exists()).toBe(true)
    wrapper.unmount()
  })

  it.each(['success', 'failure'])('reloads usage page 2 with the selected filter after visiting the guide (%s)', async outcome => {
    const page1 = { items: [{ ...usageLog, id: 1 }], total: 51 }
    const page2 = { items: [{ ...usageLog, id: 21 }], total: 50 }
    const stale = pendingPage()
    const reload = pendingPage()
    query.mockResolvedValue(page1)
    const wrapper = mountUsageView()
    const [usage, , , guide] = wrapper.findAll('button.tab')
    const table = () => wrapper.getComponent(UsageTable)
    try {
      await flushPromises()
      query.mockClear()
      const keySelect = wrapper.findAllComponents(Select).find(select =>
        (select.props('options') as SelectOption[]).some(option => option.label === 'All API Keys')
      )!
      keySelect.vm.$emit('update:modelValue', 1)
      keySelect.vm.$emit('change', 1)
      await flushPromises()
      expect(table().props()).toMatchObject({ data: page1.items, loading: false })
      const pager = wrapper.getComponent(Pagination)
      expect(pager.props()).toMatchObject({ page: 1, total: 51 })

      query.mockReturnValueOnce(stale.promise).mockReturnValueOnce(reload.promise)
      pager.vm.$emit('update:page', 2)
      await guide.trigger('click')
      expect(query.mock.calls[1][1].signal.aborted).toBe(true)
      expect(wrapper.vm.pagination).toMatchObject({ page: 2, total: 0 })
      await usage.trigger('click')
      const params = query.mock.calls.map(([request]) => request)
      expect(params.map(request => request.page)).toEqual([1, 2, 2])
      expect(params[1]).toMatchObject({ api_key_id: 1, page: 2 })
      expect(params[2]).toEqual(params[1])
      expect(table().props()).toMatchObject({ data: [], loading: true })
      stale.resolve({ items: [{ id: 99 }], total: 99 })
      await flushPromises()
      expect(table().props()).toMatchObject({ data: [], loading: true })
      expect(wrapper.vm.pagination).toMatchObject({ page: 2, total: 0 })

      if (outcome === 'success') {
        reload.resolve(page2)
        await flushPromises()
        expect(wrapper.getComponent(Pagination).props()).toMatchObject({ page: 2, total: 50 })
        expect(table().props()).toMatchObject({ data: page2.items, loading: false })
        expect(showError).not.toHaveBeenCalled()
      } else {
        reload.reject(new Error('usage unavailable'))
        await flushPromises()
        expect(wrapper.vm.pagination).toMatchObject({ page: 2, total: 0 })
        expect(wrapper.findComponent(Pagination).exists()).toBe(false)
        expect(table().props()).toMatchObject({ data: [], loading: false })
        expect(showError.mock.calls).toEqual([['Failed to load']])
      }
    } finally {
      wrapper.unmount()
    }
  })

  it.each([
    { path: 'directly', via: [] },
    { path: 'through usage details', via: ['usage'] },
    { path: 'through traffic and usage details', via: ['traffic', 'usage'] },
  ] as const)('reloads the pending error page when returning from the guide $path', async ({ via }) => {
    const page = (id: number) => ({ items: [{ id }], total: 30 })
    let finishStale!: (response: ReturnType<typeof page>) => void
    let finishCurrent!: (response: ReturnType<typeof page>) => void
    listMyErrorRequests
      .mockResolvedValueOnce(page(1))
      .mockReturnValueOnce(new Promise(resolve => { finishStale = resolve }))
      .mockReturnValueOnce(new Promise(resolve => { finishCurrent = resolve }))
    const wrapper = mountUsageView()
    const [usage, errors, traffic, guide] = wrapper.findAll('button.tab')
    const detourTabs = { usage, traffic }
    const table = () => wrapper.getComponent(UserErrorRequestsTable)
    try {
      await errors.trigger('click')
      await nextTick()
      expect(table().props()).toMatchObject({ page: 1, rows: [{ id: 1 }], loading: false })
      table().vm.$emit('update:page', 2)
      await guide.trigger('click')
      for (const tab of via) await detourTabs[tab].trigger('click')
      await errors.trigger('click')
      expect(listMyErrorRequests.mock.calls.map(([params]) => params.page)).toEqual([1, 2, 2])
      expect(table().props()).toMatchObject({ page: 2, rows: [], loading: true })
      finishStale(page(99))
      await flushPromises()
      expect(table().props()).toMatchObject({ page: 2, rows: [], loading: true })
      finishCurrent(page(21))
      await flushPromises()
      expect(table().props()).toMatchObject({ page: 2, rows: [{ id: 21 }], total: 30, loading: false })
    } finally {
      wrapper.unmount()
    }
  })

  it('keeps the selected error filter when an old response arrives after the reload', async () => {
    let finishStale!: (result: { items: { id: number }[]; total: number }) => void
    listMyErrorRequests
      .mockResolvedValueOnce({ items: [{ id: 1 }], total: 1 })
      .mockReturnValueOnce(new Promise(resolve => { finishStale = resolve }))
      .mockResolvedValueOnce({ items: [{ id: 3 }], total: 1 })
    const wrapper = mountUsageView()
    const [usage, errors, , guide] = wrapper.findAll('button.tab')
    try {
      await errors.trigger('click')
      const status = wrapper.findAllComponents(Select).find(select =>
        (select.props('options') as SelectOption[]).some(option => option.value === 502)
      )!
      status.vm.$emit('update:modelValue', 502)
      status.vm.$emit('change', 502)
      await usage.trigger('click')
      expect(listMyErrorRequests.mock.calls[1][1].signal.aborted).toBe(true)
      await guide.trigger('click')
      await errors.trigger('click')
      await nextTick()
      expect(listMyErrorRequests.mock.calls.map(([params]) => params.status_code)).toEqual([undefined, 502, 502])
      finishStale({ items: [{ id: 2 }], total: 1 })
      await flushPromises()
      expect(wrapper.getComponent(UserErrorRequestsTable).props()).toMatchObject({ page: 1, rows: [{ id: 3 }], loading: false })
    } finally {
      wrapper.unmount()
    }
  })

  it.each(['resolve', 'reject'])('aborts the error request on unmount and ignores a late %s', async outcome => {
    const pending = pendingPage()
    const httpGet = vi.spyOn(apiClient, 'get').mockReturnValueOnce(pending.promise.then(data => ({ data })))
    listMyErrorRequests.mockImplementationOnce(requestUserErrors)
    const wrapper = mountUsageView()
    const state = wrapper.vm
    let signal: AbortSignal
    try {
      await wrapper.findAll('button.tab')[1].trigger('click')
      signal = listMyErrorRequests.mock.calls[0][1].signal
      expect(httpGet).toHaveBeenCalledWith('/usage/errors', { params: expect.objectContaining({ page: 1 }), signal })
      expect(signal.aborted).toBe(false)
    } finally {
      wrapper.unmount()
      httpGet.mockRestore()
    }
    expect(signal.aborted).toBe(true)
    const snapshot = { errorLoading: state.errorLoading, errorRows: state.errorRows, errorTotal: state.errorTotal }
    if (outcome === 'resolve') pending.resolve({ items: [{ id: 99 }], total: 99 })
    else pending.reject(new Error('late error'))
    await flushPromises()
    expect(state.errorLoading).toBe(snapshot.errorLoading)
    expect(state.errorRows).toBe(snapshot.errorRows)
    expect(state.errorTotal).toBe(snapshot.errorTotal)
    expect(showError).not.toHaveBeenCalled()
  })

  it('loads only the traffic key options when leaving the guide for traffic', async () => {
    const wrapper = mountUsageView()
    const [usage, , traffic, guide] = wrapper.findAll('button.tab')
    try {
      await guide.trigger('click')
      const detailRequests = [query, getStats, getDashboardModels, getDashboardSnapshotV2, getAvailable]
      for (const request of [...detailRequests, list]) request.mockClear()
      getTrafficKeys.mockResolvedValueOnce([{ id: 2, name: 'traffic key', deleted: false }])
      await traffic.trigger('click')
      await flushPromises()
      expect(wrapper.getComponent(TrafficPanel).getComponent(Select).props('options')).toContainEqual({ value: 2, label: 'traffic key' })
      expect(getTrafficKeys).toHaveBeenCalledTimes(1)
      expect(getTrafficKeys).toHaveBeenCalledWith(false, undefined, '', expect.any(AbortSignal))
      expect(list).not.toHaveBeenCalled()
      for (const request of [...detailRequests, listMyErrorRequests]) expect(request).not.toHaveBeenCalled()
      await usage.trigger('click')
      await flushPromises()
      for (const request of [...detailRequests, list]) expect(request).toHaveBeenCalledTimes(1)
    } finally {
      wrapper.unmount()
    }
  })

  it('scrolls the tab row into view after rendering the guide only', async () => {
    const wrapper = mountUsageView()
    const [usage, errors, traffic, guide] = wrapper.findAll('button.tab')
    const row = usage.element.parentElement!
    const scrollIntoView = vi.fn(() => {
      expect(wrapper.findComponent(UsageApiGuide).exists()).toBe(true)
      expect(wrapper.findComponent(DateRangePicker).exists()).toBe(false)
    })
    row.scrollIntoView = scrollIntoView
    try {
      await errors.trigger('click')
      await usage.trigger('click')
      expect(scrollIntoView).not.toHaveBeenCalled()
      await guide.trigger('click')
      expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'instant' })
      await traffic.trigger('click')
      await errors.trigger('click')
      expect(scrollIntoView).toHaveBeenCalledTimes(1)
    } finally {
      wrapper.unmount()
    }
  })

  it('passes the same own API key choices to traffic and usage details', async () => {
    const wrapper = mountUsageView(); await flushPromises()
    const usageOptions = wrapper.findAllComponents(Select).find(select =>
      (select.props('options') as SelectOption[]).some(option => option.label === 'demo-key')
    )!.props('options')
    await wrapper.findAll('button').find(button => button.text() === 'traffic.title')!.trigger('click')
    expect(wrapper.getComponent(TrafficPanel).props('apiKeyOptions')).toEqual(usageOptions)
    wrapper.unmount()
  })

  it('loads logs, stats, model stats, and snapshot on first render', async () => {
    const wrapper = mountUsageView()
    await flushPromises()

    const usageColumnKeys = wrapper.findComponent(UsageTable).props('columns').map((column: { key: string }) => column.key)
    expect(usageColumnKeys).toContain('latency')
    expect(usageColumnKeys).not.toEqual(expect.arrayContaining([
      'content_reasoning_first_ms',
      'content_visible_text_first_ms',
      'content_response_byte_first_ms',
    ]))
    expect(query).toHaveBeenCalled()
    expect(getStats).toHaveBeenCalled()
    expect(getDashboardModels).toHaveBeenCalled()
    expect(getDashboardSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      include_trend: true,
      include_model_stats: false,
      include_group_stats: true,
    }))
    expect(list).toHaveBeenCalledTimes(1)
    expect(list).toHaveBeenCalledWith(1, 100, undefined, { signal: expect.any(AbortSignal) })
    expect(getAvailable).toHaveBeenCalled()
  })

  it('includes API keys after the first page in both record filters and queries by the selected key', async () => {
    const firstPageKeys = Array.from({ length: 100 }, (_, index) => ({
      id: index + 1,
      name: `key-${index + 1}`,
    }))
    const laterKey = { id: 101, name: 'key-from-second-page' }
    list
      .mockResolvedValueOnce({ items: firstPageKeys, total: 101, page: 1, page_size: 100, pages: 2 })
      .mockResolvedValueOnce({ items: [laterKey], total: 101, page: 2, page_size: 100, pages: 2 })

    const wrapper = mountUsageView()
    await flushPromises()

    expect(list.mock.calls.map(([page, size]) => [page, size])).toEqual([[1, 100], [2, 100]])
    const usageKeySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(usageKeySelect.props('options')).toHaveLength(102)
    expect(usageKeySelect.props('options')).toContainEqual({ value: laterKey.id, label: laterKey.name })

    query.mockClear()
    usageKeySelect.vm.$emit('update:modelValue', laterKey.id)
    usageKeySelect.vm.$emit('change', laterKey.id)
    await flushPromises()

    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ api_key_id: laterKey.id, page: 1 }),
      expect.anything()
    )

    await wrapper.findAll('button').find((button) => button.text() === 'Error records')!.trigger('click')
    await flushPromises()

    const errorKeySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(errorKeySelect.props('options')).toHaveLength(102)
    expect(errorKeySelect.props('options')).toContainEqual({ value: laterKey.id, label: laterKey.name })

    listMyErrorRequests.mockClear()
    errorKeySelect.vm.$emit('update:modelValue', laterKey.id)
    errorKeySelect.vm.$emit('change', laterKey.id)
    await flushPromises()

    expect(listMyErrorRequests).toHaveBeenCalledWith(
      expect.objectContaining({ api_key_id: laterKey.id, page: 1 }), { signal: expect.any(AbortSignal) }
    )
    expect(list).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('does not request another API key page when the user has no keys', async () => {
    list.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 100, pages: 0 })

    const wrapper = mountUsageView()
    await flushPromises()

    expect(list.mock.calls.map(([page, size]) => [page, size])).toEqual([[1, 100]])
    const keySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(keySelect.props('options')).toEqual([{ value: null, label: 'All API Keys' }])
    expect(getAvailable).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('stops loading API keys when a later page is empty despite an outdated page count', async () => {
    const firstPageKeys = Array.from({ length: 100 }, (_, index) => ({
      id: index + 1,
      name: `key-${index + 1}`,
    }))
    list
      .mockResolvedValueOnce({ items: firstPageKeys, total: 201, page: 1, page_size: 100, pages: 3 })
      .mockResolvedValueOnce({ items: [], total: 201, page: 2, page_size: 100, pages: 3 })

    const wrapper = mountUsageView()
    await flushPromises()

    expect(list.mock.calls.map(([page, size]) => [page, size])).toEqual([[1, 100], [2, 100]])
    const keySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(keySelect.props('options')).toHaveLength(101)
    expect(keySelect.props('options')).toContainEqual({ value: 100, label: 'key-100' })
    wrapper.unmount()
  })

  it('propagates and resets the native compaction filter across page requests', async () => {
    const wrapper = mountUsageView()
    await flushPromises()

    expect((wrapper.vm as any).compactionOptions).toEqual([
      { value: null, label: 'All Requests' },
      { value: true, label: 'Compaction Only' },
    ])

    query.mockClear()
    getStats.mockClear()
    getDashboardModels.mockClear()
    getDashboardSnapshotV2.mockClear()

    ;(wrapper.vm as any).filters.native_compaction_v2 = true
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()

    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: true }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(getDashboardModels).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(getDashboardSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))

    query.mockClear()
    getStats.mockClear()
    getDashboardModels.mockClear()
    getDashboardSnapshotV2.mockClear()

    ;(wrapper.vm as any).resetFilters()
    await flushPromises()

    expect((wrapper.vm as any).filters.native_compaction_v2).toBeNull()
    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: null }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
    expect(getDashboardModels).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
    expect(getDashboardSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
  })

  it('exports csv with current filters and without admin-only fields', async () => {
    query.mockResolvedValue({ items: [{ ...usageLog, actual_cost: 0.001953125 }], total: 1, pages: 1 })
    const wrapper = mountUsageView()
    await flushPromises()
    ;(wrapper.vm as any).filters.native_compaction_v2 = true

    let exportedBlob: Blob | null = null
    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn((blob: Blob | MediaSource) => {
      exportedBlob = blob as Blob
      return 'blob:usage-export'
    }) as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(exportedBlob).not.toBeNull()
    expect(query).toHaveBeenCalledWith(expect.objectContaining({
      page_size: EXPORT_PAGE_SIZE,
      sort_by: 'created_at',
      sort_order: 'desc',
      native_compaction_v2: true,
    }))
    expect(clickSpy).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()
    expect(csvContent.startsWith('\uFEFF')).toBe(true)
    expect(csvContent.slice(1)).toBe([
      'Time,API Key Name,Model,Reasoning Effort,Inbound Endpoint,IP Address,Type,Billing Mode,Input Tokens,Output Tokens,Cache Read Tokens,Cache Creation Tokens,Rate Multiplier,Billed Cost,Original Cost,First Token (ms),Duration (ms)',
      '2026-03-08T00:00:00Z,demo-key,gpt-5.4,-,,203.0.113.10,Sync,Token,4057,101,278272,4,1,0.00195313,0.09288300,12,345',
    ].join('\n'))
    expect(csvContent).not.toContain('Cache Rate')
    expect(csvContent).toContain('IP Address')
    expect(csvContent).toContain('203.0.113.10')
    expect(csvContent).toContain('Billed Cost')
    expect(csvContent).toContain('Original Cost')
    expect(csvContent).not.toContain('Upstream Endpoint')
    expect(csvContent).not.toContain('account_cost')
    expect(csvContent).not.toContain('account_rate_multiplier')

    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
  })

  it('includes cache rate in csv only when users are allowed to see it', async () => {
    appStoreState.cachedPublicSettings = { allow_user_view_error_requests: true, usage_show_cache_rate_to_users: true }
    const wrapper = mountUsageView()
    await flushPromises()

    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(csvContent).toContain('Cache Rate')
    expect(csvContent).toContain('98.6%')
    expect(csvContent).not.toContain('Upstream Response Model')
    appStoreState.cachedPublicSettings = { allow_user_view_error_requests: true }
    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
    wrapper.unmount()
  })

  it('includes the upstream response model in csv only when users are allowed to see it', async () => {
    query.mockResolvedValue({
      items: [{ ...usageLog, upstream_response_model: 'gpt-5.4-actual' }],
      total: 1,
      pages: 1,
    })
    appStoreState.cachedPublicSettings = { usage_show_upstream_response_model_to_users: true }
    const wrapper = mountUsageView()
    await flushPromises()

    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(csvContent).toContain('Upstream Response Model')
    expect(csvContent).toContain('gpt-5.4,gpt-5.4-actual,')
    expect(csvContent).not.toContain('upstream_model')
    appStoreState.cachedPublicSettings = { allow_user_view_error_requests: true }
    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
    wrapper.unmount()
  })

  it('keeps formula-injection protection for dangerous exported values', async () => {
    query.mockResolvedValue({
      items: [{ ...usageLog, api_key: { name: '-1+1' } }],
      total: 1,
      pages: 1,
    })
    const wrapper = mountUsageView()
    await flushPromises()

    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(csvContent).toContain(',"\'-1+1",gpt-5.4,-,')
    expect(showSuccess).toHaveBeenCalled()

    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
    wrapper.unmount()
  })

  it('keeps the initial filters, sort, and filename while exporting multiple pages', async () => {
    const pageResponse = { items: [usageLog], total: EXPORT_PAGE_SIZE + 1, pages: 2 }
    const fullFirstPage = {
      items: Array.from({ length: EXPORT_PAGE_SIZE }, (_, index) => ({ ...usageLog, id: index + 1 })),
      total: EXPORT_PAGE_SIZE + 1,
      pages: 2,
    }
    query.mockResolvedValue(pageResponse)
    const wrapper = mountUsageView()
    await flushPromises()

    const datePicker = wrapper.findComponent(DateRangePicker)
    datePicker.vm.$emit('change', { startDate: '2026-03-01', endDate: '2026-03-08', preset: null })
    await flushPromises()

    let resolveFirstPage!: (value: typeof pageResponse) => void
    const firstPage = new Promise<typeof pageResponse>((resolve) => { resolveFirstPage = resolve })
    query.mockClear()
    query.mockImplementation((params, options) =>
      !options && params.page === 1 ? firstPage : Promise.resolve(pageResponse)
    )
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export')
    window.URL.revokeObjectURL = vi.fn()
    let filename = ''
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () {
      filename = this.download
    })

    try {
      await wrapper.findAll('button').find((button) => button.text() === 'Export CSV')!.trigger('click')
      const initialParams = { ...query.mock.calls[0][0] }
      expect(initialParams).toMatchObject({
        page: 1, page_size: EXPORT_PAGE_SIZE, start_date: '2026-03-01', end_date: '2026-03-08',
        sort_by: 'created_at', sort_order: 'desc',
      })

      const keySelect = wrapper.findAllComponents(Select).find((select) =>
        select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
      )!
      keySelect.vm.$emit('update:modelValue', 1)
      keySelect.vm.$emit('change', 1)
      datePicker.vm.$emit('change', { startDate: '2026-04-01', endDate: '2026-04-08', preset: null })
      wrapper.findComponent(UsageTable).vm.$emit('sort', 'actual_cost', 'asc')
      await flushPromises()
      expect(query).toHaveBeenCalledWith(expect.objectContaining({
        api_key_id: 1, start_date: '2026-04-01', end_date: '2026-04-08',
        sort_by: 'actual_cost', sort_order: 'asc',
      }), expect.anything())

      resolveFirstPage(fullFirstPage)
      await flushPromises()

      const exportCalls = query.mock.calls.filter((call) => call.length === 1)
      expect.soft(exportCalls).toEqual([[initialParams], [{ ...initialParams, page: 2 }]])
      expect.soft(filename).toBe('usage_2026-03-01_to_2026-03-08.csv')
      expect(showSuccess).toHaveBeenCalledWith('Export success')
      expect(showError).not.toHaveBeenCalled()
    } finally {
      window.URL.createObjectURL = originalCreateObjectURL
      window.URL.revokeObjectURL = originalRevokeObjectURL
      clickSpy.mockRestore()
      wrapper.unmount()
    }
  })

  it('waits out a panel rate limit and finishes the export instead of failing', async () => {
    const wrapper = mountUsageView()
    await flushPromises()

    const fullFirstPage = {
      items: Array.from({ length: EXPORT_PAGE_SIZE }, (_, index) => ({ ...usageLog, id: index + 1 })),
      total: EXPORT_PAGE_SIZE + 1,
      pages: 2,
    }
    let secondPageAttempts = 0
    query.mockClear()
    query.mockImplementation((params, options) => {
      if (options) return Promise.resolve({ items: [usageLog], total: EXPORT_PAGE_SIZE + 1, pages: 2 })
      if (params.page === 1) return Promise.resolve(fullFirstPage)
      secondPageAttempts++
      return secondPageAttempts === 1
        ? Promise.reject({ status: 429, code: 'RATE_LIMITED', retry_after_seconds: 2 })
        : Promise.resolve({ items: [{ ...usageLog, id: EXPORT_PAGE_SIZE + 1 }], total: EXPORT_PAGE_SIZE + 1, pages: 2 })
    })
    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.useFakeTimers()

    try {
      const exporting = (wrapper.vm as any).exportToCSV()
      await vi.advanceTimersByTimeAsync(0)
      expect((wrapper.vm as any).exportProgress).toMatchObject({
        show: true,
        current: EXPORT_PAGE_SIZE,
        total: EXPORT_PAGE_SIZE + 1,
        notice: 'usage.exportRateLimitedWait',
      })
      expect(wrapper.findComponent(ExportProgressDialog).props('notice')).toBe('usage.exportRateLimitedWait')
      expect(clickSpy).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(2_000)
      await exporting

      const exportPages = query.mock.calls.filter((call) => call.length === 1).map(([params]) => params.page)
      expect(exportPages).toEqual([1, 2, 2])
      expect(csvContent.split('\n')).toHaveLength(EXPORT_PAGE_SIZE + 2)
      expect(clickSpy).toHaveBeenCalledTimes(1)
      expect(showSuccess).toHaveBeenCalledWith('Export success')
      expect(showError).not.toHaveBeenCalled()
      expect((wrapper.vm as any).exportProgress.show).toBe(false)
    } finally {
      vi.useRealTimers()
      vi.unstubAllGlobals()
      window.URL.createObjectURL = originalCreateObjectURL
      window.URL.revokeObjectURL = originalRevokeObjectURL
      clickSpy.mockRestore()
      wrapper.unmount()
    }
  })

  it('cancels an export from the progress dialog without downloading or reporting a failure', async () => {
    const wrapper = mountUsageView()
    await flushPromises()

    query.mockClear()
    query.mockImplementation((_params, options) => options
      ? Promise.resolve({ items: [usageLog], total: 1, pages: 1 })
      : Promise.reject({ status: 429, code: 'RATE_LIMITED', retry_after_seconds: 30 }))
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.useFakeTimers()

    try {
      const exporting = (wrapper.vm as any).exportToCSV()
      await vi.advanceTimersByTimeAsync(0)
      expect((wrapper.vm as any).exportProgress.notice).toBe('usage.exportRateLimitedWait')

      wrapper.findComponent(ExportProgressDialog).vm.$emit('cancel')
      await vi.advanceTimersByTimeAsync(0)
      await exporting

      expect(query.mock.calls.filter((call) => call.length === 1)).toHaveLength(1)
      expect(clickSpy).not.toHaveBeenCalled()
      expect(showError).not.toHaveBeenCalled()
      expect(showInfo).toHaveBeenCalledWith('usage.exportCancelled')
      expect((wrapper.vm as any).exporting).toBe(false)
      expect((wrapper.vm as any).exportProgress.show).toBe(false)
    } finally {
      vi.useRealTimers()
      clickSpy.mockRestore()
      wrapper.unmount()
    }
  })

  it('exports historical image rows with image billing mode derived from image_count', async () => {
    query.mockResolvedValue({
      items: [
        {
          ...usageLog,
          request_id: 'req-user-export-legacy-image',
          actual_cost: 0.2,
          total_cost: 0.2,
          input_cost: 0,
          output_cost: 0,
          cache_creation_cost: 0,
          cache_read_cost: 0,
          input_tokens: 0,
          output_tokens: 0,
          cache_creation_tokens: 0,
          cache_read_tokens: 0,
          image_count: 1,
          model: 'gpt-image-2',
          billing_mode: null,
          ip_address: null,
        },
      ],
      total: 1,
      pages: 1,
    })

    const wrapper = mountUsageView()
    await flushPromises()

    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(csvContent).toContain('Billing Mode')
    expect(csvContent).toContain('Image')
    expect(csvContent).not.toContain(',Token,0,0,0,0,')

    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
  })
})

describe('UsageView subscription feature flag', () => {
  afterEach(() => {
    appStoreState.cachedPublicSettings = { allow_user_view_error_requests: true }
  })

  function billingTypeSelect(wrapper: ReturnType<typeof mountUsageView>) {
    return wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'Subscription')
    )
  }

  it('offers the balance / subscription billing-type filter by default', async () => {
    const wrapper = mountUsageView()
    await flushPromises()

    expect(billingTypeSelect(wrapper)).toBeDefined()
    expect(wrapper.text()).toContain('Billing type')
    wrapper.unmount()
  })

  it('hides the billing-type filter entirely when subscriptions are disabled', async () => {
    appStoreState.cachedPublicSettings = { allow_user_view_error_requests: true, subscription_enabled: false }

    const wrapper = mountUsageView()
    await flushPromises()

    expect(billingTypeSelect(wrapper)).toBeUndefined()
    expect(wrapper.text()).not.toContain('Billing type')
    wrapper.unmount()
  })
})
