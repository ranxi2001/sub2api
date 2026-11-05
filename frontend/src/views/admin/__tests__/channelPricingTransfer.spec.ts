import { describe, expect, it } from 'vitest'
import type { PricingFormEntry } from '@/components/admin/channel/types'
import { createDefaultTimePricingForm, formIntervalsToAPI, mTokToPerToken } from '@/components/admin/channel/types'
import { MAX_PRICING_FILE_BYTES, parseChannelPricing, serializeChannelPricing } from '../channelPricingTransfer'

const t = (key: string) => key
const pricing = (): PricingFormEntry => ({
  models: ['example-model'], billing_mode: 'token',
  input_price: 2, output_price: 8, cache_write_price: 2.5, cache_write_1h_price: 4,
  cache_read_price: 0.2, image_input_price: 5, image_output_price: 10, per_request_price: 0.04,
  fast_multiplier: 2, flex_multiplier: 0.5, reasoning_effort_multipliers: { high: 1.5 },
  intervals: [{ min_tokens: 1000, max_tokens: null, tier_label: 'long', input_price: 4,
    output_price: 16, cache_write_price: 5, cache_write_1h_price: 8, cache_read_price: 0.4,
    input_multiplier: 2, output_multiplier: 1.5, cache_write_multiplier: null,
    cache_read_multiplier: null, per_request_price: 0.1, sort_order: 0 }],
  time_pricing: { timezone: 'Asia/Shanghai', weekdays_only: true,
    periods: [{ start_time: '09:00:00', end_time: '18:00:00', multiplier: 1.25 }] },
})
const file = () => JSON.parse(serializeChannelPricing('openai', [pricing()], t))

describe('channel pricing transfer', () => {
  it.each(['token', 'image', 'video', 'per_request'] as const)('round trips %s pricing without unit drift or losing optional rules', mode => {
    const original = { ...pricing(), billing_mode: mode }
    const [imported] = parseChannelPricing(serializeChannelPricing('openai', [original], t), 'openai', t)
    expect(imported).toEqual(original)
    expect(mTokToPerToken(imported.input_price)).toBe(0.000002)
    expect(formIntervalsToAPI(imported.intervals)[0].input_price).toBe(0.000004)
  })
  it('preserves zero and null and normalizes editable numeric strings', () => {
    const original = { ...pricing(), input_price: '0', output_price: null, fast_multiplier: '', time_pricing: createDefaultTimePricingForm() }
    const [imported] = parseChannelPricing(serializeChannelPricing('openai', [original], t), 'openai', t)
    expect(imported.input_price).toBe(0)
    expect(imported.output_price).toBeNull()
    expect(imported.fast_multiplier).toBeNull()
  })
  it('supports empty exports and UTF-8 BOM', () => {
    expect(parseChannelPricing('\uFEFF' + serializeChannelPricing('openai', [], t), 'openai', t)).toEqual([])
  })
  it.each([
    (f: ReturnType<typeof file>) => { f.version = 2 },
    (f: ReturnType<typeof file>) => { f.price_unit = 'USD_per_token' },
    (f: ReturnType<typeof file>) => { f.group_ids = [99] },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].input_price = -1 },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].input_price = 'Infinity' },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].input_price = true },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].fast_multiplier = 0 },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].models = [] },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].models = ['example-*', 'example-model'] },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].billing_mode = 'unknown' },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].intervals[0].max_tokens = 10 },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].time_pricing.periods[0].multiplier = 0 },
    (f: ReturnType<typeof file>) => { f.model_pricing[0].reasoning_effort_multipliers = { invalid: 1 } },
  ])('rejects invalid files before form mutation (%#)', mutate => {
    const value = file()
    mutate(value)
    expect(() => parseChannelPricing(JSON.stringify(value), 'openai', t)).toThrow()
  })
  it('rejects cross-platform files, malformed JSON and oversized input', () => {
    expect(() => parseChannelPricing(JSON.stringify(file()), 'anthropic', t)).toThrow('platformMismatch')
    expect(() => parseChannelPricing('{broken', 'openai', t)).toThrow()
    expect(() => parseChannelPricing(' '.repeat(MAX_PRICING_FILE_BYTES + 1), 'openai', t)).toThrow()
  })
  it('rejects rule/model limits before quadratic conflict checks', () => {
    const value = file()
    value.model_pricing = Array.from({ length: 1001 }, () => pricing())
    expect(() => parseChannelPricing(JSON.stringify(value), 'openai', t)).toThrow()
    value.model_pricing = [{ ...pricing(), models: Array.from({ length: 5001 }, (_, i) => `model-${i}`) }]
    expect(() => parseChannelPricing(JSON.stringify(value), 'openai', t)).toThrow()
  })
})
