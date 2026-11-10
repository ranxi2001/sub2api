export interface OpenAIAPIKeyFallbackConfig {
  enabled: boolean
  account_ids: number[]
  metric: 'sla' | 'health_score'
  threshold: number
  window_minutes: number
}

export interface OpenAIAPIKeyFallbackStatus {
  last_checked_at?: string
  last_metric_checked_at?: string
  oauth_account_count: number
  oauth_available_count: number
  metric: 'sla' | 'health_score'
  metric_value?: number | null
  samples: number
  window_minutes: number
  last_trigger_reason?: 'oauth_unavailable' | 'metric_below_threshold'
  last_enabled_at?: string
  last_error_category?: 'accounts_unavailable' | 'metric_unavailable' | 'account_enable_failed'
}

export function defaultOpenAIAPIKeyFallbackConfig(): OpenAIAPIKeyFallbackConfig {
  return { enabled: false, account_ids: [], metric: 'sla', threshold: 95, window_minutes: 5 }
}

export function openaiAPIKeyFallbackPayload(value?: Partial<OpenAIAPIKeyFallbackConfig> | null): OpenAIAPIKeyFallbackConfig {
  return { ...defaultOpenAIAPIKeyFallbackConfig(), ...value, account_ids: [...(value?.account_ids ?? [])] }
}

export function openaiAPIKeyFallbackError(value: OpenAIAPIKeyFallbackConfig): string | null {
  if (!['sla', 'health_score'].includes(value.metric) || !Number.isFinite(value.threshold) || value.threshold < 0 || value.threshold > 100 ||
      !Number.isInteger(value.window_minutes) || value.window_minutes < 1 || value.window_minutes > 1440 ||
      value.account_ids.length > 100 || new Set(value.account_ids).size !== value.account_ids.length ||
      value.account_ids.some(id => !Number.isSafeInteger(id) || id <= 0)) return 'autoConfig.apikeyFallback.invalid'
  if (value.enabled && !value.account_ids.length) return 'autoConfig.apikeyFallback.chooseAccounts'
  return null
}
