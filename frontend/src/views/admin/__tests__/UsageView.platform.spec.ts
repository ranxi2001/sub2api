import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import UsageView from '../UsageView.vue'
import UsageFilters from '@/components/admin/usage/UsageFilters.vue'
import Pagination from '@/components/common/Pagination.vue'
import ModelDistributionChart from '@/components/charts/ModelDistributionChart.vue'
import GroupDistributionChart from '@/components/charts/GroupDistributionChart.vue'
import EndpointDistributionChart from '@/components/charts/EndpointDistributionChart.vue'
import UserBreakdownSubTable from '@/components/charts/UserBreakdownSubTable.vue'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const api = vi.hoisted(() => ({ list: vi.fn(), stats: vi.fn(), export: vi.fn(), models: vi.fn(), snapshot: vi.fn(), breakdown: vi.fn() }))
vi.mock('vue-chartjs', () => ({ Doughnut: { template: '<div />' } }))
vi.mock('@/api/admin/dashboard', () => ({ getUserBreakdown: api.breakdown }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: {}, showError: vi.fn(), showSuccess: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  usage: { list: api.list, getStats: api.stats, searchUsers: vi.fn().mockResolvedValue([]), searchApiKeys: vi.fn().mockResolvedValue([]) },
  dashboard: { getModelStats: api.models, getSnapshotV2: api.snapshot },
  groups: { list: vi.fn().mockResolvedValue({ items: [{ id: 7, name: 'Synthetic group' }] }) },
} }))
vi.mock('@/api/admin/ops', () => ({ listErrorLogs: vi.fn().mockResolvedValue({ items: [], total: 0 }) }))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { list: api.export } }))
vi.mock('xlsx', () => ({ utils: { aoa_to_sheet: vi.fn(() => ({})), sheet_add_aoa: vi.fn(), book_new: vi.fn(() => ({})), book_append_sheet: vi.fn() }, write: vi.fn(() => new Uint8Array()) }))
vi.mock('file-saver', () => ({ saveAs: vi.fn() }))

let wrapper: VueWrapper
function render(locale = 'en', observerMode = false, realCharts = false) {
  wrapper = mount(UsageView, { props: { observerMode }, global: {
    plugins: [createPinia(), createI18n({ legacy: false, locale, messages: { zh, en } })],
    stubs: { AppLayout: { template: '<main><slot /></main>' }, Teleport: true,
      UsageStatsCards: true, UsageTable: true, UsageCleanupDialog: true, UsageExportProgress: true,
      UserBalanceHistoryModal: true, OpsErrorLogTable: true, OpsErrorDetailModal: true,
      UsageConnections: true, LatencyObservabilityPanel: true, UserTokenRanking: true,
      TokenUsageTrend: true, ModelDistributionChart: !realCharts, GroupDistributionChart: !realCharts, EndpointDistributionChart: !realCharts,
    },
  } })
  return wrapper
}
async function choose(container: ReturnType<VueWrapper['get']>, label: string) {
  await container.get('.select-trigger').trigger('click')
  const option = container.findAll('[role="option"]').find(el => el.text() === label)
  expect(option, `option ${label}`).toBeDefined()
  await option!.trigger('click')
  await flushPromises()
}

describe('admin usage platform behavior with real filters', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.list.mockResolvedValue({ items: [], total: 220, pages: 11 })
    api.stats.mockResolvedValue({ total_requests: 220 })
    api.models.mockResolvedValue({ models: [] })
    api.snapshot.mockResolvedValue({ trend: [], models: [], groups: [] })
    api.export.mockResolvedValue({ items: [], total: 0, pages: 0 })
  })
  afterEach(() => {
    wrapper?.unmount()
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it.each(['en', 'zh'])('offers every supported platform, default all, and localized labels (%s)', async locale => {
    render(locale)
    await flushPromises()
    const filter = wrapper.get('[data-testid="usage-platform-filter"]')
    expect(filter.get('.select-trigger').text()).toBe(locale === 'en' ? 'All platforms' : '全部平台')
    expect(api.list.mock.lastCall?.[0].platform).toBeFalsy()
    expect(api.list.mock.lastCall?.[0].exact_total).toBe(false)
    await filter.get('.select-trigger').trigger('click')
    const options = filter.findAll('[role="option"]').map(el => el.text())
    for (const platform of CONCRETE_PLATFORM_OPTIONS) expect(options).toContain(platform.label)
    expect(options).toHaveLength(CONCRETE_PLATFORM_OPTIONS.length + 2)
    expect(options).not.toContain('Composite')
  })

  it('combines group and identity filters, resets pagination, and exports the applied server query', async () => {
    render()
    await flushPromises()
    const vm = wrapper.vm as any
    const filters = wrapper.findComponent(UsageFilters)
    Object.assign(filters.props('modelValue'), { user_id: 42, api_key_id: 8, account_id: 9, model: 'fixture-model', group_id: 7 })
    wrapper.findComponent(Pagination).vm.$emit('update:page', 3)
    await flushPromises()
    expect(api.list.mock.lastCall?.[0].page).toBe(3)
    for (const option of [...CONCRETE_PLATFORM_OPTIONS, { value: 'unknown', label: 'Unknown (legacy)' }]) {
      await choose(wrapper.get('[data-testid="usage-platform-filter"]'), option.label)
      const expected = { platform: option.value, group_id: 7, user_id: 42, api_key_id: 8, account_id: 9, model: 'fixture-model' }
      expect(api.list.mock.lastCall?.[0]).toMatchObject({ ...expected, page: 1, exact_total: true })
      expect(api.stats.mock.lastCall?.[0]).toMatchObject(expected)
      expect(api.models.mock.lastCall?.[0]).toMatchObject({ platform: option.value })
      expect(api.snapshot.mock.lastCall?.[0]).toMatchObject({ platform: option.value })
      await vm.exportToExcel()
      expect(api.export.mock.lastCall?.[0]).toMatchObject({ ...api.list.mock.lastCall?.[0], page_size: 100 })
    }
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'All platforms')
    expect(api.list.mock.lastCall?.[0].platform).toBeFalsy()
    expect(api.list.mock.lastCall?.[0].exact_total).toBe(false)
    expect(api.list.mock.lastCall?.[0].group_id).toBe(7)
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'OpenAI')
    filters.vm.$emit('reset')
    await flushPromises()
    expect(api.list.mock.lastCall?.[0].platform).toBeFalsy()
    expect(api.list.mock.lastCall?.[0].exact_total).toBe(false)
    expect(api.list.mock.lastCall?.[0].group_id).toBeUndefined()
  })

  it('pins platform and group across every export page while the visible filters change', async () => {
    render()
    await flushPromises()
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'OpenAI')
    const vm = wrapper.vm as any
    vm.filters.group_id = 7
    let release!: (value: unknown) => void
    api.export.mockReturnValueOnce(new Promise(resolve => { release = resolve }))
    api.export.mockResolvedValueOnce({ items: [{ model: 'fixture-model', total_cost: 1 }], total: 101 })
    const exporting = vm.exportToExcel()
    await flushPromises()
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'Gemini')
    vm.filters.group_id = 8
    release({ items: Array.from({ length: 100 }, () => ({ model: 'fixture-model', total_cost: 1 })), total: 101 })
    await exporting
    expect(api.export).toHaveBeenCalledTimes(2)
    for (const [index, call] of api.export.mock.calls.entries()) {
      expect(call[0]).toMatchObject({ platform: 'openai', group_id: 7, page: index + 1, page_size: 100, exact_total: true })
    }
  })

  it.each([
    ['model', ModelDistributionChart],
    ['group', GroupDistributionChart],
    ['endpoint', EndpointDistributionChart],
  ])('clears expanded %s breakdowns and fences delayed responses across platform and scope changes', async (_name, component) => {
    const totals = { requests: 1, total_tokens: 30, input_tokens: 10, output_tokens: 20, cache_creation_tokens: 0, cache_read_tokens: 0, cost: 2, actual_cost: 1 }
    api.models.mockResolvedValue({ models: [{ ...totals, model: 'fixture-model' }] })
    api.snapshot.mockResolvedValue({ trend: [], groups: [{ ...totals, group_id: 7, group_name: 'Synthetic group' }] })
    api.stats.mockResolvedValue({ total_requests: 1, endpoints: [{ ...totals, endpoint: '/v1/responses' }] })
    const user = (email: string) => ({ ...totals, user_id: 42, email })
    api.breakdown.mockResolvedValueOnce({ users: [user('before@example.invalid')] })
    render('en', false, true)
    await vi.advanceTimersByTimeAsync(120)
    await flushPromises()
    const chart = wrapper.getComponent(component as any)
    const row = () => chart.get('tbody tr')
    await row().trigger('click')
    await flushPromises()
    expect(chart.text()).toContain('before@example.invalid')
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'OpenAI')
    expect(chart.findComponent(UserBreakdownSubTable).exists()).toBe(false)
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'All platforms')
    expect(chart.findComponent(UserBreakdownSubTable).exists()).toBe(false)

    let releaseOld!: (value: unknown) => void
    api.breakdown.mockReturnValueOnce(new Promise(resolve => { releaseOld = resolve }))
    await row().trigger('click')
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'OpenAI')
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'All platforms')
    api.breakdown.mockResolvedValueOnce({ users: [user('current@example.invalid')] })
    await row().trigger('click')
    await flushPromises()
    releaseOld({ users: [user('stale@example.invalid')] })
    await flushPromises()
    expect(chart.text()).toContain('current@example.invalid')
    expect(chart.text()).not.toContain('stale@example.invalid')

    await row().trigger('click')
    let releaseScope!: (value: unknown) => void
    api.breakdown.mockReturnValueOnce(new Promise(resolve => { releaseScope = resolve }))
    await row().trigger('click')
    ;(wrapper.vm as any).filters.user_id = 43
    wrapper.findComponent(UsageFilters).vm.$emit('change')
    await flushPromises()
    releaseScope({ users: [user('stale-scope@example.invalid')] })
    await flushPromises()
    expect(chart.findComponent(UserBreakdownSubTable).exists()).toBe(false)
  })

  it('disables upstream model breakdown when requested A and B share upstream U but only A is selected', async () => {
    const records = [{ model: 'A', user: 'a@example.invalid' }, { model: 'B', user: 'b@example.invalid' }]
    const totals = { requests: 1, total_tokens: 30, input_tokens: 10, output_tokens: 20, cache_creation_tokens: 0, cache_read_tokens: 0, cost: 2, actual_cost: 1 }
    api.models.mockImplementation(async params => {
      const selected = records.filter(row => !params.model || row.model === params.model)
      return { models: params.model_source === 'upstream'
        ? [{ ...totals, model: 'U', requests: selected.length, total_tokens: selected.length * 30 }]
        : selected.map(row => ({ ...totals, model: row.model })) }
    })
    api.breakdown.mockResolvedValue({ users: records.map((row, i) => ({ ...totals, user_id: i + 1, email: row.user })) })
    render('en', false, true)
    await vi.advanceTimersByTimeAsync(120)
    await flushPromises()
    const chart = wrapper.getComponent(ModelDistributionChart)
    ;(wrapper.vm as any).filters.model = 'A'
    wrapper.findComponent(UsageFilters).vm.$emit('change')
    await flushPromises()
    chart.vm.$emit('update:source', 'upstream')
    await flushPromises()
    expect(api.models.mock.lastCall?.[0]).toMatchObject({ model: 'A', model_source: 'upstream' })
    expect(chart.props('upstreamModelStats')[0]).toMatchObject({ model: 'U', requests: 1 })
    await chart.get('tbody tr').trigger('click')
    await flushPromises()
    expect(api.breakdown).not.toHaveBeenCalled()
    expect(chart.findComponent(UserBreakdownSubTable).exists()).toBe(false)
    ;(wrapper.vm as any).filters.model = undefined
    wrapper.findComponent(UsageFilters).vm.$emit('change')
    await flushPromises()
    expect(chart.props('upstreamModelStats')[0]).toMatchObject({ model: 'U', requests: 2 })
    await chart.get('tbody tr').trigger('click')
    await flushPromises()
    expect(chart.text()).toContain('a@example.invalid')
    expect(chart.text()).toContain('b@example.invalid')
  })

  it('clears the detail-only platform when switching tabs and disables unsupported cleanup', async () => {
    render()
    await flushPromises()
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'OpenAI')
    const cleanup = wrapper.findComponent(UsageFilters).findAll('button').find(b => b.classes().includes('btn-danger'))!
    expect(cleanup.attributes('disabled')).toBeDefined()
    const tabs = wrapper.findAll('[data-testid="usage-detail-tab"]')
    await tabs[1].trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="usage-platform-filter"]').exists()).toBe(false)
    expect(api.stats.mock.lastCall?.[0].platform).toBeFalsy()
    await tabs[0].trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="usage-platform-filter"] .select-trigger').text()).toBe('All platforms')
  })
})
