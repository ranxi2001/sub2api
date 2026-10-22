import { describe, it, expect } from 'vitest'
import { buildCreateOrderPayload, decidePaymentLaunch, readPaymentRecoverySnapshot } from '../paymentFlow'
import { BEPUSDT_TRADE_TYPES, PROVIDER_CONFIG_FIELDS, PROVIDER_CALLBACK_PATHS, getDefaultSupportedTypes, getAvailableTypes } from '../providerConfig'
import en from '@/i18n/locales/en/misc'
import zh from '@/i18n/locales/zh/misc'

describe('BEpusdt checkout', () => {
  it.each(BEPUSDT_TRADE_TYPES)('preserves $value through order creation and checkout recovery', ({ value }) => {
    const payload = buildCreateOrderPayload({ amount: 28.88, paymentType: value, orderType: 'balance', origin: 'https://merchant.example', isMobile: false, isWechatBrowser: false })
    expect(payload.payment_type).toBe(value)
    expect(payload.return_url).toBe('https://merchant.example/payment/result')
    for (const isMobile of [false, true]) {
      const decision = decidePaymentLaunch({ order_id: 7, amount: 28.88, pay_amount: 28.88, fee_rate: 0, expires_at: '2099-01-01T00:00:00Z', pay_url: 'https://gateway.example/pay/checkout/trade', currency: 'CNY' }, { visibleMethod: value, orderType: 'balance', isMobile })
      expect(decision.kind).toBe('redirect_waiting')
      expect(decision.paymentState.paymentType).toBe(value)
      expect(readPaymentRecoverySnapshot(JSON.stringify(decision.recovery))?.paymentType).toBe(value)
    }
  })

  it('offers named networks, defaults to TRC20, and uses native credentials', () => {
    expect(getDefaultSupportedTypes('bepusdt')).toEqual(['usdt.trc20'])
    expect(getAvailableTypes('bepusdt', [], '')).toEqual(BEPUSDT_TRADE_TYPES)
    expect(PROVIDER_CONFIG_FIELDS.bepusdt.find(field => field.key === 'apiToken')?.sensitive).toBe(true)
    expect(PROVIDER_CONFIG_FIELDS.bepusdt.find(field => field.key === 'currency')?.options?.map(option => option.value)).toEqual(['CNY', 'USD', 'EUR', 'GBP', 'JPY'])
    expect(PROVIDER_CALLBACK_PATHS.bepusdt.notifyUrl).toBe('/api/v1/payment/webhook/bepusdt')
    for (const { value, label } of BEPUSDT_TRADE_TYPES) {
      const [coin, network] = value.split('.')
      for (const locale of [en, zh]) {
        const methods = locale.payment.methods as unknown as Record<string, Record<string, string>>
        expect(methods[coin][network]).toBe(label)
      }
    }
  })
})
