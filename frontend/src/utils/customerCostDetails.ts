import type { CustomerCostDetails, UsageLog } from '@/types'

export type CostComponent = 'input_cost' | 'output_cost' | 'image_input_cost' | 'image_output_cost' | 'cache_creation_cost' | 'cache_read_cost'
const components: CostComponent[] = ['input_cost', 'output_cost', 'image_input_cost', 'image_output_cost', 'cache_creation_cost', 'cache_read_cost']
// Billing commands are canonicalized to eight decimal places; raw display
// quotes retain their precision. This tolerance is only for the final money.
const closeMoney = (a: number, b: number) => Math.abs(a - b) <= 5.00001e-9 + Math.abs(b) * 1e-12
const close = (a: number, b: number) => Math.abs(a - b) <= 1e-10 + Math.abs(b) * 1e-12

export function customerCostSnapshot(row: UsageLog | null): CustomerCostDetails | null {
  const detail = row?.customer_cost_details
  if (!row || !['priority', 'fast'].includes(row.service_tier ?? '') || !detail || detail.version !== 1 || detail.free_fast !== true) return null
  if (![...components.map(key => detail[key]), detail.total_cost, detail.actual_cost, detail.multiplier].every(value => Number.isFinite(value) && value >= 0)) return null
  const sum = components.reduce((total, key) => total + detail[key], 0)
  if (!close(sum, detail.total_cost) || !close(sum * detail.multiplier, detail.actual_cost) || !closeMoney(detail.actual_cost, row.actual_cost)) return null
  return detail
}

// No current group settings or catalogue prices. A complete token-only
// Priority breakdown with a different charge has no recoverable customer quote.
// Mixed modalities, per-request prices and additive fees can legitimately use
// different rates: retain their recorded display when no snapshot exists.
export function customerCostsUnknown(row: UsageLog | null): boolean {
  if (!row || row.service_tier !== 'priority' || customerCostSnapshot(row)) return false
  if (row.customer_cost_details) return true
  if (row.billing_mode && row.billing_mode !== 'token') return false
  if ([row.image_count, row.image_input_tokens, row.image_output_tokens, row.image_input_cost, row.image_output_cost].some(value => (value ?? 0) > 0)) return false
  const sum = components.reduce((total, key) => total + (row[key] ?? 0), 0)
  // Stored component columns have ten decimal places; account for their
  // independent rounding without treating an additive tool fee as token cost.
  if (Math.abs(sum - row.total_cost) > 5e-10 + Math.abs(row.total_cost) * 1e-12) return false
  return !closeMoney(row.total_cost * row.rate_multiplier, row.actual_cost)
}
