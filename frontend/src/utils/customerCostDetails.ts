import type { CustomerCostDetails, UsageLog } from '@/types'

export type CostComponent = 'input_cost' | 'output_cost' | 'image_input_cost' | 'image_output_cost' | 'cache_creation_cost' | 'cache_read_cost'
const components: CostComponent[] = ['input_cost', 'output_cost', 'image_input_cost', 'image_output_cost', 'cache_creation_cost', 'cache_read_cost']
const close = (a: number, b: number) => Math.abs(a - b) <= 1e-10 + Math.abs(b) * 1e-12

export function customerCostSnapshot(row: UsageLog | null): CustomerCostDetails | null {
  const detail = row?.customer_cost_details
  if (!row || row.service_tier !== 'priority' || !detail || detail.version !== 1 || detail.free_fast !== true) return null
  if (![...components.map(key => detail[key]), detail.total_cost, detail.actual_cost, detail.multiplier].every(value => Number.isFinite(value) && value >= 0)) return null
  const sum = components.reduce((total, key) => total + detail[key], 0)
  if (!close(sum, detail.total_cost) || !close(sum * detail.multiplier, detail.actual_cost) || !close(detail.actual_cost, row.actual_cost)) return null
  return detail
}

// No current group settings or catalogue prices: old divergent Priority rows
// cannot tell us which components the customer actually paid for.
export function customerCostsUnknown(row: UsageLog | null): boolean {
  return !!row && row.service_tier === 'priority' && !customerCostSnapshot(row) &&
    (!!row.customer_cost_details || !close(row.total_cost * row.rate_multiplier, row.actual_cost))
}
