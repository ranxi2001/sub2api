import { describe, expect, it } from 'vitest'

import {
  formatUsageRate,
  resolveUsageStatsWindow,
  usagePerMinute,
  usageRateWindowMinutes,
  usageTrendBucketMinutes,
  usageTrendRates,
} from '../usageRate'

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
    // 7 days of 1440 minutes, plus the hour a DST change on 2026-11-01 adds in zones such as America/Los_Angeles.
    expect(usageRateWindowMinutes({ start: '2026-11-01', end: '2026-11-07', now }))
      .toBe((localMs(2026, 11, 8) - localMs(2026, 11, 1)) / 60_000)
  })

  it('counts a range that includes now only up to now', () => {
    const now = localMs(2026, 11, 10, 6, 30)
    expect(usageRateWindowMinutes({ start: '2026-11-10', end: '2026-11-10', now })).toBe(390)
    expect(usagePerMinute(780, { start: '2026-11-10', end: '2026-11-10', now })).toBe(2)
  })

  it.each([
    { start: undefined, end: '2026-11-07' },
    { start: '2026-11-01', end: undefined },
    { start: '', end: '' },
  ])('preserves the admin today-so-far default when either date is missing: %j', (dates) => {
    const now = localMs(2026, 11, 10, 1)
    const window = resolveUsageStatsWindow({ ...dates, now }, 'admin')
    expect(window).toEqual({ start: new Date(localMs(2026, 11, 10)).toISOString(), end: new Date(now).toISOString(), now })
    expect(usageRateWindowMinutes(window)).toBe(60)
    expect(usagePerMinute(120, window)).toBe(2)
  })

  it.each([
    { start: '', end: '2026-11-05', expectedStart: '2026-11-03', expectedEnd: '2026-11-05' },
    { start: '2026-11-04', end: '', expectedStart: '2026-11-04', expectedEnd: '2026-11-10' },
    { start: '', end: '', expectedStart: '2026-11-03', expectedEnd: '2026-11-10' },
  ])('fills observer boundaries independently: %j', ({ start, end, expectedStart, expectedEnd }) => {
    const now = localMs(2026, 11, 10, 6, 30)
    const window = resolveUsageStatsWindow({ start, end, now }, 'observer')
    expect(window).toEqual({ start: expectedStart, end: expectedEnd, now })
    const startMs = expectedStart === '2026-11-04' ? localMs(2026, 11, 4) : localMs(2026, 11, 3)
    const endMs = expectedEnd === '2026-11-05' ? localMs(2026, 11, 6) : now
    const minutes = (endMs - startMs) / 60_000
    expect(usageRateWindowMinutes(window)).toBe(minutes)
    expect(usagePerMinute(minutes, window)).toBe(1)
    expect(usagePerMinute(minutes * 100, window)).toBe(100)
  })

  it('keeps explicit calendar and instant windows unchanged for their endpoint', () => {
    const now = localMs(2026, 11, 20, 12)
    const calendar = { start: '2026-11-01', end: '2026-11-07', now }
    expect(resolveUsageStatsWindow(calendar, 'observer')).toEqual(calendar)
    expect(resolveUsageStatsWindow(calendar, 'admin')).toEqual(calendar)
    const rolling = { start: '2026-11-09T03:00:00Z', end: '2026-11-10T03:00:00Z', now }
    expect(resolveUsageStatsWindow(rolling, 'admin')).toEqual(rolling)
  })

  it.each([
    { start: '2026-11-11', end: '2026-11-12', now: localMs(2026, 11, 10) },
    { start: 'not-a-date', end: '2026-11-10', now: localMs(2026, 11, 10, 12) },
    { start: '2026-11-10T00:00:00Z', end: '2026-11-10T00:00:00Z', now: Date.parse('2026-11-10T01:00:00Z') },
    { start: undefined, end: '2026-11-10', now: localMs(2026, 11, 10, 12) },
    { start: '2026-11-09', end: undefined, now: localMs(2026, 11, 10, 12) },
  ])('has no rate for an empty or invalid window %#', (window) => {
    expect(usageRateWindowMinutes(window)).toBeNull()
    expect(usagePerMinute(100, window)).toBeNull()
  })

  it('has no rate without a window or total', () => {
    expect(usagePerMinute(100, null)).toBeNull()
    expect(usagePerMinute(undefined, { start: '2026-11-09', end: '2026-11-09', now: localMs(2026, 11, 10) })).toBeNull()
  })

  describe('trend buckets', () => {
    const shanghai = { serverTimezone: 'Asia/Shanghai', serverUTCOffset: '+08:00' }
    // Rolling 24 hours ending 2026-11-14 17:37 in Shanghai.
    const rolling = {
      ...shanghai,
      granularity: 'hour',
      window: { start: '2026-11-13T09:37:00Z', end: '2026-11-14T09:37:00Z', now: Date.parse('2026-11-14T09:37:00Z') },
    }

    it('counts only the minutes of an hour bucket inside the range and up to now', () => {
      expect(usageTrendBucketMinutes('2026-11-13 16:00', rolling)).toBeNull()
      expect(usageTrendBucketMinutes('2026-11-13 17:00', rolling)).toBe(23)
      expect(usageTrendBucketMinutes('2026-11-13 18:00', rolling)).toBe(60)
      expect(usageTrendBucketMinutes('2026-11-14 17:00', rolling)).toBe(37)
      expect(usageTrendBucketMinutes('2026-11-14 18:00', rolling)).toBeNull()
      expect(usageTrendRates({ date: '2026-11-13 17:00', requests: 46, total_tokens: 2_300_000 }, rolling))
        .toEqual({ rpm: 2, tpm: 100_000 })
    })

    it('counts a day bucket in the server zone, with today only up to now', () => {
      const ctx = {
        ...shanghai,
        granularity: 'day',
        // 2026-11-13 00:00 to 2026-11-15 00:00 in Shanghai, requested at 11-14 12:00 there.
        window: { start: '2026-11-12T16:00:00Z', end: '2026-11-14T16:00:00Z', now: Date.parse('2026-11-14T04:00:00Z') },
      }
      expect(usageTrendBucketMinutes('2026-11-13', ctx)).toBe(1440)
      expect(usageTrendBucketMinutes('2026-11-14', ctx)).toBe(720)
    })

    it('follows DST changes in the server zone', () => {
      const ctx = {
        serverTimezone: 'America/New_York',
        serverUTCOffset: '-05:00',
        window: { start: '2026-01-01T00:00:00Z', end: '2027-01-01T00:00:00Z', now: Date.parse('2027-01-01T00:00:00Z') },
      }
      const hour = { ...ctx, granularity: 'hour' }
      const day = { ...ctx, granularity: 'day' }
      // Clocks go back at 02:00 on 2026-11-01, so the backend labels two hours "01:00".
      expect(usageTrendBucketMinutes('2026-11-01 00:00', hour)).toBe(60)
      expect(usageTrendBucketMinutes('2026-11-01 01:00', hour)).toBe(120)
      expect(usageTrendBucketMinutes('2026-11-01 02:00', hour)).toBe(60)
      expect(usageTrendBucketMinutes('2026-11-01', day)).toBe(1500)
      expect(usageTrendBucketMinutes('2026-11-02', day)).toBe(1440)
      // Clocks skip 02:00 on 2026-03-08.
      expect(usageTrendBucketMinutes('2026-03-08 01:00', hour)).toBe(60)
      expect(usageTrendBucketMinutes('2026-03-08 03:00', hour)).toBe(60)
      expect(usageTrendBucketMinutes('2026-03-08', day)).toBe(1380)
    })

    it('falls back to the server UTC offset when the zone name is not an IANA zone', () => {
      expect(usageTrendBucketMinutes('2026-11-13 17:00', { ...rolling, serverTimezone: 'Local' })).toBe(23)
      const ctx = {
        serverUTCOffset: '-03:30',
        granularity: 'hour',
        window: { start: '2026-11-13T08:45:00Z', end: '', now: Date.parse('2026-11-14T00:00:00Z') },
      }
      // 05:00 at -03:30 starts at 08:30Z.
      expect(usageTrendBucketMinutes('2026-11-13 05:00', ctx)).toBe(45)
    })

    it('has no rate while the server zone is unknown instead of guessing the browser zone', () => {
      for (const zone of [{}, { serverTimezone: null, serverUTCOffset: null }, { serverTimezone: 'Local' }]) {
        expect(usageTrendBucketMinutes('2026-11-13 18:00', { ...rolling, serverTimezone: undefined, serverUTCOffset: undefined, ...zone }))
          .toBeNull()
      }
    })

    describe('a fold that repeats two hours (Antarctica/Troll, +02 → +00 at 2026-10-25 01:00Z)', () => {
      const troll = (window: { start?: string; end?: string; now: number }, granularity = 'hour') => ({
        serverTimezone: 'Antarctica/Troll',
        granularity,
        window: { start: '', end: '', ...window },
      })
      const later = Date.parse('2026-10-27T00:00:00Z')

      it('sums the separate UTC hours behind one label', () => {
        // 01:00 is 23:00–00:00Z and 01:00–02:00Z; 02:00 is 00:00–01:00Z and 02:00–03:00Z.
        expect(usageTrendBucketMinutes('2026-10-25 00:00', troll({ now: later }))).toBe(60)
        expect(usageTrendBucketMinutes('2026-10-25 01:00', troll({ now: later }))).toBe(120)
        expect(usageTrendBucketMinutes('2026-10-25 02:00', troll({ now: later }))).toBe(120)
        expect(usageTrendBucketMinutes('2026-10-25 03:00', troll({ now: later }))).toBe(60)
        expect(usageTrendBucketMinutes('2026-10-25', troll({ now: later }, 'day'))).toBe(26 * 60)
      })

      it('clips each UTC hour separately', () => {
        // Only the second 01:00 lies in the range.
        expect(usageTrendBucketMinutes('2026-10-25 01:00', troll({ start: '2026-10-25T01:00:00Z', now: later }))).toBe(60)
        // Half of the first 01:00 plus all of the second.
        expect(usageTrendBucketMinutes('2026-10-25 01:00', troll({ start: '2026-10-24T23:30:00Z', now: later }))).toBe(90)
        // The range ends between the two 01:00 hours: the gap (local 02:00) is not counted.
        expect(usageTrendBucketMinutes('2026-10-25 01:00', troll({ end: '2026-10-25T01:30:00Z', now: later }))).toBe(90)
        // Requested during the second 02:00: the first is complete, the second half done.
        expect(usageTrendBucketMinutes('2026-10-25 02:00', troll({ now: Date.parse('2026-10-25T02:30:00Z') }))).toBe(90)
      })

      it('leaves out the two skipped hours of the spring gap', () => {
        const spring = Date.parse('2026-03-31T00:00:00Z')
        expect(usageTrendBucketMinutes('2026-03-29 00:00', troll({ now: spring }))).toBe(60)
        expect(usageTrendBucketMinutes('2026-03-29 03:00', troll({ now: spring }))).toBe(60)
        expect(usageTrendBucketMinutes('2026-03-29', troll({ now: spring }, 'day'))).toBe(22 * 60)
      })
    })

    it('handles a half-hour fold (Australia/Lord_Howe, 02:00 +11 → 01:30 +10:30 on 2026-04-05)', () => {
      const ctx = {
        serverTimezone: 'Australia/Lord_Howe',
        granularity: 'hour',
        window: { start: '', end: '', now: Date.parse('2026-04-06T00:00:00Z') },
      }
      expect(usageTrendBucketMinutes('2026-04-05 01:00', ctx)).toBe(90)
      expect(usageTrendBucketMinutes('2026-04-05 02:00', ctx)).toBe(60)
      expect(usageTrendBucketMinutes('2026-04-05', { ...ctx, granularity: 'day' })).toBe(24.5 * 60)
    })

    it('bounds a bucket by itself on a range side that was not sent', () => {
      const ctx = { ...rolling, window: { start: '', end: '', now: Date.parse('2026-11-14T00:00:00Z') } }
      expect(usageTrendBucketMinutes('2026-11-13 17:00', ctx)).toBe(60)
      expect(usageTrendBucketMinutes('2026-11-14 07:00', ctx)).toBe(60)
      expect(usageTrendBucketMinutes('2026-11-14 08:00', ctx)).toBeNull()
    })

    it.each([
      ['no context', '2026-11-13 17:00', null],
      ['a day label on an hourly trend', '2026-11-13', rolling],
      ['an hour label on a daily trend', '2026-11-13 17:00', { ...rolling, granularity: 'day' }],
      ['an unsupported granularity', '2026-11-13', { ...rolling, granularity: 'week' }],
      ['an invalid range start', '2026-11-13 18:00', { ...rolling, window: { ...rolling.window, start: 'not-a-date' } }],
      ['an invalid request time', '2026-11-13 18:00', { ...rolling, window: { ...rolling.window, now: Number.NaN } }],
    ])('has no trend rate for %s', (_, label, ctx) => {
      expect(usageTrendBucketMinutes(label, ctx)).toBeNull()
      expect(usageTrendRates({ date: label, requests: 10, total_tokens: 10 }, ctx)).toEqual({ rpm: null, tpm: null })
    })
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
