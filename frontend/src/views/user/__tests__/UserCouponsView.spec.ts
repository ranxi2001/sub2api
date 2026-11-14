import { describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import UserCouponsView from '../UserCouponsView.vue'
const mine = vi.hoisted(() => vi.fn())
vi.mock('@/api/coupons', () => ({ couponAPI: { mine } }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
describe('user coupon wallet', () => {
 it('shows immutable rules and permits using only available coupons', async () => {
  const base = { snapshot: { name: '满100减10', kind: 'fixed', min_amount: 100, value: 10, currency: 'CNY', max_discount: 0, stackable: false }, source: 'qq_growth', expires_at: '2099-12-31', order_id: null, used_at: null }
  mine.mockResolvedValue({ data: { total: 2, items: [{ ...base, id: 1, status: 'available' }, { ...base, id: 2, status: 'used', order_id: 9, order_status: 'COMPLETED' }] } })
  const w = shallowMount(UserCouponsView, { global: { stubs: { RouterLink: true, AppLayout: { template: '<div><slot /></div>' } } } });await flushPromises()
  expect(w.findAll('[data-testid=coupon-card]')).toHaveLength(2)
  expect(w.findAll('router-link-stub')).toHaveLength(1)
  expect(w.text()).toContain('#9');await w.get('select').setValue('used');await flushPromises()
  expect(mine).toHaveBeenLastCalledWith({ page: 1, page_size: 20, status: 'used' });w.unmount()
 })
})
