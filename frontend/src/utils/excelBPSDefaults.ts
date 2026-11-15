import { DEFAULT_EXCEL_BPS_MODELS } from '@/constants/account'
import { DEFAULT_BPS_RECOVERY_INTERVAL_MINUTES, isValidBPSRecoveryInterval } from './excelBPSRecovery'

export interface ExcelBPSDefaults {
  ws_sse_acceleration: boolean
  auto_enable_on_degradation: boolean
  all_models: boolean
  models: string[]
  omit_unsupported_tools: boolean
  ignore_encrypted_content: boolean
  web_search: boolean
  web_search_auto: boolean
  auto_disable_on_403: boolean
  auto_recover_on_403: boolean
  recovery_interval_minutes: number
  auto_move_on_403: boolean
  target_group_id: number
  session_proxy: boolean
  proxy_source: 'mihomo' | 'ip_pool'
  cache_creation_as_input: boolean
}

export function defaultExcelBPSDefaults(): ExcelBPSDefaults {
  return {
    ws_sse_acceleration: false, auto_enable_on_degradation: false,
    all_models: false, models: [...DEFAULT_EXCEL_BPS_MODELS],
    omit_unsupported_tools: false, ignore_encrypted_content: true, web_search: false, web_search_auto: false,
    auto_disable_on_403: true, auto_recover_on_403: false,
    recovery_interval_minutes: DEFAULT_BPS_RECOVERY_INTERVAL_MINUTES,
    auto_move_on_403: false, target_group_id: -1, session_proxy: false, proxy_source: 'mihomo',
    cache_creation_as_input: true
  }
}

export type ExcelBPSMode = 'defaults' | 'initial'

// The original BPS form: three model candidates with all optional checkboxes off.
export function initialExcelBPSDefaults(): ExcelBPSDefaults {
  return { ...defaultExcelBPSDefaults(), ignore_encrypted_content: false, auto_disable_on_403: false, cache_creation_as_input: false }
}

export function excelBPSDefaultsError(bps: ExcelBPSDefaults): string {
  if (!bps.all_models && !bps.models.some(model => model.trim())) return 'autoConfig.bps.modelsRequired'
  if (!isValidBPSRecoveryInterval(bps.recovery_interval_minutes)) return 'admin.accounts.openai.excelBPS403RecoveryIntervalInvalid'
  if (bps.auto_move_on_403 && (!Number.isSafeInteger(bps.target_group_id) || bps.target_group_id < 0)) return 'admin.accounts.openai.excelBPS403SelectTarget'
  return ''
}

export function excelBPSDefaultsPayload(bps: ExcelBPSDefaults): ExcelBPSDefaults {
  return { ...bps, web_search_auto: bps.web_search && bps.web_search_auto, models: [...new Set(bps.models.map(model => model.trim()).filter(Boolean))],
    auto_recover_on_403: bps.auto_disable_on_403 && bps.auto_recover_on_403 }
}
