import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
const getCheckout = vi.hoisted(() => vi.fn())
const verify = vi.hoisted(() => vi.fn())
const cancel = vi.hoisted(() => vi.fn())
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ params: { outTradeNo: 'owned-order' } }) }))
vi.mock('@/api/payment', () => ({ paymentAPI: { getCryptoCheckout: getCheckout, verifyOrder: verify, cancelOrder: cancel } }))
vi.mock('qrcode', () => ({ default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,qr') } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('@/i18n', () => ({ i18n: { global: { t: (key: string) => key } } }))
import CryptoPaymentView from '../CryptoPaymentView.vue'
const checkout = { order_id: 7, out_trade_no: 'owned-order', status: 'PENDING', network: 'usdc.base', crypto: 'USDC', address: '0x1111111111111111111111111111111111111111', crypto_amount: '4.01', fiat_amount: '28.88', currency: 'CNY', confirming: false, expires_at: '2099-01-01T00:00:00Z' }
let wrapper: VueWrapper | undefined
beforeEach(() => { vi.useFakeTimers(); vi.clearAllMocks(); getCheckout.mockResolvedValue({ data: { ...checkout } }); verify.mockResolvedValue({}); cancel.mockResolvedValue({}) })
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })
function render() { wrapper = mount(CryptoPaymentView, { global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } }); return wrapper }

describe('CryptoPaymentView', () => {
  it('copies the receiving address on the HTTP IP deployment', async () => {
    const execCommand = vi.fn(() => true)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: execCommand })
    vi.stubGlobal('isSecureContext', false)
    try {
      const page = render(); await flushPromises()
      await page.findAll('button').find(button => button.text() === 'payment.cryptoCheckout.copyAddress')!.trigger('click')
      await flushPromises()
      expect(execCommand).toHaveBeenCalledWith('copy')
      expect(page.text()).toContain('payment.cryptoCheckout.copied')
    } finally {
      vi.unstubAllGlobals()
      Reflect.deleteProperty(document, 'execCommand')
    }
  })
  it('shows the exact crypto amount, selected network and receiving address', async () => {
    const page = render(); await flushPromises()
    expect(page.text()).toContain('4.01 USDC')
    expect(page.text()).toContain('28.88 CNY')
    expect(page.text()).toContain('USDC (Base)')
    expect(page.get('[data-testid="receiving-address"]').text()).toBe(checkout.address)
    expect(page.find('img').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(5000)
    expect(verify).toHaveBeenCalledWith('owned-order')
  })
  it('hides payment instructions after detecting a transfer and waits for confirmations', async () => {
    getCheckout.mockResolvedValue({ data: { ...checkout, confirming: true } })
    const page = render(); await flushPromises()
    expect(page.text()).toContain('payment.cryptoCheckout.confirming')
    expect(page.find('img').exists()).toBe(false)
    expect(page.find('[data-testid="receiving-address"]').exists()).toBe(false)
    expect(page.findAll('button').some(button => button.text() === 'common.cancel')).toBe(false)
  })
  it('does not expose stale payment data when the server rejects ownership', async () => {
    getCheckout.mockRejectedValue({ message: 'payment order not found' })
    const page = render(); await flushPromises()
    expect(page.get('[role="alert"]').text()).toBe('payment order not found')
    expect(page.find('img').exists()).toBe(false)
  })
  it('stops polling and hides the QR code when completed', async () => {
    getCheckout.mockResolvedValue({ data: { ...checkout, status: 'COMPLETED' } })
    const page = render(); await flushPromises()
    expect(page.text()).toContain('payment.cryptoCheckout.paid')
    expect(page.find('img').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(10000)
    expect(verify).not.toHaveBeenCalled()
  })
  it('hides transfer instructions during a node outage and restores them after recovery', async () => {
    getCheckout.mockResolvedValue({ data: { ...checkout, network_status: 'delayed' } })
    const page = render(); await flushPromises()
    expect(page.get('[data-testid="payment-network-delay"]').text()).toBe('payment.cryptoCheckout.networkDelayHint')
    expect(page.find('img').exists()).toBe(false)
    expect(page.find('[data-testid="receiving-address"]').exists()).toBe(false)
    getCheckout.mockResolvedValue({ data: { ...checkout, network_status: 'healthy' } })
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(page.find('[data-testid="payment-network-delay"]').exists()).toBe(false)
    expect(page.get('[data-testid="receiving-address"]').text()).toBe(checkout.address)
  })
  it('keeps checking an expired order during recovery and shows eventual fulfillment', async () => {
    getCheckout.mockResolvedValue({ data: { ...checkout, status: 'EXPIRED', expires_at: new Date(Date.now() - 60000).toISOString(), network_status: 'delayed' } })
    const page = render(); await flushPromises()
    expect(page.find('img').exists()).toBe(false)
    getCheckout.mockResolvedValue({ data: { ...checkout, status: 'COMPLETED' } })
    await vi.advanceTimersByTimeAsync(15000)
    await flushPromises()
    expect(getCheckout).toHaveBeenCalledTimes(2)
    expect(verify).toHaveBeenCalledWith('owned-order')
    expect(page.text()).toContain('payment.cryptoCheckout.paid')
    await vi.advanceTimersByTimeAsync(15000)
    expect(getCheckout).toHaveBeenCalledTimes(2)
  })
})
