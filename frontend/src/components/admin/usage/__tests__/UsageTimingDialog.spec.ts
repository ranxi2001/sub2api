import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UsageTimingDialog from '../UsageTimingDialog.vue'
import { observerUsageContext } from '../observerUsageContext'
import type { AdminUsageLog } from '@/types'
const mocks = vi.hoisted(() => ({ get: vi.fn(), own: vi.fn() }))
vi.mock('@/api/observerUsage', () => ({ observerUsageAPI: { getTiming: mocks.own } }))
vi.mock('@/api/admin/usageTiming', () => ({ getUsageTiming: mocks.get }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (s: string) => s }))
const row = (id: number) => ({ id, model: 'test', request_id: `r${id}`, first_token_ms: 6055, duration_ms: 6067, created_at: '2026-09-23' }) as AdminUsageLog
const options = { global: { stubs: { BaseDialog: { template: '<section><slot /></section>' } } } }
describe('Usage timing details', () => {
  it('reacts to viewport changes between centered mobile and desktop drawer', async () => {
    mocks.get.mockResolvedValue({ traces: [], retention_days: 30 })
    const resize = async (width: number) => {
      Object.defineProperty(window, 'innerWidth', { value: width, configurable: true })
      window.dispatchEvent(new Event('resize'))
      await flushPromises()
    }
    await resize(390)
    const wrapper = mount(UsageTimingDialog, { attachTo: document.body, props: { record: row(1) }, global: { stubs: { Icon: true } } })
    try {
      await flushPromises()
      expect(document.body.querySelector('.modal-overlay')?.classList.contains('drawer-overlay')).toBe(false)
      await resize(1024)
      expect(document.body.querySelector('.modal-overlay')?.classList.contains('drawer-overlay')).toBe(true)
      await resize(320)
      expect(document.body.querySelector('.modal-overlay')?.classList.contains('drawer-overlay')).toBe(false)
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      expect(wrapper.emitted('close')).toHaveLength(1)
    } finally { wrapper.unmount(); await resize(1024) }
  })
  it('keeps historical values and marks missing details instead of inventing zeros', async () => {
    mocks.get.mockResolvedValue({ traces: [], retention_days: 30 })
    const wrapper = mount(UsageTimingDialog, { props: { record: row(1) }, ...options })
    await flushPromises()
    expect(wrapper.text()).toContain('6.05s')
    expect(wrapper.text()).toContain('6.07s')
    expect(wrapper.text()).toContain('requestTiming.empty')
    expect(wrapper.text()).toContain('requestTiming.missing')
    wrapper.unmount()
  })
  it('ignores stale responses when switching records', async () => {
    let resolveOld!: (data: unknown) => void
    mocks.get.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    mocks.get.mockResolvedValueOnce({ traces: [], retention_days: 30 })
    const wrapper = mount(UsageTimingDialog, { props: { record: row(1) }, ...options })
    await wrapper.setProps({ record: row(2) }); await flushPromises()
    resolveOld({ traces: [{ trace_id: 'stale' }], retention_days: 30 }); await flushPromises()
    expect(wrapper.text()).not.toContain('stale')
    expect(wrapper.text()).toContain('r2')
    wrapper.unmount()
  })
  it('explains a slow first output while keeping long streaming and new connections neutral', async () => {
    mocks.get.mockResolvedValue({ retention_days: 30, traces: [{
      trace_id: 'trace', started_at: '2026-09-23', total_ms: 50000, status: 200,
      body_bytes: 20, body_complete: true, body_read_ms: 1, downstream_bytes: 100,
      downstream_write_ms: 1, downstream_error: false, client_disconnect: false, canceled: false,
      outcome: 'success', terminal: 'completed', events: { first_visible: 40000, first_semantic: 40000 },
      spans: [{ name: 'handler', start_ms: 0, end_ms: 50000 }, { name: 'user_queue', start_ms: 0, end_ms: 6000 }],
      latency: { version: 1, status: 'measured', account_switches: 1, same_account_retries: 0, unknown_transitions: 0, large_body: true, cost_status: 'not_collected', extra_upload_component_ms: 100, extra_wait_component_ms: 200, attempts: [] },
      attempts: [{ kind: 'egress', number: 1, cleanup_canceled: true, account_id: 1, proxy_id: 0, start_ms: 6000, end_ms: 50000, status: 200, reused: false, body_eof: false, request_bytes: 20, response_bytes: 100, events: { response_headers: 10000 } }]
    }] })
    const wrapper = mount(UsageTimingDialog, { props: { record: { ...row(1), output_tokens: 1_095, duration_ms: 9_250, first_token_ms: 9_240 } }, ...options })
    await flushPromises()
    expect(wrapper.text()).toContain('usage.latencyTps 118 t/s')
    expect(wrapper.text()).toContain('requestTiming.tpsNote')
    expect(wrapper.find('[data-testid="attempt-latency"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('latencyObservability.title')
    expect(wrapper.text()).toContain('requestTiming.health.firstSlow')
    expect(wrapper.text()).toContain('requestTiming.health.largest')
    expect(wrapper.text()).toContain('requestTiming.health.normalClose')
    expect(wrapper.findAll('strong').find(n => n.text() === '40.00s')?.element.parentElement?.className).toContain('text-orange-700')
    const reuse = wrapper.findAll('div').find(n => n.element.children.length === 2 && n.element.children[0]?.textContent === 'requestTiming.fields.reused')
    expect(reuse?.element.children[1]?.className).toContain('text-slate-600')
    wrapper.unmount()
  })
  it('closes when the blank area beside the drawer is clicked', async () => {
    mocks.get.mockResolvedValue({ traces: [], retention_days: 30 })
    const wrapper = mount(UsageTimingDialog, { attachTo: document.body, props: { record: row(1) }, global: { stubs: { Icon: true } } })
    await flushPromises()
    const overlay = document.body.querySelector('.modal-overlay')!
    for (const type of ['mousedown', 'mouseup', 'click']) overlay.dispatchEvent(new MouseEvent(type, { bubbles: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

})

it('loads observer timings only from the own-usage endpoint', async () => {
  vi.clearAllMocks()
  mocks.own.mockResolvedValue({ traces: [], retention_days: 30 })
  const wrapper = mount(UsageTimingDialog, { props: { record: row(42) }, global: { ...options.global, provide: { [observerUsageContext as symbol]: true } } })
  await flushPromises()
  expect(mocks.own).toHaveBeenCalledWith(42, expect.anything())
  expect(mocks.get).not.toHaveBeenCalled()
  wrapper.unmount()
})

it('shows encoding and boundary gaps while leaving absent pre-decode data unknown', async () => {
  mocks.get.mockResolvedValue({ retention_days: 30, traces: [{
    trace_id: 'boundaries', started_at: '2026-10-29', total_ms: 5000, status: 200,
    body_bytes: 0, body_complete: true, body_read_ms: 0, downstream_bytes: 100,
    downstream_write_ms: 1, events: {}, spans: [],
    attempts: [{ kind: 'http', number: 1, account_id: 1, proxy_id: 0, start_ms: 0, end_ms: 5000,
      status: 200, reused: true, body_eof: true, content_encoding: 'gzip', auto_decompressed: true,
      events: { decoded_body_first: 100, sse_parsed_first: 300, sse_visible_first: 500, tool_visible_first: 2500 } }]
  }] })
  const wrapper = mount(UsageTimingDialog, { props: { record: row(1) }, ...options })
  await flushPromises()
  const field = (key: string) => wrapper.findAll('div').find(n => n.element.children.length === 2 && n.element.children[0]?.textContent === `requestTiming.fields.${key}`)?.text()
  expect(field('content_encoding')).toContain('gzip')
  expect(field('decode_wait')).toContain('requestTiming.missing')
  expect(field('sse_wait')).toContain('200')
  expect(field('tool_wait')).toContain('2.00s')
  wrapper.unmount()
})

it('PR610 shows separately sampled server totals when legacy passthrough already includes queue', async () => {
  mocks.get.mockResolvedValue({ traces: [], retention_days: 30 })
  const record = { ...row(610), first_token_ms: 32_000, duration_ms: 50_000, queue_wait_ms: 30_000,
    queue_timing: { status: 'measured', source: 'ws_frame_passthrough', scope: 'accepted_frame_including_admission', queue_clamped: false,
      server_first_token_ms: 32_000, server_duration_ms: 50_000, forward_first_token_ms: 2_000, forward_duration_ms: 20_000 } }
  const wrapper = mount(UsageTimingDialog, { props: { record }, ...options })
  await flushPromises()
  expect(wrapper.text()).toContain('requestTiming.serverFirst')
  expect(wrapper.text()).toContain('32.00s')
  expect(wrapper.text()).not.toContain('62.00s')
  wrapper.unmount()
})

it('omits old queue values while preserving unknown server measurements in details', async () => {
  mocks.get.mockResolvedValue({ traces: [], retention_days: 30 })
  const wrapper = mount(UsageTimingDialog, { props: { record: { ...row(610), queue_wait_ms: 5_000 } }, ...options })
  await flushPromises()
  const serverFirst = wrapper.findAll('div').find(n => n.element.children.length === 2 && n.element.children[0]?.textContent === 'requestTiming.serverFirst')
  expect(serverFirst?.text()).toContain('requestTiming.missing')
  expect(wrapper.text()).not.toContain('5.00s')
  wrapper.unmount()
})
