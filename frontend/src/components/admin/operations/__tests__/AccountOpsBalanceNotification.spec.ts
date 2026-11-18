import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import Notification from '../AccountOpsBalanceNotification.vue'
import { setAccountOpsBalanceLowNotification, type AccountOpsConfig, type AccountOpsThresholdAccount } from '@/api/admin/accountOps'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accountOps', () => ({ setAccountOpsBalanceLowNotification: vi.fn() }))
const config: AccountOpsConfig = { enabled: true, recipient: 'ops@example.test', balance_low: true, weekly_quota: true, cooldown_minutes: 60 }
const account: AccountOpsThresholdAccount = { account_id: 7, account_name: 'Example account', platform: 'anthropic', type: 'apikey', balance: 0, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }
beforeEach(() => vi.resetAllMocks())
describe('per-account request balance notifications', () => {
  it('defaults to enabled and persists only the selected account', async () => {
    const saved = { ...config, balance_low_disabled_account_ids: [7] }
    vi.mocked(setAccountOpsBalanceLowNotification).mockResolvedValue(saved)
    const wrapper = mount(Notification, { props: { account, config } })
    expect((wrapper.get('input').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('input').setValue(false)
    await flushPromises()
    expect(setAccountOpsBalanceLowNotification).toHaveBeenCalledWith(7, false)
    expect(wrapper.emitted('saved')).toEqual([[saved]])
    await wrapper.setProps({ config: saved })
    expect((wrapper.get('input').element as HTMLInputElement).checked).toBe(false)
    wrapper.unmount()
  })
  it('keeps the persisted selection when saving fails', async () => {
    vi.mocked(setAccountOpsBalanceLowNotification).mockRejectedValue(new Error('offline'))
    const wrapper = mount(Notification, { props: { account, config: { ...config, balance_low_disabled_account_ids: [7] } } })
    await wrapper.get('input').setValue(true)
    await flushPromises()
    expect((wrapper.get('input').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.emitted('saved')).toBeUndefined()
    wrapper.unmount()
  })
  it('does not save while the parent is unavailable', async () => {
    const wrapper = mount(Notification, { props: { account, config, disabled: true } })
    expect(wrapper.get('input').attributes('disabled')).toBeDefined()
    await wrapper.get('input').trigger('change')
    expect(setAccountOpsBalanceLowNotification).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
