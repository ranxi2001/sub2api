import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import V3SettingsPanel from '../V3SettingsPanel.vue'
import ComponentEditorDialog from '../ComponentEditorDialog.vue'
import type { MonitorV3Component, MonitorV3Settings } from '@/api/channelMonitorV3'

const mocks = vi.hoisted(() => ({
  getSettings: vi.fn(), getStatus: vi.fn(), updateConfig: vi.fn(), reorder: vi.fn(), deleteCategory: vi.fn(),
  deleteComponent: vi.fn(), createComponent: vi.fn(), updateComponent: vi.fn(), createCategory: vi.fn(), updateCategory: vi.fn(),
  groups: vi.fn(), error: vi.fn(), success: vi.fn(), mode: 'v2',
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true }),
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: { channel_monitor_enabled: true }, showError: mocks.error, showSuccess: mocks.success }),
}))
vi.mock('@/utils/featureFlags', () => ({ getChannelMonitorMode: () => mocks.mode }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 7 } }) }))
vi.mock('@/api/admin/groups', () => ({ getModelAllowlistCandidates: async () => ['sol', 'astra', 'terra'] }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getAll: mocks.groups } } }))
vi.mock('@/api/channelMonitorV3', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/channelMonitorV3')>()),
  getSettings: mocks.getSettings, getStatus: mocks.getStatus, updateConfig: mocks.updateConfig, reorder: mocks.reorder,
  deleteCategory: mocks.deleteCategory, deleteComponent: mocks.deleteComponent, createComponent: mocks.createComponent,
  updateComponent: mocks.updateComponent, createCategory: mocks.createCategory, updateCategory: mocks.updateCategory,
}))

const SelectStub = {
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<div class="select-stub"><button v-for="o in options" :key="String(o.value)" type="button" :data-value="String(o.value)" @click="$emit(\'update:modelValue\', o.value)">{{ o.label }}</button></div>',
}
const DialogStub = { props: ['show', 'title'], template: '<div v-if="show" class="dialog-stub"><slot /><slot name="footer" /></div>' }
const ConfirmStub = {
  props: ['show', 'message'],
  emits: ['confirm', 'cancel'],
  template: '<div v-if="show" data-testid="confirm"><span>{{ message }}</span><button type="button" data-testid="confirm-yes" @click="$emit(\'confirm\')">ok</button></div>',
}
const stubs = { Select: SelectStub, BaseDialog: DialogStub, ConfirmDialog: ConfirmStub, Toggle: true, Icon: true, StatusPage: true, IncidentsDialog: true, RouterLink: true }

function component(id: number, categoryId: number | null, extra: Partial<MonitorV3Component> = {}): MonitorV3Component {
  return {
    id, category_id: categoryId, name: `Component ${id}`, description: '', group_id: 40 + id, model: '', degraded_ttft_ms: 0,
    show_multiplier: true, visibility: 'group', enabled: true, sort_order: id, group_name: `Group ${id}`, group_platform: 'openai',
    group_status: 'active', group_rate_multiplier: 0.2, group_deleted: false, created_at: '', updated_at: '', ...extra,
  }
}

function settings(extra: Partial<MonitorV3Settings> = {}): MonitorV3Settings {
  return {
    config: {
      version: 3, interval_minutes: 5, cells: 90, availability_range: '7d', down_error_rate: 0.2, degraded_error_rate: 0.05,
      degraded_ttft_ms: 10000, min_requests: 1, ignored_error_categories: ['client_cancelled'], featured_component_id: 4,
      footer_note: '', updated_at: '',
    },
    categories: [
      { id: 1, name: 'GPT', description: 'OpenAI', sort_order: 0, created_at: '', updated_at: '' },
      { id: 2, name: 'Claude', description: '', sort_order: 1, created_at: '', updated_at: '' },
    ],
    components: [component(1, 1, { model: 'gpt-5.5' }), component(2, 1, { enabled: false }), component(3, 2), component(4, null)],
    error_categories: ['client_cancelled', 'timeout', 'upstream_5xx'],
    data_through: new Date().toISOString(),
    idle_probe: { enabled: false, groups: [] },
    idle_probe_stats: [],
    ...extra,
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  mocks.mode = 'v2'
  mocks.getSettings.mockImplementation(async () => settings())
  mocks.getStatus.mockResolvedValue(null)
  mocks.groups.mockResolvedValue([{ id: 41, name: 'Group 1', platform: 'openai', status: 'active', rate_multiplier: 0.2 }])
})

describe('V3 settings panel', () => {
  it('lists categories with their components and the uncategorized featured one', async () => {
    const wrapper = mount(V3SettingsPanel, { global: { stubs } })
    await flushPromises()
    const sections = wrapper.findAll('[data-testid="monitor-v3-section"]')
    expect(sections.map((section) => section.findAll('[data-testid="monitor-v3-admin-component"]').length)).toEqual([2, 1, 1])
    expect(sections[0].text()).toContain('gpt-5.5')
    expect(sections[0].text()).toContain('channelMonitorV3.admin.allModels')
    expect(sections[2].text()).toContain('channelMonitorV3.admin.uncategorized')
    expect(sections[2].text()).toContain('channelMonitorV3.admin.featuredBadge')
    expect(wrapper.get('[data-testid="monitor-v3-mode-banner"]').text()).toContain('channelMonitorV3.admin.modeBannerV2')
    expect(wrapper.get('[data-testid="monitor-v3-freshness"]').text()).toContain('channelMonitorV3.page.dataThrough')
    expect(mocks.getStatus).toHaveBeenCalledWith(null, true)
  })

  it('warns when the passive statistics are missing or stale and hides the banner in V3', async () => {
    mocks.mode = 'v3'
    mocks.getSettings.mockImplementation(async () => settings({ data_through: null }))
    let wrapper = mount(V3SettingsPanel, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="monitor-v3-mode-banner"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="monitor-v3-freshness"]').text()).toBe('channelMonitorV3.admin.noFacts')
    wrapper.unmount()

    mocks.getSettings.mockImplementation(async () => settings({ data_through: new Date(Date.now() - 3600_000).toISOString() }))
    wrapper = mount(V3SettingsPanel, { global: { stubs } })
    await flushPromises()
    expect(wrapper.get('[data-testid="monitor-v3-freshness"]').text()).toContain('channelMonitorV3.admin.staleFacts')
  })

  it('saves the page rules with the version it loaded', async () => {
    mocks.updateConfig.mockImplementation(async (config) => ({ ...config, version: 4 }))
    const wrapper = mount(V3SettingsPanel, { global: { stubs } })
    await flushPromises()
    const save = wrapper.get('[data-testid="monitor-v3-save-config"]')
    expect(save.attributes('disabled')).toBeDefined()
    await wrapper.get('#monitor-v3-down-rate').setValue('30')
    await wrapper.get('#monitor-v3-degraded-rate').setValue('8')
    await wrapper.get('#monitor-v3-min').setValue('5')
    await wrapper.get('[data-testid="monitor-v3-ttft"] [data-value="15000"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-range"] [data-value="24h"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-interval"] [data-value="1"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-ignored"] [data-category="client_cancelled"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-ignored"] [data-category="timeout"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-featured-select"] [data-value="null"]').trigger('click')
    await save.trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      version: 3, down_error_rate: 0.3, degraded_error_rate: 0.08, min_requests: 5, degraded_ttft_ms: 15000,
      availability_range: '24h', interval_minutes: 1, ignored_error_categories: ['timeout'], featured_component_id: null,
    }))
    expect(wrapper.get('[data-testid="monitor-v3-save-config"]').attributes('disabled')).toBeDefined()
  })

  it('reorders inside a category without disturbing the others', async () => {
    const wrapper = mount(V3SettingsPanel, { global: { stubs } })
    await flushPromises()
    const first = wrapper.findAll('[data-testid="monitor-v3-admin-component"]')[0]
    await first.findAll('button')[1].trigger('click') // move down
    await flushPromises()
    expect(mocks.reorder).toHaveBeenCalledWith([], [2, 1, 3, 4])
    await wrapper.findAll('[data-testid="monitor-v3-section"]')[0].findAll('button')[1].trigger('click') // category down
    await flushPromises()
    expect(mocks.reorder).toHaveBeenLastCalledWith([2, 1], [])
  })

  it('asks before deleting', async () => {
    const wrapper = mount(V3SettingsPanel, { global: { stubs } })
    await flushPromises()
    const row = wrapper.findAll('[data-testid="monitor-v3-admin-component"]')[2]
    await row.findAll('button').at(-1)!.trigger('click')
    expect(wrapper.get('[data-testid="confirm"]').text()).toContain('Component 3')
    await wrapper.get('[data-testid="confirm-yes"]').trigger('click')
    await flushPromises()
    expect(mocks.deleteComponent).toHaveBeenCalledWith(3)
  })
})

describe('V3 component editor', () => {
  const props = { show: true, component: null, categories: settings().categories, groups: [{ id: 41, name: 'Group 1', platform: 'openai', status: 'active', rate_multiplier: 0.2 }] as never[], defaultCategoryId: 1, inheritedTtftMs: 10000 }

  it('creates a component for a whole group', async () => {
    mocks.createComponent.mockImplementation(async (input) => ({ ...component(9, 1), ...input }))
    const wrapper = mount(ComponentEditorDialog, { props, global: { stubs } })
    expect(wrapper.get('[data-testid="monitor-v3-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="monitor-v3-name"]').setValue('  Codex Pro  ')
    await wrapper.get('[data-testid="monitor-v3-group"] [data-value="41"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-threshold"] [data-value="30000"]').trigger('click')
    await wrapper.get('[data-testid="monitor-v3-visibility-public"]').trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.createComponent).toHaveBeenCalledWith({
      category_id: 1, name: 'Codex Pro', description: '', group_id: 41, model: '', models: [], degraded_ttft_ms: 30000,
      show_multiplier: true, visibility: 'public', enabled: true,
    })
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('edits an existing component in place', async () => {
    mocks.updateComponent.mockImplementation(async (_id, input) => ({ ...component(3, 2), ...input }))
    const wrapper = mount(ComponentEditorDialog, { props: { ...props, component: component(3, 2, { visibility: 'public', model: 'claude-opus-5-5' }) }, global: { stubs } })
    await wrapper.get('input[value="claude-opus-5-5"]').setValue(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updateComponent).toHaveBeenCalledWith(3, expect.objectContaining({ visibility: 'public', category_id: 2, group_id: 43, model: '', models: [] }))
  })

  it('saves two selected group models and keeps the first as the idle probe model', async () => {
    const wrapper = mount(ComponentEditorDialog, { props: { ...props, component: component(1, 1) }, global: { stubs } })
    await flushPromises()
    expect(wrapper.findComponent({ name: 'NewFeatureBadge' }).props('noticeId')).toBe(NEW_FEATURE_NOTICES.monitorV3ComponentModels)
    await wrapper.get('input[value="sol"]').setValue(true)
    await wrapper.get('input[value="astra"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    expect(mocks.updateComponent).toHaveBeenCalledWith(1, expect.objectContaining({ model: 'sol', models: ['sol', 'astra'] }))
  })
})
