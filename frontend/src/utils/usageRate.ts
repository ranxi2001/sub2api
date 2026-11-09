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
