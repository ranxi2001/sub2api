/**
 * Admin Scheduled Tests API endpoints
 * Handles scheduled test plan management for account connectivity monitoring
 */

import { apiClient } from '../client'
import type {
  ScheduledTestPlan,
  ScheduledTestResult,
  CreateScheduledTestPlanRequest,
  UpdateScheduledTestPlanRequest
} from '@/types'

/**
 * List all scheduled test plans for an account
 * @param accountId - Account ID
 * @returns List of scheduled test plans
 */
export async function listByAccount(accountId: number): Promise<ScheduledTestPlan[]> {
  const { data } = await apiClient.get<ScheduledTestPlan[]>(
    `/admin/accounts/${accountId}/scheduled-test-plans`
  )
  return data ?? []
}

/**
 * Create a new scheduled test plan
 * @param req - Plan creation request
 * @returns Created plan
 */
export async function create(req: CreateScheduledTestPlanRequest, options?: { signal?: AbortSignal }): Promise<ScheduledTestPlan> {
  const { data } = await apiClient.post<ScheduledTestPlan>(
    '/admin/scheduled-test-plans',
    req,
    options
  )
  return data
}

/**
 * Update an existing scheduled test plan
 * @param id - Plan ID
 * @param req - Fields to update
 * @returns Updated plan
 */
export async function update(id: number, req: UpdateScheduledTestPlanRequest): Promise<ScheduledTestPlan> {
  const { data } = await apiClient.put<ScheduledTestPlan>(
    `/admin/scheduled-test-plans/${id}`,
    req
  )
  return data
}

/**
 * Delete a scheduled test plan
 * @param id - Plan ID
 */
export async function deletePlan(id: number, ownership?: { fence: string; confirm: true }): Promise<void> {
  if (ownership) await apiClient.delete(`/admin/scheduled-test-plans/${id}`, { data: ownership })
  else await apiClient.delete(`/admin/scheduled-test-plans/${id}`)
}

/**
 * List test results for a plan
 * @param planId - Plan ID
 * @param limit - Optional max number of results to return
 * @returns List of test results
 */
export async function listResults(planId: number, limit?: number, includeContent = true): Promise<ScheduledTestResult[]> {
  const { data } = await apiClient.get<ScheduledTestResult[]>(
    `/admin/scheduled-test-plans/${planId}/results`,
    {
      params: { limit, include_content: includeContent }
    }
  )
  return data ?? []
}

export async function getResult(planId: number, resultId: number): Promise<ScheduledTestResult> {
  const { data } = await apiClient.get<ScheduledTestResult>(`/admin/scheduled-test-plans/${planId}/results/${resultId}`)
  return data
}

export interface PelicanHistoryResult extends ScheduledTestResult {
  account_id: number
  account_name: string
}
// accountId narrows the history to one account; omit it for every account.
export async function listPelicanHistory(beforeId = 0, accountId?: number): Promise<{ items: PelicanHistoryResult[]; next_cursor: number }> {
  const params = accountId ? { before_id: beforeId, account_id: accountId } : { before_id: beforeId }
  const { data } = await apiClient.get('/admin/pelican-test-results', { params })
  return data
}

export const scheduledTestsAPI = {
  listPelicanHistory,
  getResult,
  listByAccount,
  create,
  update,
  delete: deletePlan,
  listResults
}

export default scheduledTestsAPI
