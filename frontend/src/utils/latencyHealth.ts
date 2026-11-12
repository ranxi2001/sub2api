/**
 * 请求延迟健康度分档（用于用量明细"延迟"列的纵向健康扫视）。
 *
 * 首 Token（TTFT）：10s 内正常，10-30s 偏慢，30-60s 缓慢，60s 及以上严重。
 * 总耗时：流式请求整体时长天然更长，阈值放宽为 1min / 3min / 5min。
 * 输出速度（TPS）：越高越好，只分三档——20 t/s 及以上正常，10-20 偏慢，10 以下严重。
 */
export type LatencySeverity = 'good' | 'warn' | 'slow' | 'critical'

export const FIRST_TOKEN_THRESHOLDS_MS = {
  warn: 10_000,
  slow: 30_000,
  critical: 60_000,
} as const

export const DURATION_THRESHOLDS_MS = {
  warn: 60_000,
  slow: 180_000,
  critical: 300_000,
} as const

interface Thresholds {
  warn: number
  slow: number
  critical: number
}

const classify = (ms: number, thresholds: Thresholds): LatencySeverity => {
  if (ms >= thresholds.critical) return 'critical'
  if (ms >= thresholds.slow) return 'slow'
  if (ms >= thresholds.warn) return 'warn'
  return 'good'
}

export const firstTokenSeverity = (ms: number): LatencySeverity =>
  classify(ms, FIRST_TOKEN_THRESHOLDS_MS)

export const durationSeverity = (ms: number): LatencySeverity =>
  classify(ms, DURATION_THRESHOLDS_MS)

export const TPS_THRESHOLDS = {
  warn: 20,
  critical: 10,
} as const

export const tpsSeverity = (tps: number): LatencySeverity => {
  if (tps < TPS_THRESHOLDS.critical) return 'critical'
  if (tps < TPS_THRESHOLDS.warn) return 'warn'
  return 'good'
}

/** Cache percentages use the same four visual levels; unknown evidence is handled by the caller. */
export const cacheRateSeverity = (percent: number): LatencySeverity => {
  if (percent >= 80) return 'good'
  if (percent >= 50) return 'warn'
  if (percent >= 20) return 'slow'
  return 'critical'
}

export const LATENCY_TEXT_CLASSES: Record<LatencySeverity, string> = {
  good: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  slow: 'text-orange-600 dark:text-orange-400',
  critical: 'text-red-600 dark:text-red-400',
}

/**
 * 渐变色条上端（首字档）；与 VIA/TO 组合成上中下三段渐变，避免硬切割裂感。
 * 某段缺数据（无首字 / 无 TPS）时由调用方沿用总耗时档，全部同档即为纯色。
 */
export const LATENCY_BAR_FROM_CLASSES: Record<LatencySeverity, string> = {
  good: 'from-emerald-500',
  warn: 'from-amber-400',
  slow: 'from-orange-500',
  critical: 'from-red-500',
}

/** 渐变色条中段（总耗时档）。 */
export const LATENCY_BAR_VIA_CLASSES: Record<LatencySeverity, string> = {
  good: 'via-emerald-500',
  warn: 'via-amber-400',
  slow: 'via-orange-500',
  critical: 'via-red-500',
}

/** 渐变色条下端（TPS 档）。 */
export const LATENCY_BAR_TO_CLASSES: Record<LatencySeverity, string> = {
  good: 'to-emerald-500',
  warn: 'to-amber-400',
  slow: 'to-orange-500',
  critical: 'to-red-500',
}
