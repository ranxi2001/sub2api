/**
 * Pelican group tests: the scheduled questions that feed the user Pelican showcase.
 * The gateway scheduler picks the answering account, exactly as for a user request.
 */

import { apiClient } from '../client'
import type { PaginatedResponse, PelicanQuestion } from '@/types'

export interface PelicanGroupTestConfig {
  prompt_mode?: 'custom' | 'random'
  pelican_question?: PelicanQuestion
  actual_prompt?: string
  question_kind?: string
  prompt: string
  reasoning_effort: string
  parallel_count: number
  model_id?: string
}

/** An account a sample left because it failed before producing any output. */
export interface PelicanGroupTestAttempt {
  account_id: number
  account_name: string
  error: string
}

export interface PelicanGroupTestResult {
  id: number
  plan_id: number
  group_id: number
  group_name?: string
  /** The account that gave the final answer; 0 when the scheduler found none. */
  account_id: number
  account_name: string
  attempts: PelicanGroupTestAttempt[]
  status: 'success' | 'failed'
  /** Raw model output; only getResult returns it. */
  response_text?: string
  error_message: string
  latency_ms: number
  /** USD upstream cost; null means usage or pricing was unavailable. */
  cost_usd: number | null
  cost_incomplete: boolean
  pelican_config?: PelicanGroupTestConfig
  started_at: string
  finished_at: string
  created_at: string
}

export interface PelicanGroupTestPlan {
  id: number
  group_id: number
  group_name: string
  today_cost_usd: number
  total_cost_usd: number
  today_cost_incomplete: boolean
  total_cost_incomplete: boolean
  group_platform: string
  /** The group's status, or "deleted" once the group was removed. */
  group_status: string
  model_id: string
  cron_expression: string
  enabled: boolean
  pelican_config: PelicanGroupTestConfig
  last_run_at: string | null
  next_run_at: string | null
  running_until?: string | null
  last_result?: PelicanGroupTestResult
  created_at: string
  updated_at: string
}

/** What an admin edits. The group of an existing plan cannot change. */
export interface PelicanGroupTestPlanInput {
  prompt_mode?: 'custom' | 'random'
  group_id: number
  model_id: string
  cron_expression: string
  enabled: boolean
  prompt: string
  reasoning_effort: string
  parallel_count: number
}

export interface PelicanShowcaseSettings {
  enabled: boolean
  api_enabled: boolean
  max_items: number
  auto_cleanup: boolean
  retention_days: number
}

export async function listPlans(): Promise<PelicanGroupTestPlan[]> {
  const { data } = await apiClient.get<PelicanGroupTestPlan[]>('/admin/pelican-group-tests')
  return data ?? []
}

export async function createPlan(input: PelicanGroupTestPlanInput): Promise<PelicanGroupTestPlan> {
  const { data } = await apiClient.post<PelicanGroupTestPlan>('/admin/pelican-group-tests', input)
  return data
}

export async function updatePlan(id: number, input: PelicanGroupTestPlanInput): Promise<PelicanGroupTestPlan> {
  const { data } = await apiClient.put<PelicanGroupTestPlan>(`/admin/pelican-group-tests/${id}`, input)
  return data
}

export async function deletePlan(id: number): Promise<void> {
  await apiClient.delete(`/admin/pelican-group-tests/${id}`)
}

/** Starts a run in the background; rejects with PELICAN_GROUP_TEST_PLAN_RUNNING while one is in progress. */
export async function runPlan(id: number): Promise<void> {
  await apiClient.post(`/admin/pelican-group-tests/${id}/run`)
}

/** Newest first, without HTML; only the requested page is transferred. */
export async function listResults(page = 1, pageSize = 20, planId = 0, signal?: AbortSignal): Promise<PaginatedResponse<PelicanGroupTestResult>> {
  const { data } = await apiClient.get<PaginatedResponse<PelicanGroupTestResult>>('/admin/pelican-group-test-results', {
    params: { page, page_size: pageSize, plan_id: planId || undefined },
    signal,
  })
  return data
}

export async function getResult(id: number): Promise<PelicanGroupTestResult> {
  const { data } = await apiClient.get<PelicanGroupTestResult>(`/admin/pelican-group-test-results/${id}`)
  return data
}

export async function getShowcaseSettings(): Promise<PelicanShowcaseSettings> {
  const { data } = await apiClient.get<PelicanShowcaseSettings>('/admin/pelican-showcase/settings')
  return data
}

export async function updateShowcaseSettings(settings: PelicanShowcaseSettings): Promise<PelicanShowcaseSettings> {
  const { data } = await apiClient.put<PelicanShowcaseSettings>('/admin/pelican-showcase/settings', settings)
  return data
}

export const pelicanTestsAPI = {
  listPlans,
  createPlan,
  updatePlan,
  deletePlan,
  runPlan,
  listResults,
  getResult,
  getShowcaseSettings,
  updateShowcaseSettings,
}

export default pelicanTestsAPI
