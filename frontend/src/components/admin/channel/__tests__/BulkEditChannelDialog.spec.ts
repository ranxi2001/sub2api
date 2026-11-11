import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Channel, ChannelModelPricing } from '@/api/admin/channels'
import BulkEditChannelDialog from '../BulkEditChannelDialog.vue'
import { channelPricingToPlatforms, type PricingBundle } from '@/views/admin/channelPricingBundle'

const { getById, update, showSuccess, showError } = vi.hoisted(() => ({
  getById: vi.fn(),
  update: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({ adminAPI: { channels: { getById, update } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

function price(platform: string, model: string, input = 0.000002): ChannelModelPricing {
  return {
    id: 70,
    platform,
    models: [model],
    billing_mode: 'token',
    input_price: input,
    output_price: null,
    cache_write_price: null,
    cache_write_12h_price: 0.000005,
    cache_read_price: 0,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: null,
  }
}

function channel(id: number, pricing = [price('openai', `example-${id}`)]): Channel {
  return {
    id,
    name: `Example channel ${id}`,
    description: 'Example only',
    status: 'active',
    billing_model_source: 'requested',
    restrict_models: true,
    group_ids: [100 + id],
    model_pricing: pricing,
    model_mapping: { openai: { alias: 'example-model' } },
    apply_pricing_to_account_stats: true,
    account_stats_pricing_rules: [],
    created_at: '2026-11-06T00:00:00Z',
    updated_at: '2026-11-06T00:00:00Z',
  }
}

function bundle(source: Channel): PricingBundle {
  return { name: source.name, platforms: channelPricingToPlatforms(source.model_pricing) }
}

function mountDialog(channels: Channel[], options: { initialPricing?: PricingBundle; initialPricingById?: Record<number, PricingBundle> } = {}) {
  return mount(BulkEditChannelDialog, {
    props: { show: true, channels, ...options },
    global: {
      stubs: {
        BaseDialog: defineComponent({
          props: ['show', 'showCloseButton', 'closeOnEscape'],
          emits: ['close'],
          template: '<section v-if="show"><button v-if="showCloseButton" data-testid="dialog-close" @click="$emit(\'close\')">close</button><slot /><slot name="footer" /></section>',
        }),
        Select: defineComponent({
          props: ['modelValue', 'options'],
          emits: ['update:modelValue'],
          template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select>',
        }),
        PricingEntryCard: defineComponent({
          props: ['entry', 'platform'],
          emits: ['update', 'remove'],
          template: '<div class="pricing-entry" :data-platform="platform"><span>{{ entry.models.join(",") }}</span><input :value="entry.input_price" @input="$emit(\'update\', { ...entry, input_price: $event.target.value })" /><button type="button" class="remove-pricing" @click="$emit(\'remove\')">remove</button></div>',
        }),
      },
    },
  })
}

describe('BulkEditChannelDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    update.mockResolvedValue({})
    getById.mockImplementation(async (id: number) => channel(id))
  })

  it('requires an opted-in change and sends only the checked basic field', async () => {
    const wrapper = mountDialog([channel(1), channel(2)])
    expect(wrapper.get('[data-testid="bulk-channel-submit"]').attributes('disabled')).toBeDefined()
    expect(update).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="bulk-status-enabled"]').setValue(true)
    await wrapper.get('[data-testid="bulk-status"]').setValue('disabled')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update.mock.calls).toEqual([[1, { status: 'disabled' }], [2, { status: 'disabled' }]])
    expect(getById).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toEqual([[[1, 2]]])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('edits a cloned pricing template and preserves fresh prices on other platforms', async () => {
    const original = channel(1, [price('openai', 'example-priced')])
    const before = JSON.stringify(original)
    const freshOther = price('anthropic', 'fresh-other', 0.000007)
    getById.mockResolvedValue(channel(1, [price('openai', 'outdated'), freshOther]))
    const wrapper = mountDialog([original])
    await wrapper.get('[data-testid="bulk-pricing-enabled"]').setValue(true)
    await wrapper.get('.pricing-entry input').setValue('0')
    expect(JSON.stringify(original)).toBe(before)
    expect(update).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(1)
    const payload = update.mock.calls[0][1]
    expect(Object.keys(payload)).toEqual(['model_pricing'])
    expect(payload.model_pricing).toHaveLength(2)
    expect(payload.model_pricing[0]).toMatchObject({ platform: 'anthropic', models: ['fresh-other'], input_price: 0.000007 })
    expect(payload.model_pricing[1]).toMatchObject({ platform: 'openai', models: ['example-priced'], input_price: 0, output_price: null, cache_write_12h_price: 0.000005, cache_read_price: 0 })
    expect(payload.model_pricing.every((entry: ChannelModelPricing) => entry.id === undefined)).toBe(true)
    wrapper.unmount()
  })

  it('applies each matched import to its own target and converts token units', async () => {
    const first = channel(1)
    const second = channel(2)
    const firstFile = bundle(channel(11, [price('openai', 'from-first-file', 0.000003)]))
    const secondFile = bundle(channel(12, [price('openai', 'from-second-file', 0.000009)]))
    const wrapper = mountDialog([first, second], { initialPricingById: { 1: firstFile, 2: secondFile } })
    expect(wrapper.find('[data-testid="bulk-matched-pricing"]').exists()).toBe(true)
    expect(wrapper.find('.pricing-entry').exists()).toBe(false)
    expect(update).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update.mock.calls[0][1].model_pricing[0]).toMatchObject({ models: ['from-first-file'], input_price: 0.000003 })
    expect(update.mock.calls[1][1].model_pricing[0]).toMatchObject({ models: ['from-second-file'], input_price: 0.000009 })
    expect(firstFile.platforms[0].entries[0].input_price).toBe(3)
    expect(secondFile.platforms[0].entries[0].input_price).toBe(9)
    wrapper.unmount()
  })

  it('freezes target IDs and retries only failed channels after partial success', async () => {
    let failSecond = true
    update.mockImplementation(async (id: number) => {
      if (id === 2 && failSecond) throw new Error('Example failure')
      return {}
    })
    const wrapper = mountDialog([channel(1), channel(2)])
    await wrapper.setProps({ channels: [channel(3)] })
    await wrapper.get('[data-testid="bulk-status-enabled"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update.mock.calls.map(call => call[0])).toEqual([1, 2])
    expect(wrapper.emitted('updated')).toEqual([[[1]]])
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.get('[data-testid="bulk-channel-failures"]').text()).toContain('Example channel 2')
    failSecond = false
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(update.mock.calls.map(call => call[0])).toEqual([1, 2, 2])
    expect(wrapper.emitted('updated')).toEqual([[[1]], [[2]]])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('does not change pricing or write when an imported file is invalid or cancelled', async () => {
    const wrapper = mountDialog([channel(1)])
    await wrapper.get('[data-testid="bulk-pricing-enabled"]').setValue(true)
    const before = wrapper.get('.pricing-entry').text()
    const input = wrapper.get('[data-testid="bulk-pricing-import"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [{ size: 20, text: async () => '{invalid' }] })
    await input.trigger('change')
    await flushPromises()
    expect(wrapper.get('.pricing-entry').text()).toBe(before)
    expect(showError).toHaveBeenCalled()
    await wrapper.get('[data-testid="dialog-close"]').trigger('click')
    expect(update).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('locks closure while writes are pending', async () => {
    let complete!: () => void
    update.mockImplementation(() => new Promise<void>(resolve => { complete = resolve }))
    const wrapper = mountDialog([channel(1)])
    await wrapper.get('[data-testid="bulk-status-enabled"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.find('[data-testid="dialog-close"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="bulk-channel-submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    complete()
    await flushPromises()
    expect(wrapper.emitted('updated')).toEqual([[[1]]])
    wrapper.unmount()
  })
})
