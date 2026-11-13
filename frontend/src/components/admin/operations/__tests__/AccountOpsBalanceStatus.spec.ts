import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import Status from '../AccountOpsBalanceStatus.vue'
import { getUpstreamBillingProbeSettings, probeUpstreamBilling, setUpstreamBillingProbeEnabled } from '@/api/admin/accounts'
import type { AccountOpsThresholdAccount } from '@/api/admin/accountOps'
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accounts', () => ({ getUpstreamBillingProbeSettings: vi.fn(), probeUpstreamBilling: vi.fn(), setUpstreamBillingProbeEnabled: vi.fn() }))
const account: AccountOpsThresholdAccount = { account_id: 1, account_name: 'Demo', platform: 'openai', type: 'apikey', unit: 'USD', balance: null, balance_status: 'stale', received_at: '2026-01-01T00:00:00Z', probe_enabled: false, last_known_balance: 42, usage_windows: [] }
const create = () => mount(Status, { props: { accounts: [account] } })
beforeEach(() => { vi.resetAllMocks(); vi.mocked(getUpstreamBillingProbeSettings).mockResolvedValue({ enabled: true, interval_minutes: 30 }) })
describe('balance collection recovery', () => {
  it('explains disabled collection and keeps historical balances labelled', () => {
    const wrapper = create()
    expect(wrapper.text()).toContain('accountOps.collectionDisabled')
    expect(wrapper.text()).toContain('accountOps.lastKnownBalance')
    expect(wrapper.text()).toContain('accountOps.sampleTime')
    expect(probeUpstreamBilling).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('enables only the chosen account before probing and reloads after an unsupported response', async () => {
    vi.mocked(probeUpstreamBilling).mockResolvedValue({ account_id: 1, snapshot: { status: 'unsupported', last_attempt_at: '', next_probe_at: '', balance: { status: 'unsupported', last_attempt_at: '' } } })
    const wrapper = create()
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(setUpstreamBillingProbeEnabled).toHaveBeenCalledTimes(1)
    expect(setUpstreamBillingProbeEnabled).toHaveBeenCalledWith(1, true)
    expect(probeUpstreamBilling).toHaveBeenCalledTimes(1)
    expect(probeUpstreamBilling).toHaveBeenCalledWith(1)
    expect(vi.mocked(setUpstreamBillingProbeEnabled).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(probeUpstreamBilling).mock.invocationCallOrder[0]!)
    expect(wrapper.text()).toContain('accountOps.balanceUnsupportedHint')
    expect(wrapper.emitted('updated')).toHaveLength(1)
    wrapper.unmount()
  })
  it('does not silently enable global collection', async () => {
    vi.mocked(getUpstreamBillingProbeSettings).mockResolvedValue({ enabled: false, interval_minutes: 30 })
    const wrapper = create()
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(setUpstreamBillingProbeEnabled).not.toHaveBeenCalled()
    expect(probeUpstreamBilling).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('accountOps.collectionGloballyDisabled')
    wrapper.unmount()
  })
  it('reloads the saved toggle even if the subsequent probe fails', async () => {
    vi.mocked(probeUpstreamBilling).mockRejectedValue(new Error('offline failure'))
    const wrapper = create()
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(setUpstreamBillingProbeEnabled).toHaveBeenCalledOnce()
    expect(wrapper.emitted('updated')).toHaveLength(1)
    expect(wrapper.get('[role="status"]').text()).toBeTruthy()
    wrapper.unmount()
  })
  it('blocks duplicate actions and stops the probe after leaving during enable', async () => {
    let resolve!: () => void
    vi.mocked(setUpstreamBillingProbeEnabled).mockImplementation(() => new Promise<void>(done => { resolve = done }))
    const wrapper = create()
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    wrapper.unmount(); resolve(); await flushPromises()
    expect(probeUpstreamBilling).not.toHaveBeenCalled()
  })
})
