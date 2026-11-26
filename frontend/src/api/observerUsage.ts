import { apiClient } from './client'
import type { AdminUsageLog, PaginatedResponse } from '@/types'
import type { AdminUsageQueryParams, AdminUsageStatsResponse } from './admin/usage'
import type { ModelStatsParams, ModelStatsResponse, DashboardSnapshotV2Params, DashboardSnapshotV2Response } from './admin/dashboard'
import type { OpsErrorLog, OpsErrorDetail } from './admin/ops'

// Identity is supplied by the authenticated backend, never by page filters.
function ownParams<T extends { user_id?: number | null }>(params: T): Omit<T, 'user_id'> {
  const { user_id: _userId, ...own } = params
  return own
}

export interface UsageFilterOption { id: number; name: string; deleted?: boolean }

export const observerUsageAPI = {
  async list(params: AdminUsageQueryParams, options?: { signal?: AbortSignal }) {
    const { data } = await apiClient.get<PaginatedResponse<AdminUsageLog>>('/usage', { params: ownParams(params), signal: options?.signal })
    return data
  },
  async getStats(params: AdminUsageQueryParams) {
    const { data } = await apiClient.get<AdminUsageStatsResponse>('/usage/stats', { params: ownParams(params) })
    return data
  },
  async getModelStats(params: ModelStatsParams) {
    const { data } = await apiClient.get<ModelStatsResponse>('/usage/dashboard/models', { params: ownParams(params) })
    return data
  },
  async getSnapshotV2(params: DashboardSnapshotV2Params) {
    const { data } = await apiClient.get<DashboardSnapshotV2Response>('/usage/dashboard/snapshot-v2', { params: ownParams(params) })
    return data
  },
  async filterOptions(kind: 'api_key' | 'account' | 'group', q = '') {
    const { data } = await apiClient.get<UsageFilterOption[]>('/usage/filter-options', { params: { kind, q } })
    return data
  },
  async listErrors(params: Record<string, unknown>) {
    const { user_id: _userId, ...own } = params
    const { data } = await apiClient.get<PaginatedResponse<OpsErrorLog>>('/usage/errors', { params: own })
    return data
  },
  async getErrorDetail(id: number) {
    const { data } = await apiClient.get<OpsErrorDetail>(`/usage/errors/${id}`)
    return data
  }
}
