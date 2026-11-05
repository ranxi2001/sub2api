vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
vi.mock('@/components/common/NewFeatureBadge.vue', () => ({ default: { template: '<span />' } }))
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  resetPlatformCatalog
} from '@/constants/platformCatalog'
import { serializeChannelPricing } from '../channelPricingTransfer'
import { createDefaultTimePricingForm } from '@/components/admin/channel/types'
import ChannelsView from '@/views/admin/ChannelsView.vue'

const { listChannels, getGroups, syncPricingModels, createChannel, updateChannel, showSuccess, showError } = vi.hoisted(() => ({
  listChannels: vi.fn(),
  getGroups: vi.fn(),
  syncPricingModels: vi.fn(),
  createChannel: vi.fn(), updateChannel: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: { list: listChannels, syncPricingModels, create: createChannel, update: updateChannel },
    groups: { getAll: getGroups },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false }) }
  }
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError }) }))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

function mountView() {
  return mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: defineComponent({ template: '<main><slot /></main>' }),
        TablePageLayout: defineComponent({
          template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
        }),
        BaseDialog: defineComponent({
          props: ['show'],
          template: '<section v-if="show"><slot /><slot name="footer" /></section>'
        }),
        PricingEntryCard: defineComponent({
          props: ['entry'],
          template: '<div class="pricing-entry">{{ entry.models.join(", ") }}</div>'
        }),
        DataTable: true,
        Pagination: true,
        ConfirmDialog: defineComponent({
          props: ['show'], emits: ['confirm', 'cancel'],
          template: `<div v-if="show" data-testid="import-confirm"><button @click="$emit('confirm')">confirm</button><button @click="$emit('cancel')">cancel</button></div>`
        }),
        EmptyState: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        Toggle: true
      }
    }
  })
}

async function openDialog(wrapper: ReturnType<typeof mountView>) {
  await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === 'admin.channels.createChannel')!.trigger('click')
  await flushPromises()
}

async function enablePlatform(wrapper: ReturnType<typeof mountView>, platform: string) {
  const label = wrapper.findAll('label').find(label => label.text() === `admin.groups.platforms.${platform}`)
  expect(label).toBeDefined()
  await label!.get('input[type="checkbox"]').setValue(true)
  await wrapper.findAll('button.channel-tab').find(button => button.text() === `admin.groups.platforms.${platform}`)!.trigger('click')
}

const json = serializeChannelPricing('openai', [{
  models: ['imported-model'], billing_mode: 'token', input_price: 2, output_price: 8,
  cache_write_price: null, cache_read_price: null, image_input_price: null,
  image_output_price: null, per_request_price: null, intervals: [], time_pricing: createDefaultTimePricingForm(),
}], key => key)

async function selectFile(wrapper: ReturnType<typeof mountView>, content: string | Promise<string>) {
  const input = wrapper.findAll('input[type="file"]').find(input => input.isVisible())!
  Object.defineProperty(input.element, 'files', { configurable: true, value: [{ size: 100, text: () => Promise.resolve(content) }] })
  await input.trigger('change')
  await flushPromises()
}

describe('ChannelsView pricing import', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listChannels.mockResolvedValue({ items: [], total: 0 })
    getGroups.mockResolvedValue([])
  })
  afterEach(() => resetPlatformCatalog())

  it('requires confirmation and only replaces form pricing without API writes', async () => {
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')
    await selectFile(wrapper, json)
    expect(wrapper.find('[data-testid="import-confirm"]').exists()).toBe(true)
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(0)
    await wrapper.get('[data-testid="import-confirm"] button').trigger('click')
    expect(wrapper.get('.pricing-entry').text()).toBe('imported-model')
    expect(showSuccess).toHaveBeenCalledWith('admin.channels.pricingTransfer.imported')
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('leaves the form intact on cancellation or invalid JSON', async () => {
    const wrapper = mountView()
    await openDialog(wrapper)
    await enablePlatform(wrapper, 'openai')
    await selectFile(wrapper, json)
    await wrapper.findAll('[data-testid="import-confirm"] button')[1].trigger('click')
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(0)
    await selectFile(wrapper, '{broken')
    expect(showError).toHaveBeenCalled()
    expect(wrapper.find('[data-testid="import-confirm"]').exists()).toBe(false)
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(0)
    wrapper.unmount()
  })
})
