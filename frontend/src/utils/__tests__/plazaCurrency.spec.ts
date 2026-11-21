import { describe, expect, it } from 'vitest'
import { formatPlazaPrice, hasPlazaCurrencyRates } from '../plazaCurrency'

describe('plaza display currency', () => {
  it('separates real USD from wallet credits for either display currency', () => {
    const cny = { currency: 'CNY' as const, usdToCny: 7, creditPerCny: 1 }
    const usd = { ...cny, currency: 'USD' as const }
    expect(formatPlazaPrice(2.25e-6, 1e6, 'credit', cny)).toBe('¥2.25')
    expect(formatPlazaPrice(0.66e-6, 1e6, 'usd', cny)).toBe('¥4.62')
    expect(formatPlazaPrice(2.25e-6, 1e6, 'credit', usd)).toBe('$0.3214285714')
    expect(formatPlazaPrice(0.66e-6, 1e6, 'usd', usd)).toBe('$0.66')
    expect(formatPlazaPrice(2.25e-6, 1e6, 'credit', { ...cny, creditPerCny: 2 })).toBe('¥1.125')
    expect(formatPlazaPrice(0.01, 1, 'credit', cny)).toBe('¥0.01')
    expect(formatPlazaPrice(0, 1, 'credit', usd)).toBe('$0.00')
    expect(formatPlazaPrice(null, 1, 'credit', cny)).toBe('-')
  })
  it.each([undefined, 0, -1, NaN, Infinity])('does not fabricate a conversion from %s', rate => {
    expect(hasPlazaCurrencyRates(rate, 1)).toBe(false)
    expect(hasPlazaCurrencyRates(7, rate)).toBe(false)
    expect(formatPlazaPrice(2, 1, 'credit')).toBe('$2.00')
  })
})
