import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import RechargeCouponPicker from '../RechargeCouponPicker.vue'
const api = vi.hoisted(() => ({ mine: vi.fn(), quote: vi.fn() }))
vi.mock('@/api/coupons', () => ({ couponAPI: api }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const coupon = { id: 7, snapshot: { name: '满100减10', currency: 'CNY', min_amount: 100, stackable: false }, expires_at: '2099-12-31T00:00:00Z' }
function setup() { return mount(RechargeCouponPicker, { props: { amount: 100, method: 'alipay', currency: 'CNY' }, global: { stubs: { RouterLink: true } } }) }
beforeEach(() => { vi.useFakeTimers(); api.mine.mockReset().mockResolvedValue({ data: { items: [coupon], total: 1 } }); api.quote.mockReset() })
afterEach(() => vi.useRealTimers())
describe('recharge coupon picker', () => {
 it('blocks checkout immediately and discards stale quote responses', async () => {
  let first!: (value: unknown) => void
  api.quote.mockImplementationOnce(() => new Promise(resolve => { first = resolve })).mockResolvedValueOnce({ data: { coupon_id: 7, pay_amount: 180 } })
  const w = setup(); await flushPromises(); await w.get('select').setValue('7')
  expect(w.emitted('change')?.at(-1)).toEqual([7, null, true])
  await vi.advanceTimersByTimeAsync(250)
  await w.setProps({ amount: 200 }); await vi.advanceTimersByTimeAsync(250)
  expect(w.emitted('change')?.at(-1)).toEqual([7, { coupon_id: 7, pay_amount: 180 }, false])
  first({ data: { coupon_id: 7, pay_amount: 90 } }); await flushPromises()
  expect(w.emitted('change')?.at(-1)).toEqual([7, { coupon_id: 7, pay_amount: 180 }, false]); w.unmount()
 })
 it('retries a failed wallet request and reports an unusable selected coupon', async () => {
  api.mine.mockRejectedValueOnce(new Error('wallet unavailable'))
  const w = setup(); await flushPromises(); expect(w.get('[role=alert]').text()).toContain('wallet unavailable')
  await w.get('[role=alert] button').trigger('click'); await flushPromises(); expect(api.mine).toHaveBeenCalledTimes(2)
  api.quote.mockRejectedValueOnce(new Error('coupon expired'))
  await w.get('select').setValue('7'); await vi.advanceTimersByTimeAsync(250)
  expect(w.emitted('change')?.at(-1)).toEqual([7, null, false]); expect(w.get('[role=alert]').text()).toContain('coupon expired')
  await w.get('select').setValue('0'); expect(w.emitted('change')?.at(-1)).toEqual([0, null, false]);w.unmount()
 })
 it('keeps checkout blocked while a linked coupon loads and when it is unavailable', async () => {
  let finish!: (value: unknown) => void
  api.mine.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const w = mount(RechargeCouponPicker, { props: { amount: 100, method: 'alipay', currency: 'CNY', initialId: 7 }, global: { stubs: { RouterLink: true } } })
  expect(w.emitted('change')?.at(-1)).toEqual([7, null, true])
  finish({ data: { items: [], total: 0 } }); await flushPromises()
  expect(w.emitted('change')?.at(-1)).toEqual([7, null, false])
  expect(w.get('[role=alert]').text()).toContain('coupon.unavailableSelected')
  await w.get('select').setValue('0')
  expect(w.emitted('change')?.at(-1)).toEqual([0, null, false]);w.unmount()
 })
 it('disables coupons with a different currency or unmet minimum', async () => {
  const w = setup(); await flushPromises(); await w.setProps({ amount: 50 })
  expect(w.get('option[value="7"]').attributes()).toHaveProperty('disabled')
  await w.setProps({ amount: 100, currency: 'USD' }); expect(w.get('option[value="7"]').attributes()).toHaveProperty('disabled');w.unmount()
 })
})
