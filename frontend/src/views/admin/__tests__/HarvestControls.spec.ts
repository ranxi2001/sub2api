import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import HarvestControlsPanel from '@/components/admin/HarvestControlsPanel.vue'
import HarvestNodeRecords from '@/components/admin/HarvestNodeRecords.vue'
import type { CodexHarvestControlSnapshot, CodexHarvestControls, CodexHarvestNodePage } from '@/api/admin/codexHarvest'

const api = vi.hoisted(() => ({ getControls: vi.fn(), save: vi.fn(), getNodes: vi.fn(), reset: vi.fn() }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin/codexHarvest', () => ({
  getCodexHarvestControls: api.getControls,
  saveCodexHarvestControls: api.save,
  getCodexHarvestNodes: api.getNodes,
  resetCodexHarvestNodes: api.reset
}))
vi.mock('@/stores', () => ({ useAppStore: () => toast }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }))

function snapshot(): CodexHarvestControlSnapshot {
  const speed = { round_interval_seconds: 180, probe_interval_seconds: 2, attempt_timeout_seconds: 25, cooldown_seconds: 180, max_requests_per_round: 6, max_node_attempts: 3, refresh_before_seconds: 600 }
  const settings = { version: 1, node_memory_enabled: false, speed }
  return {
    settings, configured: false, settings_error: '', defaults: settings,
    presets: { standard: speed, fast: { ...speed, round_interval_seconds: 60, max_requests_per_round: 12, refresh_before_seconds: 300 }, burst: { round_interval_seconds: 1, probe_interval_seconds: 0, attempt_timeout_seconds: 15, cooldown_seconds: 1, max_requests_per_round: 20, max_node_attempts: 5, refresh_before_seconds: 60 } },
    bounds: {
      round_interval_seconds: { min: 1, max: 3600 },
      probe_interval_seconds: { min: 0, max: 60 },
      attempt_timeout_seconds: { min: 1, max: 120 },
      cooldown_seconds: { min: 1, max: 3600 },
      max_requests_per_round: { min: 1, max: 100 },
      max_node_attempts: { min: 1, max: 10 },
      refresh_before_seconds: { min: 60, max: 1800 }
    },
    runtime: { running: false, next_round_at: null, requests_used: 0, request_budget: 6, current_node: '', selection_reason: '', degraded_reason: '' },
    available: true, availability_reason: ''
  }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const selector = (id: string) => `[data-testid="${id}"]`
const input = (wrapper: VueWrapper, id: string) => wrapper.get(selector(id)).element as HTMLInputElement
const empty: CodexHarvestNodePage = { items: [], total: 0 }
function nodes(): CodexHarvestNodePage {
  return { total: 1, items: [{
    id: 7, pool_id: 'pool', node_id: 'node', node_name: 'Known node', provider: 'fixture', account_id: 1,
    model: 'gpt-6-astra', blocks: 10, successes: 2, misses: 1, network_errors: 0, account_errors: 0,
    consecutive_failures: 0, last_success: null, cooldown_until: null, latency_ms: 80, last_result: 'success', updated_at: ''
  }] }
}

let wrapper: VueWrapper | undefined
beforeEach(() => {
  vi.resetAllMocks()
  api.getControls.mockResolvedValue(snapshot())
  api.getNodes.mockResolvedValue(nodes())
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('Harvest controls draft and request ordering', () => {
  it('defaults to unified-88 without overwriting a saved gateway', async () => {
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    expect(input(wrapper, 'target-gateway').value).toBe('unified-88')
    expect(api.save).not.toHaveBeenCalled()
    const data = snapshot()
    data.settings.target_gateway = 'unified-95'
    api.getControls.mockResolvedValue(data)
    await wrapper.setProps({ refreshKey: 'saved-gateway' })
    await flushPromises()
    expect(input(wrapper, 'target-gateway').value).toBe('unified-95')
    expect(api.save).not.toHaveBeenCalled()
  })
  it.each(['any', 'chat.gateway.unified-123.api.openai.com'])('saves gateway policy %s', async target => {
    api.save.mockImplementation(async settings => settings)
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('target-gateway')).setValue(target)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ target_gateway: target }))
  })
  it('saves edge IP, gateway and protocol through the existing controls API', async () => {
    api.save.mockImplementation(async settings => settings)
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('edge-ip')).setValue('104.18.32.7')
    await wrapper.get(selector('target-gateway')).setValue('unified-88')
    await wrapper.get('select:not([data-testid])').setValue('websocket')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({edge_ip: '104.18.32.7', target_gateway: 'unified-88', transport: 'websocket'}))
    expect(wrapper.get(selector('save-controls')).attributes('disabled')).toBeDefined()
  })

  it('saves the ticket-first scheduling switch, which defaults off', async () => {
    api.save.mockImplementation(async settings => settings)
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    expect(input(wrapper, 'ticket-first-toggle').checked).toBe(false)
    await wrapper.get(selector('ticket-first-toggle')).setValue(true)
    expect(wrapper.text()).toContain('admin.harvestFlow.controls.unsaved')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ ticket_first_scheduling: true }))
    expect(input(wrapper, 'ticket-first-toggle').checked).toBe(true)
  })

  it('keeps an edited draft across automatic refreshes', async () => {
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('max_requests_per_round')).setValue(9)
    await wrapper.setProps({ refreshKey: 'next' })
    await flushPromises()
    expect(input(wrapper, 'max_requests_per_round').value).toBe('9')
    expect(wrapper.text()).toContain('admin.harvestFlow.controls.unsaved')
  })

  it('presets and defaults edit only the draft until saved', async () => {
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('speed-preset')).setValue('fast')
    expect(input(wrapper, 'max_requests_per_round').value).toBe('12')
    await wrapper.get(selector('memory-toggle')).setValue(true)
    await wrapper.get(selector('restore-defaults')).trigger('click')
    expect(input(wrapper, 'memory-toggle').checked).toBe(false)
    expect(input(wrapper, 'max_requests_per_round').value).toBe('6')
    expect(api.save).not.toHaveBeenCalled()
  })

  it.each(['resolve', 'reject'])('ignores old refresh %s after save', async completion => {
    const old = deferred<CodexHarvestControlSnapshot>()
    const saving = deferred<CodexHarvestControls>()
    api.getControls.mockResolvedValueOnce(snapshot()).mockReturnValueOnce(old.promise)
    api.save.mockReturnValue(saving.promise)
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.setProps({ refreshKey: 'old' })
    await wrapper.get(selector('memory-toggle')).setValue(true)
    await wrapper.get('form').trigger('submit')
    await wrapper.setProps({ refreshKey: 'during-save' })
    expect(api.getControls).toHaveBeenCalledTimes(2)
    const saved = { ...snapshot().settings, node_memory_enabled: true }
    saving.resolve(saved)
    await flushPromises()
    if (completion === 'resolve') old.resolve(snapshot())
    else old.reject(new Error('old failure'))
    await flushPromises()
    expect(input(wrapper, 'memory-toggle').checked).toBe(true)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(toast.showSuccess).toHaveBeenCalledWith('admin.harvestFlow.controls.saveSuccess')
    expect(toast.showError).not.toHaveBeenCalled()
  })

  it('preserves the draft on failed save', async () => {
    api.save.mockRejectedValue(new Error('storage failed'))
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('memory-toggle')).setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(input(wrapper, 'memory-toggle').checked).toBe(true)
    expect(wrapper.text()).toContain('admin.harvestFlow.controls.saveFailed')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(toast.showError).toHaveBeenCalledWith('admin.harvestFlow.controls.saveFailed')
    expect(toast.showSuccess).not.toHaveBeenCalled()
  })

  it('does not emit a save result after unmount', async () => {
    const saving = deferred<CodexHarvestControls>()
    api.save.mockReturnValue(saving.promise)
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    wrapper.unmount()
    saving.resolve(snapshot().settings)
    await flushPromises()
    expect(wrapper.emitted('saved')).toBeUndefined()
    wrapper = undefined
  })

  it('disables enabling unsupported sidecars but permits turning off', async () => {
    const data = snapshot()
    data.available = false
    api.getControls.mockResolvedValue(data)
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    expect(input(wrapper, 'memory-toggle').disabled).toBe(true)
    data.settings.node_memory_enabled = true
    await wrapper.setProps({ refreshKey: 'next' })
    await flushPromises()
    expect(input(wrapper, 'memory-toggle').disabled).toBe(false)
  })

  it('allows the aggressive cadence floors and burst preset', async () => {
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    expect(input(wrapper, 'round_interval_seconds').min).toBe('1')
    expect(input(wrapper, 'probe_interval_seconds').min).toBe('0')
    expect(input(wrapper, 'attempt_timeout_seconds').min).toBe('1')
    expect(input(wrapper, 'cooldown_seconds').min).toBe('1')
    expect(input(wrapper, 'refresh_before_seconds').min).toBe('60')
    await wrapper.get(selector('speed-preset')).setValue('burst')
    expect(input(wrapper, 'round_interval_seconds').value).toBe('1')
    expect(input(wrapper, 'probe_interval_seconds').value).toBe('0')
    expect(input(wrapper, 'cooldown_seconds').value).toBe('1')
    expect(input(wrapper, 'refresh_before_seconds').value).toBe('60')
  })

  it('collapses advanced fields after a successful save', async () => {
    api.save.mockResolvedValue({ ...snapshot().settings, node_memory_enabled: true })
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('advanced-toggle')).trigger('click')
    expect(wrapper.get(selector('advanced-toggle')).attributes('aria-expanded')).toBe('true')
    expect((wrapper.get(selector('advanced-fields')).element as HTMLElement).style.display).not.toBe('none')
    await wrapper.get(selector('memory-toggle')).setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get(selector('advanced-toggle')).attributes('aria-expanded')).toBe('false')
    expect((wrapper.get(selector('advanced-fields')).element as HTMLElement).style.display).toBe('none')
  })

  it('keeps advanced fields open when save fails', async () => {
    api.save.mockRejectedValue(new Error('storage failed'))
    wrapper = mount(HarvestControlsPanel)
    await flushPromises()
    await wrapper.get(selector('advanced-toggle')).trigger('click')
    await wrapper.get(selector('memory-toggle')).setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get(selector('advanced-toggle')).attributes('aria-expanded')).toBe('true')
    expect((wrapper.get(selector('advanced-fields')).element as HTMLElement).style.display).not.toBe('none')
  })
})

describe('Harvest node resets', () => {
  it('resets one record without resetting accounts or tickets', async () => {
    api.reset.mockResolvedValue(undefined)
    api.getNodes.mockResolvedValueOnce(nodes()).mockResolvedValue(empty)
    wrapper = mount(HarvestNodeRecords)
    await flushPromises()
    await wrapper.get('[data-testid="reset-one-node"]').trigger('click')
    await flushPromises()
    expect(api.reset).toHaveBeenCalledWith(7)
    expect(wrapper.text()).not.toContain('Known node')
  })

  it('suppresses polling while reset is pending and then reloads', async () => {
    const resetting = deferred<void>()
    api.reset.mockReturnValue(resetting.promise)
    api.getNodes.mockResolvedValueOnce(nodes()).mockResolvedValue(empty)
    wrapper = mount(HarvestNodeRecords)
    await flushPromises()
    await wrapper.get(selector('reset-all-nodes')).trigger('click')
    await wrapper.setProps({ refreshKey: 'during-reset' })
    expect(api.getNodes).toHaveBeenCalledTimes(1)
    resetting.resolve()
    await flushPromises()
    expect(api.reset).toHaveBeenCalledWith(0)
    expect(api.getNodes).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('Known node')
  })

  it('preserves displayed records when reset fails', async () => {
    api.reset.mockRejectedValue(new Error('storage failed'))
    wrapper = mount(HarvestNodeRecords)
    await flushPromises()
    await wrapper.get(selector('reset-all-nodes')).trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Known node')
    expect(wrapper.text()).toContain('admin.harvestFlow.nodes.resetFailed')
  })

  it('does not refetch after unmount during reset', async () => {
    const resetting = deferred<void>()
    api.reset.mockReturnValue(resetting.promise)
    wrapper = mount(HarvestNodeRecords)
    await flushPromises()
    await wrapper.get(selector('reset-all-nodes')).trigger('click')
    wrapper.unmount()
    wrapper = undefined
    resetting.resolve()
    await flushPromises()
    expect(api.getNodes).toHaveBeenCalledTimes(1)
  })
})
