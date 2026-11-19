import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  DEFAULT_RATE_LIMIT_WAIT_SECONDS,
  EXPORT_PAGE_SIZE,
  MAX_RATE_LIMIT_WAIT_SECONDS,
  fetchAllExportPages,
  isExportAbortError,
  rateLimitWaitSeconds,
  waitWithSignal,
} from '../pagedExport'

const rows = (start: number, count: number) => Array.from({ length: count }, (_, i) => start + i)
const rateLimited = (retryAfter?: number) => ({ status: 429, code: 'RATE_LIMITED', retry_after_seconds: retryAfter })

afterEach(() => {
  vi.useRealTimers()
})

describe('rateLimitWaitSeconds', () => {
  it('only treats HTTP 429 as retryable', () => {
    expect(rateLimitWaitSeconds({ status: 500, retry_after_seconds: 5 })).toBeNull()
    expect(rateLimitWaitSeconds({ status: 0, code: 'ERR_NETWORK' })).toBeNull()
    expect(rateLimitWaitSeconds(new Error('boom'))).toBeNull()
    expect(rateLimitWaitSeconds(undefined)).toBeNull()
  })

  it('uses Retry-After, falls back to the one-minute window and clamps bogus values', () => {
    expect(rateLimitWaitSeconds(rateLimited(37))).toBe(37)
    expect(rateLimitWaitSeconds(rateLimited(0.2))).toBe(1)
    expect(rateLimitWaitSeconds(rateLimited())).toBe(DEFAULT_RATE_LIMIT_WAIT_SECONDS)
    expect(rateLimitWaitSeconds(rateLimited(0))).toBe(DEFAULT_RATE_LIMIT_WAIT_SECONDS)
    expect(rateLimitWaitSeconds(rateLimited(86400))).toBe(MAX_RATE_LIMIT_WAIT_SECONDS)
  })
})

describe('fetchAllExportPages', () => {
  it('pages at the backend maximum page size until a short page and reports progress', async () => {
    const fetchPage = vi.fn(async (page: number, pageSize: number) => ({
      items: page < 3 ? rows((page - 1) * pageSize, pageSize) : rows(2 * pageSize, 5),
      total: 2 * pageSize + 5,
    }))
    const onProgress = vi.fn()

    const items = await fetchAllExportPages({ fetchPage, onProgress })

    expect(fetchPage.mock.calls.map(([page, pageSize]) => [page, pageSize])).toEqual([
      [1, EXPORT_PAGE_SIZE],
      [2, EXPORT_PAGE_SIZE],
      [3, EXPORT_PAGE_SIZE],
    ])
    expect(items).toEqual(rows(0, 2 * EXPORT_PAGE_SIZE + 5))
    expect(onProgress).toHaveBeenLastCalledWith({ fetched: 2 * EXPORT_PAGE_SIZE + 5, total: 2 * EXPORT_PAGE_SIZE + 5 })
  })

  it('stops once the reported total is reached even when the last page is full', async () => {
    const fetchPage = vi.fn(async (page: number) => ({ items: rows((page - 1) * 2, 2), total: 4 }))

    await expect(fetchAllExportPages({ fetchPage, pageSize: 2 })).resolves.toEqual([0, 1, 2, 3])
    expect(fetchPage).toHaveBeenCalledTimes(2)
  })

  it('waits for Retry-After on 429 and retries the same page instead of failing', async () => {
    const fetchPage = vi.fn()
      .mockResolvedValueOnce({ items: rows(0, 2), total: 3 })
      .mockRejectedValueOnce(rateLimited(37))
      .mockResolvedValueOnce({ items: rows(2, 1), total: 3 })
    const wait = vi.fn().mockResolvedValue(undefined)
    const onRateLimited = vi.fn()

    const items = await fetchAllExportPages({ fetchPage, pageSize: 2, wait, onRateLimited })

    expect(items).toEqual([0, 1, 2])
    expect(fetchPage.mock.calls.map(([page]) => page)).toEqual([1, 2, 2])
    expect(wait).toHaveBeenCalledTimes(1)
    expect(wait.mock.calls[0][0]).toBe(37_000)
    expect(onRateLimited).toHaveBeenCalledWith({ page: 2, attempt: 1, waitSeconds: 37 })
  })

  it('resets the retry budget after a page succeeds', async () => {
    const fetchPage = vi.fn()
      .mockRejectedValueOnce(rateLimited(1))
      .mockResolvedValueOnce({ items: rows(0, 2), total: 4 })
      .mockRejectedValueOnce(rateLimited(1))
      .mockResolvedValueOnce({ items: rows(2, 2), total: 4 })
    const wait = vi.fn().mockResolvedValue(undefined)

    await expect(fetchAllExportPages({ fetchPage, pageSize: 2, wait, maxRateLimitRetries: 1 })).resolves.toEqual([0, 1, 2, 3])
    expect(wait).toHaveBeenCalledTimes(2)
  })

  it('gives up with the original error after the retry budget for one page is spent', async () => {
    const error = rateLimited(5)
    const fetchPage = vi.fn().mockRejectedValue(error)
    const wait = vi.fn().mockResolvedValue(undefined)

    await expect(fetchAllExportPages({ fetchPage, wait, maxRateLimitRetries: 2 })).rejects.toBe(error)
    expect(fetchPage).toHaveBeenCalledTimes(3)
    expect(wait).toHaveBeenCalledTimes(2)
  })

  it('does not retry other errors', async () => {
    const error = { status: 500, message: 'internal' }
    const fetchPage = vi.fn().mockRejectedValue(error)
    const wait = vi.fn()

    await expect(fetchAllExportPages({ fetchPage, wait })).rejects.toBe(error)
    expect(fetchPage).toHaveBeenCalledTimes(1)
    expect(wait).not.toHaveBeenCalled()
  })

  it('stops with an abort error when cancelled during a rate-limit wait', async () => {
    vi.useFakeTimers()
    const controller = new AbortController()
    const fetchPage = vi.fn().mockRejectedValue(rateLimited(30))

    const exporting = fetchAllExportPages({ fetchPage, signal: controller.signal })
    const outcome = exporting.then(() => null, (error: unknown) => error)
    await vi.advanceTimersByTimeAsync(1_000)
    controller.abort()

    const error = await outcome
    expect(isExportAbortError(error)).toBe(true)
    expect(fetchPage).toHaveBeenCalledTimes(1)
  })
})

describe('waitWithSignal and isExportAbortError', () => {
  it('resolves after the delay and rejects immediately for an aborted signal', async () => {
    vi.useFakeTimers()
    const done = vi.fn()
    void waitWithSignal(1_500).then(done)
    await vi.advanceTimersByTimeAsync(1_499)
    expect(done).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(done).toHaveBeenCalled()

    const controller = new AbortController()
    controller.abort()
    await expect(waitWithSignal(10, controller.signal)).rejects.toSatisfy(isExportAbortError)
  })

  it('recognizes axios cancellations but not ordinary failures', () => {
    expect(isExportAbortError({ code: 'ERR_CANCELED' })).toBe(true)
    expect(isExportAbortError({ name: 'CanceledError' })).toBe(true)
    expect(isExportAbortError({ status: 429 })).toBe(false)
    expect(isExportAbortError(null)).toBe(false)
  })
})
