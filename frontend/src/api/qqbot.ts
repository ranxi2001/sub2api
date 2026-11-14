import { apiClient } from './client'
function versions(headers: Record<string, unknown>) {
 const extra: { _version?: string; _sections?: Record<string, string> } = { _version: (headers?.['x-qqbot-config-version'] || headers?.etag) as string | undefined }
 if (headers?.['x-qqbot-checkin-version']) extra._sections = Object.fromEntries(['checkin', 'tasks', 'periodic_tasks'].map(key => [key, headers[`x-qqbot-${key}-version`] as string]))
 return extra
}
async function versionedGet<T>(path: string): Promise<T> { const r = await apiClient.get<T>(path); return { ...r.data, ...versions(r.headers) } }
async function versionedPut<T extends { _version?: string; _section?: string }>(path: string, value: T): Promise<T> { const r = await apiClient.put<T>(path, value, { headers: { 'If-Match': value._version || '', 'X-QQBot-Section': value._section || '' } }); return { ...r.data, ...versions(r.headers) } }


export interface QQBotCustomCommand { command: string; reply: string; enabled: boolean }
export interface QQSiteKnowledgeEntry { id: string; title: string; category: string; content: string; url: string; keywords: string[]; enabled: boolean; updated_at: string }
export interface QQSiteKnowledgeConfig { enabled: boolean; content: string; entries?: QQSiteKnowledgeEntry[] }
export function defaultSiteKnowledgeConfig(): QQSiteKnowledgeConfig {
  return { enabled: false, content: '' }
}
export interface QQGroupAdmin { app_id: string; group_id: string; member_id: string; nickname: string }
export interface QQModerationConfig {
  inherit_groups?: boolean; enabled: boolean; groups: string[]; repeat_window_seconds: number; repeat_count: number
  burst_window_seconds: number; burst_count: number; mute_minutes: number
  repeat_offense_minutes: number; escalated_mute_minutes: number
  report_enabled: boolean; report_mute_minutes: number
  exemptions: { group_id: string; member_id: string; nickname?: string }[]
}
export function defaultModerationConfig(): QQModerationConfig {
  return { enabled: false, groups: [], repeat_window_seconds: 15, repeat_count: 4,
    burst_window_seconds: 10, burst_count: 10, mute_minutes: 5,
    repeat_offense_minutes: 30, escalated_mute_minutes: 30,
    report_enabled: false, report_mute_minutes: 5, exemptions: [] }
}
export interface QQModerationRecord {
  id: string; group_id: string; member_id: string; reason: string; mute_seconds: number
  created_at: string; mute_status: string; recall_total: number; recall_succeeded: number; pending: number; error: string
}
export interface QQAutonomousConfig {
  reply_mode?: 'quiet' | 'natural' | 'active' | 'always'; merge_seconds?: number; input_token_budget?: number
  inherit_groups?: boolean; enabled: boolean; groups: string[]; names: string[]; topics: string[]
  sample_percent: number; cooldown_seconds: number; min_messages: number
  member_cooldown_seconds: number; hourly_replies: number; daily_replies: number; daily_calls: number
  start_hour: number; end_hour: number; followup_seconds: number; max_chars: number; mention_user: boolean
}
export function defaultAutonomousConfig(): QQAutonomousConfig {
  return { reply_mode: 'natural', merge_seconds: 0, input_token_budget: 8192, enabled: false, groups: [], names: ['CTAV'], topics: [], sample_percent: 25, cooldown_seconds: 60,
    min_messages: 2, member_cooldown_seconds: 120, hourly_replies: 12, daily_replies: 80, daily_calls: 160,
    start_hour: 0, end_hour: 0, followup_seconds: 90, max_chars: 100, mention_user: false }
}
export interface QQHealthNotificationConfig {
  enabled: boolean; mention_all?: boolean; groups: string[]; minimum_samples: number; error_percent: number
  recovery_percent: number; ttft_seconds: number; failure_minutes: number; recovery_minutes: number; cooldown_minutes: number
}
export function defaultHealthNotificationConfig(): QQHealthNotificationConfig {
  return { enabled: false, mention_all: false, groups: [], minimum_samples: 50, error_percent: 20, recovery_percent: 5,
    ttft_seconds: 10, failure_minutes: 3, recovery_minutes: 2, cooldown_minutes: 30 }
}
export interface QQGroupPolicy {
  disable_autonomous?: boolean; disable_moderation?: boolean; group_id: string; label: string; disable_chat: boolean; disable_images: boolean
  disable_activities: boolean; disable_notifications: boolean; chat_daily_limit: number | null; image_daily_limit: number | null
}
export interface QQBotConfig {
  setup_warning?: string
  _version?: string
  notification_quiet_hours?: { enabled: boolean; start_hour: number; end_hour: number }
  section_versions?: Record<string, string>
  chat_disabled?: boolean
  group_policies?: QQGroupPolicy[]
  reward_budget_daily?: number | null
  ai_connection?: 'site' | 'external' | ''
  site_api_key_id?: number; site_group_id?: number; site_owner_id?: number
  image_ai_connection?: '' | 'reuse_chat' | 'external' | 'site'
  image_site_api_key_id?: number; image_site_group_id?: number; image_site_owner_id?: number
  reward_alert_daily?: number
  moderation?: QQModerationConfig
  health_notifications?: QQHealthNotificationConfig
  autonomous?: QQAutonomousConfig
  enabled: boolean; app_id: string; app_secret: string; app_secret_set: boolean; sandbox: boolean
  allowed_groups: string[]; base_url: string; api_key: string; api_key_set: boolean
  custom_commands: QQBotCustomCommand[]
  site_knowledge?: QQSiteKnowledgeConfig
  group_admins?: QQGroupAdmin[]
  price_notifications?: { enabled: boolean; scheduled: boolean; mention_all?: boolean; groups: string[] }
  chat_model: string; chat_protocol: 'chat_completions' | 'responses'
  image_enabled: boolean; image_base_url: string; image_api_key: string; image_api_key_set: boolean
  image_model: string; image_size: string; system_prompt: string; max_output_tokens: number
  image_output_format?: '' | 'png' | 'jpeg' | 'webp' | 'gif'
  history_messages: number; daily_chat_limit: number; daily_image_limit: number
  total_daily_chat_limit: number; total_daily_image_limit: number; cooldown_seconds: number
  checkin_reward: number; invite_enabled: boolean; invite_reward: number; invite_daily_limit: number
  invite_min_recharge_total?: number; invite_min_recharge_30d?: number; invite_recharge_window_days?: number
  clear_app_secret?: boolean; clear_api_key?: boolean; clear_image_api_key?: boolean
}
export interface QQAnnouncementRequest {
  app_id: string; sandbox: boolean; groups: string[]; text: string; mention_all: boolean; request_id: string
}
export interface QQAnnouncementResult {
  batch_id: string; jobs: { id: string; group_id: string }[]; duplicate: boolean
}
export interface QQRecentMember {
  group_id: string; member_id: string; nickname: string; email: string
  message_preview: string; last_seen_at: string
}
export interface QQMemberOption extends QQRecentMember { group_label: string }
export interface QQMemberPage { app_id: string; items: QQMemberOption[]; total: number; page: number; page_size: number }
export type QQSiteQueryKind = 'model_price' | 'channel_status' | 'activity_rules' | 'account_balance' | 'today_usage' | 'lottery_chances'
export type QQSiteQueryStatus = 'succeeded' | 'not_found' | 'unavailable' | 'failed'
export interface QQSiteQueryDecision {
  kind: QQSiteQueryKind; model: string; activity: string; page: number
  status: QQSiteQueryStatus; error_code: string; duration_ms: number
}
export interface QQChatExecution {
  attempts: number
  retries: number
  usage_complete: boolean
  last_error_code?: string
}
export interface QQAutonomousExecution {
  mode?: string; batch_messages?: number; model_started?: boolean; model_ms?: number
  input_estimate?: { system: number; knowledge: number; history: number; current: number; images: number; total: number; removed: number }
}
export interface QQChatPreviewRequest { group_id: string; member_id?: string; question: string }
export interface QQChatPreviewResult {
  text: string; action: string; input_tokens: number; output_tokens: number; duration_ms: number
  knowledge_sources: { id: string; title: string; url: string; updated_at: string }[]
  query?: QQSiteQueryDecision
  chat_execution?: QQChatExecution
}
export interface QQBotRuntime {
  recent_members?: QQRecentMember[]
  moderation_records?: QQModerationRecord[]
  autonomous_stats?: { group_id: string; reason: string; count: number }[]
  encryption_ready: boolean; bindings: number
  groups: { id: string; last_event: string; last_seen_at: string }[]
  jobs: { autonomous_data?: QQAutonomousExecution; site_query?: QQSiteQueryDecision; chat_execution?: QQChatExecution; image_decision?: { tool?: string; review?: string; basis?: string; reason?: string; execution?: string; message_ref?: string; plan_ref?: string }; trigger_reason?: string; decision_reason?: string; id: string; group_id: string; member_id: string; kind: string; status: string; error: string; input_tokens: number; output_tokens: number; created_at: string }[]
  rewards: { id: number; user_id: number; kind: string; amount: number; created_at: string }[]
}
export interface QQBindingRequest { code?: string; member_id: string; group_id: string; nickname: string; expires_at: string }
export interface QQEmailBindingPreview { member_id: string; group_id: string; nickname: string; expires_at: string }
export interface QQAdminBinding {
  app_id: string; member_id: string; user_id: number; nickname: string; group_id: string
  created_at: string; email: string; user_status: string; user_deleted: boolean
}
export interface QQAdminBindingsPage { items: QQAdminBinding[]; total: number; page: number; page_size: number }
export interface QQBindingState {
  enabled: boolean
  binding: { member_id: string; group_id: string; nickname: string; created_at: string } | null
  pending: QQBindingRequest | null
}
export type QQGrowthTaskKind = 'binding' | 'usage_count' | 'checkin_total' | 'checkin_streak' | 'recharge_total' | 'usage_tokens' | 'usage_amount' | 'usage_days' | 'model_count' | 'invite_count' | 'usage_amount_days' | 'usage_amount_streak' | 'recharge_days' | 'recharge_streak'
export type QQGrowthTaskPeriod = 'once' | 'daily' | 'weekly'
export interface QQGrowthReward {
  coupon_template_id?: number
  type: QQRewardType; amount: number; quantity: number; prize_name: string; claim_instructions: string
}
export interface QQGrowthTask {
  period?: QQGrowthTaskPeriod
  daily_amount?: number
  reward: QQGrowthReward
  id: string; name: string; kind: QQGrowthTaskKind; target: number; chances: number; enabled: boolean
}
export interface QQRankingPrize { rank: number; reward: QQGrowthReward }
export interface QQRankingConfig {
  _version?: string
  enabled: boolean; rewards_enabled: boolean; effective_day: string
  token_rewards: QQRankingPrize[]; amount_rewards: QQRankingPrize[]
}
export interface QQCheckinRewardDistribution {
  lower_half_bps: number
  next_quarter_bps: number
  upper_quarter_bps: number
}
export interface QQCheckinMilestone {
  id: string; name: string; kind: 'checkin_total' | 'checkin_streak'; days: number; repeat?: boolean; reward: QQGrowthReward; enabled: boolean
}
export interface QQCheckinConfig {
 coupon_template_id?: number; coupon_quantity?: number
  milestones: QQCheckinMilestone[]
  reward_distribution?: QQCheckinRewardDistribution
  _sections?: Record<string, string>
  _section?: string
  _version?: string
  streak_reward: QQGrowthReward
  streak_days: number
  streak_chances: number
  growth: { binding_chances: number; first_usage_chances: number; checkin_30_chances: number; tasks: QQGrowthTask[]; periodic_tasks: QQGrowthTask[] }
  enabled: boolean; min_amount: number; max_amount: number
  min_recharge_total: number; min_recharge_30d: number; recharge_window_days: number
}
export interface QQInviteConfig {
 coupon_template_id?: number; coupon_quantity?: number
  _version?: string
  enabled: boolean
  invite_reward: number; invite_daily_limit: number
  min_recharge_total: number; min_recharge_30d: number; recharge_window_days: number
}
export interface QQTransferConfig {
  _version?: string
  transfer_enabled: boolean; transfer_fee_percent: number
  transfer_min_amount: number; transfer_max_amount: number
}
export interface QQTransferPreview {
  from_email: string; to_email: string; amount: string; fee: string
  net_amount: string; expires_at: string
}
export interface QQLotteryPrize { coupon_template_id?: number; coupon_quantity?: number; name: string; amount: number; chances?: number; probability_bps: number }
export interface QQLotteryConfig {
  _version?: string
  enabled: boolean; title: string; recharge_step: number; chances_per_step: number; prizes: QQLotteryPrize[]
  guarantee_enabled: boolean; guarantee_every: number; guarantee_prizes: QQLotteryPrize[]
}
export interface QQLotteryState { user_id: number; recharge_total: number; earned: number; bonus_chances?: number; used: number; remaining: number; next_recharge: number; guarantee_every: number; draws_until_guarantee: number }
export interface QQLotteryDraw { coupon_template_id?: number; coupon_quantity?: number; id: number; user_id: number; email: string; member_id: string; prize_name: string; amount: number; chances?: number; remaining: number; created_at: string; is_guaranteed: boolean }
export interface QQLotteryDrawsPage { items: QQLotteryDraw[]; total: number; page: number; page_size: number }
export type QQActivityKind = 'checkin' | 'lottery' | 'invite'
export interface QQActivityParticipant {
  member_id: string; user_id: number; email: string; nickname: string
  bound: boolean; user_status: string; user_deleted: boolean
}
export interface QQActivityRecord {
  id: string; group_id: string; participant: QQActivityParticipant; inviter?: QQActivityParticipant
  status: string; amount: number; chances?: number; coupon_template_id?: number; coupon_quantity?: number; prize_name: string; is_guaranteed?: boolean; remaining?: number; created_at: string
  invited_at?: string; joined_at?: string; left_at?: string; rewarded_at?: string
  reward_user_id: number; reward_email: string
}
export interface QQActivityRecordsPage { items: QQActivityRecord[]; total: number; page: number; page_size: number }
export type QQRewardType = 'balance' | 'chances' | 'physical' | 'other' | 'coupon'
export interface QQRewardCode {
  batch_name: string; reward_type: QQRewardType; amount: number; quantity: number
  prize_name: string; claim_instructions: string; target_user_id: number | null; expires_at: string | null
  source_kind?: 'code' | 'growth' | 'streak' | 'ranking'
  id: number; app_id: string; code_hint: string; status: 'unused' | 'disabled' | 'expired' | 'pending' | 'fulfilled'
  user_id: number | null; email: string; member_id: string; redeemed_at: string | null
  fulfilled_at: string | null; fulfillment_note: string; created_at: string
}
export interface QQRewardCodesPage { items: QQRewardCode[]; total: number; page: number; page_size: number }
export interface QQDiagnosticCheck { code: string; ok: boolean; message: string }
export interface QQOverview {
  day: string; last_event: string | null; queued: number; running: number; failed: number
  chat_used: number; image_used: number; chat_remaining: number; image_remaining: number
  reward_amount: number; pending_rewards: number; reward_alert: boolean; checks: QQDiagnosticCheck[]
}
export interface QQJobPage { items: QQBotRuntime['jobs']; total: number; page: number; page_size: number }
export interface QQConfigChange { at: string; section: string; before: Partial<QQBotConfig>; after: Partial<QQBotConfig> }
export interface QQCommandInfo { command: string; description: string; usage: string; builtin: boolean; enabled: boolean; reply: string }
export interface QQActivityPreview {
 checkin_total?: number; checkin_milestones?: { id: string; name: string; progress: string; eligible: boolean; status: string; reward: string }[]
 user_id: number; bound: boolean; checkin_eligible: boolean; checkin_rule: string; checkin_done: boolean
 invite_eligible: boolean; invite_rule: string; lottery: QQLotteryState
 checkin_expected: number; lottery_expected: number; lottery_next_expected: number
 tasks: { id: string; name: string; progress: string; eligible: boolean; status: string; reward: string }[]
}
export interface QQTransferHistory { items: { id: number; from_user_id: number; to_user_id: number; from_email: string; to_email: string; amount: string; fee: string; net_amount: string; created_at: string }[]; total: number; page: number; page_size: number }
export interface QQGroupInfo { app_id: string; sandbox: boolean; group_id: string; name: string; status: 'available' | 'permission_required' | 'unavailable' | 'unconfigured' | 'rate_limited'; retry_after_seconds?: number }
export interface QQSetupOptions {
  groups: { id: number; name: string; models: string[]; platform?: string; chat_protocols?: ('chat_completions' | 'responses')[] }[]
  image_groups?: QQSetupOptions['groups']
  current_image_quota?: number; current_image_used?: number
  frontend_url: string; email_configured: boolean; current_quota: number; current_used: number
}
export interface QQSetupCheck { code: string; status: 'ready' | 'blocked' | 'warning' | 'pending'; message: string; action: string }
export interface QQSetupReadiness { checks: QQSetupCheck[]; ready: boolean; version: string }
export interface QQSetupProbe { kind: string; status: 'ready' | 'blocked'; message: string; at: string }
export interface QQActivityBundle { version: string; checkin: QQCheckinConfig; lottery: QQLotteryConfig; invite: QQInviteConfig; ranking: QQRankingConfig; budget: number | null }
export interface QQSetupRequest { config: QQBotConfig; versions: Record<string, string>; sections: string[]; site_quota: number; regenerate_site_key?: boolean; image_site_quota?: number; regenerate_image_site_key?: boolean }
export const qqbotAPI = {
  sendAnnouncement: async (request: QQAnnouncementRequest) => (await apiClient.post<QQAnnouncementResult>('/admin/qqbot/announcements', request)).data,
  setupOptions: async () => (await apiClient.get<QQSetupOptions>('/admin/qqbot/setup/options')).data,
  setupChecks: async () => (await apiClient.get<QQSetupReadiness>('/admin/qqbot/setup/checks')).data,
  saveSetup: async (request: QQSetupRequest) => (await apiClient.put<QQBotConfig>('/admin/qqbot/setup', request)).data,
  setupProbe: async (kind: string) => (await apiClient.post<QQSetupProbe>('/admin/qqbot/setup/probe/' + kind, {}, { timeout: 230000 })).data,
  activityTemplates: async () => (await apiClient.get<Record<'current' | 'light' | 'active', QQActivityBundle>>('/admin/qqbot/activity-templates')).data,
  applyActivityTemplate: async (request: { version: string; template: 'light' | 'active'; budget: number; confirmed: boolean }) => (await apiClient.post<QQBotConfig>('/admin/qqbot/activity-templates', request)).data,

 groupInfo: async (appID: string, groupID: string, signal?: AbortSignal) => (await apiClient.get<QQGroupInfo>(`/admin/qqbot/groups/${encodeURIComponent(groupID)}/info`, { params: { app_id: appID }, signal, timeout: 10000 })).data,
 chatPreview: async (params: QQChatPreviewRequest) => (await apiClient.post<QQChatPreviewResult>('/admin/qqbot/chat-preview', params, { timeout: 180000 })).data,
 members: async (params: { app_id?: string; group_id?: string; member_id?: string; search?: string; page?: number; page_size?: number }) => (await apiClient.get<QQMemberPage>('/admin/qqbot/members', { params })).data,
 moderationRecords: async (params: Record<string, string | number>) => (await apiClient.get<{ items: QQModerationRecord[]; total: number; page: number; page_size: number }>('/admin/qqbot/moderation-records', { params })).data,
 transferHistory: async (search: string, page: number) => (await apiClient.get<QQTransferHistory>('/admin/qqbot/transfers', { params: { search, page } })).data,
 activityPreview: async (user_id: number) => (await apiClient.get<QQActivityPreview>('/admin/qqbot/activity-preview', { params: { user_id } })).data,
  saveSection: async (section: string, version: string, config: QQBotConfig) => (await apiClient.put<QQBotConfig>(`/admin/qqbot/config/${section}`, { version, config })).data,
  configHistory: async () => (await apiClient.get<QQConfigChange[]>('/admin/qqbot/config-history')).data,
  overview: async () => (await apiClient.get<QQOverview>('/admin/qqbot/overview')).data,
  jobs: async (params: Record<string, string | number>) => (await apiClient.get<QQJobPage>('/admin/qqbot/jobs', { params })).data,
  diagnose: async (group_id: string, member_id: string) => (await apiClient.get<{ checks: QQDiagnosticCheck[]; jobs: QQBotRuntime['jobs'] }>('/admin/qqbot/diagnosis', { params: { group_id, member_id } })).data,
  commands: async () => (await apiClient.get<QQCommandInfo[]>('/admin/qqbot/commands')).data,
  rankingConfig: async () => versionedGet<QQRankingConfig>('/admin/qqbot/rankings'),
  saveRankings: async (config: QQRankingConfig) => versionedPut('/admin/qqbot/rankings', config),
  rewardCodes: async (search: string, status: string, page: number) => (await apiClient.get<QQRewardCodesPage>('/admin/qqbot/reward-codes', { params: { search, status, page } })).data,
  updateRewardCode: async (id: number, action: 'disable' | 'fulfill', note = '') => (await apiClient.patch(`/admin/qqbot/reward-codes/${id}`, { action, note })).data,
  activityRecords: async (kind: QQActivityKind, search: string, page: number) => (await apiClient.get<QQActivityRecordsPage>(`/admin/qqbot/records/${kind}`, { params: { search, page, page_size: 20 } })).data,
  inviteConfig: async () => versionedGet<QQInviteConfig>('/admin/qqbot/invite'),
  saveInvite: async (config: QQInviteConfig) => versionedPut('/admin/qqbot/invite', config),
  checkinConfig: async () => versionedGet<QQCheckinConfig>('/admin/qqbot/checkin'),
  saveCheckin: async (config: QQCheckinConfig) => versionedPut('/admin/qqbot/checkin', config),
  transferConfig: async () => versionedGet<QQTransferConfig>('/admin/qqbot/transfer'),
  saveTransfer: async (config: QQTransferConfig) => versionedPut('/admin/qqbot/transfer', config),
  lotteryConfig: async () => versionedGet<QQLotteryConfig>('/admin/qqbot/lottery'),
  saveLottery: async (config: QQLotteryConfig) => versionedPut('/admin/qqbot/lottery', config),
  lotteryState: async (userID: number) => (await apiClient.get<QQLotteryState>(`/admin/qqbot/lottery/users/${userID}`)).data,
  lotteryDraws: async (search: string, page: number) => (await apiClient.get<QQLotteryDrawsPage>('/admin/qqbot/lottery/draws', { params: { search, page } })).data,
  config: async () => versionedGet<QQBotConfig>('/admin/qqbot/config'),
  save: async (config: QQBotConfig) => versionedPut('/admin/qqbot/config', config),
  runtime: async () => (await apiClient.get<QQBotRuntime>('/admin/qqbot/runtime')).data,
  deleteDiscoveredGroup: async (groupID: string) => (await apiClient.delete('/admin/qqbot/groups', { data: { group_id: groupID } })).data,
  bindings: async (search: string, page: number) => (await apiClient.get<QQAdminBindingsPage>('/admin/qqbot/bindings', { params: { search, page, page_size: 20 } })).data,
  unbind: async (binding: QQAdminBinding) => (await apiClient.delete(`/admin/qqbot/bindings/${binding.user_id}`, { data: { app_id: binding.app_id, member_id: binding.member_id, created_at: binding.created_at } })).data,
  probe: async (kind: 'qq' | 'chat' | 'image' | 'vision' | 'tools' | 'image_edit') => (await apiClient.post<{message: string}>(`/admin/qqbot/test/${kind}`, {}, { timeout: 230000 })).data,
  binding: async () => (await apiClient.get<QQBindingState>('/user/qqbot/binding')).data,
  createCode: async () => (await apiClient.post<QQBindingRequest>('/user/qqbot/binding-code')).data,
  confirm: async (member_id: string) => (await apiClient.post('/user/qqbot/binding-confirm', { member_id })).data,
  previewEmail: async (token: string) => (await apiClient.post<QQEmailBindingPreview>('/qqbot/email-binding/preview', { token })).data,
  confirmEmail: async (token: string, member_id: string) => (await apiClient.post('/qqbot/email-binding/confirm', { token, member_id })).data,
  previewTransfer: async (token: string) => (await apiClient.post<QQTransferPreview>('/qqbot/transfer/preview', { token })).data,
  confirmTransfer: async (token: string) => (await apiClient.post('/qqbot/transfer/confirm', { token })).data
}
