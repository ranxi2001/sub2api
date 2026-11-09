export interface OAuthDegradationConfig {
 enabled: boolean
 threshold_percent: number | null
 target_concurrency: number
 models: string[]
}
export function defaultOAuthDegradationConfig(): OAuthDegradationConfig {
 return { enabled: false, threshold_percent: null, target_concurrency: 10, models: ['gpt-5.6-terra', 'gpt-6.1-terra', 'gpt-6-luna'] }
}
export function oauthDegradationError(c: OAuthDegradationConfig): boolean {
 return c.enabled && c.threshold_percent == null || c.threshold_percent != null && (!Number.isFinite(c.threshold_percent) || c.threshold_percent < 0 || c.threshold_percent > 100) || !Number.isInteger(c.target_concurrency) || c.target_concurrency < 1 || c.target_concurrency > 10000
}
