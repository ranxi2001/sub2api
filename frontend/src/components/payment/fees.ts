import type { MethodFeeRates } from '@/types/payment'

export interface FeeRange { min: number; max: number }

/** Older checkout responses exposed a zero placeholder and a separate global fee. */
export function paymentFeeRange(rates: Partial<MethodFeeRates> | undefined, globalRate = 0): FeeRange {
  const fallback = rates?.fee_rate || globalRate
  const min = rates?.fee_rate_min ?? fallback
  const max = rates?.fee_rate_max ?? min
  return { min, max }
}

/** Rates have two decimal places. Calculate in minor units to avoid float ceil errors. */
export function quotePaymentFee(amount: number, rate: number, fractionDigits: number): { fee: number; total: number } {
  if (!Number.isFinite(amount) || amount <= 0) return { fee: 0, total: 0 }
  const factor = 10 ** fractionDigits
  const minor = BigInt(Math.round(amount * factor))
  const hundredths = BigInt(Math.round(Math.max(0, rate) * 100))
  const feeMinor = (minor * hundredths + 9999n) / 10000n
  return { fee: Number(feeMinor) / factor, total: Number(minor + feeMinor) / factor }
}

export function paymentRangeFitsLimits(min: number, max: number, singleMin: number, singleMax: number): boolean {
  return !(singleMin > 0 && max < singleMin) && !(singleMax > 0 && min > singleMax)
}
