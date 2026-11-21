import { describe, expect, it } from 'vitest'
import { defaultPlazaCurrencySettings, formatPlazaPrice, hasPlazaCurrencyRates, plazaOfficialCurrency } from '../plazaCurrency'

describe('configurable plaza currencies', () => {
  it('preserves a USD deployment with no site-specific assumptions', () => {
    const display = { usdToCny: 7, settings: defaultPlazaCurrencySettings() }
    expect(formatPlazaPrice(2.25e-6, 1e6, 'credit', display)).toBe('$2.25')
    expect(formatPlazaPrice(0.66e-6, 1e6, 'usd', display)).toBe('$0.66')
    expect(plazaOfficialCurrency({ name: 'deepseek-v4-pro', platform: 'deepseek' }, display)).toBe('USD')
  })
  it('supports CNY customer prices, arbitrary platform rules and exact model exceptions', () => {
    const settings = { ...defaultPlazaCurrencySettings(), wallet_currency: 'CNY' as const, customer_currency: 'CNY' as const,
      official_platform_currencies: { 'custom-provider': 'CNY' as const }, official_model_currencies: { 'custom/exception': 'USD' as const } }
    const display = { usdToCny: 7, settings }
    expect(formatPlazaPrice(2.25e-6, 1e6, 'credit', display)).toBe('¥2.25')
    expect(formatPlazaPrice(0.66e-6, 1e6, 'usd', display, 'CNY')).toBe('¥4.62')
    expect(plazaOfficialCurrency({ name:'example', platform:'CUSTOM-PROVIDER' }, display)).toBe('CNY')
    expect(plazaOfficialCurrency({ name:'custom/exception', platform:'custom-provider' }, display)).toBe('USD')
    expect(plazaOfficialCurrency({ name:'custom/exception-preview', platform:'custom-provider' }, display)).toBe('CNY')
    expect(plazaOfficialCurrency({ name:'example', platform:'another-provider' }, display)).toBe('USD')
    expect(plazaOfficialCurrency({ name:'constructor', platform:'custom-provider' }, display)).toBe('CNY')
    expect(formatPlazaPrice(2.25e-6, 1e6, 'credit', { ...display, settings:{...settings,wallet_unit_value:0.5} })).toBe('¥1.125')
    expect(formatPlazaPrice(0, 1, 'credit', display)).toBe('¥0.00')
    expect(formatPlazaPrice(null, 1, 'credit', display)).toBe('-')
  })
  it.each([undefined, 0, -1, NaN, Infinity])('does not fabricate a conversion from %s', rate => {
    expect(hasPlazaCurrencyRates(rate, defaultPlazaCurrencySettings())).toBe(false)
    expect(formatPlazaPrice(2, 1, 'credit')).toBe('$2.00')
  })
})
