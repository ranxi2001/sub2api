export interface OAuthDegradationConfig {
 probe_enabled?: boolean | null
 upstream_429_enabled?: boolean | null
 upstream_error_threshold_percent?: number | null
 upstream_429_share_threshold_percent?: number | null
 enabled: boolean
 threshold_percent: number | null
 limit_concurrency?: boolean
 limit_models?: boolean
 limit_groups?: boolean
 target_concurrency: number
 models: string[]
 group_ids?: number[]
}
export function defaultOAuthDegradationConfig(): OAuthDegradationConfig {
 return { probe_enabled: true, upstream_429_enabled: true, upstream_error_threshold_percent: 80, upstream_429_share_threshold_percent: 80, enabled: false, threshold_percent: null, limit_concurrency: true, limit_models: true, limit_groups: false, target_concurrency: 10, models: ['gpt-5.6-luna', 'gpt-5.6-terra', 'gpt-6-luna', 'gpt-6.1-terra'], group_ids: [] }
}
export function oauthDegradationError(c: OAuthDegradationConfig): boolean {
 const limitConcurrency = c.limit_concurrency !== false, limitModels = c.limit_models !== false
 if (c.enabled && ((c.probe_enabled !== false && c.threshold_percent == null) || (c.probe_enabled === false && c.upstream_429_enabled === false) || !limitConcurrency && !limitModels && !c.limit_groups)) return true
 if (c.threshold_percent != null && (!Number.isFinite(c.threshold_percent) || c.threshold_percent < 0 || c.threshold_percent > 100)) return true
 for (const value of [c.upstream_error_threshold_percent ?? 80, c.upstream_429_share_threshold_percent ?? 80]) {
  if (!Number.isFinite(value) || value <= 0 || value > 100) return true
 }
 if (limitConcurrency && (!Number.isInteger(c.target_concurrency) || c.target_concurrency < 1 || c.target_concurrency > 10000)) return true
 if (limitModels && (c.models.length > 500 || new Set(c.models).size !== c.models.length || c.models.some(model => !model || /[\s*]/.test(model) || new TextEncoder().encode(model).length > 256 || [...model].some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) >= 127 && char.charCodeAt(0) <= 159)))) return true
 const groups = c.group_ids ?? []
 return !!c.limit_groups && (!groups.length || groups.length > 100 || groups.some(id => !Number.isSafeInteger(id) || id <= 0) || new Set(groups).size !== groups.length)
}

export function oauthDegradationPayload(c?: OAuthDegradationConfig | null): OAuthDegradationConfig {
 const cfg=c ?? defaultOAuthDegradationConfig()
 return { ...cfg, probe_enabled: cfg.probe_enabled ?? true, upstream_429_enabled: cfg.upstream_429_enabled ?? true, upstream_error_threshold_percent: cfg.upstream_error_threshold_percent ?? 80, upstream_429_share_threshold_percent: cfg.upstream_429_share_threshold_percent ?? 80, limit_concurrency: cfg.limit_concurrency ?? true, limit_models: cfg.limit_models ?? true, limit_groups: cfg.limit_groups ?? false, models: cfg.models ?? [], group_ids: cfg.group_ids ?? [] }
}
