import type { PricingFormEntry, IntervalFormEntry } from '@/components/admin/channel/types'
import { findModelConflict, validateIntervals, validateReasoningEffortMultipliers, validateTimePricing } from '@/components/admin/channel/types'

export const MAX_PRICING_FILE_BYTES = 2 * 1024 * 1024
const FORMAT = 'sub2api-channel-pricing'
const priceFields = ['input_price', 'output_price', 'cache_write_price', 'cache_write_1h_price', 'cache_write_12h_price', 'cache_read_price', 'per_request_price'] as const
const multiplierFields = ['input_multiplier', 'output_multiplier', 'cache_write_multiplier', 'cache_read_multiplier'] as const

type Translate = (key: string, params?: Record<string, unknown>) => string

function invalid(): never {
  throw new Error('admin.channels.pricingTransfer.invalidFile')
}
function object(value: unknown, allowed: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return invalid()
  const record = value as Record<string, unknown>
  if (Object.keys(record).some(key => !allowed.includes(key))) return invalid()
  return record
}
function number(value: unknown, positive = false): number | null {
  if (value == null || value === '') return null
  if (typeof value !== 'number' && typeof value !== 'string') return invalid()
  if (typeof value === 'string' && !value.trim()) return invalid()
  const result = Number(value)
  if (!Number.isFinite(result) || (positive ? result <= 0 : result < 0)) return invalid()
  return result
}
function integer(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) return invalid()
  return value
}

function entry(value: unknown, t: Translate): PricingFormEntry {
  const row = object(value, [...priceFields, 'models', 'billing_mode', 'fast_multiplier', 'flex_multiplier', 'reasoning_effort_multipliers', 'image_input_price', 'image_output_price', 'intervals', 'time_pricing'])
  if (!Array.isArray(row.models) || !row.models.length || row.models.some(m => typeof m !== 'string' || !m.trim())) return invalid()
  if (!['token', 'per_request', 'image', 'video'].includes(String(row.billing_mode))) return invalid()
  if (!Array.isArray(row.intervals)) return invalid()
  const intervals: IntervalFormEntry[] = row.intervals.map(value => {
    const iv = object(value, [...priceFields, ...multiplierFields, 'min_tokens', 'max_tokens', 'tier_label', 'sort_order'])
    if (typeof iv.tier_label !== 'string') return invalid()
    return {
      ...Object.fromEntries(priceFields.map(key => [key, number(iv[key])])),
      ...Object.fromEntries(multiplierFields.map(key => [key, number(iv[key], true)])),
      min_tokens: integer(iv.min_tokens),
      max_tokens: iv.max_tokens === null ? null : integer(iv.max_tokens),
      tier_label: iv.tier_label,
      sort_order: integer(iv.sort_order),
    } as IntervalFormEntry
  })
  let reasoning: Record<string, number> | null = null
  if (row.reasoning_effort_multipliers != null) {
    const values = object(row.reasoning_effort_multipliers, ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
    reasoning = Object.fromEntries(Object.entries(values).filter(([, v]) => v !== '').map(([k, v]) => {
      const multiplier = number(v, true)
      if (multiplier === null) return invalid()
      return [k, multiplier]
    }))
  }
  const time = object(row.time_pricing, ['timezone', 'weekdays_only', 'periods'])
  if (typeof time.timezone !== 'string' || typeof time.weekdays_only !== 'boolean' || !Array.isArray(time.periods)) return invalid()
  const result: PricingFormEntry = {
    ...Object.fromEntries(priceFields.map(key => [key, number(row[key])])),
    models: row.models.map(m => (m as string).trim()),
    billing_mode: row.billing_mode as PricingFormEntry['billing_mode'],
    image_input_price: number(row.image_input_price),
    image_output_price: number(row.image_output_price),
    fast_multiplier: number(row.fast_multiplier, true),
    flex_multiplier: number(row.flex_multiplier, true),
    reasoning_effort_multipliers: reasoning,
    intervals,
    time_pricing: {
      timezone: time.timezone,
      weekdays_only: time.weekdays_only,
      periods: time.periods.map(value => {
        const period = object(value, ['start_time', 'end_time', 'multiplier'])
        if (typeof period.start_time !== 'string' || typeof period.end_time !== 'string') return invalid()
        const multiplier = number(period.multiplier, true)
        if (multiplier === null) return invalid()
        return { start_time: period.start_time, end_time: period.end_time, multiplier }
      }),
    },
  } as PricingFormEntry
  const error = validateIntervals(result.intervals, result.billing_mode, t)
    || validateReasoningEffortMultipliers(result.reasoning_effort_multipliers, t)
    || validateTimePricing(result.time_pricing, t)
  if (error) throw new Error(error)
  return result
}

function validateEntries(value: unknown, t: Translate): PricingFormEntry[] {
  if (!Array.isArray(value) || value.length > 1000) return invalid()
  const entries = value.map(value => entry(value, t))
  const models = entries.flatMap(row => row.models)
  if (models.length > 5000) return invalid()
  const conflict = findModelConflict(models)
  if (conflict) throw new Error(t('admin.channels.modelConflict', { model1: conflict[0], model2: conflict[1] }))
  return entries
}

export function serializeChannelPricing(platform: string, entries: PricingFormEntry[], t: Translate): string {
  const text = JSON.stringify({ format: FORMAT, version: 1, platform, price_unit: 'USD_per_million_tokens', model_pricing: validateEntries(entries, t) }, null, 2)
  if (new Blob([text]).size > MAX_PRICING_FILE_BYTES) return invalid()
  return text
}

export function parseChannelPricing(text: string, platform: string, t: Translate): PricingFormEntry[] {
  if (new Blob([text]).size > MAX_PRICING_FILE_BYTES) return invalid()
  let value: unknown
  try { value = JSON.parse(text.replace(/^\uFEFF/, '')) } catch { return invalid() }
  const file = object(value, ['format', 'version', 'platform', 'price_unit', 'model_pricing'])
  if (file.format !== FORMAT || file.version !== 1 || file.price_unit !== 'USD_per_million_tokens') return invalid()
  if (file.platform !== platform) throw new Error(t('admin.channels.pricingTransfer.platformMismatch', { platform: file.platform }))
  return validateEntries(file.model_pricing, t)
}
