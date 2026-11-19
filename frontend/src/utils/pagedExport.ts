/**
 * Fetch every page of a paginated list for a client-side export.
 *
 * Exports page through the regular list endpoint, which the panel rate limiter
 * counts per request. A long range at a small page size used to fire hundreds
 * of back-to-back requests, hit HTTP 429 part-way and throw the whole export
 * away. This helper keeps the request count low with the largest page size the
 * backend accepts, and when the limiter still answers 429 it waits for the
 * advertised Retry-After and retries the same page instead of failing.
 */

/** Largest page_size accepted by the backend list endpoints (response.ParsePagination). */
export const EXPORT_PAGE_SIZE = 1000

/** Panel rate limits use a fixed one-minute window; used when Retry-After is missing. */
export const DEFAULT_RATE_LIMIT_WAIT_SECONDS = 60

/** Upper bound for a single wait, so a bogus header cannot stall an export for long. */
export const MAX_RATE_LIMIT_WAIT_SECONDS = 120

/** Consecutive 429 answers tolerated for one page before giving up. */
export const DEFAULT_MAX_RATE_LIMIT_RETRIES = 3

export interface ExportPage<T> {
  items: T[]
  total: number
}

export interface ExportProgressState {
  fetched: number
  total: number
}

export interface ExportRateLimitState {
  page: number
  attempt: number
  waitSeconds: number
}

export interface FetchAllExportPagesOptions<T> {
  fetchPage: (page: number, pageSize: number, signal?: AbortSignal) => Promise<ExportPage<T>>
  pageSize?: number
  signal?: AbortSignal
  maxRateLimitRetries?: number
  onProgress?: (state: ExportProgressState) => void
  onRateLimited?: (state: ExportRateLimitState) => void
  /** Injectable for tests; must reject when the signal aborts. */
  wait?: (ms: number, signal?: AbortSignal) => Promise<void>
}

function abortError(): Error {
  if (typeof DOMException === 'function') {
    return new DOMException('Export cancelled', 'AbortError')
  }
  const error = new Error('Export cancelled')
  error.name = 'AbortError'
  return error
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw abortError()
}

export function waitWithSignal(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortError())
      return
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(abortError())
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * Seconds to wait before retrying when the error is an HTTP 429, or null for
 * any other error. Reads the `retry_after_seconds` field the API client copies
 * from the Retry-After header and clamps it to [1, MAX_RATE_LIMIT_WAIT_SECONDS].
 */
export function rateLimitWaitSeconds(error: unknown): number | null {
  if (!error || typeof error !== 'object') return null
  const { status, retry_after_seconds: retryAfter } = error as { status?: unknown; retry_after_seconds?: unknown }
  if (status !== 429) return null
  if (typeof retryAfter !== 'number' || !Number.isFinite(retryAfter) || retryAfter <= 0) {
    return DEFAULT_RATE_LIMIT_WAIT_SECONDS
  }
  return Math.min(MAX_RATE_LIMIT_WAIT_SECONDS, Math.max(1, Math.ceil(retryAfter)))
}

/** True when an error came from aborting the export (cancel button or unmount). */
export function isExportAbortError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const { name, code } = error as { name?: unknown; code?: unknown }
  return name === 'AbortError' || name === 'CanceledError' || code === 'ERR_CANCELED'
}

export async function fetchAllExportPages<T>(options: FetchAllExportPagesOptions<T>): Promise<T[]> {
  const pageSize = options.pageSize ?? EXPORT_PAGE_SIZE
  const maxRetries = options.maxRateLimitRetries ?? DEFAULT_MAX_RATE_LIMIT_RETRIES
  const wait = options.wait ?? waitWithSignal
  const { signal } = options
  const items: T[] = []
  let page = 1
  let attempt = 0

  for (;;) {
    throwIfAborted(signal)
    let response: ExportPage<T>
    try {
      response = await options.fetchPage(page, pageSize, signal)
    } catch (error) {
      const waitSeconds = rateLimitWaitSeconds(error)
      if (waitSeconds === null || attempt >= maxRetries || signal?.aborted) throw error
      attempt++
      options.onRateLimited?.({ page, attempt, waitSeconds })
      await wait(waitSeconds * 1000, signal)
      continue
    }
    throwIfAborted(signal)
    attempt = 0
    const pageItems = response.items ?? []
    items.push(...pageItems)
    const total = Math.max(response.total ?? 0, items.length)
    options.onProgress?.({ fetched: items.length, total })
    if (pageItems.length < pageSize || items.length >= total) return items
    page++
  }
}
