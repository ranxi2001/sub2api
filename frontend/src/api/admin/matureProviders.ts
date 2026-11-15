import api from '@/api/client'

export interface ProviderMetrics {
  balance: number | null
  today_spend: number | null
  total_spend: number | null
  total_recharge: number | null
  currency: string
}
export interface MatureProvider {
  refresh_interval_seconds: number
  id: number
  revision: number
  name: string
  site_type: 'sub2api' | 'newapi'
  base_url: string
  notes: string
  cost_multiplier: number | null
  low_balance_threshold: number | null
  suppress_low_balance_alert: boolean
  status: 'unknown' | 'connected' | 'disconnected'
  metrics: ProviderMetrics
  observed_at: string | null
  updated_at: string
  auth_mode: 'token' | 'password'
  account: string
  user_id: string
  has_credential: boolean
  last_error?: string
}
export interface ProviderInput {
  refresh_interval_seconds?: number
  name: string
  site_type: 'sub2api' | 'newapi'
  base_url: string
  notes: string
  cost_multiplier: number | null
  low_balance_threshold: number | null
  suppress_low_balance_alert: boolean
  auth_mode: 'token' | 'password'
  account: string
  user_id: string
  access_token?: string
  refresh_token?: string
  password?: string
  revision?: number
}
export interface ProviderPage { items: MatureProvider[]; total: number; page: number; page_size: number }
export const listProviders = async (params: { search: string; status: string; page: number; page_size: number }, signal?: AbortSignal) =>
  (await api.get<ProviderPage>('/admin/providers', { params, signal })).data
export const saveProvider = async (id: number | null, input: ProviderInput) =>
  (id == null ? await api.post<MatureProvider>('/admin/providers', input) : await api.put<MatureProvider>(`/admin/providers/${id}`, input)).data
export const refreshProvider = async (p: MatureProvider) =>
  (await api.post<MatureProvider>(`/admin/providers/${p.id}/refresh`, { revision: p.revision })).data
export const deleteProvider = async (p: MatureProvider) =>
  (await api.delete(`/admin/providers/${p.id}`, { data: { revision: p.revision, confirm: true } })).data
