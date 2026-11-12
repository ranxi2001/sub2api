import { apiClient } from '@/api/client'
export interface KeyRiskConfig { enabled: boolean; auto_email: boolean; age_email: boolean; max_reminders: number; age_days: number; window_minutes: number; ips_per_key: number; trusted_networks: string[] }
export interface RiskIdentity { id: number; name: string }
export interface KeyRiskRecipient { user_name?: string; user_id: number; generation: number; attempts: number; sent: number; uncertain: number; last_attempt: string | null; acknowledged_at: string | null }
export interface KeyRiskEvent { users?: RiskIdentity[]; keys?: RiskIdentity[]; id: number; generation: number; recipients: KeyRiskRecipient[]; rule: string; subject: string; state: string; first_seen: string; last_seen: string; evidence: { ip?: string; ips?: string[]; rapid_switch?: boolean; user_ids: number[]; key_ids: number[]; requests: number; window_minutes: number; first: string; last: string } }
export interface KeyRiskSnapshot { config: KeyRiskConfig; items: KeyRiskEvent[]; last_scan_at: string; coverage_issue: string; pending: number; uncertain: number; sent: number }
const base = '/admin/settings/key-risk'
export const keyRiskApi = {
  async get(before = 0) { return (await apiClient.get<KeyRiskSnapshot>(base, { params: { before } })).data },
  async save(config: KeyRiskConfig) { await apiClient.put(base, config) },
  async action(id: number, action: string, restriction?: { generation: number; confirm: boolean; allowed_networks: string[] }) { return (await apiClient.post<{ queued: boolean; recipients: number }>(`${base}/${id}`, { action, ...restriction })).data }
}

export interface UserKeyRiskNotice { keys?: RiskIdentity[]; event_id: number; generation: number; rule: string; state: string; key_ids: number[]; first_seen: string; last_seen: string; attempts: number; max_reminders: number; acknowledged_at: string | null }
export const userKeyRiskApi = {
 async list(before = 0) { return (await apiClient.get<{ items: UserKeyRiskNotice[] }>('/user/key-risks', { params: { before } })).data },
 async acknowledge(event: UserKeyRiskNotice) { await apiClient.post(`/user/key-risks/${event.event_id}/acknowledge`, { generation: event.generation, confirm: true }) }
}
