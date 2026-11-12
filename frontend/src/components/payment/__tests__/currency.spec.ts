import { describe, expect, it } from 'vitest'
import { currencySymbol, formatPaymentAmount, hasPaymentAmountPrecision } from '../currency'

describe('formatPaymentAmount', () => {
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
  })
})

describe('currencySymbol', () => {
  it('maps common payment currencies and falls back safely', () => {
    expect(currencySymbol('USD')).toBe('$')
    expect(currencySymbol('cny')).toBe('¥')
    expect(currencySymbol('EUR')).toBe('€')
    expect(currencySymbol('')).toBe('¥')
    expect(currencySymbol('XYZ')).toBe('XYZ')
  })
})

describe('payment currency precision', () => {
  it('matches zero-, two-, and three-decimal currencies', () => {
    expect(hasPaymentAmountPrecision(100.5, 'JPY')).toBe(false)
    expect(hasPaymentAmountPrecision(101, 'JPY')).toBe(true)
    expect(hasPaymentAmountPrecision(0.29, 'CNY')).toBe(true)
    expect(hasPaymentAmountPrecision(1.234, 'KWD')).toBe(true)
    expect(hasPaymentAmountPrecision(1.2345, 'KWD')).toBe(false)
    expect(hasPaymentAmountPrecision(1.23, 'CLF')).toBe(true)
    expect(hasPaymentAmountPrecision(1.234, 'CLF')).toBe(false)
  })
})
