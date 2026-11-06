/** Shared upstream error-rate colors; percentages use the 0–100 scale. */
export function upstreamErrorRateColorClass(
  percent: number | null,
  threshold: number | null | undefined,
): string {
  if (percent != null && threshold != null) {
    if (percent >= threshold) return 'text-red-600 dark:text-red-400'
    if (percent >= threshold * 0.8) return 'text-yellow-600 dark:text-yellow-400'
  }
  return 'text-green-600 dark:text-green-400'
}
