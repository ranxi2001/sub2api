import { apiClient } from '../client'

export interface AutoCreditConfig {
  enabled: boolean
  notify_email?: boolean
  threshold: string | null
  amount: string | null
  cap: string | null
  version: number
}
export interface AutoCreditEntry {
  id: number
  user_id: number
  epoch: number
  source: 'auto_credit'
  trigger: string
  amount: string
  balance_before: string
  balance_after: string
  note: string
  status: 'pending' | 'settled'
  created_at: string
  settled_at: string | null
  settled_by: number | null
}
export interface AutoCreditState {
  config: AutoCreditConfig
  outstanding: string
  cap_blocked: boolean
  entries: AutoCreditEntry[]
  has_more: boolean
  next_before_id: number
}
export type CreditStatus = 'all' | 'pending' | 'settled'
export async function getAutoCredit(userId: number, status: CreditStatus = 'all', beforeId = 0): Promise<AutoCreditState> {
  const { data } = await apiClient.get<AutoCreditState>(`/admin/users/${userId}/auto-credit`, { params: { status, before_id: beforeId } })
  return data
}
export async function saveAutoCredit(userId: number, config: AutoCreditConfig): Promise<AutoCreditState> {
  const { data } = await apiClient.put<AutoCreditState>(`/admin/users/${userId}/auto-credit`, config)
  return data
}
export async function settleAutoCredit(userId: number, entryId: number): Promise<AutoCreditState> {
  const { data } = await apiClient.post<AutoCreditState>(`/admin/users/${userId}/auto-credit/${entryId}/settle`, { confirm: true })
  return data
}
