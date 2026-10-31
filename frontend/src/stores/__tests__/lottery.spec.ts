import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { reactive } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { useLotteryStore } from '../lottery'
import type { LotteryPool } from '@/api/lottery'

const { pools } = vi.hoisted(() => ({ pools: vi.fn() }))
let auth: { user: { id: number } | null }
let app: { cachedPublicSettings: { lottery_enabled?: boolean } }
let pinia: Pinia
vi.mock('@/api/lottery', () => ({ lotteryAPI: { pools } }))
vi.mock('../auth', () => ({ useAuthStore: () => auth }))
vi.mock('../app', () => ({ useAppStore: () => app }))
const pool = (id: number, available: number) => ({ id, available, name: `Pool ${id}` }) as LotteryPool

beforeEach(() => {
  vi.clearAllMocks()
  auth = reactive({ user: { id: 7 } })
  app = reactive({ cachedPublicSettings: {} })
  pinia = createPinia()
  setActivePinia(pinia)
  pools.mockResolvedValue([pool(1, 2), pool(2, 0), pool(3, 1)])
})
afterEach(() => disposePinia(pinia))

describe('lottery notifications', () => {
  it('is opt-in, sums independent pools and clears immediately when disabled', async () => {
    const store = useLotteryStore()
    expect(pools).not.toHaveBeenCalled()
    expect(store.enabled).toBe(false)
    app.cachedPublicSettings.lottery_enabled = true
    await flushPromises()
    expect(store.available).toBe(3)
    expect(store.pendingPools.map(p => p.id)).toEqual([1, 3])
    app.cachedPublicSettings.lottery_enabled = false
    expect(store.available).toBe(0)
    expect(store.pools).toEqual([])
    await store.refresh(true)
    expect(pools).toHaveBeenCalledTimes(1)
  })

  it('rejects an old account response after logout and login', async () => {
    let resolve!: (rows: LotteryPool[]) => void
    pools.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const store = useLotteryStore()
    app.cachedPublicSettings.lottery_enabled = true
    auth.user = null
    expect(store.available).toBe(0)
    auth.user = { id: 8 }
    await flushPromises()
    resolve([pool(9, 100)])
    await flushPromises()
    expect(store.available).toBe(3)
    expect(store.pools.some(p => p.id === 9)).toBe(false)
  })

  it('forces a fresh count after a draw even with an older request pending', async () => {
    let resolve!: (rows: LotteryPool[]) => void
    pools.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const store = useLotteryStore()
    app.cachedPublicSettings.lottery_enabled = true
    pools.mockResolvedValueOnce([pool(1, 0)])
    await store.refresh(true)
    resolve([pool(1, 10)])
    await flushPromises()
    expect(store.available).toBe(0)
  })

  it('drops stale notification counts on errors and allows a retry', async () => {
    const store = useLotteryStore()
    app.cachedPublicSettings.lottery_enabled = true
    await flushPromises()
    pools.mockRejectedValueOnce(new Error('offline'))
    await store.refresh(true)
    expect(store.available).toBe(0)
    await store.refresh()
    expect(store.available).toBe(3)
  })
})
