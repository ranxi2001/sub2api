import { apiClient } from '../client'

export interface AIOpsStatus {
  enabled: boolean
  mode: 'observe'
  configured: boolean
  running: boolean
  group_id: number | null
  window_minutes: number
  min_interval_seconds: number
  automatic_interval_seconds: number
  daily_run_limit: number
  last_run_at?: string
}

export type AIOpsRunStatus = 'queued' | 'collecting' | 'analyzing' | 'completed' | 'failed' | 'interrupted'

export interface AIOpsEvidence {
  id: string
  source: string
  status: 'ok' | 'unavailable' | 'truncated'
  collected_at: string
  metrics: Record<string, unknown>
}

export interface AIOpsFinding {
  severity: 'info' | 'warning' | 'critical'
  category: 'availability' | 'capacity' | 'upstream' | 'data_quality'
  description: string
  recommendation: string
  evidence_ids: string[]
  account_ids: number[]
}

export interface AIOpsProposal {
  action: 'none' | 'set_concurrency' | 'set_load_factor' | 'pause_account' | 'resume_account'
  account_id: number | null
  proposed_value: number | null
  reason: string
  evidence_ids: string[]
  executable: false
}

export interface AIOpsRun {
  id: string
  status: AIOpsRunStatus
  created_at: string
  finished_at?: string
  group_id: number | null
  window: { start: string; end: string }
  evidence: AIOpsEvidence[]
  result?: {
    summary: string
    findings: AIOpsFinding[]
    proposals: AIOpsProposal[]
    limitations: string[]
  }
  error_code?: string
}

export const aiOpsAPI = {
  async status(signal?: AbortSignal): Promise<AIOpsStatus> {
    const { data } = await apiClient.get<AIOpsStatus>('/admin/ops/ai/status', { signal })
    return data
  },
  async list(signal?: AbortSignal): Promise<{ items: AIOpsRun[]; total: number }> {
    const { data } = await apiClient.get<{ items: AIOpsRun[]; total: number }>('/admin/ops/ai/runs', { signal })
    return data
  },
  async get(id: string, signal?: AbortSignal): Promise<AIOpsRun> {
    const { data } = await apiClient.get<AIOpsRun>(`/admin/ops/ai/runs/${encodeURIComponent(id)}`, { signal })
    return data
  },
  async create(signal?: AbortSignal): Promise<AIOpsRun> {
    const { data } = await apiClient.post<AIOpsRun>('/admin/ops/ai/runs', {}, { signal })
    return data
  },
}
