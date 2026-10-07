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
const slowFirstOutputTrace = {
  trace_id: 'trace', started_at: '2026-09-23', total_ms: 50000, status: 200,
  body_bytes: 20, body_complete: true, body_read_ms: 1, downstream_bytes: 100,
  downstream_write_ms: 1, downstream_error: false, client_disconnect: false, canceled: false,
  outcome: 'success', terminal: 'completed', events: { first_visible: 40000, first_semantic: 40000 },
  spans: [{ name: 'handler', start_ms: 0, end_ms: 50000 }, { name: 'user_queue', start_ms: 0, end_ms: 6000 }],
  attempts: [{ kind: 'egress', number: 1, cleanup_canceled: true, account_id: 1, proxy_id: 0, start_ms: 6000, end_ms: 50000, status: 200, reused: false, body_eof: false, request_bytes: 20, response_bytes: 100, events: { response_headers: 10000 } }]
}
describe('Usage timing details', () => {
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
    mocks.get.mockResolvedValue({ retention_days: 30, traces: [slowFirstOutputTrace] })
    const wrapper = mount(UsageTimingDialog, { props: { record: { ...row(1), output_tokens: 1_095, duration_ms: 9_250, first_token_ms: 9_240 } }, ...options })
    await flushPromises()
    expect(wrapper.text()).toContain('usage.latencyTps 118 t/s')
    expect(wrapper.text()).toContain('requestTiming.tpsNote')
    expect(wrapper.text()).toContain('requestTiming.health.firstSlow')
    expect(wrapper.text()).toContain('requestTiming.health.largest')
    expect(wrapper.text()).toContain('requestTiming.health.normalClose')
    expect(wrapper.findAll('strong').find(n => n.text() === '40.00s')?.element.parentElement?.className).toContain('text-orange-700')
    const reuse = wrapper.findAll('div').find(n => n.element.children.length === 2 && n.element.children[0]?.textContent === 'requestTiming.fields.reused')
    expect(reuse?.element.children[1]?.className).toContain('text-slate-600')
    wrapper.unmount()
  })
  it('explains a missing TPS instead of the formula note when only one output token was recorded', async () => {
    mocks.get.mockResolvedValue({ retention_days: 30, traces: [slowFirstOutputTrace] })
    const wrapper = mount(UsageTimingDialog, { props: { record: { ...row(1), output_tokens: 1, duration_ms: 21_135, first_token_ms: 973 } }, ...options })
    await flushPromises()
    expect(wrapper.text()).toContain('usage.latencyTps —')
    expect(wrapper.text()).toContain('usage.latencyTpsUnavailable.singleToken')
    expect(wrapper.text()).not.toContain('requestTiming.tpsNote')
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
