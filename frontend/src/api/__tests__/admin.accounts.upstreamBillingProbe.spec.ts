import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put, delete: del }
}))

import {
  getNewAPIUpstreamConfig,
  previewNewAPIUpstreamConfig,
  saveNewAPIUpstreamConfig,
  deleteNewAPIUpstreamConfig,
  getUpstreamBillingProbeSettings,
  previewUpstreamGroup,
  probeUpstreamBilling,
  probeUpstreamBillingBatch,
  setUpstreamBillingProbeEnabled,
  updateUpstreamBillingProbeSettings
} from '@/api/admin/accounts'

describe('admin account upstream billing probe API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    del.mockReset()
  })

  it('uses the config endpoints and preserves explicit account and token selections', async () => {
    const config = { account_id: 7, site_url: 'https://upstream.example', configured: false, encryption_key_configured: true, accounts: [] }
    const result = { site_url: config.site_url, user_id: 42, wallet: { amount: 12.34, unit: 'USD' }, accounts: [] }
    const payload = { user_id: 42, access_token: 'fixture-secret', account_ids: [7, 8], token_selections: { '8': 19 } }
    get.mockResolvedValueOnce({ data: config })
    post.mockResolvedValueOnce({ data: result })
    put.mockResolvedValueOnce({ data: result })
    del.mockResolvedValueOnce({ data: { account_id: 7, configured: false } })
    await expect(getNewAPIUpstreamConfig(7)).resolves.toEqual(config)
    await expect(previewNewAPIUpstreamConfig(7, payload)).resolves.toEqual(result)
    await expect(saveNewAPIUpstreamConfig(7, payload)).resolves.toEqual(result)
    await expect(deleteNewAPIUpstreamConfig(7)).resolves.toEqual({ account_id: 7, configured: false })
    expect(get).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe/config')
    expect(post).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe/config/preview', payload)
    expect(put).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe/config', payload)
    expect(del).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe/config')
  })

  it('reads and updates global settings', async () => {
    const settings = { enabled: true, interval_minutes: 30 }
    get.mockResolvedValueOnce({ data: settings })
    put.mockResolvedValueOnce({ data: settings })

    await expect(getUpstreamBillingProbeSettings()).resolves.toEqual(settings)
    await expect(updateUpstreamBillingProbeSettings(settings)).resolves.toEqual(settings)
    expect(get).toHaveBeenCalledWith('/admin/accounts/upstream-billing-probe/settings')
    expect(put).toHaveBeenCalledWith('/admin/accounts/upstream-billing-probe/settings', settings)
  })

  it('uses dedicated account and batch endpoints', async () => {
    const result = { account_id: 7, snapshot: { status: 'unsupported' } }
    put.mockResolvedValueOnce({ data: {} })
    post.mockResolvedValueOnce({ data: result })
    post.mockResolvedValueOnce({ data: { results: [result] } })

    await setUpstreamBillingProbeEnabled(7, true)
    await expect(probeUpstreamBilling(7)).resolves.toEqual(result)
    await expect(probeUpstreamBillingBatch([7])).resolves.toEqual([result])

    expect(put).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe', { enabled: true })
    expect(post).toHaveBeenNthCalledWith(1, '/admin/accounts/7/upstream-billing-probe', undefined, { timeout: 45_000 })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/accounts/upstream-billing-probe/batch', { account_ids: [7] }, { timeout: 225_000 })
  })

  it('maps upstream group text to editable fields and skips empty values', async () => {
    const source = { account_id: 7 }
    post.mockResolvedValueOnce({ data: { group_name: '标准组', group_description: '共享账号' } })
    await expect(previewUpstreamGroup(source)).resolves.toEqual({ name: '标准组', notes: '共享账号' })
    post.mockResolvedValueOnce({ data: { group_name: '标准组', group_description: '' } })
    await expect(previewUpstreamGroup(source)).resolves.toEqual({ name: '标准组' })
    const guard = { source: 'synthetic-source', name: 'Original', notes: null }
    post.mockResolvedValueOnce({ data: { group_name: 'New', sync_guard: guard } })
    await expect(previewUpstreamGroup(source)).resolves.toEqual({ name: 'New', upstream_group_guard: guard })
    expect(post).toHaveBeenCalledWith('/admin/accounts/upstream-billing-probe/group-preview', source, { signal: undefined })
  })
})
