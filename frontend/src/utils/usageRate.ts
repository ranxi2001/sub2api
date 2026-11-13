/**
 * The time window a usage stats response covers, captured when the request was
 * sent so a range change cannot be paired with stats loaded for another range.
 */
export interface UsageRateWindow {
  /** The start_date sent to the stats API: YYYY-MM-DD or an RFC3339 instant. */
  start?: string | null
  /** The end_date sent to the stats API: YYYY-MM-DD (inclusive day) or an RFC3339 instant. */
  end?: string | null
  /** Epoch ms when the stats were requested; a range reaching past it ends here. */
  now: number
}

const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})$/

const localDate = (date: Date): string =>
  `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`

/** Resolve the stats endpoint's defaults once, for both the request and card. */
export const resolveUsageStatsWindow = (
  window: UsageRateWindow,
  endpoint: 'admin' | 'observer'
): UsageRateWindow & { start: string; end: string } => {
  const now = new Date(window.now)
  if (endpoint === 'observer') {
    // /usage/stats fills each absent date independently. Its inclusive end
    // date is today, so the backend's exclusive boundary is tomorrow midnight.
    return {
      start: window.start?.trim() || localDate(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 7)),
      end: window.end?.trim() || localDate(now),
      now: window.now,
    }
  }
  if (window.start && window.end) return { ...window, start: window.start, end: window.end }
  // /admin/usage/stats defaults to today so far when either date is absent.
  return {
    start: new Date(now.getFullYear(), now.getMonth(), now.getDate()).toISOString(),
    end: now.toISOString(),
    now: window.now,
  }
}

// Mirrors the backend's parseUsageDateBoundary: an RFC3339 value is an exact
// instant; a calendar date is local midnight, and an end date covers its whole
// day. The API client sends the browser timezone, so local time matches.
const parseBoundary = (value: string, end: boolean): number | null => {
  const day = DATE_ONLY.exec(value)
  if (day) {
    const date = new Date(Number(day[1]), Number(day[2]) - 1, Number(day[3]) + (end ? 1 : 0))
    return date.getTime()
  }
  const instant = Date.parse(value)
  return Number.isFinite(instant) ? instant : null
}

/**
 * Minutes already elapsed in an explicit stats window. Endpoint defaults must
 * be resolved before requesting stats. Returns null for a missing/invalid range.
 */
export const usageRateWindowMinutes = (window: UsageRateWindow | null | undefined): number | null => {
  if (!window || !Number.isFinite(window.now) || !window.start || !window.end) return null
  const startMs = parseBoundary(window.start, false)
  const endMs = parseBoundary(window.end, true)
  if (startMs == null || endMs == null) return null
  const minutes = (Math.min(endMs, window.now) - startMs) / 60_000
  return Number.isFinite(minutes) && minutes > 0 ? minutes : null
}

/** Average per minute over the window, or null when it cannot be computed. */
export const usagePerMinute = (total: number | null | undefined, window: UsageRateWindow | null | undefined): number | null => {
  const minutes = usageRateWindowMinutes(window)
  if (minutes == null || total == null || !Number.isFinite(total) || total < 0) return null
  return total / minutes
}

/**
 * What the Token usage trend needs to turn one bucket's totals into per-minute
 * rates. Like UsageRateWindow, it is captured when the trend is requested.
 */
export interface UsageTrendRateContext {
  /** start_date/end_date sent with the trend request, and when it was sent. */
  window: UsageRateWindow
  /** The granularity sent with the trend request ('hour' or 'day'). */
  granularity: string
  /** IANA zone the backend formats bucket labels in (public settings server_timezone). */
  serverTimezone?: string | null
  /** That zone's UTC offset such as "+08:00", used when the name is not an IANA zone. */
  serverUTCOffset?: string | null
}

const HOUR_BUCKET = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}):00$/
const UTC_OFFSET = /^([+-])(\d{2}):(\d{2})$/
const DAY_MS = 86_400_000
const zoneFormatters = new Map<string, Intl.DateTimeFormat | null>()

const zoneFormatter = (zone: string): Intl.DateTimeFormat | null => {
  if (!zoneFormatters.has(zone)) {
    let formatter: Intl.DateTimeFormat | null = null
    try {
      formatter = new Intl.DateTimeFormat('en-US', {
        timeZone: zone,
        hourCycle: 'h23',
        year: 'numeric',
        month: 'numeric',
        day: 'numeric',
        hour: 'numeric',
        minute: 'numeric',
        second: 'numeric',
      })
    } catch {
      // Go also accepts names such as "Local" that Intl rejects.
    }
    zoneFormatters.set(zone, formatter)
  }
  return zoneFormatters.get(zone) ?? null
}

/** The zone's UTC offset in ms at an instant. */
const zoneOffsetAt = (formatter: Intl.DateTimeFormat, instant: number): number => {
  const part: Record<string, number> = {}
  for (const { type, value } of formatter.formatToParts(instant)) part[type] = Number(value)
  const wall = Date.UTC(part.year, part.month - 1, part.day, part.hour, part.minute, part.second)
  return wall - Math.floor(instant / 1000) * 1000
}

// The backend labels trend buckets with TO_CHAR in the server timezone.
const serverWallTime = (ctx: UsageTrendRateContext, year: number, month: number, day: number, hour: number): number => {
  const wall = Date.UTC(year, month - 1, day, hour)
  const formatter = ctx.serverTimezone ? zoneFormatter(ctx.serverTimezone) : null
  if (formatter) {
    // A wall time repeated by a DST fold maps to two instants: the bucket
    // starts at the first. One skipped by a DST gap maps to none: use the end
    // of the gap, where the next bucket starts.
    const before = wall - zoneOffsetAt(formatter, wall - DAY_MS)
    const after = wall - zoneOffsetAt(formatter, wall + DAY_MS)
    const valid = [before, after].filter((instant) => zoneOffsetAt(formatter, instant) === wall - instant)
    return valid.length > 0 ? Math.min(...valid) : before
  }
  const offset = UTC_OFFSET.exec(ctx.serverUTCOffset ?? '')
  if (offset) {
    const offsetMs = (Number(offset[2]) * 60 + Number(offset[3])) * 60_000
    return wall - (offset[1] === '-' ? -offsetMs : offsetMs)
  }
  // No server zone known: assume the browser's, as the API's calendar dates do.
  return new Date(year, month - 1, day, hour).getTime()
}

const trendBucketBounds = (label: string, ctx: UsageTrendRateContext): { start: number; end: number } | null => {
  if (ctx.granularity === 'hour') {
    const hour = HOUR_BUCKET.exec(label)
    if (!hour) return null
    const [year, month, day, h] = hour.slice(1).map(Number)
    return { start: serverWallTime(ctx, year, month, day, h), end: serverWallTime(ctx, year, month, day, h + 1) }
  }
  if (ctx.granularity === 'day') {
    const date = DATE_ONLY.exec(label)
    if (!date) return null
    const [year, month, day] = date.slice(1).map(Number)
    return { start: serverWallTime(ctx, year, month, day, 0), end: serverWallTime(ctx, year, month, day + 1, 0) }
  }
  return null
}

/**
 * Minutes a trend bucket covers within the requested range, up to when the
 * trend was requested: the first hour of a rolling range or the hour in
 * progress count only their own minutes. A range side that was not sent is
 * bounded by the bucket alone. Returns null when it cannot be worked out.
 */
export const usageTrendBucketMinutes = (label: string, ctx: UsageTrendRateContext | null | undefined): number | null => {
  if (!ctx || !Number.isFinite(ctx.window.now)) return null
  const bucket = trendBucketBounds(label, ctx)
  if (!bucket) return null
  let start = bucket.start
  let end = Math.min(bucket.end, ctx.window.now)
  if (ctx.window.start) {
    const rangeStart = parseBoundary(ctx.window.start, false)
    if (rangeStart == null) return null
    start = Math.max(start, rangeStart)
  }
  if (ctx.window.end) {
    const rangeEnd = parseBoundary(ctx.window.end, true)
    if (rangeEnd == null) return null
    end = Math.min(end, rangeEnd)
  }
  const minutes = (end - start) / 60_000
  return Number.isFinite(minutes) && minutes > 0 ? minutes : null
}

/** Average RPM and TPM (all tokens, cache included) of one trend bucket. */
export const usageTrendRates = (
  point: { date: string; requests: number; total_tokens: number },
  ctx: UsageTrendRateContext | null | undefined
): { rpm: number | null; tpm: number | null } => {
  const minutes = usageTrendBucketMinutes(point.date, ctx)
  const perMinute = (total: number) =>
    minutes != null && Number.isFinite(total) && total >= 0 ? total / minutes : null
  return { rpm: perMinute(point.requests), tpm: perMinute(point.total_tokens) }
}

/** "196.4", "0.52", "13.89M"; tiny positive rates show as "<0.01". */
export const formatUsageRate = (value: number | null): string => {
  if (value == null) return '—'
  if (value >= 1e9) return `${(value / 1e9).toFixed(2)}B`
  if (value >= 1e6) return `${(value / 1e6).toFixed(2)}M`
  if (value >= 1e3) return `${(value / 1e3).toFixed(2)}K`
  if (value >= 10) return value.toFixed(1)
  if (value >= 0.01) return value.toFixed(2)
  return value > 0 ? '<0.01' : '0'
}
