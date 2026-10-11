import { apiClient } from '../client'

export interface CodexHarvestSpeed {
  round_interval_seconds: number
  probe_interval_seconds: number
  attempt_timeout_seconds: number
  cooldown_seconds: number
  max_requests_per_round: number
  max_node_attempts: number
  refresh_before_seconds: number
}

export interface CodexHarvestControls {
  edge_ip?: string
  target_gateway?: string
  transport?: 'sse' | 'websocket'
  version: number
  node_memory_enabled: boolean
  ticket_first_scheduling?: boolean
  speed: CodexHarvestSpeed
}

export interface CodexHarvestRuntime {
  running: boolean
  next_round_at: string | null
  requests_used: number
  request_budget: number
  current_node: string
  selection_reason: string
  degraded_reason: string
}

export interface CodexHarvestBound {
  min: number
  max: number
}

export interface CodexHarvestControlSnapshot {
  settings: CodexHarvestControls
  configured: boolean
  settings_error: string
  presets: Record<string, CodexHarvestSpeed>
  bounds?: Record<string, CodexHarvestBound>
  defaults: CodexHarvestControls
  runtime: CodexHarvestRuntime
  available: boolean
  availability_reason: string
}

export interface CodexHarvestNodeRecord {
  id: number
  pool_id: string
  node_id: string
  node_name: string
  provider: string
  account_id: number
  model: string
  blocks: number
  successes: number
  misses: number
  network_errors: number
  account_errors: number
  consecutive_failures: number
  last_success: string | null
  cooldown_until: string | null
  latency_ms: number
  last_result: string
  updated_at: string
}

export interface CodexHarvestNodePage {
  items: CodexHarvestNodeRecord[]
  total: number
}

const base = '/admin/accounts'

export async function getCodexHarvestControls(): Promise<CodexHarvestControlSnapshot> {
  const { data } = await apiClient.get<CodexHarvestControlSnapshot>(`${base}/codex-harvest-controls`)
  return data
}

export async function saveCodexHarvestControls(settings: CodexHarvestControls): Promise<CodexHarvestControls> {
  const { data } = await apiClient.put<CodexHarvestControls>(`${base}/codex-harvest-controls`, settings)
  return data
}

export async function getCodexHarvestNodes(offset = 0, limit = 20): Promise<CodexHarvestNodePage> {
  const { data } = await apiClient.get<CodexHarvestNodePage>(`${base}/codex-harvest-nodes`, { params: { offset, limit } })
  return { items: data.items || [], total: data.total || 0 }
}

export async function resetCodexHarvestNodes(recordId: number): Promise<void> {
  await apiClient.post(`${base}/codex-harvest-nodes/reset`, { record_id: recordId })
}