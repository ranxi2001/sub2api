import { apiClient } from '../client'

export type AIOpsControlAction = 'set_concurrency' | 'set_load_factor' | 'pause_account' | 'resume_account'
export type AIOpsAutoAction = Exclude<AIOpsControlAction, 'set_load_factor'>
export interface AIOpsPolicy {
  control_enabled: boolean
  revision: number
  mode: 'disabled' | 'approve_each' | 'bounded_auto'
  halted: boolean
  group_id: number
  accounts: { account_id: number; capacity_domain: string }[]
  min_healthy_accounts: number
  min_total_concurrency: number
  min_concurrency: number
  max_concurrency: number
  max_step_percent: number
  daily_action_limit: number
  cooldown_seconds: number
  observation_seconds: number
  evidence_max_age_seconds: number
  min_samples: number
  error_rate_threshold: number
  rollback_error_rate_delta: number
  auto_actions: AIOpsAutoAction[]
  max_pause_seconds: number
}

export interface AIOpsFrozenProposal {
  id: string
  run_id: string
  proposal_index: number
  action: AIOpsControlAction
  account_id: number
  proposed_value: number | null
  before_value: number | null
  policy_revision: number
  target_revision: number
  digest: string
  expires_at: string
  created_at: string
  status: 'ready' | 'blocked' | 'applied' | 'expired' | 'conflict' | 'cancelled'
  cancelled_at?: string
  cancelled_by?: number
  blocked_reasons: string[]
  evidence: {
    observed_at: string
    window_start: string
    window_end: string
    samples: number
    error_rate: number
    inflight: number
    eligible_accounts: number
    total_concurrency: number
    model_coverage_complete: boolean
    probe_passed: boolean
    model?: string
    effort?: string
    protocol?: string
    input_bucket?: string
    cache_bucket?: string
    output_bucket?: string
  }
  impact_group_ids: number[]
}

export interface AIOpsActionRecord {
  id: string
  proposal_id: string
  run_id: string
  action: AIOpsControlAction
  account_id: number
  before_value: number | null
  after_value: number | null
  status: 'propagation_pending' | 'observing' | 'succeeded' | 'inconclusive' | 'rolled_back' | 'conflict' | 'failed'
  created_at: string
  updated_at: string
  observe_until: string
  observation_started_at?: string
  owner_revision: number
  target_revision: number
  reason_code: string
  auto: boolean
}

export interface AIOpsRecoveryProbe {
  id: string
  account_id: number
  model: string
  effort?: string
  protocol?: string
  status: 'reserved' | 'passed' | 'failed' | 'unknown'
  created_at: string
  finished_at?: string
  expires_at: string
  owner_revision: number
  target_revision: number
  reason_code: string
}

const root = '/admin/ops/ai'
export const aiOpsControlAPI = {
  async policy(signal?: AbortSignal): Promise<AIOpsPolicy> {
    return (await apiClient.get<AIOpsPolicy>(`${root}/policy`, { signal })).data
  },
  async savePolicy(policy: AIOpsPolicy, signal?: AbortSignal): Promise<AIOpsPolicy> {
    return (await apiClient.put<AIOpsPolicy>(`${root}/policy`, policy, { signal })).data
  },
  async actions(signal?: AbortSignal): Promise<{ items: AIOpsActionRecord[]; total: number }> {
    return (await apiClient.get<{ items: AIOpsActionRecord[]; total: number }>(`${root}/actions`, { signal })).data
  },
  async halt(signal?: AbortSignal): Promise<AIOpsPolicy> {
    return (await apiClient.post<AIOpsPolicy>(`${root}/halt`, {}, { signal })).data
  },
  async preview(runId: string, proposalIndex: number, signal?: AbortSignal): Promise<AIOpsFrozenProposal> {
    return (await apiClient.post<AIOpsFrozenProposal>(`${root}/proposals/preview`, { run_id: runId, proposal_index: proposalIndex }, { signal })).data
  },
  async apply(id: string, digest: string, signal?: AbortSignal): Promise<AIOpsActionRecord> {
    return (await apiClient.post<AIOpsActionRecord>(`${root}/proposals/${encodeURIComponent(id)}/apply`, { digest, confirm: true }, { signal })).data
  },
  async cancel(id: string, digest: string, signal?: AbortSignal): Promise<AIOpsFrozenProposal> {
    return (await apiClient.post<AIOpsFrozenProposal>(`${root}/proposals/${encodeURIComponent(id)}/cancel`, { digest }, { signal })).data
  },
  async rollback(id: string, revision: number, signal?: AbortSignal): Promise<AIOpsActionRecord> {
    return (await apiClient.post<AIOpsActionRecord>(`${root}/actions/${encodeURIComponent(id)}/rollback`, { expected_revision: revision, confirm: true }, { signal })).data
  },
  async probes(signal?: AbortSignal): Promise<{ items: AIOpsRecoveryProbe[]; total: number }> {
    return (await apiClient.get<{ items: AIOpsRecoveryProbe[]; total: number }>(`${root}/probes`, { signal })).data
  },
  async probe(accountId: number, model: string, signal?: AbortSignal): Promise<AIOpsRecoveryProbe> {
    return (await apiClient.post<AIOpsRecoveryProbe>(`${root}/probes`, { account_id: accountId, model, confirm: true }, { signal })).data
  },
}
