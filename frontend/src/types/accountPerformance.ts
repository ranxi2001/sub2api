export interface PerformanceBounds { min: number | null; max: number | null }
export interface PerformanceConfig {
 enabled: boolean
 tps: PerformanceBounds
 degradation: PerformanceBounds
 ttft: PerformanceBounds
}
export interface AccountPerformance {
 account_id: number
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
 enabled: false, tps: {min:null,max:null}, degradation: {min:null,max:null}, ttft: {min:null,max:null}
})
