import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import PricingEntryCard from '../PricingEntryCard.vue'
import type { PricingFormEntry } from '../types'
import channelsAPI from '@/api/admin/channels'
import { createPinia, setActivePinia } from 'pinia'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'

vi.mock('@/api/admin/channels', () => ({
  default: { getModelDefaultPricing: vi.fn() },
}))

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

const getModelDefaultPricing = vi.mocked(channelsAPI.getModelDefaultPricing)

function createEntry(models: string[] = []): PricingFormEntry {
  return {
    models,
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    fast_multiplier: null,
    flex_multiplier: null,
    ultrafast_multiplier: null,
    reasoning_effort_multipliers: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: {
      timezone: 'Asia/Shanghai',
      weekdays_only: false,
      periods: [],
    },
  }
}

function addModels(entry: PricingFormEntry, models: string[]) {
  const wrapper = shallowMount(PricingEntryCard, { props: { entry } })
  wrapper.findComponent({ name: 'ModelTagInput' }).vm.$emit('update:models', models)
  return wrapper
}

const emittedEntries = (wrapper: ReturnType<typeof shallowMount>) =>
  (wrapper.emitted('update') ?? []).map(e => e[0] as PricingFormEntry)

describe('PricingEntryCard default price auto-fill', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getModelDefaultPricing.mockResolvedValue({ found: true, input_price: 3e-6, output_price: 15e-6 })
  })

  it('auto-fills the default price when a model is added to an empty entry', async () => {
    const wrapper = addModels(createEntry(), ['claude-opus-4-8'])
    await flushPromises()

    expect(getModelDefaultPricing).toHaveBeenCalledTimes(1)
    expect(getModelDefaultPricing).toHaveBeenCalledWith('claude-opus-4-8')
    expect(emittedEntries(wrapper).at(-1)).toMatchObject({
      models: ['claude-opus-4-8'],
      input_price: 3,
      output_price: 15,
    })
  })

  it('does not overwrite prices when appending a model to an entry that already has one', async () => {
    const entry = createEntry(['claude-opus-4-7'])
    const wrapper = addModels(entry, ['claude-opus-4-7', 'claude-opus-4-8'])
    await flushPromises()

    expect(getModelDefaultPricing).not.toHaveBeenCalled()
    expect(emittedEntries(wrapper).at(-1)).toMatchObject({
      models: ['claude-opus-4-7', 'claude-opus-4-8'],
    })
    for (const emitted of emittedEntries(wrapper)) {
      expect(emitted.input_price).toBeNull()
      expect(emitted.output_price).toBeNull()
      expect(emitted.cache_write_price).toBeNull()
      expect(emitted.cache_read_price).toBeNull()
    }
  })

  it('does not auto-fill a shared price when several models are added to an empty entry at once', async () => {
    const wrapper = addModels(createEntry(), ['claude-opus-4-7', 'claude-opus-4-8'])
    await flushPromises()

    expect(getModelDefaultPricing).not.toHaveBeenCalled()
    expect(emittedEntries(wrapper).at(-1)).toMatchObject({
      models: ['claude-opus-4-7', 'claude-opus-4-8'],
      input_price: null,
      output_price: null,
    })
  })

  it('keeps manual prices untouched when a model is appended', async () => {
    const entry = { ...createEntry(['claude-opus-4-7']), input_price: '1.5' }
    const wrapper = addModels(entry, ['claude-opus-4-7', 'claude-opus-4-8'])
    await flushPromises()

    expect(getModelDefaultPricing).not.toHaveBeenCalled()
    for (const emitted of emittedEntries(wrapper)) {
      expect(emitted.input_price).toBe('1.5')
    }
  })
})

describe('image usage billing notice in group pricing entries', () => {
  beforeEach(() => localStorage.clear())

  it.each(['create', 'edit'])('dismisses both entries from %s and persists A → B → C for the same administrator', async entryToDismiss => {
    const admin = 41
    const oldNotice = NEW_FEATURE_NOTICES.openaiCacheWriteTTL12h
    const imageNotice = NEW_FEATURE_NOTICES.imageUsageBilling
    const futureNotice = 'image-usage-future-feature-C'
    let pinia = createPinia()
    setActivePinia(pinia)
    useNewFeatureNoticeStore().dismiss(admin, oldNotice)
    const card = () => shallowMount(PricingEntryCard, {
      props: { entry: createEntry(), platform: 'openai', hideTokenIntervals: true, noticeUserId: admin },
      global: { plugins: [pinia], stubs: { NewFeatureBadge: false } },
    })
    const badge = (wrapper: ReturnType<typeof card>, id: string) =>
      wrapper.findAllComponents(NewFeatureBadge).find(item => item.props('noticeId') === id)!
    const create = card()
    const edit = card()
    for (const wrapper of [create, edit]) {
      expect(badge(wrapper, oldNotice).find('button').exists()).toBe(false)
      expect(badge(wrapper, imageNotice).find('button').exists()).toBe(true)
    }
    await badge(entryToDismiss === 'create' ? create : edit, imageNotice).get('button').trigger('click')
    for (const wrapper of [create, edit]) {
      expect(badge(wrapper, imageNotice).find('button').exists()).toBe(false)
      wrapper.unmount()
    }
    // New Pinia simulates a refreshed session using this administrator's
    // existing localStorage. No server or current pricing is involved.
    pinia = createPinia()
    setActivePinia(pinia)
    const refreshed = card()
    expect(badge(refreshed, oldNotice).find('button').exists()).toBe(false)
    expect(badge(refreshed, imageNotice).find('button').exists()).toBe(false)
    const future = shallowMount(NewFeatureBadge, {
      props: { noticeId: futureNotice, userId: admin }, global: { plugins: [pinia] },
    })
    expect(future.find('button').exists()).toBe(true)
    const ordinaryUpgrade = card()
    expect(badge(ordinaryUpgrade, oldNotice).find('button').exists()).toBe(false)
    expect(badge(ordinaryUpgrade, imageNotice).find('button').exists()).toBe(false)
    refreshed.unmount()
    ordinaryUpgrade.unmount()
    future.unmount()
  })
})
