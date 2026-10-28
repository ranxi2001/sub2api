import { apiClient } from '../client'
import type { Section, Quality, ErrorLogs } from './channelOperations'
export interface OperationsQuery {
 from: string; until: string; upstream: string; account_id: number; group_id: number
 model: string; model_scope: 'public' | 'recorded' | 'upstream'; dimension: 'upstream' | 'account' | 'model'
 sort: string; direction: 'asc' | 'desc'; page: number; page_size: number
}
export interface ProfitMetrics {
 requests: number; standard_charge: number | null; request_income: number | null
 estimated_cost: number | null; estimated_profit: number | null; margin: number | null
 covered_estimated_cost: number | null; covered_income: number | null
 unknown_standard_requests: number; unknown_income_requests: number; unmetered_zero_records: number; cost_covered_requests: number; unknown_cost_requests: number; unknown_upstream_requests: number
 saved_multiplier_requests: number; default_multiplier_requests: number; saved_stats_base_requests: number; standard_base_fallback_requests: number
 subscription_requests: number; deleted_account_requests: number; samples: number; unknown_samples: number
 p50_ms: number | null; p90_ms: number | null; p95_ms: number | null; duration_p90_ms: number | null
 latest_config_updated_at: string | null; oldest_config_updated_at: string | null
}
export interface OperationsRow extends ProfitMetrics { key: string; name: string }
export interface AccountBasis { id: number; name: string; key: string; attribution: string; updated_at: string; deleted: boolean; multiplier: number | null; cost_basis: string }
export interface OperationsSnapshot {
 scope: OperationsQuery; queried_at: string; attribution: string; currency: string
 usage: { summary: ProfitMetrics; rows: OperationsRow[]; total: number; accounts: AccountBasis[]; capacity: { accounts: number; schedulable: number; configured_concurrency: number }; account_details_limit: number; catalog: Array<{key: string; name: string; accounts: number}>; catalog_total: number; historical_upstream_snapshot_requests: number; historical_cost_multiplier_snapshot_requests: number; cost_formula: string; income_source: string; standard_source: string }
 quality: Section<Quality & { total: number }>; errors: Section<ErrorLogs>
}
export async function readOperations(kind: 'profit' | 'upstream', params: OperationsQuery, signal?: AbortSignal): Promise<OperationsSnapshot> {
 return (await apiClient.get<OperationsSnapshot>(`/admin/${kind}-operations/snapshot`, { params, signal })).data
}
