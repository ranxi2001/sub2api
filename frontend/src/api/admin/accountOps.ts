import { apiClient } from '../client'
export type AccountOpsWebhookProvider = 'wecom' | 'dingtalk' | 'feishu' | 'custom'
export interface AccountOpsWebhook {
  id: string
  name?: string
  provider: AccountOpsWebhookProvider
  enabled: boolean
  url_configured?: boolean
  secret_configured?: boolean
  message_template?: string
}
export interface AccountOpsWebhookInput {
  id: string
  name?: string
  provider?: AccountOpsWebhookProvider | 'auto'
  enabled: boolean
  url?: string
  secret?: string
  clear_secret?: boolean
  message_template?: string
}
export interface AccountOpsBalanceThreshold {
  account_id: number
  enabled: boolean
  threshold: number
  unit: string
  notify_alert?: boolean
  notify_recovery?: boolean
}
export interface AccountOpsQuotaThreshold {
  account_id: number
  enabled: boolean
  threshold_percent: number
  window: string
  notify_alert?: boolean
  notify_recovery?: boolean
}
export type AccountOpsObservationStatus = 'ok' | 'stale' | 'failed' | 'unsupported' | 'unknown'
export interface AccountOpsThresholdAccount {
  account_id: number
  account_name: string
  group_id?: string
  group_name?: string
  group_mode?: 'auto' | 'manual' | 'account'
  platform: string
  type: 'apikey' | 'oauth'
  balance: number | null
  unit: string
  balance_status: AccountOpsObservationStatus
  received_at: string | null
  usage_windows: Array<{
    window: string
    label: string
    used_percent: number | null
    status: AccountOpsObservationStatus
    observed_at?: string
    resets_at?: string
  }>
}
export interface AccountOpsGroup {
  id: string
  name: string
  default_name: string
  provider: string
  site: string
  mode: 'auto' | 'manual' | 'account'
  account_ids: number[]
}
export interface AccountOpsConfig {
  enabled: boolean
  recipient: string
  email_name?: string
  balance_low: boolean
  weekly_quota: boolean
  cooldown_minutes: number
  webhooks?: AccountOpsWebhook[]
  groups?: Array<{ id: string; name: string; provider?: string; site?: string; account_ids?: number[] }>
  balance_thresholds?: AccountOpsBalanceThreshold[]
  quota_thresholds?: AccountOpsQuotaThreshold[]
}
export type AccountOpsConfigInput = Omit<AccountOpsConfig, 'webhooks'> & { webhooks?: AccountOpsWebhookInput[] }
export interface AccountOpsEvent {
  id?: string
  episode_id?: string
  phase?: 'alert' | 'recovery'
  notification_enabled?: boolean
  account_id: number
  account_name: string
  group_id?: string
  group_name?: string
  kind: 'balance_low' | 'weekly_quota' | 'balance_threshold' | 'quota_threshold'
  signal: string
  http_status: number
  first_seen: string
  last_seen: string
  occurrences: number
  state: 'pending' | 'sending' | 'sent' | 'failed' | 'suppressed' | 'resolved'
  last_sent_at: string | null
  next_send_at: string
  attempts: number
  details?: { balance?: number; threshold?: number; unit?: string; used_percent?: number; threshold_percent?: number; window?: string; observed_at?: string; resets_at?: string }
  deliveries?: Record<string, { provider: string; name?: string; status: 'sent' | 'failed'; attempts: number; last_sent_at?: string }>
}
export type AccountOpsNotificationSettings = Partial<Pick<AccountOpsConfig, 'enabled' | 'recipient' | 'email_name' | 'balance_low' | 'weekly_quota' | 'cooldown_minutes'>>
export interface AccountOpsRuleInput {
  metric: 'balance' | 'quota'
  enabled: boolean
  enabled_only?: boolean
  threshold?: number
  unit?: string
  threshold_percent?: number
  window?: string
  notify_alert?: boolean
  notify_recovery?: boolean
}
export async function saveAccountOpsNotificationSettings(settings: AccountOpsNotificationSettings): Promise<AccountOpsConfig> {
  return (await apiClient.put('/admin/account-ops/notification-settings', settings)).data
}
export async function saveAccountOpsRule(id: number, rule: AccountOpsRuleInput): Promise<AccountOpsConfig> {
  return (await apiClient.put(`/admin/account-ops/rules/${id}`, rule)).data
}
export async function saveAccountOpsRulesBatch(accountIds: number[], rule: AccountOpsRuleInput): Promise<AccountOpsConfig> {
  return (await apiClient.put('/admin/account-ops/rules/batch', { account_ids: accountIds, rule })).data
}
export interface AccountOpsRuleBatchGroup {
  account_ids: number[]
  rule: AccountOpsRuleInput
}
export async function saveAccountOpsRuleGroups(groups: AccountOpsRuleBatchGroup[]): Promise<AccountOpsConfig> {
  return (await apiClient.put('/admin/account-ops/rules/batch', { groups })).data
}
export async function deleteAccountOpsRule(id: number, metric: 'balance' | 'quota'): Promise<AccountOpsConfig> {
  return (await apiClient.delete(`/admin/account-ops/rules/${id}`, { params: { metric } })).data
}
export async function saveAccountOpsWebhook(id: string, input: Omit<AccountOpsWebhookInput, 'id'>): Promise<AccountOpsConfig> {
  return (await apiClient.put(`/admin/account-ops/webhooks/${encodeURIComponent(id)}`, input)).data
}
export async function deleteAccountOpsWebhook(id: string): Promise<AccountOpsConfig> {
  return (await apiClient.delete(`/admin/account-ops/webhooks/${encodeURIComponent(id)}`)).data
}
export interface AccountOpsSettings {
  config: AccountOpsConfig
  smtp_configured: boolean
  dropped_signals: number
  storage_failures: number
  encryption_key_configured?: boolean
}
export async function getAccountOpsSettings(): Promise<AccountOpsSettings> {
  return (await apiClient.get('/admin/account-ops/config')).data
}
export async function saveAccountOpsSettings(config: AccountOpsConfigInput): Promise<AccountOpsConfig> {
  return (await apiClient.put('/admin/account-ops/config', config)).data
}
export async function getAccountOpsThresholdAccounts(): Promise<AccountOpsThresholdAccount[]> {
  const { data } = await apiClient.get<{ items: AccountOpsThresholdAccount[] }>('/admin/account-ops/threshold-accounts')
  return data.items ?? []
}
export async function getAccountOpsThresholdGroups(): Promise<AccountOpsGroup[]> {
  const { data } = await apiClient.get<{ groups: AccountOpsGroup[] }>('/admin/account-ops/groups')
  return (data.groups ?? []).map(group => ({
    ...group,
    account_ids: Array.isArray(group.account_ids) ? [...group.account_ids] : [],
  }))
}
export async function saveAccountOpsGroups(groups: Array<{ id: string; name: string; provider?: string; site?: string; account_ids: number[] }>): Promise<AccountOpsConfig> {
  return (await apiClient.put('/admin/account-ops/groups', { groups })).data
}
export async function testAccountOpsWebhook(id: string): Promise<{ ok: boolean }> {
  return (await apiClient.post(`/admin/account-ops/webhooks/${encodeURIComponent(id)}/test`)).data
}
export async function getAccountOpsEvents(offset = 0): Promise<{ items: AccountOpsEvent[]; has_more: boolean }> {
  const { data } = await apiClient.get('/admin/account-ops/alerts', { params: { offset, limit: 50 } })
  return { items: data.items ?? [], has_more: data.has_more ?? false }
}
