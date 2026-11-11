import type { ChannelModelPricing } from '@/api/admin/channels'
import type { PricingFormEntry } from '@/components/admin/channel/types'
import { apiIntervalsToForm, apiTimePricingToForm, mTokToPerToken, perTokenToMTok } from '@/components/admin/channel/types'
import { listPlatformIds } from '@/constants/platformCatalog'
import type { GroupPlatform } from '@/types'
import { MAX_PRICING_FILE_BYTES, parseChannelPricing, serializeChannelPricing } from './channelPricingTransfer'

export interface PricingPlatform {
  platform: GroupPlatform
  entries: PricingFormEntry[]
}

export interface PricingBundle {
  name: string
  platforms: PricingPlatform[]
}

export interface PricingImport {
  bundles: PricingBundle[]
  isCollection: boolean
}

type Translate = (key: string, params?: Record<string, unknown>) => string

const FORMAT = 'sub2api-channel-pricing-bundle'
const SINGLE_PLATFORM_FORMAT = 'sub2api-channel-pricing'
const COLLECTION_FORMAT = 'sub2api-channel-pricing-collection'
const PRICE_UNIT = 'USD_per_million_tokens'
const TOKEN_PRICE_FIELDS = ['input_price', 'output_price', 'cache_write_price', 'cache_write_1h_price', 'cache_write_12h_price', 'cache_read_price'] as const

interface PricingTotals { rules: number; models: number }
interface PlatformRecord { platform: GroupPlatform; model_pricing: unknown[] }
interface BundleRecord { name: string; platforms: PlatformRecord[] }

function invalid(): never {
  throw new Error('admin.channels.pricingTransfer.invalidFile')
}

function object(value: unknown, allowed: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return invalid()
  const record = value as Record<string, unknown>
  if (Object.keys(record).some(key => !allowed.includes(key))) return invalid()
  return record
}

function platformId(value: unknown): GroupPlatform {
  if (typeof value !== 'string' || !listPlatformIds().includes(value)) return invalid()
  return value as GroupPlatform
}

// Check the entire bundle before running the per-platform conflict validators.
function platformRecords(value: unknown, totals: PricingTotals): PlatformRecord[] {
  if (!Array.isArray(value)) return invalid()
  const seen = new Set<GroupPlatform>()
  return value.map(value => {
    const section = object(value, ['platform', 'model_pricing'])
    const platform = platformId(section.platform)
    if (seen.has(platform) || !Array.isArray(section.model_pricing)) return invalid()
    seen.add(platform)
    totals.rules += section.model_pricing.length
    if (totals.rules > 1000) return invalid()
    for (const row of section.model_pricing) {
      if (!row || typeof row !== 'object' || !Array.isArray(row.models)) return invalid()
      totals.models += row.models.length
      if (totals.models > 5000) return invalid()
    }
    return { platform, model_pricing: section.model_pricing }
  })
}

function bundleRecord(value: unknown, totals: PricingTotals): BundleRecord {
  const file = object(value, ['format', 'version', 'name', 'price_unit', 'platforms'])
  if (file.format !== FORMAT || file.version !== 1 || file.price_unit !== PRICE_UNIT || typeof file.name !== 'string') return invalid()
  return { name: file.name, platforms: platformRecords(file.platforms, totals) }
}

function formBundleRecord(bundle: PricingBundle, totals: PricingTotals): BundleRecord {
  if (!Array.isArray(bundle.platforms)) return invalid()
  return bundleRecord({
    format: FORMAT, version: 1, name: bundle.name, price_unit: PRICE_UNIT,
    platforms: bundle.platforms.map(section => ({ platform: section.platform, model_pricing: section.entries })),
  }, totals)
}

function bundleDocument(bundle: BundleRecord, t: Translate) {
  return {
    format: FORMAT, version: 1, name: bundle.name, price_unit: PRICE_UNIT,
    platforms: bundle.platforms.map(section => ({
      platform: section.platform,
      model_pricing: JSON.parse(serializeChannelPricing(section.platform, section.model_pricing as PricingFormEntry[], t)).model_pricing,
    })),
  }
}

function decodeBundle(bundle: BundleRecord, t: Translate): PricingBundle {
  return {
    name: bundle.name,
    platforms: bundle.platforms.map(section => ({
      platform: section.platform,
      entries: parseChannelPricing(JSON.stringify({
        format: SINGLE_PLATFORM_FORMAT, version: 1, price_unit: PRICE_UNIT, ...section,
      }), section.platform, t),
    })),
  }
}

function parseFile(text: string): unknown {
  if (new Blob([text]).size > MAX_PRICING_FILE_BYTES) return invalid()
  try { return JSON.parse(text.replace(/^\uFEFF/, '')) } catch { return invalid() }
}

function serializeFile(value: unknown): string {
  const text = JSON.stringify(value, null, 2)
  if (new Blob([text]).size > MAX_PRICING_FILE_BYTES) return invalid()
  return text
}

function decodeBundleFile(value: unknown, t: Translate): PricingBundle {
  const file = object(value, ['format', 'version', 'name', 'price_unit', 'platforms', 'platform', 'model_pricing'])
  if (file.format === SINGLE_PLATFORM_FORMAT) {
    const platform = platformId(file.platform)
    return { name: '', platforms: [{ platform, entries: parseChannelPricing(JSON.stringify(file), platform, t) }] }
  }
  return decodeBundle(bundleRecord(file, { rules: 0, models: 0 }), t)
}

function requireLosslessTokenPrice(value: number | null | undefined) {
  if (value != null && (!Number.isFinite(value) || mTokToPerToken(perTokenToMTok(value)) !== value)) return invalid()
}

export function channelPricingToPlatforms(pricing: ChannelModelPricing[]): PricingPlatform[] {
  const sections = new Map<GroupPlatform, PricingFormEntry[]>()
  for (const row of pricing) {
    const platform = platformId(row.platform)
    for (const field of TOKEN_PRICE_FIELDS) requireLosslessTokenPrice(row[field])
    requireLosslessTokenPrice(row.image_input_price)
    requireLosslessTokenPrice(row.image_output_price)
    for (const interval of row.intervals || []) {
      for (const field of TOKEN_PRICE_FIELDS) requireLosslessTokenPrice(interval[field])
    }
    const timePricing = apiTimePricingToForm(row.time_pricing)
    for (const [index, period] of (row.time_pricing?.periods || []).entries()) {
      if (!Number.isFinite(period.multiplier) || Number(timePricing.periods[index].multiplier) !== period.multiplier) return invalid()
    }
    const entries = sections.get(platform) || []
    entries.push({
      models: [...row.models],
      billing_mode: row.billing_mode,
      input_price: perTokenToMTok(row.input_price),
      output_price: perTokenToMTok(row.output_price),
      cache_write_price: perTokenToMTok(row.cache_write_price),
      cache_write_1h_price: perTokenToMTok(row.cache_write_1h_price),
      cache_write_12h_price: perTokenToMTok(row.cache_write_12h_price),
      cache_read_price: perTokenToMTok(row.cache_read_price),
      fast_multiplier: row.fast_multiplier ?? null,
      flex_multiplier: row.flex_multiplier ?? null,
      reasoning_effort_multipliers: row.reasoning_effort_multipliers ? { ...row.reasoning_effort_multipliers } : null,
      image_input_price: perTokenToMTok(row.image_input_price),
      image_output_price: perTokenToMTok(row.image_output_price),
      per_request_price: row.per_request_price,
      intervals: apiIntervalsToForm(row.intervals),
      time_pricing: timePricing,
    })
    sections.set(platform, entries)
  }
  return [...sections].map(([platform, entries]) => ({ platform, entries }))
}

export function serializePricingBundle(name: string, platforms: PricingPlatform[], t: Translate): string {
  const record = formBundleRecord({ name, platforms }, { rules: 0, models: 0 })
  return serializeFile(bundleDocument(record, t))
}

export function parsePricingBundle(text: string, t: Translate): PricingBundle {
  return decodeBundleFile(parseFile(text), t)
}

export function serializePricingCollection(bundles: PricingBundle[], t: Translate): string {
  if (!Array.isArray(bundles) || !bundles.length || bundles.length > 100) return invalid()
  const totals = { rules: 0, models: 0 }
  const records = bundles.map(bundle => formBundleRecord(bundle, totals))
  return serializeFile({
    format: COLLECTION_FORMAT, version: 1, price_unit: PRICE_UNIT,
    channels: records.map(bundle => bundleDocument(bundle, t)),
  })
}

export function parsePricingImport(text: string, t: Translate): PricingImport {
  const value = parseFile(text)
  const file = object(value, ['format', 'version', 'price_unit', 'channels', 'name', 'platforms', 'platform', 'model_pricing'])
  if (file.format !== COLLECTION_FORMAT) return { bundles: [decodeBundleFile(file, t)], isCollection: false }
  object(file, ['format', 'version', 'price_unit', 'channels'])
  if (file.version !== 1 || file.price_unit !== PRICE_UNIT || !Array.isArray(file.channels) || !file.channels.length || file.channels.length > 100) return invalid()
  const totals = { rules: 0, models: 0 }
  const records = file.channels.map(channel => bundleRecord(channel, totals))
  return { bundles: records.map(bundle => decodeBundle(bundle, t)), isCollection: true }
}

export function parsePricingCollection(text: string, t: Translate): PricingBundle[] {
  return parsePricingImport(text, t).bundles
}
