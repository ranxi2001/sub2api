import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAdminSettingsStore } from '../adminSettings'
import { DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS } from '@/constants/smartOpsMenu'

const mocks = vi.hoisted(() => ({ getSettings: vi.fn(), getConfig: vi.fn() }))
vi.mock('@/api', () => ({ adminAPI: { settings: { getSettings: mocks.getSettings }, payment: { getConfig: mocks.getConfig } } }))
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setActivePinia(createPinia())
  mocks.getSettings.mockResolvedValue({ ops_monitoring_enabled: true, custom_menu_items: [{ id: 'custom', title: 'Custom' }] })
  mocks.getConfig.mockResolvedValue({ data: { enabled: true } })
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => { vi.restoreAllMocks(); localStorage.clear() })

describe('admin settings fetch retry', () => {
  it.each(['settings', 'payment'])('retries a failed initial %s request without forcing a refresh', async (source) => {
    const request = source === 'settings' ? mocks.getSettings : mocks.getConfig
    request.mockRejectedValueOnce(new Error('Temporarily unavailable'))
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.loaded).toBe(false)
    expect(store.loading).toBe(false)
    await store.fetch()
    expect(mocks.getSettings).toHaveBeenCalledTimes(2)
    expect(mocks.getConfig).toHaveBeenCalledTimes(2)
    expect(store.loaded).toBe(true)
    expect(store.paymentEnabled).toBe(true)
    expect(store.customMenuItems).toEqual([{ id: 'custom', title: 'Custom' }])
  })

  it('keeps cached values visible when the initial request fails', async () => {
    localStorage.setItem('ops_monitoring_enabled_cached', 'false')
    localStorage.setItem('payment_enabled_cached', 'true')
    mocks.getSettings.mockRejectedValueOnce(new Error('Offline'))
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.opsMonitoringEnabled).toBe(false)
    expect(store.paymentEnabled).toBe(true)
    expect(localStorage.getItem('payment_enabled_cached')).toBe('true')
  })

  it('still reuses successfully loaded settings', async () => {
    const store = useAdminSettingsStore()
    await store.fetch()
    await store.fetch()
    expect(mocks.getSettings).toHaveBeenCalledTimes(1)
    expect(mocks.getConfig).toHaveBeenCalledTimes(1)
  })
})

describe('request capture setting', () => {
  it('stays hidden until settings explicitly enable it', async () => {
    const store = useAdminSettingsStore(); expect(store.requestCaptureEnabled).toBe(false)
    mocks.getSettings.mockResolvedValueOnce({ request_capture_enabled: true }); await store.fetch(); expect(store.requestCaptureEnabled).toBe(true)
    store.setRequestCaptureEnabledLocal(false); expect(store.requestCaptureEnabled).toBe(false)
  })
  it('awaits the same in-flight settings load for route guards', async () => {
    let resolve!: (v: unknown) => void
    mocks.getSettings.mockReturnValueOnce(new Promise(r => { resolve = r }))
    const store = useAdminSettingsStore(), first = store.fetch(), second = store.fetch(true)
    let finished = false; void second.then(() => { finished = true }); await Promise.resolve(); expect(finished).toBe(false)
    resolve({ request_capture_enabled: true }); await Promise.all([first, second]); expect(store.requestCaptureEnabled).toBe(true); expect(mocks.getSettings).toHaveBeenCalledTimes(1)
  })
})

describe('smart operations menu preferences', () => {
  it('uses the six-entry default for missing preferences and preserves saved choices on refresh', async () => {
    const store = useAdminSettingsStore()
    expect(store.smartOpsHiddenMenuItems).toEqual(DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS)
    await store.fetch()
    expect(store.smartOpsHiddenMenuItems).toEqual(DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS)
    mocks.getSettings.mockResolvedValueOnce({ smart_ops_hidden_menu_items: ['/admin/token-guard'] })
    await store.fetch(true)
    expect(store.smartOpsHiddenMenuItems).toEqual(['/admin/token-guard'])
    mocks.getSettings.mockRejectedValueOnce(new Error('Offline'))
    await store.fetch(true)
    expect(store.smartOpsHiddenMenuItems).toEqual(['/admin/token-guard'])
    mocks.getSettings.mockResolvedValueOnce({ smart_ops_hidden_menu_items: [] })
    await store.fetch(true)
    expect(store.smartOpsHiddenMenuItems).toEqual([])
  })
})
