import { describe, expect, it } from 'vitest'
import { sectorPath, wheelRotation, wheelSegments } from './wheel'

describe('weighted prize wheel', () => {
  it('excludes unavailable prizes and gives sectors their actual weighted angles', () => {
    const segments = wheelSegments([
      { id: 1, weight: 1, available: true },
      { id: 2, weight: 100, available: false },
      { id: 3, weight: 3, available: true },
    ])
    expect(segments.map((s) => [s.prize.id, s.start, s.end])).toEqual([
      [1, 0, 90],
      [3, 90, 360],
    ])
    expect(segments[0].center).toBe(45)
  })
  it('lands the selected sector under the top pointer across consecutive draws', () => {
    let rotation = 0
    for (const center of [45, 225, 180, 12.5]) {
      const next = wheelRotation(rotation, center)
      expect(next - rotation).toBeGreaterThanOrEqual(1440)
      expect((next + center) % 360).toBeCloseTo(0)
      rotation = next
    }
  })
  it('renders a complete circle for a one-prize pool and handles empty pools', () => {
    expect(sectorPath(0, 360).match(/A /g)).toHaveLength(2)
    expect(wheelSegments([])).toEqual([])
  })
})
