import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import LatencyObservabilityPanel from '../LatencyObservabilityPanel.vue'
import en from '@/i18n/locales/en/latencyObservability'
import zh from '@/i18n/locales/zh/latencyObservability'
const mocks = vi.hoisted(() => ({ read: vi.fn() }))
vi.mock('@/api/admin/latencyObservability', () => ({ getLatencyReport: mocks.read }))
const group = { body_bucket: '1-4MiB', cache_bucket: '95%+', reasoning_effort: 'xhigh', requests: 2, traces: 2, attempts: 3, reasoning_samples: 1, reasoning_first_avg_ms: 120, text_samples: 1, visible_text_first_avg_ms: 450, byte_samples: 1, response_byte_first_avg_ms: 30, upload_samples: 1, wait_samples: 0, visible_samples: 0, body_upload_avg_ms: 0, post_upload_first_content_wait_avg_ms: null, post_upload_first_visible_wait_avg_ms: null, large_body_switches: 1, large_body_same_account_retries: 1, failover_samples: 1, cost_samples: 0, extra_upload_component_ms: null, extra_wait_component_ms: null }
const result = { groups: [group], end_to_end_ms: null, end_to_end_status: 'not_collected', end_to_end_source: 'none', client_upload_ms: null, client_upload_status: 'not_collected' }
beforeEach(() => {
  localStorage.clear()
  mocks.read.mockReset().mockResolvedValue(result)
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
})

const locales = { en, zh }
function renderPanel(props: Partial<InstanceType<typeof LatencyObservabilityPanel>['$props']> = {}, locale: keyof typeof locales = 'en') {
  return mount(LatencyObservabilityPanel, {
    props,
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: { [locale]: { latencyObservability: locales[locale], common: { loading: 'Loading' } } } })],
      stubs: { teleport: true },
    },
  })
}

async function selectFilter(wrapper: ReturnType<typeof renderPanel>, key: string, label: string) {
  await wrapper.get(`[data-testid="latency-filter-${key}"] button`).trigger('click')
  const option = wrapper.findAll('[role="option"]').find(item => item.text() === label)
  expect(option, `filter ${key} should offer ${label}`).toBeDefined()
  await option!.trigger('click')
  await flushPromises()
}

describe('Issue603 latency observations', () => {
  for (const [locale, messages] of Object.entries({ en, zh })) {
    it(`omits client timing while retaining server observations in ${locale}`, async () => {
      mocks.read.mockResolvedValue(result)
      const w = mount(LatencyObservabilityPanel, { props: { model: 'fixture', accountId: 4 }, global: { plugins: [createI18n({ legacy: false, locale, messages: { [locale]: { latencyObservability: messages } } })] } })
      await flushPromises()
      expect(w.text()).toContain('0.0 ms (n=1)')
      expect(w.text()).toContain(`${messages.notCollected} (n=0)`)
      expect(w.text()).not.toMatch(/Client end-to-end time|Client upload before application entry|客户端端到端耗时|进入应用前的客户端上传/)
      expect(w.find('[data-testid="reasoning-capability"]').exists()).toBe(false)
      expect(w.text()).toContain('1 / 1 (n=1)')
      expect(w.text()).toContain(messages.reasoning_first_ms)
      expect(w.text()).toContain('120.0 ms (n=1)')
      expect(w.text()).toContain(messages.visible_text_first_ms)
      expect(w.text()).toContain('450.0 ms (n=1)')
      expect(w.text()).toContain(messages.response_byte_first_ms)
      expect(w.text()).toContain('30.0 ms (n=1)')
      expect(mocks.read).toHaveBeenLastCalledWith(false, expect.objectContaining({ account_id: 4, model: 'fixture' }), expect.any(AbortSignal))
      w.unmount()
    })
  }
  it('rejects stale report responses when filters change', async () => {
    let resolve!: (v: unknown) => void
    mocks.read.mockImplementationOnce(() => new Promise(r => { resolve = r })).mockResolvedValueOnce({ ...result, groups: [] })
    const w = mount(LatencyObservabilityPanel, { props: { model: 'old' }, global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { latencyObservability: en } } })] } })
    await w.setProps({ model: 'new', ops: true }); await flushPromises()
    resolve(result); await flushPromises()
    expect(w.find('[data-testid="latency-group"]').exists()).toBe(false)
    expect(mocks.read).toHaveBeenLastCalledWith(true, expect.objectContaining({ model: 'new' }), expect.any(AbortSignal))
    w.unmount()
  })
})

it('omits unavailable reasoning capability and retains per-hop socket evidence scope', async () => {
  mocks.read.mockResolvedValue(result)
  const w = mount(LatencyObservabilityPanel, { props: { ops: true }, global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { latencyObservability: en } } })] } })
  await flushPromises()
  expect(w.find('[data-testid="reasoning-capability"]').exists()).toBe(false)
  expect(w.get('[data-testid="socket-evidence"]').text()).toContain('source none')
  expect(w.get('[data-testid="socket-evidence"]').text()).toContain('reverse proxy')
  expect(w.text()).toContain(en.typedScope)
  w.unmount()
})

it('supports flat embedding without a duplicate title inside the latency tab', async () => {
  mocks.read.mockResolvedValue({ ...result, groups: [] })
  const w = mount(LatencyObservabilityPanel, {
    props: { flat: true, hideTitle: true },
    global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { latencyObservability: en } } })] },
  })
  await flushPromises()
  expect(w.get('[data-testid="latency-observability"]').classes()).not.toContain('card')
  expect(w.find('h3').exists()).toBe(false)
  w.unmount()
})

describe('Issue624 latency panel visibility and group browsing', () => {
  it.each(['en', 'zh'] as const)('keeps Ops collapsed and idle until expanded in %s', async (locale) => {
    const wrapper = renderPanel({ ops: true, collapsible: true }, locale)
    await flushPromises()
    expect(wrapper.get('[data-testid="latency-panel-toggle"]').attributes('aria-expanded')).toBe('false')
    expect(wrapper.text()).toContain(locales[locale].collapsedHint)
    expect(wrapper.find('[data-testid="latency-panel-content"]').exists()).toBe(false)
    expect(mocks.read).not.toHaveBeenCalled()

    await wrapper.setProps({ refreshToken: 1, platform: 'openai', groupId: 7, endDate: '2026-11-02T10:00:00Z' })
    expect(mocks.read).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="latency-panel-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="latency-panel-toggle"]').attributes('aria-expanded')).toBe('true')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(1)
    expect(wrapper.get('details').attributes('open')).toBeUndefined()
    expect(mocks.read).toHaveBeenCalledTimes(1)
    expect(mocks.read).toHaveBeenLastCalledWith(true, expect.objectContaining({ platform: 'openai', group_id: 7, end_date: '2026-11-02T10:00:00Z' }), expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('remembers both expanded and collapsed preferences across remounts', async () => {
    const first = renderPanel({ ops: true, collapsible: true })
    await first.get('[data-testid="latency-panel-toggle"]').trigger('click')
    await flushPromises()
    first.unmount()

    const second = renderPanel({ ops: true, collapsible: true })
    await flushPromises()
    expect(second.get('[data-testid="latency-panel-toggle"]').attributes('aria-expanded')).toBe('true')
    expect(mocks.read).toHaveBeenCalledTimes(2)
    await second.get('[data-testid="latency-panel-toggle"]').trigger('click')
    second.unmount()

    const third = renderPanel({ ops: true, collapsible: true })
    await flushPromises()
    expect(third.get('[data-testid="latency-panel-toggle"]').attributes('aria-expanded')).toBe('false')
    expect(mocks.read).toHaveBeenCalledTimes(2)
    third.unmount()
  })

  it('keeps the toggle usable when browser storage is unavailable', async () => {
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('Storage unavailable') },
      setItem: () => { throw new Error('Storage unavailable') },
    })
    const wrapper = renderPanel({ ops: true, collapsible: true })
    expect(mocks.read).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="latency-panel-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(1)
    await wrapper.get('[data-testid="latency-panel-toggle"]').trigger('click')
    expect(wrapper.find('table').exists()).toBe(false)
    expect(mocks.read).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('aborts pending work on collapse and ignores stale responses after reopening', async () => {
    let resolve!: (value: unknown) => void
    mocks.read.mockImplementationOnce(() => new Promise(r => { resolve = r }))
      .mockResolvedValueOnce({ ...result, groups: [{ ...group, model: 'current-report' }] })
    const wrapper = renderPanel({ ops: true, collapsible: true, model: 'old' })
    await wrapper.get('[data-testid="latency-panel-toggle"]').trigger('click')
    const firstSignal = mocks.read.mock.calls[0][2] as AbortSignal
    await wrapper.get('[data-testid="latency-panel-toggle"]').trigger('click')
    expect(firstSignal.aborted).toBe(true)
    await wrapper.setProps({ model: 'new', refreshToken: 1 })
    expect(mocks.read).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-testid="latency-panel-toggle"]').trigger('click')
    await flushPromises()
    resolve({ ...result, groups: [{ ...group, model: 'stale-report' }] })
    await flushPromises()
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('current-report')
    expect(wrapper.text()).not.toContain('stale-report')
    expect(mocks.read).toHaveBeenLastCalledWith(true, expect.objectContaining({ model: 'new' }), expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('combines all five group dimensions without extra queries and retains unknown observations', async () => {
    const target = { ...group, account_id: 1, model: 'model-a', cache_bucket: '80-95%', reasoning_effort: 'high' }
    mocks.read.mockResolvedValue({ ...result, groups: [
      target,
      { ...target, account_id: 2 },
      { ...target, model: 'model-b' },
      { ...target, body_bucket: '4MiB+' },
      { ...target, cache_bucket: '95%+' },
      { ...target, reasoning_effort: 'low' },
      { ...target, account_id: 3, model: 'legacy-model', body_bucket: 'unknown', cache_bucket: 'unknown', reasoning_effort: 'unknown', upload_samples: 0, body_upload_avg_ms: null },
    ] })
    const wrapper = renderPanel()
    await flushPromises()
    await selectFilter(wrapper, 'account_id', '#1')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(5)
    await selectFilter(wrapper, 'model', 'model-a')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(4)
    await selectFilter(wrapper, 'body_bucket', '1-4MiB')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(3)
    await selectFilter(wrapper, 'cache_bucket', '80-95%')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(2)
    await selectFilter(wrapper, 'reasoning_effort', 'high')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('0.0 ms (n=1)')

    await wrapper.get('[data-testid="latency-clear-filters"]').trigger('click')
    await selectFilter(wrapper, 'cache_bucket', 'unknown')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('legacy-model')
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain(`${en.notCollected} (n=0)`)
    expect(mocks.read).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('keeps filters accessible when no groups match and lets the user reset them', async () => {
    mocks.read.mockResolvedValue({ ...result, groups: [
      { ...group, account_id: 1, model: 'model-a' },
      { ...group, account_id: 2, model: 'model-b' },
    ] })
    const wrapper = renderPanel()
    await flushPromises()
    await selectFilter(wrapper, 'account_id', '#1')
    await selectFilter(wrapper, 'model', 'model-b')
    expect(wrapper.get('[data-testid="latency-no-matches"]').text()).toBe(en.noMatches)
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.find('[data-testid="latency-filter-model"]').exists()).toBe(true)
    await wrapper.get('[data-testid="latency-clear-filters"]').trigger('click')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(2)
    expect(mocks.read).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('pages loaded groups and returns to page one after filtering or refreshing', async () => {
    mocks.read.mockResolvedValue({ ...result, groups: Array.from({ length: 23 }, (_, index) => ({
      ...group, account_id: index + 1, model: index < 20 ? 'model-a' : 'model-b',
    })) })
    const wrapper = renderPanel()
    await flushPromises()
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(10)
    await wrapper.findAll('nav button').find(button => button.text() === '3')!.trigger('click')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(3)
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('#21')

    await selectFilter(wrapper, 'model', 'model-a')
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(10)
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('#1 ·')
    expect(mocks.read).toHaveBeenCalledTimes(1)

    await wrapper.findAll('nav button').find(button => button.text() === '2')!.trigger('click')
    mocks.read.mockResolvedValueOnce({ ...result, groups: [{ ...group, account_id: 99, model: 'model-a' }] })
    await wrapper.setProps({ refreshToken: 1 })
    await flushPromises()
    expect(wrapper.findAll('[data-testid="latency-group"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('#99')
    expect(wrapper.find('nav').exists()).toBe(false)

    mocks.read.mockResolvedValueOnce({ ...result, groups: [{ ...group, account_id: 100, model: 'model-b' }] })
    await wrapper.setProps({ refreshToken: 2 })
    await flushPromises()
    expect(wrapper.find('[data-testid="latency-no-matches"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="latency-filter-model"] button').text()).toContain('model-a')
    await wrapper.get('[data-testid="latency-clear-filters"]').trigger('click')
    expect(wrapper.get('[data-testid="latency-group"]').text()).toContain('#100')
    wrapper.unmount()
  })
})
