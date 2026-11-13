import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import UsageView from '../UsageView.vue'
import UsageFilters from '@/components/admin/usage/UsageFilters.vue'
import Pagination from '@/components/common/Pagination.vue'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const api = vi.hoisted(() => ({ list: vi.fn(), stats: vi.fn(), export: vi.fn(), models: vi.fn(), snapshot: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: {}, showError: vi.fn(), showSuccess: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  usage: { list: api.list, getStats: api.stats, searchUsers: vi.fn().mockResolvedValue([]), searchApiKeys: vi.fn().mockResolvedValue([]) },
  dashboard: { getModelStats: api.models, getSnapshotV2: api.snapshot },
  groups: { list: vi.fn().mockResolvedValue({ items: [{ id: 7, name: 'Synthetic group' }] }) },
} }))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { list: api.export } }))
vi.mock('xlsx', () => ({ utils: { aoa_to_sheet: vi.fn(() => ({})), sheet_add_aoa: vi.fn(), book_new: vi.fn(() => ({})), book_append_sheet: vi.fn() }, write: vi.fn(() => new Uint8Array()) }))
vi.mock('file-saver', () => ({ saveAs: vi.fn() }))

let wrapper: VueWrapper
function render(locale = 'en', observerMode = false) {
  wrapper = mount(UsageView, { props: { observerMode }, global: {
    plugins: [createPinia(), createI18n({ legacy: false, locale, messages: { zh, en } })],
    stubs: { AppLayout: { template: '<main><slot /></main>' }, Teleport: true,
      UsageStatsCards: true, UsageTable: true, UsageCleanupDialog: true, UsageExportProgress: true,
      UserBalanceHistoryModal: true, OpsErrorLogTable: true, OpsErrorDetailModal: true,
      UsageConnections: true, LatencyObservabilityPanel: true, UserTokenRanking: true,
      TokenUsageTrend: true, ModelDistributionChart: true, GroupDistributionChart: true, EndpointDistributionChart: true,
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
    localStorage.clear()
    setActivePinia(createPinia())
    vi.clearAllMocks()
    api.list.mockResolvedValue({ items: [], total: 220, pages: 11 })
    api.stats.mockResolvedValue({ total_requests: 220 })
    api.models.mockResolvedValue({ models: [] })
    api.snapshot.mockResolvedValue({ trend: [], models: [], groups: [] })
    api.export.mockResolvedValue({ items: [], total: 0, pages: 0 })
  })
  afterEach(() => wrapper?.unmount())

  it.each(['en', 'zh'])('offers every supported platform, default all, and localized labels (%s)', async locale => {
    render(locale)
    await flushPromises()
    const filter = wrapper.get('[data-testid="usage-platform-filter"]')
    expect(filter.get('.select-trigger').text()).toBe(locale === 'en' ? 'All platforms' : '全部平台')
    expect(api.list.mock.lastCall?.[0].platform).toBeFalsy()
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
    expect(api.list.mock.lastCall?.[0].group_id).toBe(7)
    await choose(wrapper.get('[data-testid="usage-platform-filter"]'), 'OpenAI')
    filters.vm.$emit('reset')
    await flushPromises()
    expect(api.list.mock.lastCall?.[0].platform).toBeFalsy()
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
