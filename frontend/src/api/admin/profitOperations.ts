import { apiClient } from '../client'
import type { Section, Quality, ErrorLogs } from './channelOperations'
import type { MatureProvider, ProviderMetrics } from './matureProviders'
export interface OperationsQuery {
 from: string; until: string; upstream: string; channel_id?: number; account_id: number; group_id: number
 model: string; model_scope: 'public' | 'recorded' | 'upstream'; dimension: 'upstream' | 'account' | 'model'
 sort: string; direction: 'asc' | 'desc'; page: number; page_size: number
}
export interface ProfitMetrics {
 requests: number; standard_charge: number | null; request_income: number | null
 estimated_cost: number | null; estimated_profit: number | null; margin: number | null
 reported_usage_cost: number | null; reported_usage_requests: number | null
 reconciled_cost: number | null; reconciled_profit: number | null; reconciled_requests: number | null; covered_reconciled_cost: number | null
 covered_estimated_cost: number | null; covered_income: number | null
 unknown_standard_requests: number; unknown_income_requests: number; unmetered_zero_records: number; cost_covered_requests: number; unknown_cost_requests: number; unknown_upstream_requests: number
 saved_multiplier_requests: number; default_multiplier_requests: number; saved_stats_base_requests: number; standard_base_fallback_requests: number
 subscription_requests: number; deleted_account_requests: number; samples: number; unknown_samples: number
 p50_ms: number | null; p90_ms: number | null; p95_ms: number | null; duration_p90_ms: number | null
 latest_config_updated_at: string | null; oldest_config_updated_at: string | null
}
export interface OperationsRow extends ProfitMetrics { key: string; name: string; assignment?: string; channel_id?: number; channel_code?: string; channel_remark?: string; channel_revision?: number }
export interface AccountBasis { id: number; name: string; key: string; attribution: string; updated_at: string; deleted: boolean; multiplier: number | null; cost_basis: string }
export interface OperationsSnapshot {
 scope: OperationsQuery; queried_at: string; attribution: string; currency: string
 usage: { summary: ProfitMetrics; rows: OperationsRow[]; total: number; accounts: AccountBasis[]; members: AccountBasis[]; member_total: number; capacity: { accounts: number; schedulable: number; configured_concurrency: number }; account_details_limit: number; catalog: Array<{key: string; name: string; accounts: number; channel_id?: number; channel_code?: string; channel_remark?: string; assignment?: string}>; catalog_total: number; historical_upstream_snapshot_requests: number; historical_cost_multiplier_snapshot_requests: number; cost_formula: string; income_source: string; standard_source: string }
 quality: Section<Quality & { total: number }>; errors: Section<ErrorLogs>
}
export async function readProfitOperations( params: OperationsQuery, signal?: AbortSignal): Promise<OperationsSnapshot> {
 // Full-range accounting may take minutes; leave time for the server's five-minute budget and response.
 return (await apiClient.get<OperationsSnapshot>('/admin/profit-operations/snapshot', { params, signal, timeout: 330_000 })).data
}
export interface ProfitProvider extends Pick<MatureProvider, 'site_type' | 'status' | 'observed_at' | 'revision'> {
  provider_id: number
  provider_name: string
  channel_id: number | null
  channel_name: string | null
  metrics: Pick<ProviderMetrics, 'today_spend' | 'total_spend' | 'currency'>
}
export interface ProfitProviderPage { items: ProfitProvider[]; total: number; page: number; page_size: number }
export async function listProfitProviders(page: number, signal?: AbortSignal) {
  return (await apiClient.get<ProfitProviderPage>('/admin/profit-operations/providers', { params: { page, page_size: 20 }, signal })).data
}
export async function linkProfitChannel(providerId: number, channelId: number, revision: number) {
  await apiClient.put(`/admin/providers/${providerId}/profit-channel`, { channel_id: channelId, revision, confirm: true })
}
export async function syncProfitCharges(providerId: number, dates: Pick<OperationsQuery, 'from' | 'until'>) {
  return (await apiClient.post<{ matched: number; unmatched: number }>(`/admin/providers/${providerId}/profit-charges/sync`, dates, { timeout: 55_000 })).data
}
export async function readExplicitChannelProfitOperations(query: OperationsQuery, channelId: number, signal?: AbortSignal): Promise<OperationsSnapshot> {
 return readProfitOperations({ ...query, upstream: '', channel_id: channelId }, signal)
}
