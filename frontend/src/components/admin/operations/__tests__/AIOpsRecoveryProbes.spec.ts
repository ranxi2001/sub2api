import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AIOpsRecoveryProbes from '../AIOpsRecoveryProbes.vue'
import type { AIOpsPolicy, AIOpsRecoveryProbe } from '@/api/admin/aiOpsControl'
import messages from '@/i18n/locales/en/aiOpsControl'

const { api, auth } = vi.hoisted(() => ({ api: { probes: vi.fn(), probe: vi.fn() }, auth: { isAdmin: true } }))
vi.mock('@/api/admin/aiOpsControl', () => ({ aiOpsControlAPI: api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
const policy = (): AIOpsPolicy => ({ control_enabled: true, revision: 5, mode: 'approve_each', halted: false, accounts: [{ account_id: 3, capacity_domain: 'provider-a' }] } as AIOpsPolicy)
const probe = (status: AIOpsRecoveryProbe['status'] = 'reserved'): AIOpsRecoveryProbe => ({ id: 'probe-1', account_id: 3, model: 'synthetic-model', status, created_at: '2026-11-05T00:00:00Z', expires_at: '2026-11-05T00:05:00Z', owner_revision: 4, target_revision: 8, reason_code: '' })
const mountProbes = () => mount(AIOpsRecoveryProbes, { props: { policy: policy() }, global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { aiOpsControl: messages } } })] } })
let wrapper: ReturnType<typeof mountProbes> | undefined
async function fill(): Promise<void> {
  await wrapper!.get('[data-testid="ai-probe-account"]').setValue(3)
  await wrapper!.get('[data-testid="ai-probe-model"]').setValue('synthetic-model')
  await wrapper!.get('[data-testid="ai-probe-consent"]').setValue(true)
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-05T00:00:00Z'))
  auth.isAdmin = true
  api.probes.mockReset().mockResolvedValue({ items: [], total: 0 })
  api.probe.mockReset().mockResolvedValue(probe())
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })

describe('AI account recovery probes', () => {
  it('requires an account, model, separate paid-request consent and an explicit click', async () => {
    wrapper = mountProbes(); await flushPromises()
    expect(api.probe).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-probe-account"]').setValue(3)
    await wrapper.get('[data-testid="ai-probe-model"]').setValue('synthetic-model')
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-probe-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-probe-start"]').trigger('click'); await flushPromises()
    expect(api.probe).toHaveBeenCalledOnce()
    expect(api.probe).toHaveBeenCalledWith(3, 'synthetic-model', expect.any(AbortSignal))
    expect((wrapper.get('[data-testid="ai-probe-consent"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-testid="ai-probe-record"]').text()).toContain(messages.probes.status.reserved)
  })

  it('records passed evidence without sending another probe or restoring scheduling', async () => {
    api.probes.mockResolvedValueOnce({ items: [], total: 0 }).mockResolvedValue({ items: [probe('passed')], total: 1 })
    wrapper = mountProbes(); await flushPromises(); await fill()
    await wrapper.get('[data-testid="ai-probe-start"]').trigger('click'); await flushPromises()
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.get('[data-testid="ai-probe-record"]').text()).toContain(messages.probes.status.passed)
    expect(wrapper.text()).toContain(messages.probes.passedHint)
    const reads = api.probes.mock.calls.length
    await vi.advanceTimersByTimeAsync(20000)
    expect(api.probes).toHaveBeenCalledTimes(reads)
    expect(api.probe).toHaveBeenCalledOnce()
  })

  it('does not retry an uncertain submission and requires a history refresh first', async () => {
    api.probe.mockRejectedValue(new Error('sensitive provider endpoint'))
    wrapper = mountProbes(); await flushPromises(); await fill()
    await wrapper.get('[data-testid="ai-probe-start"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(messages.probes.failed)
    expect(wrapper.text()).not.toContain('sensitive provider endpoint')
    await wrapper.get('[data-testid="ai-probe-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeDefined()
    await vi.advanceTimersByTimeAsync(10000)
    expect(api.probe).toHaveBeenCalledOnce()
    await wrapper.get('[data-testid="ai-probe-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeUndefined()
  })

  it('requires refreshing after a reserved probe becomes unknown', async () => {
    api.probes.mockResolvedValueOnce({ items: [], total: 0 }).mockResolvedValue({ items: [probe('unknown')], total: 1 })
    wrapper = mountProbes(); await flushPromises(); await fill()
    await wrapper.get('[data-testid="ai-probe-start"]').trigger('click'); await flushPromises()
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(wrapper.get('[data-testid="ai-probe-record"]').text()).toContain(messages.probes.status.unknown)
    await wrapper.get('[data-testid="ai-probe-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeDefined()
    expect(api.probe).toHaveBeenCalledOnce()
  })

  it('bounds polling to 90 seconds and lets the user stop it without cancelling the probe', async () => {
    api.probes.mockResolvedValue({ items: [probe()], total: 1 })
    wrapper = mountProbes(); await flushPromises()
    await wrapper.get('[data-testid="ai-probe-stop-polling"]').trigger('click')
    const initial = api.probes.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(api.probes).toHaveBeenCalledTimes(initial)
    expect(wrapper.text()).toContain(messages.probes.stopped)
    await wrapper.get('[data-testid="ai-probe-resume-polling"]').trigger('click')
    await vi.advanceTimersByTimeAsync(90000); await flushPromises()
    expect(wrapper.text()).toContain(messages.probes.expired)
    const reads = api.probes.mock.calls.length
    await vi.advanceTimersByTimeAsync(15000)
    expect(api.probes).toHaveBeenCalledTimes(reads)
    expect(api.probe).not.toHaveBeenCalled()
  })

  it('clears paid-request consent when the target or saved policy changes', async () => {
    wrapper = mountProbes(); await flushPromises(); await fill()
    await wrapper.get('[data-testid="ai-probe-model"]').setValue('other-model')
    expect((wrapper.get('[data-testid="ai-probe-consent"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-testid="ai-probe-consent"]').setValue(true)
    await wrapper.setProps({ policy: { ...policy(), revision: 6 } })
    expect((wrapper.get('[data-testid="ai-probe-consent"]').element as HTMLInputElement).checked).toBe(false)
    expect(api.probe).not.toHaveBeenCalled()
  })

  it('blocks probing when policy or deployment execution is off', async () => {
    wrapper = mountProbes(); await flushPromises(); await fill()
    await wrapper.setProps({ policy: { ...policy(), control_enabled: false } })
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ policy: { ...policy(), mode: 'disabled' } })
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeDefined()
    expect(api.probe).not.toHaveBeenCalled()
  })

  it('allows an explicitly authorized probe during an emergency stop without releasing the stop', async () => {
    api.probe.mockResolvedValue({ ...probe('passed'), effort: 'high', protocol: 'native_http' })
    wrapper = mountProbes(); await flushPromises()
    await wrapper.setProps({ policy: { ...policy(), halted: true } })
    await fill()
    expect(wrapper.get('[data-testid="ai-probe-start"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="ai-probe-start"]').trigger('click'); await flushPromises()
    expect(api.probe).toHaveBeenCalledOnce()
    expect(wrapper.props('policy').halted).toBe(true)
    expect(wrapper.get('[data-testid="ai-probe-record"]').text()).toContain('native_http')
    expect(wrapper.get('[data-testid="ai-probe-record"]').text()).toContain('high')
  })

  it('aborts a pending history read when the component unmounts', async () => {
    api.probes.mockImplementation(() => new Promise(() => {}))
    wrapper = mountProbes(); await flushPromises()
    const signal = api.probes.mock.calls[0][0] as AbortSignal
    wrapper.unmount(); wrapper = undefined
    expect(signal.aborted).toBe(true)
  })
})
