export interface PerformanceBounds { min: number | null; max: number | null }
export const DEFAULT_PERFORMANCE_WINDOW_MINUTES = 60

// Display the window used to calculate this response, including data returned
// before a global setting change, rather than guessing from local settings.
export function performanceWindowMinutes(metrics?: Partial<AccountPerformance>): number {
 const minutes = (Date.parse(metrics?.window_end ?? '') - Date.parse(metrics?.window_start ?? '')) / 60_000
 return Number.isInteger(minutes) && minutes > 0 ? minutes : DEFAULT_PERFORMANCE_WINDOW_MINUTES
}

export interface PerformanceConfig {
 enabled: boolean
 tps: PerformanceBounds
 degradation: PerformanceBounds
 ttft: PerformanceBounds
 upstream_error: PerformanceBounds
}
export interface UpstreamHTTPErrorEvidence {
 rate_limited?: number
 percent: number | null
 failures: number
 total: number
 source: 'process_local_http_responses'
 observed_since: string
 window_complete: boolean
}
export interface AccountPerformance {
 upstream_error?: UpstreamHTTPErrorEvidence
 account_id: number
 avg_cache_percent?: number | null
 cache_samples?: number
 avg_tps: number | null
 degradation_percent: number | null
 avg_ttft_ms: number | null
 tps_samples: number
 ttft_samples: number
 quality_samples: number
 config: PerformanceConfig
 revision: number
 paused: boolean
 pause_generation: number
 paused_at: string | null
 reason: string
 window_start: string
 window_end: string
}
export interface PerformanceEvent {
 id: number
 action: 'configure' | 'pause' | 'release'
 actor_id: number | null
 revision: number
 pause_generation: number
 created_at: string
 evidence: unknown
}
export const emptyPerformanceConfig = (): PerformanceConfig => ({
 enabled: false, tps: {min:null,max:null}, degradation: {min:null,max:null}, ttft: {min:null,max:null}, upstream_error: {min:null,max:null}
})
