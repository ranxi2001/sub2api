import { apiClient } from './client'

export interface LotteryPrize {
  id: number
  pool_id: number
  name: string
  kind: 'balance' | 'subscription' | 'text'
  weight: number
  stock: number
  enabled: boolean
  available: boolean
  sort_order: number
  amount: number
  plan_id: number
  plan_name: string
  group_id: number
  validity_days: number
  text?: string
  probability: number
  updated_at: string
}

export interface LotteryPool {
  id: number
  name: string
  mode: 'single' | 'cumulative'
  threshold: number
  grant_enabled: boolean
  draw_enabled: boolean
  rule_locked: boolean
  sort_order: number
  available: number
  debt: number
  progress: number
  prizes: LotteryPrize[]
}

export interface LotteryDraw {
  id: number
  pool_id: number
  user_id: number
  prize: LotteryPrize & { pool_name: string }
  created_at: string
}

export interface LotteryLedger {
  id: number
  pool_id: number
  user_id: number
  action: 'recharge' | 'refund' | 'draw'
  source_type: 'payment' | 'redeem' | 'admin' | 'draw'
  source_id: string
  amount: number
  ticket_delta: number
  created_at: string
}

export interface LotteryPage<T> {
  items: T[]
  total: number
  page: number
  page_size: number
  pages: number
}
export type PoolInput = Pick<
  LotteryPool,
  | 'name'
  | 'mode'
  | 'threshold'
  | 'grant_enabled'
  | 'draw_enabled'
  | 'sort_order'
>
export type PrizeInput = Pick<
  LotteryPrize,
  | 'name'
  | 'kind'
  | 'weight'
  | 'stock'
  | 'enabled'
  | 'sort_order'
  | 'amount'
  | 'plan_id'
  | 'text'
> & { updated_at?: string }

const writeConfig = (key: string = crypto.randomUUID()) => ({
  headers: { 'Idempotency-Key': key },
})

export const lotteryAPI = {
  async pools() {
    return (await apiClient.get<LotteryPool[]>('/lottery/pools')).data
  },
  async draw(poolID: number, key: string) {
    return (
      await apiClient.post<LotteryDraw>(
        `/lottery/pools/${poolID}/draw`,
        {},
        writeConfig(key),
      )
    ).data
  },
  async records(page = 1, poolID?: number) {
    return (
      await apiClient.get<LotteryPage<LotteryDraw>>('/lottery/records', {
        params: { page, page_size: 10, pool_id: poolID },
      })
    ).data
  },
}

export const adminLotteryAPI = {
  async settings() {
    return (await apiClient.get<{ enabled: boolean }>('/admin/lottery/settings')).data
  },
  async saveSettings(enabled: boolean, key: string) {
    return (await apiClient.put<{ enabled: boolean }>('/admin/lottery/settings', { enabled }, writeConfig(key))).data
  },
  async deletePool(id: number, key: string) {
    await apiClient.delete(`/admin/lottery/pools/${id}`, writeConfig(key))
  },
  async deletePrize(poolID: number, id: number, key: string) {
    await apiClient.delete(`/admin/lottery/pools/${poolID}/prizes/${id}`, writeConfig(key))
  },
  async pools() {
    return (await apiClient.get<LotteryPool[]>('/admin/lottery/pools')).data
  },
  async savePool(id: number | null, input: PoolInput, key: string) {
    const url = `/admin/lottery/pools${id ? `/${id}` : ''}`
    return (
      await apiClient.request<LotteryPool>({
        method: id ? 'PUT' : 'POST',
        url,
        data: input,
        ...writeConfig(key),
      })
    ).data
  },
  async savePrize(
    poolID: number,
    id: number | null,
    input: PrizeInput,
    key: string,
  ) {
    const url = `/admin/lottery/pools/${poolID}/prizes${id ? `/${id}` : ''}`
    return (
      await apiClient.request<LotteryPrize>({
        method: id ? 'PUT' : 'POST',
        url,
        data: input,
        ...writeConfig(key),
      })
    ).data
  },
  async records(page: number, poolID?: number, userID?: number) {
    return (
      await apiClient.get<LotteryPage<LotteryDraw>>('/admin/lottery/records', {
        params: { page, page_size: 20, pool_id: poolID, user_id: userID },
      })
    ).data
  },
  async ledger(page: number, poolID?: number, userID?: number) {
    return (
      await apiClient.get<LotteryPage<LotteryLedger>>('/admin/lottery/ledger', {
        params: { page, page_size: 20, pool_id: poolID, user_id: userID },
      })
    ).data
  },
}
