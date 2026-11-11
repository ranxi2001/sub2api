import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import PricingEntryCard from '../PricingEntryCard.vue'
import type { PricingFormEntry } from '../types'
import common from '@/i18n/locales/en/common'
import channels from '@/i18n/locales/en/admin/channels'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'

const entry = (): PricingFormEntry => ({
  models: ['gpt-6-fixture'], billing_mode: 'token', input_price: 2, output_price: 8,
  cache_write_price: 2.5, cache_write_1h_price: null, cache_write_12h_price: 5, cache_read_price: 0.2,
  fast_multiplier: null, flex_multiplier: null, reasoning_effort_multipliers: null,
  image_input_price: null, image_output_price: null, per_request_price: null,
  intervals: [{ min_tokens: 0, max_tokens: null, sort_order: 0, tier_label: 'fixture',
    input_price: 2, output_price: 8, cache_write_price: 2.5, cache_write_12h_price: 7,
    cache_read_price: 0.2, input_multiplier: null, output_multiplier: null,
    cache_write_multiplier: null, cache_read_multiplier: null, per_request_price: null }],
  time_pricing: { timezone: 'UTC', weekdays_only: false, periods: [] },
})

describe('OpenAI cache TTL price entries', () => {
  it('preserves explicit zero and shares the new notice across cards and interval fields', async () => {
    localStorage.clear()
    const pinia = createPinia()
    const notices = useNewFeatureNoticeStore(pinia)
    notices.dismiss(41, NEW_FEATURE_NOTICES.channelPricingTransfer)
    const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: { ...common, admin: { channels } } } })
    const card = () => mount(PricingEntryCard, {
      props: { entry: entry(), platform: 'openai', noticeUserId: 41 },
      global: { plugins: [pinia, i18n], stubs: { Icon: true, Select: true, ModelTagInput: true, TimePricingSection: true } },
    })
    const first = card(), second = card()
    await first.get('.cursor-pointer.select-none').trigger('click')
    await second.get('.cursor-pointer.select-none').trigger('click')
    expect(first.findAll('[data-testid="new-feature-badge"]')).toHaveLength(2)
    expect(second.findAll('[data-testid="new-feature-badge"]')).toHaveLength(2)
    await first.get('[data-testid="cache-write-12h-price"]').setValue('0')
    expect((first.emitted('update')!.at(-1)![0] as PricingFormEntry).cache_write_12h_price).toBe('0')
    await first.get('[data-testid="interval-cache-write-12h-price"]').setValue('0')
    expect((first.emitted('update')!.at(-1)![0] as PricingFormEntry).intervals[0].cache_write_12h_price).toBe('0')
    await first.findAll('[data-testid="new-feature-badge"]')[1].trigger('click')
    expect(first.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    expect(second.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    first.unmount(); second.unmount()
    const persisted = useNewFeatureNoticeStore(createPinia())
    persisted.loadUser(41)
    expect(persisted.isDismissed(41, NEW_FEATURE_NOTICES.openaiCacheWriteTTL12h)).toBe(true)
    expect(persisted.isDismissed(41, NEW_FEATURE_NOTICES.channelPricingTransfer)).toBe(true)
    expect(persisted.isDismissed(41, 'future-cache-feature-C')).toBe(false)
  })
})
