import { beforeEach, describe, expect, it, vi } from 'vitest'
import { observerUsageAPI } from '../observerUsage'

const get = vi.hoisted(() => vi.fn())
vi.mock('../client', () => ({ apiClient: { get } }))

describe('observer own usage API', () => {
  beforeEach(() => get.mockReset().mockResolvedValue({ data: {} }))
  it('uses only own endpoints and strips supplied user scope for every query', async () => {
    const query = { user_id: 999, account_id: 7, group_id: 8, api_key_id: 9 }
    await observerUsageAPI.list(query)
    await observerUsageAPI.getStats(query)
    await observerUsageAPI.getModelStats({ ...query, model_source: 'upstream' })
    await observerUsageAPI.getSnapshotV2(query)
    await observerUsageAPI.listErrors(query)
    expect(get.mock.calls.map(([url]) => url)).toEqual([
      '/usage', '/usage/stats', '/usage/dashboard/models', '/usage/dashboard/snapshot-v2', '/usage/errors'
    ])
    for (const [, options] of get.mock.calls) {
      expect(options.params).not.toHaveProperty('user_id')
      expect(options.params).toMatchObject({ account_id: 7, group_id: 8, api_key_id: 9 })
    }
  })
  it('keeps detail, filter choice and export requests inside the own-usage surface', async () => {
    const signal = new AbortController().signal
    await observerUsageAPI.getErrorDetail(43)
    await observerUsageAPI.filterOptions('account', 'name')
    await observerUsageAPI.list({ user_id: 99, page: 2, page_size: 100, exact_total: true }, { signal })
    expect(get).toHaveBeenCalledWith('/usage/errors/43')
    expect(get).toHaveBeenCalledWith('/usage/filter-options', { params: { kind: 'account', q: 'name' } })
    expect(get).toHaveBeenLastCalledWith('/usage', { params: { page: 2, page_size: 100, exact_total: true }, signal })
  })
})
