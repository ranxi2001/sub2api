import { createPinia, setActivePinia } from 'pinia'
import { beforeEach as setupPiniaBeforeEach } from 'vitest'
setupPiniaBeforeEach(() => setActivePinia(createPinia()))
import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PelicanTestsView from '../PelicanTestsView.vue'
import Pagination from '@/components/common/Pagination.vue'
import type { PelicanGroupTestPlan, PelicanGroupTestResult } from '@/api/admin/pelicanTests'

const api = vi.hoisted(() => ({
  listPlans: vi.fn(),
  createPlan: vi.fn(),
  updatePlan: vi.fn(),
  deletePlan: vi.fn(),
  runPlan: vi.fn(),
  listResults: vi.fn(),
  getResult: vi.fn(),
  getShowcaseSettings: vi.fn(),
  updateShowcaseSettings: vi.fn(),
}))
const { getGroups, fetchPublicSettings } = vi.hoisted(() => ({ getGroups: vi.fn(), fetchPublicSettings: vi.fn() }))
vi.mock('@/api/admin/pelicanTests', () => ({ pelicanTestsAPI: api }))
vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: getGroups } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ fetchPublicSettings }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, named?: Record<string, unknown>) => (named ? `${key} ${JSON.stringify(named)}` : key) }),
}))

const result = (overrides: Partial<PelicanGroupTestResult> = {}): PelicanGroupTestResult => ({
  id: 90, plan_id: 7, group_id: 4, group_name: 'GPT PRO号池', account_id: 12, account_name: 'pool-b',
  attempts: [{ account_id: 11, account_name: 'pool-a', error: 'API returned 429' }],
  status: 'success', error_message: '', latency_ms: 106600, pelican_config: { prompt: '', reasoning_effort: 'high', parallel_count: 1, model_id: 'gpt-6-astra' },
  cost_usd: 0.012345, cost_incomplete: false,
  started_at: '2026-09-28T04:00:00Z', finished_at: '2026-09-28T04:02:00Z', created_at: '2026-09-28T04:02:00Z',
  ...overrides,
})
const plan = (overrides: Partial<PelicanGroupTestPlan> = {}): PelicanGroupTestPlan => ({
  id: 7, group_id: 4, group_name: 'GPT PRO号池', group_platform: 'openai', group_status: 'active',
  model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: true,
  today_cost_usd: 0.123456, total_cost_usd: 12.345678, today_cost_incomplete: false, total_cost_incomplete: false,
  pelican_config: { question_kind: 'pelican', prompt: 'draw a pelican', reasoning_effort: 'high', parallel_count: 2 },
  last_run_at: '2026-09-28T04:00:00Z', next_run_at: '2026-09-28T04:30:00Z', last_result: result(), created_at: '', updated_at: '',
  ...overrides,
})
const settings = { enabled: false, api_enabled: true, max_items: 20, auto_cleanup: true, retention_days: 7 }

const SelectStub = {
  props: ['modelValue', 'options', 'disabled'],
  emits: ['update:modelValue'],
  template: `<select :disabled="disabled" :value="modelValue" @change="$emit('update:modelValue', options.find((o) => String(o.value) === $event.target.value)?.value)">
    <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>`,
}
const ToggleStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `<input type="checkbox" :checked="modelValue" @change="$emit('update:modelValue', $event.target.checked)" />`,
}

const mountView = () => mount(PelicanTestsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      SmartOpsNav: true,
      RouterLink: RouterLinkStub,
      Icon: true,
      PlatformIcon: true,
      Select: SelectStub,
      Toggle: ToggleStub,
      BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" class="dialog"><h3>{{ title }}</h3><slot /><slot name="footer" /></div>' },
      PelicanShowcaseApiDialog: {
        props: ['show', 'enabled'],
        template: '<div v-if="show" data-testid="api-info-dialog" :data-enabled="enabled" />',
      },
      ConfirmDialog: {
        props: ['show'], emits: ['confirm', 'cancel'],
        template: `<div v-if="show" class="confirm"><button class="confirm-yes" @click="$emit('confirm')" /></div>`,
      },
    },
  },
})

let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  localStorage.clear()
  for (const fn of Object.values(api)) fn.mockReset()
  api.listPlans.mockResolvedValue([plan(), plan({ id: 8, group_id: 5, group_name: '迁移来的分组', model_id: '', enabled: false, last_run_at: null, next_run_at: null, last_result: undefined })])
  api.getShowcaseSettings.mockResolvedValue({ ...settings })
  api.listResults.mockResolvedValue({ items: [result(), result({ id: 89, status: 'failed', account_id: 0, account_name: '', attempts: [], error_message: 'no_available_account: no available accounts' })], total: 2 })
  getGroups.mockReset().mockResolvedValue([
    { id: 4, name: 'GPT PRO号池', platform: 'openai', status: 'active' },
    { id: 6, name: 'Claude Max', platform: 'anthropic', status: 'active' },
    { id: 9, name: 'Paused', platform: 'openai', status: 'disabled' },
  ])
  fetchPublicSettings.mockReset().mockResolvedValue(undefined)
})
afterEach(() => {
  wrapper?.unmount()
  vi.useRealTimers()
})

describe('PelicanTestsView', () => {
  it('shows USD totals beside group names and preserves unknown, zero and partial costs', async () => {
    api.listPlans.mockResolvedValue([plan(), plan({ id: 8, today_cost_usd: 0, total_cost_usd: 0.4, total_cost_incomplete: true })])
    api.listResults.mockResolvedValue({ items: [result(), result({ id: 89, cost_usd: null }), result({ id: 88, cost_usd: 0 }), result({ id: 87, cost_usd: 0.0000001 }), result({ id: 86, cost_usd: 0.2, cost_incomplete: true })], total: 5 })
    wrapper = mountView()
    await flushPromises()
    const summary = wrapper.get('[data-testid="pelican-plan-cost-7"]')
    expect(summary.text()).toContain('$0.123456')
    expect(summary.text()).toContain('$12.345678')
    expect(summary.element.parentElement?.classList.contains('plan-title')).toBe(true)
    expect(wrapper.get('[data-testid="pelican-plan-cost-8"]').text()).toContain('pelicanTests.cost.partial')
    expect(wrapper.get('[data-testid="pelican-result-cost-90"]').text()).toBe('$0.012345')
    expect(wrapper.get('[data-testid="pelican-result-cost-89"]').text()).toBe('pelicanTests.cost.unknown')
    expect(wrapper.get('[data-testid="pelican-result-cost-88"]').text()).toBe('$0.000000')
    expect(wrapper.get('[data-testid="pelican-result-cost-87"]').text()).toBe('<$0.000001')
    expect(wrapper.get('[data-testid="pelican-result-cost-86"]').text()).toContain('pelicanTests.cost.partial')
  })

  it('lists the group tests with the account the scheduler picked', async () => {
    wrapper = mountView()
    await flushPromises()
    const row = wrapper.get('[data-testid="pelican-plan-7"]')
    expect(row.text()).toContain('GPT PRO号池')
    expect(row.text()).toContain('gpt-6-astra')
    expect(row.text()).toContain('pelicanShowcase.efforts.high')
    expect(row.text()).toContain('pelicanTests.schedules.every30m')
    expect(row.text()).toContain('pelicanTests.plans.parallel {"count":2}')
    const last = wrapper.get('[data-testid="pelican-plan-last-7"]').text()
    expect(last).toContain('pelicanTests.plans.routedTo {"name":"pool-b"}')
    expect(last).toContain('pelicanTests.plans.failedOver {"count":1}')
    expect(wrapper.get('[data-testid="pelican-plan-8"]').text()).toContain('pelicanTests.plans.modelMissing')
    expect(wrapper.get('[data-testid="pelican-plan-run-8"]').attributes('disabled')).toBeDefined()

    const history = wrapper.get('[data-testid="pelican-result-90"]').text()
    expect(history).toContain('pool-b')
    expect(history).toContain('pelicanTests.history.tried {"accounts":"pool-a"}')
    const failed = wrapper.get('[data-testid="pelican-result-89"]')
    expect(failed.text()).toContain('pelicanTests.plans.noAccount')
    expect(failed.text()).toContain('no_available_account')
    expect(failed.find('[data-testid="pelican-result-view-89"]').exists()).toBe(false)
    expect(wrapper.findComponent(RouterLinkStub).props('to')).toBe('/pelican-showcase')
  })

  it('saves the showcase switch and limits, then refreshes the public flags', async () => {
    api.updateShowcaseSettings.mockImplementation(async (value) => value)
    wrapper = mountView()
    await flushPromises()
    const save = wrapper.get('[data-testid="pelican-showcase-save"]')
    expect(save.attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="pelican-showcase-enabled"]').setValue(true)
    await wrapper.get('[data-testid="pelican-showcase-api-enabled"]').setValue(false)
    await wrapper.get('[data-testid="pelican-showcase-max-items"]').setValue('40')
    await wrapper.get('[data-testid="pelican-showcase-auto-cleanup"]').setValue(false)
    expect(wrapper.find('[data-testid="pelican-showcase-retention-days"]').exists()).toBe(false)
    await wrapper.get('[data-testid="pelican-showcase-settings"] form').trigger('submit')
    await flushPromises()
    expect(api.updateShowcaseSettings).toHaveBeenCalledWith({ enabled: true, api_enabled: false, max_items: 40, auto_cleanup: false, retention_days: 7 })
    expect(fetchPublicSettings).toHaveBeenCalledWith(true)
    expect(wrapper.text()).toContain('pelicanTests.showcase.saved')
  })

  it('saves API access independently and reports the saved availability in its call information', async () => {
    api.getShowcaseSettings.mockResolvedValue({ ...settings, enabled: true })
    api.updateShowcaseSettings.mockImplementation(async (value) => value)
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-showcase-api-enabled"]').setValue(false)
    expect(wrapper.get('[data-testid="pelican-showcase-save"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="pelican-showcase-api-info"]').trigger('click')
    expect(wrapper.get('[data-testid="api-info-dialog"]').attributes('data-enabled')).toBe('true')
    await wrapper.get('[data-testid="pelican-showcase-settings"] form').trigger('submit')
    await flushPromises()
    expect(api.updateShowcaseSettings).toHaveBeenCalledWith({ ...settings, enabled: true, api_enabled: false })
    expect(wrapper.get('[data-testid="api-info-dialog"]').attributes('data-enabled')).toBe('false')
    expect(wrapper.get('[data-testid="pelican-showcase-enabled"]').element).toHaveProperty('checked', true)
  })

  it('shows that API output depends on opening the showcase to users', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="pelican-showcase-api-requires-gallery"]').exists()).toBe(true)
    await wrapper.get('[data-testid="pelican-showcase-enabled"]').setValue(true)
    expect(wrapper.find('[data-testid="pelican-showcase-api-requires-gallery"]').exists()).toBe(false)
  })

  it('creates a group test on a preset schedule', async () => {
    api.createPlan.mockResolvedValue(plan({ id: 9 }))
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-tests-create"]').trigger('click')
    await flushPromises()
    const editor = wrapper.get('[data-testid="pelican-test-editor"]')
    const selects = editor.findAll('select')
    expect(selects[0].findAll('option').map((option) => option.text())).toEqual(['GPT PRO号池 · OpenAI', 'Claude Max · Anthropic'])
    await selects[0].setValue('6')
    await editor.get('[data-testid="pelican-test-model"]').setValue('claude-opus-5-5')
    await editor.get('[data-testid="pelican-test-schedule"]').setValue('0 * * * *')
    expect(editor.find('[data-testid="pelican-test-cron"]').exists()).toBe(false)
    await editor.trigger('submit')
    await flushPromises()
    expect(api.createPlan).toHaveBeenCalledTimes(1)
    expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({
      group_id: 6, model_id: 'claude-opus-5-5', cron_expression: '0 * * * *', enabled: true, reasoning_effort: 'medium', parallel_count: 1,
    }))
    expect(api.createPlan.mock.calls[0][0].prompt).toContain('鹈鹕')
    expect(wrapper.find('[data-testid="pelican-test-editor"]').exists()).toBe(false)
  })

  it('saves a random group rule without a custom prompt or result metadata', async () => {
    api.createPlan.mockResolvedValue(plan())
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-tests-create"]').trigger('click')
    await flushPromises()
    const editor = wrapper.get('[data-testid="pelican-test-editor"]')
    await editor.findAll('select')[0].setValue('6')
    await editor.get('[data-testid="pelican-test-model"]').setValue('fixture-model')
    await editor.get('[data-testid="pelican-test-prompt"]').setValue('')
    await editor.get('[data-testid="pelican-mode-select"]').setValue('random')
    expect(editor.find('[data-testid="pelican-test-prompt"]').exists()).toBe(false)
    await editor.trigger('submit')
    await flushPromises()
    expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({ prompt_mode: 'random', prompt: '', model_id: 'fixture-model' }))
    expect(api.createPlan.mock.calls[0][0]).not.toHaveProperty('pelican_question')
  })

  it('asks for the missing fields before creating', async () => {
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-tests-create"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="pelican-test-editor"]').trigger('submit')
    await flushPromises()
    expect(api.createPlan).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('pelicanTests.editor.required')
  })

  it('keeps a custom schedule and the group of an edited test', async () => {
    api.listPlans.mockResolvedValue([plan({ cron_expression: '5 */3 * * *' })])
    api.updatePlan.mockResolvedValue(plan())
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-plan-edit-7"]').trigger('click')
    await flushPromises()
    const editor = wrapper.get('[data-testid="pelican-test-editor"]')
    expect(editor.findAll('select')[0].attributes('disabled')).toBeDefined()
    expect((editor.get('[data-testid="pelican-test-cron"]').element as HTMLInputElement).value).toBe('5 */3 * * *')
    await editor.trigger('submit')
    await flushPromises()
    expect(api.updatePlan).toHaveBeenCalledWith(7, expect.objectContaining({ group_id: 4, cron_expression: '5 */3 * * *', prompt: 'draw a pelican', parallel_count: 2 }))
  })

  it('runs a test now and explains a run already in progress', async () => {
    api.runPlan.mockResolvedValueOnce(undefined)
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-plan-run-7"]').trigger('click')
    await flushPromises()
    expect(api.runPlan).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('pelicanTests.plans.runStarted')

    api.runPlan.mockRejectedValueOnce({ reason: 'PELICAN_GROUP_TEST_PLAN_RUNNING', message: 'this group test is already running' })
    await wrapper.get('[data-testid="pelican-plan-run-7"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('pelicanTests.plans.alreadyRunning')
  })

  it('pauses with the full plan, and opens the editor to enable a test without a model', async () => {
    api.updatePlan.mockResolvedValue(plan({ enabled: false }))
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-plan-toggle-7"]').trigger('click')
    await flushPromises()
    expect(api.updatePlan).toHaveBeenCalledWith(7, {
      group_id: 4, model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: false,
      prompt: 'draw a pelican', prompt_mode: 'custom', reasoning_effort: 'high', parallel_count: 2,
    })

    api.updatePlan.mockClear()
    await wrapper.get('[data-testid="pelican-plan-toggle-8"]').trigger('click')
    await flushPromises()
    expect(api.updatePlan).not.toHaveBeenCalled()
    const editor = wrapper.get('[data-testid="pelican-test-editor"]')
    expect((editor.get('[data-testid="pelican-test-enabled"]').element as HTMLInputElement).checked).toBe(true)
  })

  it('deletes a test after confirmation', async () => {
    api.deletePlan.mockResolvedValue(undefined)
    wrapper = mountView()
    await flushPromises()
    api.listResults.mockResolvedValue({ items: [], total: 0 })
    await wrapper.get('[data-testid="pelican-plan-delete-7"]').trigger('click')
    await wrapper.get('.confirm-yes').trigger('click')
    await flushPromises()
    expect(api.deletePlan).toHaveBeenCalledWith(7)
    expect(wrapper.find('[data-testid="pelican-plan-7"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pelican-result-90"]').exists()).toBe(false)
  })

  it('previews an answer in the sandbox and pages the history', async () => {
    api.listResults.mockResolvedValueOnce({ items: [result()], total: 21 })
    api.listResults.mockResolvedValueOnce({ items: [result({ id: 80 })], total: 21 })
    api.getResult.mockResolvedValue(result({ response_text: '<svg data-answer="90"></svg>' }))
    wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="pelican-result-view-90"]').trigger('click')
    await flushPromises()
    const frame = wrapper.get('[data-testid="pelican-result-preview"] iframe')
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('srcdoc')).toContain('data-answer="90"')

    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    expect(api.listResults).toHaveBeenLastCalledWith(2, 20, 0, expect.any(AbortSignal))
    expect(wrapper.find('[data-testid="pelican-result-80"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="pelican-result-90"]').exists()).toBe(false)
    expect(wrapper.getComponent(Pagination).props()).toMatchObject({ total: 21, page: 2, pageSize: 20, showJump: true })
  })

  it('uses the shared page size and returns to page one when it changes', async () => {
    localStorage.setItem('table-page-size', '50')
    api.listResults.mockResolvedValue({ items: [result()], total: 120 })
    wrapper = mountView()
    await flushPromises()
    expect(api.listResults).toHaveBeenLastCalledWith(1, 50, 0, expect.any(AbortSignal))
    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    await wrapper.get('[data-testid="pelican-history-pagination"] select').setValue('20')
    await flushPromises()
    expect(api.listResults).toHaveBeenLastCalledWith(1, 20, 0, expect.any(AbortSignal))
    expect(localStorage.getItem('table-page-size')).toBe('20')
    expect(wrapper.getComponent(Pagination).props('page')).toBe(1)
  })

  it('replaces the current page while polling instead of accumulating rows', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    api.listResults.mockResolvedValue({ items: [result()], total: 80 })
    wrapper = mountView()
    await flushPromises()
    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    api.listResults.mockResolvedValue({ items: [result({ id: 91, cost_usd: 0.6 })], total: 81 })
    api.listPlans.mockResolvedValue([plan({ today_cost_usd: 0.7, total_cost_usd: 1.2 })])
    await vi.advanceTimersByTimeAsync(15000)
    await flushPromises()
    expect(api.listResults).toHaveBeenLastCalledWith(2, 20, 0, expect.any(AbortSignal))
    expect(wrapper.getComponent(Pagination).props('page')).toBe(2)
    expect(wrapper.findAll('[data-testid="pelican-test-history"] tbody tr')).toHaveLength(1)
    expect(wrapper.find('[data-testid="pelican-result-90"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pelican-result-91"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="pelican-result-cost-91"]').text()).toBe('$0.600000')
    expect(wrapper.get('[data-testid="pelican-plan-cost-7"]').text()).toContain('$0.700000')
    expect(wrapper.get('[data-testid="pelican-plan-cost-7"]').text()).toContain('$1.200000')
  })

  it('ignores a late response from an earlier page and aborts it', async () => {
    api.listResults.mockResolvedValue({ items: [result()], total: 80 })
    wrapper = mountView()
    await flushPromises()
    let resolveOld!: (value: unknown) => void
    api.listResults.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    const oldSignal = api.listResults.mock.calls.at(-1)?.[3] as AbortSignal
    api.listResults.mockResolvedValueOnce({ items: [result({ id: 70 })], total: 80 })
    wrapper.getComponent(Pagination).vm.$emit('update:page', 3)
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolveOld({ items: [result({ id: 80 })], total: 80 })
    await flushPromises()
    expect(wrapper.getComponent(Pagination).props('page')).toBe(3)
    expect(wrapper.find('[data-testid="pelican-result-70"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="pelican-result-80"]').exists()).toBe(false)
  })

  it('moves to the last available page after history is deleted', async () => {
    api.listResults.mockResolvedValue({ items: [result()], total: 21 })
    wrapper = mountView()
    await flushPromises()
    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    api.listResults.mockResolvedValueOnce({ items: [], total: 1 })
    api.listResults.mockResolvedValueOnce({ items: [result({ id: 70, plan_id: 8 })], total: 1 })
    await wrapper.get('[data-testid="pelican-plan-delete-7"]').trigger('click')
    await wrapper.get('.confirm-yes').trigger('click')
    await flushPromises()
    expect(api.listResults).toHaveBeenLastCalledWith(1, 20, 0, expect.any(AbortSignal))
    expect(wrapper.getComponent(Pagination).props()).toMatchObject({ page: 1, total: 1 })
    expect(wrapper.find('[data-testid="pelican-result-70"]').exists()).toBe(true)
  })

  it('reports a failed page request and cancels requests on unmount', async () => {
    api.listResults.mockResolvedValue({ items: [result()], total: 80 })
    wrapper = mountView()
    await flushPromises()
    api.listResults.mockRejectedValueOnce(new Error('history unavailable'))
    wrapper.getComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('history unavailable')
    api.listResults.mockReturnValueOnce(new Promise(() => {}))
    wrapper.getComponent(Pagination).vm.$emit('update:page', 3)
    const signal = api.listResults.mock.calls.at(-1)?.[3] as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
  })
})
