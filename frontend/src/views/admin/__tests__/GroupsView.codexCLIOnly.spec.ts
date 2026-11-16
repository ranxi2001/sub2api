import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const mocks = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: mocks.listGroups,
      create: mocks.createGroup,
      update: mocks.updateGroup,
      getModelAllowlistCandidates: mocks.getModelAllowlistCandidates,
      getUsageSummary: mocks.getUsageSummary,
      getCapacitySummary: mocks.getCapacitySummary,
      getLiveCapability: mocks.getLiveCapability,
      getAll: vi.fn().mockResolvedValue([]),
      duplicate: vi.fn(),
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: { list: vi.fn(), getById: vi.fn() }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: vi.fn(() => false), nextStep: vi.fn() })
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const baseGroup = {
  id: 42, name: 'Codex', description: null, platform: 'openai', rate_multiplier: 1, rpm_limit: 0,
  is_exclusive: false, status: 'active', subscription_type: 'standard',
  daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null,
  allow_image_generation: false, allow_batch_image_generation: false,
  peak_rate_enabled: false, peak_start: '', peak_end: '', peak_rate_multiplier: 1,
  claude_code_only: false, fallback_group_id: null, fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false, require_oauth_only: false, require_privacy_set: false,
  force_openai_fast: false, free_openai_fast: false, free_openai_ultrafast: false, codex_cli_only: true, block_subagents: true,
  model_routing: null, model_routing_enabled: false, mcp_xml_inject: true, supported_model_scopes: [],
  account_count: 1, active_account_count: 1, rate_limited_account_count: 0, sort_order: 10,
  created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z'
} as unknown as AdminGroup

// 同时渲染分组策略标记和编辑按钮。
const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template: `<div><div v-for="row in data" :key="row.id" :data-testid="'group-row-' + row.id">
    <slot name="cell-name" :value="row.name" :row="row" /><slot name="cell-actions" :row="row" />
  </div></div>`
})

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: defineComponent({ template: '<main><slot /></main>' }),
        TablePageLayout: defineComponent({ template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>' }),
        DataTable: DataTableStub,
        BaseDialog: defineComponent({ props: { show: Boolean }, template: '<div v-if="show"><slot /><slot name="footer" /></div>' }),
        Pagination: true, ConfirmDialog: true, EmptyState: true, Select: true, PlatformIcon: true, Icon: true,
        GroupCapacityBadge: true, GroupRateMultipliersModal: true, GroupRPMOverridesModal: true, VueDraggable: true
      }
    }
  })
}

const buttonByText = (wrapper: ReturnType<typeof mountView>, text: string) =>
  wrapper.findAll('button').find((button) => button.text() === text)

describe('GroupsView codex-cli-only switch', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.clearAllMocks()
    mocks.listGroups.mockResolvedValue({ items: [baseGroup, { ...baseGroup, id: 43, name: 'Normal', codex_cli_only: false }], total: 2, page: 1, page_size: 20, pages: 1 })
    mocks.createGroup.mockResolvedValue(baseGroup)
    mocks.updateGroup.mockResolvedValue(baseGroup)
    mocks.getModelAllowlistCandidates.mockResolvedValue([])
    mocks.getUsageSummary.mockResolvedValue([])
    mocks.getCapacitySummary.mockResolvedValue([])
    mocks.getLiveCapability.mockResolvedValue({ supported: false })
  })
  afterEach(() => vi.restoreAllMocks())

  it('marks codex-cli-only groups in the list', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="group-row-42"] [data-testid="group-codex-cli-only-badge"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="group-row-43"] [data-testid="group-codex-cli-only-badge"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('creates a group with the switch turned on', async () => {
    const wrapper = mountView()
    await flushPromises()
    await buttonByText(wrapper, 'admin.groups.createGroup')!.trigger('click')
    await flushPromises()

    const toggle = wrapper.get('[data-testid="create-codex-cli-only"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    await toggle.trigger('click')
    await wrapper.get('#create-group-form input[type="text"]').setValue('Codex')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(mocks.createGroup).toHaveBeenCalledWith(expect.objectContaining({ name: 'Codex', codex_cli_only: true }))
    wrapper.unmount()
  })

  it('loads the saved value when editing and sends the change', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="group-row-42"]').findAll('button').find((b) => b.text() === 'common.edit')!.trigger('click')
    await flushPromises()

    const toggle = wrapper.get('[data-testid="edit-codex-cli-only"]')
    expect(toggle.attributes('aria-checked')).toBe('true')
    await toggle.trigger('click')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(mocks.updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({ codex_cli_only: false }))
    wrapper.unmount()
  })

  it('creates a group with subagents blocked independently of the client gate', async () => {
    const wrapper = mountView()
    await flushPromises()
    await buttonByText(wrapper, 'admin.groups.createGroup')!.trigger('click')
    await flushPromises()
    const toggle = wrapper.get('[data-testid="create-block-subagents"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    await toggle.trigger('click')
    await wrapper.get('#create-group-form input[type="text"]').setValue('Usage controls')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(mocks.createGroup).toHaveBeenCalledWith(expect.objectContaining({ block_subagents: true, codex_cli_only: false }))
    wrapper.unmount()
  })

  it('loads and disables the saved subagent policy', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="group-row-42"]').findAll('button').find((b) => b.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    const toggle = wrapper.get('[data-testid="edit-block-subagents"]')
    expect(toggle.attributes('aria-checked')).toBe('true')
    await toggle.trigger('click')
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(mocks.updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({ block_subagents: false, codex_cli_only: true }))
    wrapper.unmount()
  })})
