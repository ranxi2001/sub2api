import { formatScaled } from './pricing'
import type { PlazaModel, PlazaCurrencySettings } from '@/api/modelPlaza'

export type PlazaCurrency = 'CNY' | 'USD'
export interface PlazaPriceDisplay {
  usdToCny: number
  settings: PlazaCurrencySettings
}

export function defaultPlazaCurrencySettings(): PlazaCurrencySettings {
  return { wallet_currency: 'USD', wallet_unit_value: 1, customer_currency: 'USD', official_currency: 'USD', official_platform_currencies: {}, official_model_currencies: {} }
}

export function hasPlazaCurrencyRates(usdToCny?: number, settings?: PlazaCurrencySettings): boolean {
  return typeof usdToCny === 'number' && Number.isFinite(usdToCny) && usdToCny > 0 && !!settings &&
    Number.isFinite(settings.wallet_unit_value) && settings.wallet_unit_value > 0 &&
    [settings.wallet_currency, settings.customer_currency, settings.official_currency].every(c => c === 'USD' || c === 'CNY')
}

export function plazaOfficialCurrency(model: Pick<PlazaModel, 'name' | 'platform'>, display?: PlazaPriceDisplay): PlazaCurrency {
  const settings = display?.settings
  const own = (map: Record<string, PlazaCurrency> | undefined, key: string) =>
    map && Object.prototype.hasOwnProperty.call(map, key) ? map[key] : undefined
  const currency = own(settings?.official_model_currencies, model.name.trim().toLowerCase()) ??
    own(settings?.official_platform_currencies, model.platform.trim().toLowerCase()) ?? settings?.official_currency
  return currency === 'CNY' ? 'CNY' : 'USD'
}

/** Wallet reference values, official USD prices and display FX remain separate. */
export function formatPlazaPrice(value: number | null | undefined, scale: number, source: 'credit' | 'usd', display?: PlazaPriceDisplay, officialCurrency: PlazaCurrency = 'USD'): string {
  if (value == null) return '-'
  if (!display || !hasPlazaCurrencyRates(display.usdToCny, display.settings)) return formatScaled(value, scale, 2)
  const settings = display.settings
  const sourceCurrency = source === 'usd' ? 'USD' : settings.wallet_currency
  const targetCurrency = source === 'usd' ? officialCurrency : settings.customer_currency
  const amount = source === 'usd' ? value : value * settings.wallet_unit_value
  const converted = sourceCurrency === targetCurrency ? amount : targetCurrency === 'CNY' ? amount * display.usdToCny : amount / display.usdToCny
  return formatScaled(converted, scale, 2, targetCurrency === 'CNY' ? '¥' : '$')
}
