import { apiClient } from './client'
import type { ChannelMonitorMode } from '@/utils/featureFlags'

/** Latest judged status of a component; `unknown` without traffic in the window. */
export type MonitorV3Status = 'operational' | 'degraded' | 'down' | 'unknown'
/** `insufficient`: fewer requests than the page minimum, so the slot is not judged. */
export type MonitorV3CellStatus = 'operational' | 'degraded' | 'down' | 'insufficient'
export type MonitorV3Visibility = 'group' | 'public'
export type MonitorV3Range = '24h' | '7d' | '30d'

export const MONITOR_V3_INTERVALS = [1, 2, 3, 5, 10, 15, 30, 60] as const
/** V2 latency histogram bounds; thresholds compare exactly against the P50 bucket. */
export const MONITOR_V3_TTFT_THRESHOLDS = [1000, 2000, 3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000] as const
export const MONITOR_V3_RANGES: MonitorV3Range[] = ['24h', '7d', '30d']

export interface MonitorV3Cell {
  start: string
  status: MonitorV3CellStatus
  /** 0–1 over counted requests (errors users caused are not counted). */
  success_rate: number
  ttft_p50_ms?: number | null
  top_error?: string
  /**
   * The slot had too little real traffic and was judged by idle probes;
   * success_rate and top_error then describe the probes.
   */
  probed?: boolean
  /** Median latency of the slot's successful probes. */
  probe_latency_ms?: number | null
  /** Admin-only volumes. */
  requests?: number
  errors?: number
  ignored_errors?: number
  probes?: number
  probe_errors?: number
}

export interface MonitorV3ComponentStatus {
  id: number
  name: string
  description?: string
  model?: string
  models?: string[]
  multiplier?: number | null
  status: MonitorV3Status
  /** 0–100 over the configured range; null without traffic. */
  availability: number | null
  /** Admin-only request count over the range. */
  requests?: number
  /** Admin-only number of counted idle probes over the range. */
  probes?: number
  last_data_at: string | null
  /** One entry per slot of the window, oldest first; null without traffic. */
  cells: Array<MonitorV3Cell | null>
  group_id?: number
}

export interface MonitorV3CategoryStatus {
  id: number
  name: string
  description?: string
  availability: number | null
  components: MonitorV3ComponentStatus[]
}

export interface MonitorV3Window {
  start: string
  end: string
  latest: boolean
  has_older: boolean
}

export interface MonitorV3StatusPage {
  generated_at: string
  interval_minutes: number
  cells: number
  availability_range: MonitorV3Range
  availability_since: string
  down_error_rate: number
  degraded_error_rate: number
  degraded_ttft_ms: number
  min_requests: number
  data_through: string | null
  footer_note: string
  window: MonitorV3Window
  featured: MonitorV3ComponentStatus | null
  categories: MonitorV3CategoryStatus[]
  open_incidents: number
  /** Quiet slots of some shown components are filled in by idle probes. */
  idle_probe?: boolean
}

export interface MonitorV3Incident {
  component_id: number
  component_name: string
  started_at: string
  ended_at: string | null
  down_slots: number
  top_error?: string
  requests?: number
  errors?: number
  /** Admin-only: idle probes behind down slots real traffic could not judge. */
  probes?: number
}

export interface MonitorV3IncidentPage {
  items: MonitorV3Incident[]
  total: number
  page: number
  page_size: number
  since: string
}

export interface MonitorV3Config {
  version: number
  interval_minutes: number
  cells: number
  availability_range: MonitorV3Range
  down_error_rate: number
  degraded_error_rate: number
  degraded_ttft_ms: number
  min_requests: number
  ignored_error_categories: string[]
  featured_component_id: number | null
  footer_note: string
  updated_at: string
}

export interface MonitorV3Category {
  id: number
  name: string
  description: string
  sort_order: number
  created_at: string
  updated_at: string
}

export interface MonitorV3CategoryInput {
  name: string
  description: string
}

export interface MonitorV3ComponentInput {
  category_id: number | null
  name: string
  description: string
  group_id: number
  /** First selected model for older clients. */
  model: string
  /** Empty selects every requested model in this group. */
  models?: string[]
  /** 0 inherits the page threshold. */
  degraded_ttft_ms: number
  show_multiplier: boolean
  visibility: MonitorV3Visibility
  enabled: boolean
}

export interface MonitorV3Component extends MonitorV3ComponentInput {
  id: number
  sort_order: number
  group_name: string
  group_platform: string
  group_status: string
  group_rate_multiplier: number
  group_deleted: boolean
  created_at: string
  updated_at: string
}

export interface MonitorV3IdleProbeGroup {
  group_id: number
  /** Asked for components covering every model; others probe their own model. */
  model: string
  /** Empty keeps the model's default. */
  reasoning_effort: string
}

export interface MonitorV3IdleProbe {
  enabled: boolean
  groups: MonitorV3IdleProbeGroup[]
}

export type MonitorV3ProbeOutcome = 'success' | 'failure'

/** One group's probes of the last day; last_* describe the newest probe. */
export interface MonitorV3IdleProbeStat {
  group_id: number
  probes: number
  failures: number
  cost_usd: number
  last_at: string | null
  last_model?: string
  last_outcome?: MonitorV3ProbeOutcome
  last_latency_ms?: number | null
  last_category?: string
  last_error?: string
}

/** Reasoning efforts offered for idle probes; '' keeps the model default. */
export const MONITOR_V3_PROBE_EFFORTS = ['', 'low', 'medium', 'high'] as const

export interface MonitorV3Settings {
  config: MonitorV3Config
  categories: MonitorV3Category[]
  components: MonitorV3Component[]
  error_categories: string[]
  data_through: string | null
  idle_probe: MonitorV3IdleProbe
  idle_probe_stats: MonitorV3IdleProbeStat[]
}

const base = (admin: boolean) => (admin ? '/admin/channel-monitor-v3' : '/channel-monitor-v3')

/** `end` (unix seconds) selects an older window by its last slot. */
export async function getStatus(end: number | null, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorV3StatusPage>(`${base(admin)}/status`, {
    params: end ? { end } : undefined,
    signal,
  })
  return data
}

export async function getIncidents(page: number, pageSize: number, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorV3IncidentPage>(`${base(admin)}/incidents`, {
    params: { page, page_size: pageSize },
    signal,
  })
  return data
}

export async function getSettings() {
  const { data } = await apiClient.get<MonitorV3Settings>('/admin/channel-monitor-v3/settings')
  return data
}

export async function updateConfig(config: MonitorV3Config) {
  const { data } = await apiClient.put<MonitorV3Config>('/admin/channel-monitor-v3/config', config)
  return data
}

/** Saves which groups get idle probes; the page config is left alone. */
export async function updateIdleProbe(probe: MonitorV3IdleProbe) {
  const { data } = await apiClient.put<MonitorV3IdleProbe>('/admin/channel-monitor-v3/idle-probe', probe)
  return data
}

export async function createCategory(input: MonitorV3CategoryInput) {
  const { data } = await apiClient.post<MonitorV3Category>('/admin/channel-monitor-v3/categories', input)
  return data
}

export async function updateCategory(id: number, input: MonitorV3CategoryInput) {
  const { data } = await apiClient.put<MonitorV3Category>(`/admin/channel-monitor-v3/categories/${id}`, input)
  return data
}

export async function deleteCategory(id: number) {
  await apiClient.delete(`/admin/channel-monitor-v3/categories/${id}`)
}

export async function createComponent(input: MonitorV3ComponentInput) {
  const { data } = await apiClient.post<MonitorV3Component>('/admin/channel-monitor-v3/components', input)
  return data
}

export async function updateComponent(id: number, input: MonitorV3ComponentInput) {
  const { data } = await apiClient.put<MonitorV3Component>(`/admin/channel-monitor-v3/components/${id}`, input)
  return data
}

export async function deleteComponent(id: number) {
  await apiClient.delete(`/admin/channel-monitor-v3/components/${id}`)
}

export async function reorder(categoryIds: number[], componentIds: number[]) {
  await apiClient.post('/admin/channel-monitor-v3/reorder', { category_ids: categoryIds, component_ids: componentIds })
}

/** Switches the site between V1, V2 and V3 without a full settings save. */
export async function setMonitorMode(mode: ChannelMonitorMode) {
  const { data } = await apiClient.put<{ mode: ChannelMonitorMode }>('/admin/channel-monitor-mode', { mode })
  return data.mode
}
