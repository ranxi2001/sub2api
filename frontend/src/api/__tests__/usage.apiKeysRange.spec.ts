import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getDashboardApiKeysUsage } from '../usage'
import { apiClient } from '../client'

vi.mock('../client', () => ({ apiClient: { post: vi.fn() } }))

describe('API key usage date ranges', () => {
  beforeEach(() => vi.resetAllMocks())

  it('sends the calendar range and timezone in the POST body with cancellation', async () => {
    const stats = { 7: { api_key_id: 7, today_actual_cost: 1.25, total_actual_cost: 12.5 } }
    vi.mocked(apiClient.post).mockResolvedValue({ data: { stats } })
    const signal = new AbortController().signal
    expect(await getDashboardApiKeysUsage([7], {
      start_date: '2026-11-01', end_date: '2026-11-03', timezone: 'Asia/Shanghai', signal,
    })).toEqual({ stats })
    expect(apiClient.post).toHaveBeenCalledWith('/usage/dashboard/api-keys-usage', {
      api_key_ids: [7], start_date: '2026-11-01', end_date: '2026-11-03', timezone: 'Asia/Shanghai',
    }, { signal })
  })

  it('preserves calls without dates', async () => {
    vi.mocked(apiClient.post).mockResolvedValue({ data: { stats: {} } })
    expect(await getDashboardApiKeysUsage([7])).toEqual({ stats: {} })
    expect(JSON.stringify(vi.mocked(apiClient.post).mock.calls[0][1])).toBe('{"api_key_ids":[7]}')
  })
})
