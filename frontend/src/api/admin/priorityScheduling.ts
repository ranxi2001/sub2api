import { apiClient } from '../client'
export interface PrioritySchedulingConfig {
  error_rate_weight?: number
  error_rate_window_minutes?: number
  error_rate_prior_samples?: number
  oauth_quota_priority?: boolean
  oauth_quota_threshold?: number
  balance_protocols?: boolean
  enabled: boolean
  mode: 'experience' | 'balanced' | 'profit' | 'custom'
  group_ids: number[]
  models: string[]
  window_minutes: number
  min_samples: number
  latency_penalty_mode?: 'deprioritize' | 'stop'
  latency_penalty_minutes?: number
  latency_percentile?: 50 | 70 | 90 | 95
  latency_measurement?: 'legacy_forward' | 'attributed_v1'
  recovery_exploration_percent?: number
  target_ttft_ms: number
  max_load_percent: number
  min_quality_percent: number
  quality_max_age_hours: number
  quality_weight: number
  latency_weight: number
  load_weight: number
  cost_weight: number
}
export type PriorityHistoryStatus = 'ready' | 'stale' | 'partial' | 'loading' | 'error' | 'unavailable' | 'limited'
export interface PriorityCandidate {
  error_rate?: number | null
  upstream_error_rate?: number
  error_smoothed_rate?: number
  error_successes?: number
  error_failures?: number
  error_window_minutes?: number
  error_prior_samples?: number
  error_observed_at?: string
  error_endpoint?: string
  error_penalty?: number
  base_score?: number
  oauth_quota_role?: 'preferred' | 'standby'
  history_status?: PriorityHistoryStatus
  capacity_band?: number
  selection_weight?: number
  exploration_eligible?: boolean
  latency_penalty_until?: string
  latency_recovery?: boolean
  latency_stopped?: boolean
  bound_groups?: number
  profit: number | null
  margin: number | null
  economics_source: 'usage' | 'rate' | 'unknown'
  revenue: number
  theoretical_cost: number
  profit_samples: number
  priority: number
  concurrency: number
  load_factor: number
  account_id: number
  account_name: string
  score: number
  tier: string
  reasons: string[]
  rate: number | null
  load_percent: number | null
  waiting: number
  samples: number
  p50_ttft_ms?: number
  p90_ttft_ms: number
  selected_ttft_ms?: number | null
  p70_ttft_ms?: number
  p95_ttft_ms?: number
  p99_ttft_ms?: number
  latency_percentile?: number
  latency_measurement?: string
  latency_unknown_samples?: number
  experience_samples?: number
  experience_p90_ms?: number
  slow_samples?: number
  error_samples?: number
  quality_passed: number
  quality_samples: number
}
export interface PrioritySnapshot {
  latency_percentile?: number
  latency_measurement?: string
  latency_penalty_mode?: 'deprioritize' | 'stop'
  latency_fallback?: boolean
  min_samples?: number
  window_minutes?: number
  history_window_end?: string
  config_revision?: string
  evaluated_at?: string
  history_status?: PriorityHistoryStatus
  history_observed_at?: string
  history_error?: string
  history_refreshing?: boolean
  selection_policy?: string
  at: string
  model: string
  group_id: number | null
  mode: string
  history_ready: boolean
  candidates: PriorityCandidate[]
}
export interface PriorityLatencyBucket {
  account_id: number
  bucket: string
  samples: number
  p50_ttft_ms: number
  p70_ttft_ms?: number
  p90_ttft_ms: number
  p95_ttft_ms?: number
  p99_ttft_ms: number
  mean_ttft_ms?: number
  selected_ttft_ms?: number
  slow_samples: number
  error_samples: number
}
export interface PriorityLatencyAccount {
  account_id: number
  account_name: string
  samples: number
  p50_ttft_ms: number
  p70_ttft_ms?: number
  p90_ttft_ms: number
  p95_ttft_ms?: number
  p99_ttft_ms: number
  mean_ttft_ms?: number
  selected_ttft_ms?: number
  latency_percentile?: number
  latency_measurement?: string
  slow_samples: number
  error_samples: number
  error_rate: number
  buckets: PriorityLatencyBucket[]
}
export interface PriorityLatencySeries {
  at: string
  model: string
  group_id: number | null
  bucket_minutes: number
  latency_percentile?: number
  latency_measurement?: string
  accounts: PriorityLatencyAccount[]
}
export interface PrioritySchedulingDiffRow {
  account_id: number
  account_name: string
  old_rank?: number
  new_rank?: number
  old_score: number
  new_score: number
  old_weight: number
  new_weight: number
  old_tier?: string
  new_tier?: string
  old_p90_ttft_ms: number
  new_p90_ttft_ms: number
  old_samples: number
  new_samples: number
  old_error_samples: number
  new_error_samples: number
  changed: boolean
  reasons?: string[]
}
export interface PrioritySchedulingRecomputeResult {
  before: PrioritySnapshot | null
  after: PrioritySnapshot | null
  diff: PrioritySchedulingDiffRow[]
  timed_out: boolean
}
export interface PriorityStickyBindingRow {
  account_id: number
  count: number
}
export interface PriorityStickyBindings {
  group_id: number
  total: number
  by_account: PriorityStickyBindingRow[]
}
export interface PriorityStickyClearResult {
  group_id: number
  deleted: number
  remaining: number
  dry_run?: boolean
  by_account: PriorityStickyBindingRow[]
}
export async function getPriorityConfig(): Promise<PrioritySchedulingConfig> {
  return (await apiClient.get('/admin/priority-scheduling/config')).data
}
export async function savePriorityConfig(config: PrioritySchedulingConfig): Promise<PrioritySchedulingConfig> {
  return (await apiClient.put('/admin/priority-scheduling/config', config)).data
}
export async function getPrioritySnapshot(): Promise<PrioritySnapshot | null> {
  return (await apiClient.get('/admin/priority-scheduling/snapshot')).data.snapshot
}
export async function getPriorityLatency(minutes = 60, bucketMinutes = 5): Promise<PriorityLatencySeries> {
  return (await apiClient.get('/admin/priority-scheduling/latency', { params: { minutes, bucket_minutes: bucketMinutes } })).data
}
export async function recomputePriorityScheduling(): Promise<PrioritySchedulingRecomputeResult> {
  return (await apiClient.post('/admin/priority-scheduling/recompute')).data
}
export async function getPriorityStickyBindings(groupID: number): Promise<PriorityStickyBindings> {
  return (await apiClient.get('/admin/priority-scheduling/sticky-bindings', { params: { group_id: groupID } })).data
}
export async function clearPriorityStickyBindings(payload: { group_id: number; account_ids: number[]; dry_run?: boolean }): Promise<PriorityStickyClearResult> {
  return (await apiClient.post('/admin/priority-scheduling/sticky-bindings/clear', payload)).data
}
