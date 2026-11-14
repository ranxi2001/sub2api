import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AdminOrdersView from '../AdminOrdersView.vue'

const api = vi.hoisted(() => ({ getOrders: vi.fn(), retryRecharge: vi.fn() }))
vi.mock('@/api/admin/payment', () => ({ adminPaymentAPI: api, default: api }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/payment/AdminRefundDialog.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const fixtures = ['CANCELLED', 'EXPIRED'].map((status, i) => ({
  id: i + 11, status, paid_at: '2026-11-14T12:00:00Z',
  coupon: { review_required: true },
}))

describe('coupon recovery manual review', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    api.getOrders.mockResolvedValue({ data: { items: [...fixtures, { id: 99, status: 'CANCELLED', paid_at: null }], total: 3 } })
    api.retryRecharge.mockResolvedValue({ data: {} })
    api.retryRecharge.mockClear()
  })

  it('offers the existing explicit approval for paid cancellation/expiry holds only', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const w = mount(AdminOrdersView, { global: { stubs: {
      AppLayout: { template: '<main><slot /></main>' },
      OrderTable: { props: ['orders'], template: '<div><div v-for="row in orders" :key="row.id" :data-order="row.id"><slot name="actions" :row="row" /></div></div>' },
      Select: true, Icon: true, Pagination: true, BaseDialog: true, AdminRefundDialog: true,
    } } })
    await flushPromises()
    for (const order of fixtures) {
      const retry = w.get(`[data-order="${order.id}"]`).findAll('button').find(b => b.text() === 'payment.admin.retry')
      expect(retry, 'F2 manual reconciliation must expose approval for a paid recovery hold').toBeDefined()
      await retry!.trigger('click'); await flushPromises()
      expect(confirm).toHaveBeenCalledWith('coupon.honorConfirm')
      expect(api.retryRecharge).not.toHaveBeenCalled()
    }
    expect(w.get('[data-order="99"]').text()).not.toContain('payment.admin.retry')
    confirm.mockReturnValue(true)
    await w.get('[data-order="11"]').findAll('button').find(b => b.text() === 'payment.admin.retry')!.trigger('click')
    await flushPromises()
    expect(api.retryRecharge).toHaveBeenCalledTimes(1)
    expect(api.retryRecharge).toHaveBeenCalledWith(11, true)
    w.unmount()
  })
})
