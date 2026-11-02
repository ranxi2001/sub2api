import { describe, expect, it } from 'vitest'
import type { LotteryPrize } from '@/api/lottery'
import { createReel, landingFraction, reelOffset, reelKeyframes, REEL_DURATION, WINNER_INDEX } from './reel'
import { isDefinitiveDrawError, readDrawKeys, serializeDrawKeys } from './batch'

const prize = (id: number, amount: number, overrides = {}): LotteryPrize => ({
  id, name: `Prize ${id}`, kind: 'balance', amount, available: true, enabled: true, weight: 1, stock: -1, ...overrides,
} as LotteryPrize)

describe('prize reel', () => {
  it('approaches the unchanged result continuously and spends two seconds on the last card', () => {
    const frames = reelKeyframes(800, 8000, 148)
    expect((1 - frames[1].offset!) * REEL_DURATION).toBeGreaterThan(2000)
    expect(frames[2].transform).toBe('translate3d(-8000px, 0, 0)')
    const firstControls = String(frames[0].easing).match(/[\d.]+/g)!.map(Number)
    const approach = 8000 - 148 * 0.85
    const firstEndVelocity = (1 - firstControls[3]) / (1 - firstControls[2]) * (approach - 800) / (REEL_DURATION * 0.62)
    const secondStartVelocity = 2 * (8000 - approach) / (REEL_DURATION * 0.38)
    expect(firstEndVelocity).toBeCloseTo(secondStartVelocity, 8)
    expect(firstEndVelocity).toBeGreaterThan(0)
  })
  it('lands within the actual winner at both edges and centrally', () => {
    for (const sequence of [[0, 0, 0], [0.74, 0.9, 0.99], [0.75, 0], [0.99, 0.99]]) {
      const fraction = landingFraction(() => sequence.shift()!)
      for (const width of [124, 148]) {
        const offset = reelOffset(WINNER_INDEX, width, 8, fraction)
        const left = WINNER_INDEX * (width + 8) - offset
        expect(left).toBeLessThan(0)
        expect(left + width).toBeGreaterThan(0)
      }
    }
  })
  it('uses edge stops for three quarters of the visual range', () => {
    const fractions = Array.from({ length: 100 }, (_, i) => {
      let first = true
      return landingFraction(() => { if (first) { first = false; return i / 100 } return 0.5 })
    })
    expect(fractions.filter(f => f < 0.2 || f > 0.8)).toHaveLength(75)
  })
  it('uses the awarded snapshot even after deletion, keeping high-value available neighbours', () => {
    const winner = prize(99, 5, { name: 'Historical prize', available: false, stock: 0 })
    const cards = createReel([prize(1, 10), prize(2, 500), prize(3, 1000, { stock: 0 }), prize(4, 100, { available: false })], winner, () => 0)
    expect(cards[WINNER_INDEX]).toBe(winner)
    expect(cards[WINNER_INDEX - 1].amount).toBe(500)
    expect(cards[WINNER_INDEX + 1].amount).toBe(10)
    expect(cards.some(p => p.id === 3 || p.id === 4)).toBe(false)
  })
  it('handles no available filler and all three prize types', () => {
    expect(createReel([])).toEqual([])
    for (const kind of ['balance', 'subscription', 'text']) {
      const winner = prize(9, 5, { kind })
      expect(createReel([], winner)[WINNER_INDEX]).toEqual(winner)
    }
  })
})

describe('draw recovery keys', () => {
  it('reads legacy single keys and versioned batches', () => {
    for (const keys of [['one'], ['one', 'two', 'three']]) expect(readDrawKeys(serializeDrawKeys(keys))).toEqual(keys)
  })
  it('rejects malformed batches and duplicate keys', () => {
    for (const value of [null, '', '{', '{"version":1,"keys":["a","a","a"]}', '{"version":1,"keys":["a",null,"c"]}']) expect(readDrawKeys(value)).toEqual([])
  })
  it('preserves keys for ambiguous timeouts, rate limits, and server failures', () => {
    for (const status of [undefined, 408, 429, 500, 503]) expect(isDefinitiveDrawError({ status })).toBe(false)
    for (const status of [400, 403, 404, 409]) expect(isDefinitiveDrawError({ status })).toBe(true)
  })
})
