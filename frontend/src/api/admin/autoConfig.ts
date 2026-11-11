import type { OpenAITwoFAAdmissionConfig } from '@/utils/openaiTwoFAAdmission'
import { list as listGroups } from './groups'
import type { AdminGroup } from '@/types'
import type { ScheduledTestPlan } from '@/types'
import { apiClient } from '../client'
import type { ModelBillingConfig } from '@/utils/modelBilling'
import type { ExcelBPSDefaults } from '@/utils/excelBPSDefaults'
import type { OAuthModelMappingRule } from '@/utils/oauthModelMappings'
import type { OpenAIWSMode } from '@/utils/openaiWsMode'
import type { OpenAIAPIKeyFallbackConfig, OpenAIAPIKeyFallbackStatus } from '@/utils/openaiAPIKeyFallback'
export type OAuthInitialQualityRule = Pick<ScheduledTestPlan, 'model_id' | 'cron_expression' | 'enabled' | 'max_results' | 'pelican_config'>
export interface AutoConfig {
 degradation_action?: import('@/utils/oauthDegradation').OAuthDegradationConfig
 openai_two_fa_admission?: OpenAITwoFAAdmissionConfig
 openai_totp_rotate_on_429?: boolean
 openai_disable_scheduling_on_429?: boolean
 openai_disable_scheduling_on_429_7d_threshold_percent?: number
 openai_apikey_fallback?: OpenAIAPIKeyFallbackConfig
 openai_apikey_fallback_status?: OpenAIAPIKeyFallbackStatus
 openai_ws_mode?: OpenAIWSMode
 openai_ws_sse_acceleration?: boolean
 quality_rule?: OAuthInitialQualityRule | null
 model_billing?: ModelBillingConfig
 selected_models?: string[] | null
 model_mappings?: OAuthModelMappingRule[] | null
 excel_bps?: ExcelBPSDefaults
 enabled: boolean
 platform: string
 priority: number
 load_factor: number
 concurrency: number
 group_ids: number[]
 upgrade_enabled: boolean
 upgrade_group_ids: number[]
 successes_per_step: number
 upgrade_step: number
 max_concurrency: number
 cooldown_seconds: number
 cost_multiplier?: number
 revision: string
 runtime_blocked?: boolean
}
export async function getAutoConfig(): Promise<AutoConfig> { return (await apiClient.get('/admin/account-ops/auto-config')).data }
export async function saveAutoConfig(config: AutoConfig): Promise<AutoConfig> { return (await apiClient.put('/admin/account-ops/auto-config', config)).data }

export type AutoConfigEventKind = 'config_saved' | 'initial_applied' | 'concurrency_upgraded' | 'failure_cooldown' | 'degradation_applied'
export interface AutoConfigEvent {
  id: number
  account_id: number
  account_name: string
  platform: string
  kind: AutoConfigEventKind
  created_at: string
  details: {
    degradation?: { trigger_reason?: string; upstream_error?: import('@/types/accountPerformance').UpstreamHTTPErrorEvidence; upstream_error_threshold_percent?: number; upstream_429_share_threshold_percent?: number; revision: string; limit_concurrency?: boolean; limit_models?: boolean; limit_groups?: boolean; models: string[] | null; group_ids?: number[]; removed_group_ids?: number[]; threshold_percent: number; degradation_percent: number; quality_samples: number; window_start: string; window_end: string; concurrency_cap: number }
    model_mapping?: Record<string, string>
    config?: AutoConfig
    priority: number
    load_factor: number
    concurrency: number
    previous_concurrency: number
    group_ids?: number[]
    cooldown_seconds: number
  }
}
export interface AutoConfigEventsPage {
  items: AutoConfigEvent[]
  has_more: boolean
}
export async function getAutoConfigEvents(params: { before?: number; kind?: AutoConfigEventKind | ''; limit?: number } = {}): Promise<AutoConfigEventsPage> {
  return (await apiClient.get('/admin/account-ops/auto-config/events', { params })).data
}

// Collect every page before exposing scope options. A partial list must never
// turn selected IDs into an empty (global) scope.
export async function getModelBillingGroups(): Promise<AdminGroup[]> {
 const groups = new Map<number, AdminGroup>()
 for (let page = 1; ; page++) {
  const batch = await listGroups(page, 100, { status: 'active', sort_by: 'id', sort_order: 'asc' })
  if (batch.page !== page || !Number.isSafeInteger(batch.pages) || batch.pages < 0 ||
      !Number.isSafeInteger(batch.total) || batch.total < 0) throw new Error('Incomplete group list')
  for (const group of batch.items) groups.set(group.id, group)
  if (page >= batch.pages) {
   if (groups.size < batch.total) throw new Error('Incomplete group list')
   return [...groups.values()]
  }
  if (!batch.items.length) throw new Error('Incomplete group list')
 }
}
