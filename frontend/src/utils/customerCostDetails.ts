import type { CustomerCacheCostDetail, CustomerCacheCostKind, CustomerCostDetails, UsageLog } from '@/types'

export type CostComponent = 'input_cost' | 'output_cost' | 'image_input_cost' | 'image_output_cost' | 'cache_creation_cost' | 'cache_read_cost'
const components: CostComponent[] = ['input_cost', 'output_cost', 'image_input_cost', 'image_output_cost', 'cache_creation_cost', 'cache_read_cost']
// Billing commands are canonicalized to eight decimal places; raw display
// quotes retain their precision. This tolerance is only for the final money.
const closeMoney = (a: number, b: number) => Math.abs(a - b) <= 5.00001e-9 + Math.abs(b) * 1e-12
const close = (a: number, b: number) => Math.abs(a - b) <= 1e-10 + Math.abs(b) * 1e-12

export function customerCostSnapshot(row: UsageLog | null): CustomerCostDetails | null {
  const detail = row?.customer_cost_details
  if (!row || !['priority', 'fast', 'ultrafast'].includes(row.service_tier ?? '') || !detail || ![1, 2, 3].includes(detail.version) || (row.service_tier === 'ultrafast' ? detail.free_ultrafast !== true : detail.free_fast !== true)) return null
  if (![...components.map(key => detail[key]), detail.total_cost, detail.actual_cost, detail.multiplier].every(value => Number.isFinite(value) && value >= 0)) return null
  const sum = components.reduce((total, key) => total + detail[key], 0)
  if (!close(sum, detail.total_cost) || !closeMoney(detail.actual_cost, row.actual_cost)) return null
  const base = sum * detail.multiplier
  if (detail.version === 3) {
    const minimum = detail.image_minimum_charge ?? 0
    const adjustment = detail.image_minimum_charge_adjustment ?? 0
    if (!Number.isFinite(minimum) || minimum <= 0 || !Number.isFinite(adjustment) || adjustment < 0 ||
        !closeMoney(Math.max(base, minimum), detail.actual_cost) ||
        !closeMoney(Math.max(minimum - base, 0), adjustment)) return null
  } else if (!close(base, detail.actual_cost)) return null
  if ((detail.version === 2 || (detail.version === 3 && detail.cache_cost_details !== undefined)) && !validCachePrices(row, detail)) return null
  return detail
}

// No current group settings or catalogue prices. A complete token-only
// Priority breakdown with a different charge has no recoverable customer quote.
// Mixed modalities, per-request prices and additive fees can legitimately use
// different rates: retain their recorded display when no snapshot exists.
export function customerCostsUnknown(row: UsageLog | null): boolean {
  if (!row || !['priority', 'fast', 'ultrafast'].includes(row.service_tier ?? '') || customerCostSnapshot(row)) return false
  if (row.customer_cost_details) return true
  if (row.billing_mode && row.billing_mode !== 'token') return false
  if ([row.image_count, row.image_input_tokens, row.image_output_tokens, row.image_input_cost, row.image_output_cost].some(value => (value ?? 0) > 0)) return false
  const sum = components.reduce((total, key) => total + (row[key] ?? 0), 0)
  // Stored component columns have ten decimal places; account for their
  // independent rounding without treating an additive tool fee as token cost.
  if (Math.abs(sum - row.total_cost) > 5e-10 + Math.abs(row.total_cost) * 1e-12) return false
  return !closeMoney(row.total_cost * row.rate_multiplier, row.actual_cost)
}

const cacheKinds: CustomerCacheCostKind[] = ['read', 'read_image', 'write', 'write_5m', 'write_1h', 'write_30m', 'write_12h']
function validCachePrices(row: UsageLog, detail: CustomerCostDetails): boolean {
  const lines = detail.cache_cost_details ?? []
  if (!Array.isArray(lines)) return false
  const seen = new Set<string>()
  let writeTokens = 0, readTokens = 0, writeCost = 0, readCost = 0
  for (const line of lines) {
    if (!line || !cacheKinds.includes(line.kind) || seen.has(line.kind) || !Number.isInteger(line.tokens) || line.tokens <= 0 || !Number.isFinite(line.unit_price) || line.unit_price < 0) return false
    seen.add(line.kind)
    if (line.kind.startsWith('write')) { writeTokens += line.tokens; writeCost += line.tokens * line.unit_price }
    else { readTokens += line.tokens; readCost += line.tokens * line.unit_price }
  }
  return writeTokens === (row.cache_creation_tokens ?? 0) && readTokens === (row.cache_read_tokens ?? 0) && close(writeCost, detail.cache_creation_cost) && close(readCost, detail.cache_read_cost)
}

export function customerCachePrices(row: UsageLog | null): Array<Omit<CustomerCacheCostDetail, 'unit_price'> & { unit_price: number | null }> {
  const detail = customerCostSnapshot(row)
  if (!row || (!detail && !customerCostsUnknown(row))) return []
  if (detail?.version === 2 || (detail?.version === 3 && detail.cache_cost_details !== undefined)) return detail.cache_cost_details ?? []
  // Version 1 saved aggregates only. TTL counts can label unknown prices, but
  // cannot reconstruct a price. Do not consult today's catalogue or group.
  const lines: Array<Omit<CustomerCacheCostDetail, 'unit_price'> & { unit_price: number | null }> = []
  const add = (kind: CustomerCacheCostKind, tokens: number | undefined) => { if ((tokens ?? 0) > 0) lines.push({ kind, tokens: tokens!, unit_price: null }) }
  if ((row.cache_creation_30m_tokens ?? 0) + (row.cache_creation_12h_tokens ?? 0) === row.cache_creation_tokens) {
    add('write_30m', row.cache_creation_30m_tokens); add('write_12h', row.cache_creation_12h_tokens)
  } else if ((row.cache_creation_5m_tokens ?? 0) + (row.cache_creation_1h_tokens ?? 0) === row.cache_creation_tokens) {
    add('write_5m', row.cache_creation_5m_tokens); add('write_1h', row.cache_creation_1h_tokens)
  } else { add('write', row.cache_creation_tokens) }
  add('read', row.cache_read_tokens)
  return lines
}
