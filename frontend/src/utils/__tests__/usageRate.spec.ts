import { describe, expect, it } from 'vitest'

import { formatUsageRate, usagePerMinute, usageRateWindowMinutes } from '../usageRate'

const localMs = (year: number, month: number, day: number, hour = 0, minute = 0) =>
  new Date(year, month - 1, day, hour, minute).getTime()

describe('usageRate', () => {
  it('uses the exact instants of a rolling range', () => {
    const window = { start: '2026-11-09T03:00:00.000Z', end: '2026-11-10T03:00:00.000Z', now: Date.parse('2026-11-10T03:00:00.000Z') }
    expect(usageRateWindowMinutes(window)).toBe(1440)
    expect(usagePerMinute(2880, window)).toBe(2)
  })

  it('treats calendar dates like the API: local midnight to the end of the last day', () => {
    const now = localMs(2026, 11, 20, 12)
    expect(usageRateWindowMinutes({ start: '2026-11-01', end: '2026-11-07', now })).toBe(7 * 1440)
  })

  it('counts a range that includes now only up to now', () => {
    const now = localMs(2026, 11, 10, 6, 30)
    expect(usageRateWindowMinutes({ start: '2026-11-10', end: '2026-11-10', now })).toBe(390)
    expect(usagePerMinute(780, { start: '2026-11-10', end: '2026-11-10', now })).toBe(2)
  })

  it('falls back to today so far when a date is missing, as the API does', () => {
    const now = localMs(2026, 11, 10, 1)
    expect(usageRateWindowMinutes({ start: undefined, end: '2026-11-10', now })).toBe(60)
  })

  it.each([
    { start: '2026-11-11', end: '2026-11-12', now: localMs(2026, 11, 10) },
    { start: 'not-a-date', end: '2026-11-10', now: localMs(2026, 11, 10, 12) },
    { start: '2026-11-10T00:00:00Z', end: '2026-11-10T00:00:00Z', now: Date.parse('2026-11-10T01:00:00Z') },
  ])('has no rate for an empty or invalid window %#', (window) => {
    expect(usageRateWindowMinutes(window)).toBeNull()
    expect(usagePerMinute(100, window)).toBeNull()
  })

  it('has no rate without a window or total', () => {
    expect(usagePerMinute(100, null)).toBeNull()
    expect(usagePerMinute(undefined, { start: '2026-11-09', end: '2026-11-09', now: localMs(2026, 11, 10) })).toBeNull()
  })

  it.each([
    [null, '—'],
    [0, '0'],
    [0.004, '<0.01'],
    [0.5234, '0.52'],
    [196.43, '196.4'],
    [1234.5, '1.23K'],
    [13_888_888, '13.89M'],
    [2_500_000_000, '2.50B'],
  ])('formats %s as %s', (value, expected) => {
    expect(formatUsageRate(value)).toBe(expected)
  })
})
