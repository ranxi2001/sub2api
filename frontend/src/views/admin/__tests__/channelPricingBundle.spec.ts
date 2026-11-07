import { describe, expect, it } from 'vitest'
import type { ChannelModelPricing } from '@/api/admin/channels'
import type { PricingFormEntry } from '@/components/admin/channel/types'
import { formIntervalsToAPI, mTokToPerToken } from '@/components/admin/channel/types'
import type { PricingBundle, PricingPlatform } from '../channelPricingBundle'
import { channelPricingToPlatforms, parsePricingBundle, parsePricingCollection, serializePricingBundle, serializePricingCollection } from '../channelPricingBundle'
import { MAX_PRICING_FILE_BYTES, serializeChannelPricing } from '../channelPricingTransfer'

const t = (key: string) => key
const pricing = (): PricingFormEntry => ({
  models: ['shared-model'], billing_mode: 'token',
  input_price: 2, output_price: 8, cache_write_price: 0, cache_write_1h_price: null,
  cache_read_price: 0.2, image_input_price: 5, image_output_price: 10, per_request_price: 0.04,
  fast_multiplier: 2, flex_multiplier: 0.5, reasoning_effort_multipliers: { high: 1.5 },
  intervals: [{ min_tokens: 1000, max_tokens: null, tier_label: 'long', input_price: 4,
    output_price: 16, cache_write_price: 0, cache_write_1h_price: null, cache_read_price: 0.4,
    input_multiplier: 2, output_multiplier: 1.5, cache_write_multiplier: null,
    cache_read_multiplier: null, per_request_price: 0.1, sort_order: 0 }],
  time_pricing: { timezone: 'Asia/Shanghai', weekdays_only: true,
    periods: [{ start_time: '09:00:00', end_time: '18:00:00', multiplier: 1.25 }] },
})
const platforms = (): PricingPlatform[] => [
  { platform: 'openai', entries: [pricing()] },
  { platform: 'anthropic', entries: [pricing()] },
]
const file = () => JSON.parse(serializePricingBundle('示例渠道', platforms(), t))
const bundles = (): PricingBundle[] => [
  { name: '渠道 A', platforms: platforms() },
  { name: '渠道 B', platforms: [{ platform: 'openai', entries: [pricing()] }] },
]
const collection = () => JSON.parse(serializePricingCollection(bundles(), t))
const apiPricing = (platform: string): ChannelModelPricing => ({
  id: 42, platform, models: ['shared-model'], billing_mode: 'token',
  input_price: 0.000002, output_price: 0.000008, cache_write_price: 0,
  cache_write_1h_price: null, cache_read_price: 0.0000002,
  image_input_price: 0.000005, image_output_price: 0.00001, per_request_price: 0.04,
  fast_multiplier: 2, flex_multiplier: 0.5, reasoning_effort_multipliers: { high: 1.5 },
  intervals: [{ ...formIntervalsToAPI(pricing().intervals)[0], id: 43 }],
  time_pricing: { timezone: 'Asia/Shanghai', weekdays_only: true,
    periods: [{ start_time: '09:00', end_time: '18:00', multiplier: 1.25 }] },
})

describe('channel pricing collection', () => {
  it('round trips multiple channels while preserving platform boundaries and duplicate source names', () => {
    const original = bundles()
    original[1].name = original[0].name
    const snapshot = structuredClone(original)
    expect(parsePricingCollection('\uFEFF' + serializePricingCollection(original, t), t)).toEqual(original)
    expect(original).toEqual(snapshot)
    expect(parsePricingCollection(serializePricingCollection([{ name: '空渠道', platforms: [] }], t), t)).toEqual([
      { name: '空渠道', platforms: [] },
    ])
  })

  it('accepts a bundle or legacy single-platform export as a one-channel collection', () => {
    expect(parsePricingCollection(serializePricingBundle('单渠道', platforms(), t), t)).toEqual([
      { name: '单渠道', platforms: platforms() },
    ])
    expect(parsePricingCollection(serializeChannelPricing('openai', [pricing()], t), t)).toEqual([
      { name: '', platforms: [{ platform: 'openai', entries: [pricing()] }] },
    ])
  })

  it.each([
    (f: ReturnType<typeof collection>) => { f.version = 2 },
    (f: ReturnType<typeof collection>) => { f.price_unit = 'USD_per_token' },
    (f: ReturnType<typeof collection>) => { f.extra = true },
    (f: ReturnType<typeof collection>) => { f.channels = [] },
    (f: ReturnType<typeof collection>) => { f.channels = null },
    (f: ReturnType<typeof collection>) => { f.channels[1].extra = true },
    (f: ReturnType<typeof collection>) => { f.channels[1].platforms[0].platform = 'unknown' },
    (f: ReturnType<typeof collection>) => { f.channels[1].platforms[0].model_pricing[0].input_price = -1 },
  ])('rejects invalid collections without accepting only the earlier channels (%#)', mutate => {
    const value = collection()
    mutate(value)
    expect(() => parsePricingCollection(JSON.stringify(value), t)).toThrow()
  })

  it('allows at most 100 channels and rejects empty exports', () => {
    const channels = Array.from({ length: 100 }, (_, i) => ({ name: `channel-${i}`, platforms: [] }))
    expect(parsePricingCollection(serializePricingCollection(channels, t), t)).toHaveLength(100)
    expect(() => serializePricingCollection([], t)).toThrow()
    expect(() => serializePricingCollection([...channels, channels[0]], t)).toThrow()
    const value = collection()
    value.channels = Array.from({ length: 101 }, () => file())
    expect(() => parsePricingCollection(JSON.stringify(value), t)).toThrow()
  })

  it('enforces global rule and model limits across separately valid channels', () => {
    const ruleBundles: PricingBundle[] = [501, 500].map(count => ({
      name: 'Rules', platforms: [{ platform: 'openai', entries: Array.from({ length: count }, (_, i) => ({ ...pricing(), models: [`model-${i}`] })) }],
    }))
    const ruleFile = collection()
    ruleFile.channels = ruleBundles.map(bundle => JSON.parse(serializePricingBundle(bundle.name, bundle.platforms, t)))
    expect(() => parsePricingCollection(JSON.stringify(ruleFile), t)).toThrow()
    expect(() => serializePricingCollection(ruleBundles, t)).toThrow()
    const modelBundles: PricingBundle[] = [2501, 2500].map(count => ({
      name: 'Models', platforms: [{ platform: 'openai', entries: [{ ...pricing(), models: Array.from({ length: count }, (_, i) => `model-${i}`) }] }],
    }))
    const modelFile = collection()
    modelFile.channels = modelBundles.map(bundle => JSON.parse(serializePricingBundle(bundle.name, bundle.platforms, t)))
    expect(() => parsePricingCollection(JSON.stringify(modelFile), t)).toThrow()
    expect(() => serializePricingCollection(modelBundles, t)).toThrow()
  })

  it('enforces the byte limit across the whole collection and leaves invalid source forms untouched', () => {
    const original = bundles()
    original[1].platforms[0].entries[0].input_price = -1
    const snapshot = structuredClone(original)
    expect(() => serializePricingCollection(original, t)).toThrow()
    expect(original).toEqual(snapshot)
    expect(() => parsePricingCollection(' '.repeat(MAX_PRICING_FILE_BYTES + 1), t)).toThrow()
    const name = '价'.repeat(MAX_PRICING_FILE_BYTES / 4)
    expect(() => serializePricingCollection([{ name, platforms: [] }, { name, platforms: [] }], t)).toThrow()
  })
})

describe('channel pricing bundle', () => {
  it('round trips multiple platforms with the same model name and all pricing rules', () => {
    const original = platforms()
    expect(parsePricingBundle(serializePricingBundle('示例渠道', original, t), t)).toEqual({
      name: '示例渠道', platforms: original,
    })
  })

  it.each(['token', 'image', 'video', 'per_request'] as const)('converts API %s pricing without changing units or sharing mutable state', billing_mode => {
    const original = [{ ...apiPricing('openai'), billing_mode }, apiPricing('anthropic')]
    const snapshot = structuredClone(original)
    const converted = channelPricingToPlatforms(original)
    const entries = converted[0].entries
    expect(entries[0]).toMatchObject({
      ...pricing(), billing_mode,
      time_pricing: { timezone: 'Asia/Shanghai', weekdays_only: true,
        periods: [{ start_time: '09:00:00', end_time: '18:00:00', multiplier: '1.25' }] },
    })
    expect(entries[0]).not.toHaveProperty('id')
    expect(entries[0].intervals[0]).not.toHaveProperty('id')
    const imported = parsePricingBundle(serializePricingBundle('Source', converted, t), t).platforms[0].entries[0]
    expect(mTokToPerToken(imported.input_price)).toBe(original[0].input_price)
    expect(mTokToPerToken(imported.image_output_price)).toBe(original[0].image_output_price)
    expect(formIntervalsToAPI(imported.intervals)[0].input_price).toBe(original[0].intervals[0].input_price)
    expect(imported.per_request_price).toBe(0.04)
    expect(imported.cache_write_price).toBe(0)
    expect(imported.cache_write_1h_price).toBeNull()
    entries[0].models.push('other-model')
    entries[0].intervals[0].input_price = 99
    entries[0].reasoning_effort_multipliers!.high = 99
    entries[0].time_pricing.periods[0].multiplier = 99
    expect(original).toEqual(snapshot)
  })

  it('groups nonadjacent API entries by platform and supports empty channels', () => {
    const rows = [apiPricing('openai'), apiPricing('anthropic'), { ...apiPricing('openai'), models: ['second-model'] }]
    const converted = channelPricingToPlatforms(rows)
    expect(converted.map(section => [section.platform, section.entries.length])).toEqual([['openai', 2], ['anthropic', 1]])
    expect(channelPricingToPlatforms([])).toEqual([])
    expect(parsePricingBundle('\uFEFF' + serializePricingBundle('空渠道', [], t), t)).toEqual({ name: '空渠道', platforms: [] })
  })

  it.each([
    ['default token prices', (row: ChannelModelPricing) => { row.input_price = 0.000001234567890123 }],
    ['tier token prices', (row: ChannelModelPricing) => { row.intervals[0].cache_write_price = 0.000001234567890123 }],
    ['image token prices', (row: ChannelModelPricing) => { row.image_output_price = 0.000001234567890123 }],
    ['time multipliers', (row: ChannelModelPricing) => { row.time_pricing!.periods[0].multiplier = 1.234 }],
  ] as const)('rejects lossy conversion of %s without changing the API source', (_label, mutate) => {
    const original = [apiPricing('anthropic'), apiPricing('openai')]
    mutate(original[1])
    const snapshot = structuredClone(original)
    expect(() => channelPricingToPlatforms(original)).toThrow('admin.channels.pricingTransfer.invalidFile')
    expect(original).toEqual(snapshot)
  })

  it('supports old single-platform files without inventing a source channel name', () => {
    const original = pricing()
    expect(parsePricingBundle('\uFEFF' + serializeChannelPricing('openai', [original], t), t)).toEqual({
      name: '', platforms: [{ platform: 'openai', entries: [original] }],
    })
    expect(parsePricingBundle(serializeChannelPricing('anthropic', [], t), t).platforms[0].entries).toEqual([])
  })

  it('normalizes numeric input while keeping the source form unchanged', () => {
    const original = platforms()
    original[0].entries[0].input_price = '0'
    original[0].entries[0].output_price = null
    const snapshot = structuredClone(original)
    const imported = parsePricingBundle(serializePricingBundle('Source', original, t), t)
    expect(imported.platforms[0].entries[0].input_price).toBe(0)
    expect(imported.platforms[0].entries[0].output_price).toBeNull()
    expect(original).toEqual(snapshot)
    imported.platforms[0].entries[0].models.push('new-model')
    expect(original).toEqual(snapshot)
  })

  it.each([
    (f: ReturnType<typeof file>) => { f.format = 'unknown' },
    (f: ReturnType<typeof file>) => { f.version = 2 },
    (f: ReturnType<typeof file>) => { f.name = 42 },
    (f: ReturnType<typeof file>) => { f.price_unit = 'USD_per_token' },
    (f: ReturnType<typeof file>) => { f.group_ids = [99] },
    (f: ReturnType<typeof file>) => { f.platforms = null },
    (f: ReturnType<typeof file>) => { f.platforms[0].platform = 'unknown' },
    (f: ReturnType<typeof file>) => { f.platforms[1].platform = 'openai' },
    (f: ReturnType<typeof file>) => { f.platforms[0].model_mapping = {} },
    (f: ReturnType<typeof file>) => { f.platforms[0].model_pricing[0].id = 42 },
    (f: ReturnType<typeof file>) => { f.platforms[1].model_pricing[0].input_price = -1 },
    (f: ReturnType<typeof file>) => { f.platforms[1].model_pricing[0].models = ['shared-*', 'shared-model'] },
    (f: ReturnType<typeof file>) => { f.platforms[1].model_pricing[0].intervals[0].max_tokens = 10 },
    (f: ReturnType<typeof file>) => { f.platforms[1].model_pricing[0].time_pricing.periods[0].multiplier = 0 },
  ])('rejects invalid bundles including errors in later platforms (%#)', mutate => {
    const value = file()
    mutate(value)
    expect(() => parsePricingBundle(JSON.stringify(value), t)).toThrow()
  })

  it('rejects malformed JSON, unknown legacy/API platforms and duplicate serialized platforms', () => {
    expect(() => parsePricingBundle('{broken', t)).toThrow()
    expect(() => parsePricingBundle(serializeChannelPricing('unknown', [], t), t)).toThrow()
    expect(() => channelPricingToPlatforms([apiPricing('unknown')])).toThrow()
    expect(() => serializePricingBundle('Source', [platforms()[0], platforms()[0]], t)).toThrow()
    const invalid = platforms()
    invalid[1].entries[0].fast_multiplier = 0
    const snapshot = structuredClone(invalid)
    expect(() => serializePricingBundle('Source', invalid, t)).toThrow()
    expect(invalid).toEqual(snapshot)
  })

  it('enforces the UTF-8 byte limit for input and the entire serialized bundle', () => {
    expect(() => parsePricingBundle(' '.repeat(MAX_PRICING_FILE_BYTES + 1), t)).toThrow()
    expect(() => serializePricingBundle('价'.repeat(MAX_PRICING_FILE_BYTES / 2), [], t)).toThrow()
  })

  it('enforces total rule and model limits across platforms before per-platform validation', () => {
    const rules = file()
    rules.platforms[0].model_pricing = Array.from({ length: 501 }, (_, i) => ({ ...pricing(), models: [`model-${i}`] }))
    rules.platforms[1].model_pricing = Array.from({ length: 500 }, (_, i) => ({ ...pricing(), models: [`model-${i}`] }))
    expect(() => parsePricingBundle(JSON.stringify(rules), t)).toThrow()
    expect(() => serializePricingBundle('Source', rules.platforms.map((section: { platform: PricingPlatform['platform']; model_pricing: PricingFormEntry[] }) => ({ platform: section.platform, entries: section.model_pricing })), t)).toThrow()
    const models = platforms()
    models[0].entries[0].models = Array.from({ length: 2501 }, (_, i) => `model-${i}`)
    models[1].entries[0].models = Array.from({ length: 2500 }, (_, i) => `model-${i}`)
    expect(() => serializePricingBundle('Source', models, t)).toThrow()
    const modelFile = file()
    modelFile.platforms[0].model_pricing[0].models = models[0].entries[0].models
    modelFile.platforms[1].model_pricing[0].models = models[1].entries[0].models
    expect(() => parsePricingBundle(JSON.stringify(modelFile), t)).toThrow()
  })
})
