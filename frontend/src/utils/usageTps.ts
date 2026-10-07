import type { UsageLog } from '@/types'
import { BILLING_MODE_VIDEO } from './billingMode'

type UsageTpsRow = Partial<
  Pick<UsageLog, 'output_tokens' | 'duration_ms' | 'first_token_ms' | 'image_count' | 'image_output_tokens' | 'billing_mode'>
>

/** Why a record has no TPS; each value maps to `usage.latencyTpsUnavailable.<reason>`. */
export type UsageTpsUnavailableReason = 'media' | 'noOutput' | 'singleToken' | 'noDuration'

/**
 * Explains why TPS cannot be computed for a record, or returns null when it can.
 *
 * A single output token is not a rate, and it is what interrupted Anthropic
 * streams leave behind: message_start carries a placeholder output_tokens of 1.
 * When the stream ends before message_delta (upstream error or client
 * disconnect), the gateway bills the usage observed so far, so the record keeps
 * that 1 while duration_ms covers everything streamed until the cut.
 */
export const usageOutputTpsUnavailableReason = (row: UsageTpsRow | null | undefined): UsageTpsUnavailableReason | null => {
  if ((row?.image_count ?? 0) > 0 || (row?.image_output_tokens ?? 0) > 0 || row?.billing_mode === BILLING_MODE_VIDEO) {
    return 'media'
  }
  const outputTokens = row?.output_tokens ?? 0
  if (!Number.isFinite(outputTokens) || outputTokens <= 0) return 'noOutput'
  if (outputTokens < 2) return 'singleToken'
  const durationMs = row?.duration_ms ?? 0
  if (!Number.isFinite(durationMs) || durationMs <= 0) return 'noDuration'
  return null
}

/**
 * Average output throughput (tokens/s) over the recorded request duration.
 *
 * Use the same window for streaming and non-streaming records. Output tokens
 * can include reasoning generated before the first visible token, and buffered
 * tool output can arrive only at completion. Subtracting first_token_ms would
 * divide the full output count by an unrelated, potentially tiny window.
 *
 * This includes waiting time and is not a measurement of model generation speed.
 * Records rejected by usageOutputTpsUnavailableReason are excluded.
 */
export const usageOutputTps = (row: UsageTpsRow | null | undefined): number | null => {
  if (!row || usageOutputTpsUnavailableReason(row)) return null
  const tps = (row.output_tokens ?? 0) / ((row.duration_ms ?? 0) / 1000)
  return Number.isFinite(tps) ? tps : null
}

/** "30.8 t/s"；100 t/s 及以上取整。不可计算时返回 null，由调用方显示占位符。 */
export const formatUsageOutputTps = (row: UsageTpsRow | null | undefined): string | null => {
  const tps = usageOutputTps(row)
  if (tps == null) return null
  return `${tps >= 100 ? Math.round(tps) : tps.toFixed(1)} t/s`
}
