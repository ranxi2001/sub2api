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

export type LotteryMode = 'single' | 'cumulative' | 'invite_visit' | 'invite_signup' | 'invite_recharge'
export interface LotteryInviteConfig {
  reward_to: 'inviter' | 'both'
  tickets: number
  daily_limit: number
  ip_daily_limit: number
  blocked_ips?: string[]
  blocked_countries?: string[]
  deny_unknown_country: boolean
}
export interface LotteryInviteSummary {
  path: string
  visits: number
  registered: number
  qualified: number
  incoming_progress: number
  invited: boolean
}
export interface LotteryInvitation {
  id: number
  pool_id: number
  inviter_id: number
  invitee_id: number | null
  ip_address: string
  country: string
  amount: number
  qualified: boolean
  inviter_awarded: boolean
  invitee_awarded: boolean
  created_at: string
}

export const isInvitationPool = (pool: Pick<LotteryPool, 'mode'>) => pool.mode.startsWith('invite_')
export const lotteryRuleKey = (pool: Pick<LotteryPool, 'mode'>) => `lottery.${pool.mode}Rule`
export const lotteryRuleParams = (pool: LotteryPool) => ({ amount: pool.threshold, count: pool.invite_config?.tickets ?? 1 })

export interface LotteryPool {
  id: number
  name: string
  mode: LotteryMode
  invite_config?: LotteryInviteConfig
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
  action: 'recharge' | 'refund' | 'draw' | 'invite'
  source_type: 'payment' | 'redeem' | 'admin' | 'draw' | 'invite_visit' | 'invite_signup' | 'invite_recharge'
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
  | 'invite_config'
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
  async invite(poolID: number) {
    return (await apiClient.get<LotteryInviteSummary>(`/lottery/pools/${poolID}/invite`)).data
  },
  async visit(code: string) {
    return (await apiClient.post<LotteryPool>('/auth/lottery/visit', { code }, { timeout: 5000 })).data
  },
  async claimInvitation() {
    await apiClient.post('/lottery/invitations/claim')
  },
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
  async invitations(page: number, poolID?: number, userID?: number) {
    return (await apiClient.get<LotteryPage<LotteryInvitation>>('/admin/lottery/invitations', {
      params: { page, page_size: 20, pool_id: poolID, user_id: userID },
    })).data
  },
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
