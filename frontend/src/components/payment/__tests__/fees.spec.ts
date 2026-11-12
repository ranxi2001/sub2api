import { describe, expect, it } from 'vitest'
import { paymentFeeRange, paymentRangeFitsLimits, quotePaymentFee } from '../fees'

describe('payment fee preview', () => {
  it('preserves an explicit zero override and falls back for legacy checkout data', () => {
    expect(paymentFeeRange({ fee_rate: 0, fee_rate_min: 0, fee_rate_max: 0 }, 2)).toEqual({ min: 0, max: 0 })
    expect(paymentFeeRange({ fee_rate: 0 }, 2)).toEqual({ min: 2, max: 2 })
    expect(paymentFeeRange({ fee_rate: 0.6, fee_rate_min: 0.6, fee_rate_max: 3 }, 2)).toEqual({ min: 0.6, max: 3 })
  })

  it.each([
    [100, 0.6, 2, 0.6, 100.6],
    [100, 1.1, 2, 1.1, 101.1],
    [71.43, 2.5, 2, 1.79, 73.22],
    [1, 0.01, 2, 0.01, 1.01],
    [101, 0.6, 0, 1, 102],
    [100, 0, 2, 0, 100],
  ])('quotes %s at %s%% with %s fraction digits', (amount, rate, digits, fee, total) => {
    expect(quotePaymentFee(amount, rate, digits)).toEqual({ fee, total })
  })

  it('uses the possible payable range when checking aggregated method limits', () => {
    expect(paymentRangeFitsLimits(100, 103, 101, 102)).toBe(true)
    expect(paymentRangeFitsLimits(101, 103, 0, 100)).toBe(false)
    expect(paymentRangeFitsLimits(100, 103, 104, 0)).toBe(false)
    expect(paymentRangeFitsLimits(100, 100, 100, 100)).toBe(true)
  })
})
