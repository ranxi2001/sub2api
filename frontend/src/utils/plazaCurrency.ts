import { formatScaled } from './pricing'

export type PlazaCurrency = 'CNY' | 'USD'
export interface PlazaPriceDisplay {
  currency: PlazaCurrency
  usdToCny: number
  creditPerCny: number
}

export function hasPlazaCurrencyRates(usdToCny?: number, creditPerCny?: number): boolean {
  return typeof usdToCny === 'number' && Number.isFinite(usdToCny) && usdToCny > 0 &&
    typeof creditPerCny === 'number' && Number.isFinite(creditPerCny) && creditPerCny > 0
}

/** Pricing API amounts stay unchanged; only their presentation is converted. */
export function formatPlazaPrice(value: number | null | undefined, scale: number, source: 'credit' | 'usd', display?: PlazaPriceDisplay): string {
  if (value == null) return '-'
  if (!display || !hasPlazaCurrencyRates(display.usdToCny, display.creditPerCny)) return formatScaled(value, scale, 2)
  const cny = source === 'usd' ? value * display.usdToCny : value / display.creditPerCny
  return formatScaled(display.currency === 'CNY' ? cny : cny / display.usdToCny, scale, 2, display.currency === 'CNY' ? '¥' : '$')
}
