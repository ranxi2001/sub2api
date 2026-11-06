vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
vi.mock('@/components/common/NewFeatureBadge.vue', () => ({
  default: { props: ['noticeId'], template: '<span :data-notice-id="noticeId" />' }
}))
import { Blob as NodeBlob } from 'node:buffer'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  resetPlatformCatalog
} from '@/constants/platformCatalog'
import { serializeChannelPricing } from '../channelPricingTransfer'
import { parsePricingBundle, parsePricingCollection, serializePricingBundle, serializePricingCollection } from '../channelPricingBundle'
import type { PricingBundle } from '../channelPricingBundle'
import type { Channel, ChannelModelPricing } from '@/api/admin/channels'
import type { PricingFormEntry } from '@/components/admin/channel/types'
import { createDefaultTimePricingForm } from '@/components/admin/channel/types'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import ChannelsView from '@/views/admin/ChannelsView.vue'

const { listChannels, getChannelById, getGroups, syncPricingModels, createChannel, updateChannel, showSuccess, showError } = vi.hoisted(() => ({
  listChannels: vi.fn(),
  getChannelById: vi.fn(),
  getGroups: vi.fn(),
  syncPricingModels: vi.fn(),
  createChannel: vi.fn(), updateChannel: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: { list: listChannels, getById: getChannelById, syncPricingModels, create: createChannel, update: updateChannel },
    groups: { getAll: getGroups },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false }) }
  }
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, showError }) }))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params?.name ? `${key}:${params.name}` : key })
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
          name: 'PricingEntryPreview',
          props: ['entry', 'platform'],
          emits: ['update'],
          template: '<div class="pricing-entry" :data-platform="platform">{{ entry.models.join(", ") }}</div>'
        }),
        DataTable: defineComponent({
          props: {
            data: { type: Array, default: () => [] },
            selectedKeys: { type: Array, default: () => [] },
            selectable: Boolean,
          },
          emits: ['update:selectedKeys'],
          template: `<div><div v-for="row in data" :key="row.id">
            <input v-if="selectable" type="checkbox" :data-row-select="row.id" :checked="(selectedKeys || []).includes(row.id)"
              @change="$emit('update:selectedKeys', $event.target.checked ? [...(selectedKeys || []), row.id] : (selectedKeys || []).filter(key => key !== row.id))" />
            <slot name="cell-actions" :row="row" />
          </div></div>`
        }),
        BulkEditChannelDialog: defineComponent({
          name: 'BulkPricingEditorPreview',
          props: ['show', 'channels', 'initialPricing', 'initialPricingById'],
          emits: ['close', 'updated'],
          template: `<div v-if="show" data-testid="bulk-editor">
            <span v-for="channel in channels" :key="channel.id" :data-bulk-channel="channel.id">{{ channel.name }}</span>
            <button data-testid="close-bulk-editor" @click="$emit('close')">close</button>
          </div>`
        }),
        Pagination: true,
        ConfirmDialog: defineComponent({
          props: ['show'], emits: ['confirm', 'cancel'],
          template: `<div v-if="show" data-testid="import-confirm"><button @click="$emit('confirm')">confirm</button><button @click="$emit('cancel')">cancel</button></div>`
        }),
        EmptyState: true,
        Select: defineComponent({
          props: ['modelValue', 'options', 'placeholder'],
          emits: ['update:modelValue', 'change'],
          template: `<select :value="modelValue" :aria-label="placeholder"
            @change="$emit('update:modelValue', $event.target.value); $emit('change')">
            <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>`
        }),
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

function formEntry(model: string): PricingFormEntry {
  return {
    models: [model], billing_mode: 'token', input_price: 2, output_price: 8,
    cache_write_price: null, cache_read_price: 0, image_input_price: null,
    image_output_price: null, per_request_price: null, intervals: [],
    time_pricing: createDefaultTimePricingForm(),
  }
}

const bundleJSON = serializePricingBundle('Imported channel', [
  { platform: 'openai', entries: [formEntry('imported-openai')] },
  { platform: 'anthropic', entries: [formEntry('imported-anthropic')] },
], key => key)

function sourceChannel(): Channel {
  const pricing: ChannelModelPricing = {
    id: 91, platform: 'openai', models: ['example-model'], billing_mode: 'token',
    input_price: 0.000002, output_price: 0.000008, cache_write_price: null,
    cache_read_price: 0, image_input_price: null, image_output_price: null,
    per_request_price: null, intervals: [], time_pricing: null,
  }
  return {
    id: 7, name: 'Example channel', description: 'Example pricing', status: 'active',
    billing_model_source: 'upstream', restrict_models: true,
    features_config: { web_search_emulation: { openai: true } },
    group_ids: [11, 12],
    model_mapping: { openai: { 'original-alias': 'example-model' } },
    model_pricing: [pricing, {
      ...pricing, id: 92, platform: 'anthropic', models: ['example-image'],
      billing_mode: 'image', input_price: null, output_price: null, per_request_price: 0.04,
    }],
    apply_pricing_to_account_stats: true,
    account_stats_pricing_rules: [{ id: 31, name: 'Source rule', group_ids: [11], account_ids: [21], pricing: [pricing] }],
    created_at: '2026-11-06T00:00:00Z', updated_at: '2026-11-06T00:00:00Z',
  }
}

async function selectFile(wrapper: ReturnType<typeof mountView>, content: string | Promise<string>) {
  const input = wrapper.findAll('#channel-form input[type="file"]').find(input => input.isVisible())!
  Object.defineProperty(input.element, 'files', { configurable: true, value: [{ size: 100, text: () => Promise.resolve(content) }] })
  await input.trigger('change')
  await flushPromises()
}

async function selectListFile(wrapper: ReturnType<typeof mountView>, content: string | Promise<string>, intoSelection = false) {
  const input = wrapper.get(`[data-testid="${intoSelection ? 'selected-pricing-import' : 'list-pricing-import'}"]`)
  Object.defineProperty(input.element, 'files', { configurable: true, value: [{ size: 100, text: () => Promise.resolve(content) }] })
  await input.trigger('change')
  await flushPromises()
}

function nameInput(wrapper: ReturnType<typeof mountView>) {
  return wrapper.get<HTMLInputElement>('input[placeholder="admin.channels.form.namePlaceholder"]')
}

async function cancelDialog(wrapper: ReturnType<typeof mountView>) {
  await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
}

describe('ChannelsView pricing import', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listChannels.mockResolvedValue({ items: [], total: 0 })
    getChannelById.mockReset()
    getGroups.mockResolvedValue([])
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    resetPlatformCatalog()
  })

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

  it('offers pricing import, copy and export directly on the list with a shared new notice', async () => {
    listChannels.mockResolvedValue({ items: [sourceChannel()], total: 1 })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="list-pricing-import"]').element.tagName).toBe('INPUT')
    expect(wrapper.get('[data-testid="copy-channel-pricing"]').isVisible()).toBe(true)
    expect(wrapper.get('[data-testid="export-channel-pricing"]').isVisible()).toBe(true)
    expect(wrapper.find('#channel-form').exists()).toBe(false)
    expect(wrapper.findAll(`[data-notice-id="${NEW_FEATURE_NOTICES.channelPricingListActions}"]`)).toHaveLength(2)
    wrapper.unmount()
  })

  it('copies only pricing into an independent unsaved draft without taking over source bindings', async () => {
    const channel = sourceChannel()
    const original = JSON.stringify(channel)
    listChannels.mockResolvedValue({ items: [channel], total: 1 })
    getGroups.mockResolvedValue([
      { id: 11, name: 'Source OpenAI', platform: 'openai', rate_multiplier: 1 },
      { id: 12, name: 'Source Anthropic', platform: 'anthropic', rate_multiplier: 1 },
    ])
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="copy-channel-pricing"]').trigger('click')
    await flushPromises()

    expect(nameInput(wrapper).element.value).toContain(channel.name)
    expect(wrapper.findAll('.pricing-entry').map(entry => entry.text()).sort()).toEqual(['example-image', 'example-model'])
    for (const groupName of ['Source OpenAI', 'Source Anthropic']) {
      const label = wrapper.findAll('label').find(label => label.text().includes(groupName))!
      expect(label.get<HTMLInputElement>('input[type="checkbox"]').element.checked).toBe(false)
    }
    expect(wrapper.find('input[placeholder="admin.channels.form.mappingSource"]').exists()).toBe(false)
    expect(wrapper.find('input[placeholder="admin.channels.form.ruleName"]').exists()).toBe(false)
    const preview = wrapper.findComponent({ name: 'PricingEntryPreview' })
    const entry = preview.props('entry') as PricingFormEntry
    preview.vm.$emit('update', { ...entry, models: ['changed-copy-model'] })
    await flushPromises()
    expect(JSON.stringify(channel)).toBe(original)

    await wrapper.get('#channel-form').trigger('submit')
    expect(showError).toHaveBeenCalledWith('admin.channels.noGroupsSelected')
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    await cancelDialog(wrapper)
    expect(wrapper.find('#channel-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('exports all pricing platforms in form units with zero and null preserved', async () => {
    const channel = sourceChannel()
    listChannels.mockResolvedValue({ items: [channel], total: 1 })
    const wrapper = mountView()
    await flushPromises()
    let exported: NodeBlob | undefined
    const createObjectURL = vi.fn((blob: NodeBlob) => { exported = blob; return 'blob:pricing-export' })
    const revokeObjectURL = vi.fn()
    vi.stubGlobal('Blob', NodeBlob)
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.useFakeTimers()
    try {
      await wrapper.get('[data-testid="export-channel-pricing"]').trigger('click')
      expect(createObjectURL).toHaveBeenCalledOnce()
      const bundle = parsePricingBundle(await exported!.text(), key => key)
      expect(bundle.name).toBe(channel.name)
      expect(bundle.platforms.map(platform => platform.platform).sort()).toEqual(['anthropic', 'openai'])
      expect(bundle.platforms.find(platform => platform.platform === 'openai')!.entries[0]).toMatchObject({
        models: ['example-model'], input_price: 2, output_price: 8,
        cache_read_price: 0, cache_write_price: null,
      })
      expect(bundle.platforms.find(platform => platform.platform === 'anthropic')!.entries[0].per_request_price).toBe(0.04)
      expect(createChannel).not.toHaveBeenCalled()
      expect(updateChannel).not.toHaveBeenCalled()
      vi.runOnlyPendingTimers()
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:pricing-export')
    } finally {
      vi.runOnlyPendingTimers()
      vi.useRealTimers()
      wrapper.unmount()
    }
  })

  it.each([
    ['multi-platform bundle', bundleJSON, ['imported-anthropic', 'imported-openai']],
    ['legacy platform file', json, ['imported-model']],
  ] as const)('opens an unsaved draft from a %s on the list', async (_label, content, models) => {
    const wrapper = mountView()
    await flushPromises()
    await selectListFile(wrapper, content)

    expect(wrapper.get('#channel-form').isVisible()).toBe(true)
    expect(nameInput(wrapper).element.value).not.toBe('')
    if (content === bundleJSON) expect(nameInput(wrapper).element.value).toBe('Imported channel')
    expect(wrapper.findAll('.pricing-entry').map(entry => entry.text()).sort()).toEqual([...models].sort())
    expect(wrapper.find('input[placeholder="admin.channels.form.mappingSource"]').exists()).toBe(false)
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    await cancelDialog(wrapper)
    expect(wrapper.find('#channel-form').exists()).toBe(false)
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('rejects a bundle with invalid later pricing without partially opening a draft', async () => {
    const invalid = JSON.parse(bundleJSON)
    invalid.platforms[1].model_pricing[0].input_price = -1
    const wrapper = mountView()
    await flushPromises()
    await selectListFile(wrapper, JSON.stringify(invalid))

    expect(showError).toHaveBeenCalled()
    expect(wrapper.find('#channel-form').exists()).toBe(false)
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('exports the selected channels and clears selection when the status filter changes', async () => {
    const first = sourceChannel()
    const second = { ...sourceChannel(), id: 8, name: 'Other channel' }
    listChannels.mockResolvedValue({ items: [first, second], total: 2 })
    getChannelById.mockImplementation((id: number) => Promise.resolve(id === first.id ? first : second))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-row-select="7"]').setValue(true)
    expect(wrapper.get('[data-testid="channel-bulk-actions"]').isVisible()).toBe(true)

    const downloads: NodeBlob[] = []
    vi.stubGlobal('Blob', NodeBlob)
    vi.stubGlobal('URL', {
      createObjectURL: vi.fn((blob: NodeBlob) => { downloads.push(blob); return 'blob:pricing-selection' }),
      revokeObjectURL: vi.fn(),
    })
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.useFakeTimers()
    try {
      await wrapper.get('[data-testid="export-channel-selection"]').trigger('click')
      await flushPromises()
      expect(parsePricingCollection(await downloads[0].text(), key => key).map(bundle => bundle.name)).toEqual([first.name])
      expect(getChannelById).toHaveBeenCalledOnce()
      expect(getChannelById).toHaveBeenCalledWith(7)

      await wrapper.get('select[aria-label="admin.channels.allStatus"]').setValue('disabled')
      await flushPromises()
      expect(wrapper.find('[data-testid="channel-bulk-actions"]').exists()).toBe(false)
      expect(wrapper.get<HTMLInputElement>('[data-row-select="7"]').element.checked).toBe(false)
      await wrapper.get('[data-testid="export-channel-selection"]').trigger('click')
      await flushPromises()
      expect(parsePricingCollection(await downloads[1].text(), key => key).map(bundle => bundle.name)).toEqual([first.name, second.name])
      expect(getChannelById).toHaveBeenCalledOnce()
    } finally {
      vi.runOnlyPendingTimers()
      vi.useRealTimers()
      wrapper.unmount()
    }
  })

  it('freezes the batch edit targets when the editor opens', async () => {
    const first = sourceChannel()
    const second = { ...sourceChannel(), id: 8, name: 'Other channel' }
    listChannels.mockResolvedValue({ items: [first, second], total: 2 })
    getChannelById.mockImplementation((id: number) => Promise.resolve(id === first.id ? first : second))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-row-select="7"]').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-channel-pricing"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-bulk-channel]').map(channel => channel.attributes('data-bulk-channel'))).toEqual(['7'])

    await wrapper.get('[data-row-select="8"]').setValue(true)
    await wrapper.get('[data-row-select="7"]').setValue(false)
    expect(wrapper.findAll('[data-bulk-channel]').map(channel => channel.attributes('data-bulk-channel'))).toEqual(['7'])
    expect(updateChannel).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="close-bulk-editor"]').trigger('click')
    await wrapper.get('[data-testid="bulk-edit-channel-pricing"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-bulk-channel]').map(channel => channel.attributes('data-bulk-channel'))).toEqual(['8'])
    wrapper.unmount()
  })

  it('matches imported collections to selected channels by name rather than file order', async () => {
    const first = sourceChannel()
    const second = { ...sourceChannel(), id: 8, name: 'Other channel' }
    listChannels.mockResolvedValue({ items: [first, second], total: 2 })
    getChannelById.mockImplementation((id: number) => Promise.resolve(id === first.id ? first : second))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-row-select="7"]').setValue(true)
    await wrapper.get('[data-row-select="8"]').setValue(true)
    const file = serializePricingCollection([
      { name: second.name, platforms: [{ platform: 'openai', entries: [formEntry('second-import')] }] },
      { name: first.name, platforms: [{ platform: 'openai', entries: [formEntry('first-import')] }] },
    ], key => key)
    await selectListFile(wrapper, file, true)

    const editor = wrapper.findComponent({ name: 'BulkPricingEditorPreview' })
    expect(editor.props('show')).toBe(true)
    expect(editor.props('initialPricing')).toBeUndefined()
    const byId = editor.props('initialPricingById') as Record<number, PricingBundle>
    expect(byId[7].platforms[0].entries[0].models).toEqual(['first-import'])
    expect(byId[8].platforms[0].entries[0].models).toEqual(['second-import'])
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    ['bundle', bundleJSON, ['openai', 'anthropic']],
    ['legacy single-platform file', json, ['openai']],
  ] as const)('previews a %s as one pricing template for all selected channels without writes', async (_label, file, expectedPlatforms) => {
    const first = sourceChannel()
    const second = { ...sourceChannel(), id: 8, name: 'Other channel' }
    listChannels.mockResolvedValue({ items: [first, second], total: 2 })
    getChannelById.mockImplementation((id: number) => Promise.resolve(id === first.id ? first : second))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-row-select="7"]').setValue(true)
    await wrapper.get('[data-row-select="8"]').setValue(true)
    await selectListFile(wrapper, file, true)

    const editor = wrapper.findComponent({ name: 'BulkPricingEditorPreview' })
    expect(editor.props('show')).toBe(true)
    expect(wrapper.findAll('[data-bulk-channel]').map(channel => channel.attributes('data-bulk-channel'))).toEqual(['7', '8'])
    expect((editor.props('initialPricing') as PricingBundle).platforms.map(platform => platform.platform)).toEqual(expectedPlatforms)
    expect(editor.props('initialPricingById')).toBeUndefined()
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    ['additional selected channels', [7, 8]],
    ['a different selected channel', [8]],
  ] as const)('rejects a one-channel collection when there are %s without writes', async (_label, selectedIds) => {
    const first = sourceChannel()
    const second = { ...sourceChannel(), id: 8, name: 'Other channel' }
    listChannels.mockResolvedValue({ items: [first, second], total: 2 })
    getChannelById.mockImplementation((id: number) => Promise.resolve(id === first.id ? first : second))
    const wrapper = mountView()
    await flushPromises()
    for (const id of selectedIds) await wrapper.get(`[data-row-select="${id}"]`).setValue(true)
    const file = serializePricingCollection([
      { name: first.name, platforms: [{ platform: 'openai', entries: [formEntry('first-import')] }] },
    ], key => key)
    await selectListFile(wrapper, file, true)

    expect(showError).toHaveBeenCalledWith('admin.channels.pricingTransfer.selectionMismatch')
    expect(wrapper.find('[data-testid="bulk-editor"]').exists()).toBe(false)
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('matches a one-channel collection to its selected channel by name without writes', async () => {
    const first = sourceChannel()
    listChannels.mockResolvedValue({ items: [first], total: 1 })
    getChannelById.mockResolvedValue(first)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-row-select="7"]').setValue(true)
    const file = serializePricingCollection([
      { name: first.name, platforms: [{ platform: 'openai', entries: [formEntry('first-import')] }] },
    ], key => key)
    await selectListFile(wrapper, file, true)

    const editor = wrapper.findComponent({ name: 'BulkPricingEditorPreview' })
    expect(editor.props('show')).toBe(true)
    expect(editor.props('initialPricing')).toBeUndefined()
    const byId = editor.props('initialPricingById') as Record<number, PricingBundle>
    expect(Object.keys(byId)).toEqual(['7'])
    expect(byId[7].platforms[0].entries[0].models).toEqual(['first-import'])
    expect(showError).not.toHaveBeenCalled()
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('rejects an imported collection that does not match all selected channels', async () => {
    const first = sourceChannel()
    const second = { ...sourceChannel(), id: 8, name: 'Other channel' }
    listChannels.mockResolvedValue({ items: [first, second], total: 2 })
    getChannelById.mockImplementation((id: number) => Promise.resolve(id === first.id ? first : second))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-row-select="7"]').setValue(true)
    await wrapper.get('[data-row-select="8"]').setValue(true)
    const file = serializePricingCollection([
      { name: first.name, platforms: [] },
      { name: 'Unselected channel', platforms: [] },
    ], key => key)
    await selectListFile(wrapper, file, true)

    expect(showError).toHaveBeenCalledWith('admin.channels.pricingTransfer.selectionMismatch')
    expect(wrapper.find('[data-testid="bulk-editor"]').exists()).toBe(false)
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('advances a collection only after saving each draft and stops the remaining imports on cancel', async () => {
    const file = serializePricingCollection([
      { name: 'First imported channel', platforms: [{ platform: 'openai', entries: [formEntry('first-model')] }] },
      { name: 'Second imported channel', platforms: [{ platform: 'openai', entries: [formEntry('second-model')] }] },
    ], key => key)
    getGroups.mockResolvedValue([{ id: 11, name: 'Available group', platform: 'openai', rate_multiplier: 1 }])
    createChannel.mockResolvedValue({ id: 99 })
    const wrapper = mountView()
    await flushPromises()
    await selectListFile(wrapper, file)
    expect(nameInput(wrapper).element.value).toBe('First imported channel')
    expect(createChannel).not.toHaveBeenCalled()
    await enablePlatform(wrapper, 'openai')
    const group = wrapper.findAll('label').find(label => label.text().includes('Available group'))!
    await group.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()

    expect(createChannel).toHaveBeenCalledOnce()
    expect(createChannel).toHaveBeenCalledWith(expect.objectContaining({
      name: 'First imported channel', group_ids: [11],
      model_pricing: [expect.objectContaining({ models: ['first-model'], input_price: 0.000002 })],
    }))
    expect(nameInput(wrapper).element.value).toBe('Second imported channel')
    await cancelDialog(wrapper)
    expect(wrapper.find('#channel-form').exists()).toBe(false)
    expect(createChannel).toHaveBeenCalledOnce()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps a newer independent draft when an earlier queued import finishes saving', async () => {
    const file = serializePricingCollection([
      { name: 'Queued import A', platforms: [{ platform: 'openai', entries: [formEntry('first-model')] }] },
      { name: 'Queued import A next', platforms: [{ platform: 'openai', entries: [formEntry('next-model')] }] },
    ], key => key)
    getGroups.mockResolvedValue([{ id: 11, name: 'Available group', platform: 'openai', rate_multiplier: 1 }])
    let resolveCreate!: (value: { id: number }) => void
    createChannel.mockImplementationOnce(() => new Promise(resolve => { resolveCreate = resolve }))
    const wrapper = mountView()
    await flushPromises()
    await selectListFile(wrapper, file)
    await enablePlatform(wrapper, 'openai')
    const group = wrapper.findAll('label').find(label => label.text().includes('Available group'))!
    await group.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('#channel-form').trigger('submit')
    await flushPromises()
    expect(createChannel).toHaveBeenCalledOnce()

    // Closing while the request is pending must preserve the submitted draft.
    await cancelDialog(wrapper)
    expect(nameInput(wrapper).element.value).toBe('Queued import A')
    await openDialog(wrapper)
    await nameInput(wrapper).setValue('Independent draft B')
    resolveCreate({ id: 99 })
    await flushPromises()

    expect(nameInput(wrapper).element.value).toBe('Independent draft B')
    expect(wrapper.get('#channel-form').isVisible()).toBe(true)
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(0)
    expect(createChannel).toHaveBeenCalledOnce()
    expect(createChannel).toHaveBeenCalledWith(expect.objectContaining({ name: 'Queued import A' }))
    expect(updateChannel).not.toHaveBeenCalled()
    await cancelDialog(wrapper)
    expect(wrapper.find('#channel-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps a newer create draft when an earlier file read finishes', async () => {
    let resolveFile!: (value: string) => void
    const content = new Promise<string>(resolve => { resolveFile = resolve })
    const wrapper = mountView()
    await flushPromises()
    await selectListFile(wrapper, content)
    await openDialog(wrapper)
    await nameInput(wrapper).setValue('Independent draft')
    resolveFile(bundleJSON)
    await flushPromises()

    expect(nameInput(wrapper).element.value).toBe('Independent draft')
    expect(wrapper.findAll('.pricing-entry')).toHaveLength(0)
    expect(showError).not.toHaveBeenCalled()
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('ignores file completion after leaving the page', async () => {
    let resolveFile!: (value: string) => void
    const content = new Promise<string>(resolve => { resolveFile = resolve })
    const wrapper = mountView()
    await flushPromises()
    await selectListFile(wrapper, content)
    const groupLoadsBeforeUnmount = getGroups.mock.calls.length
    wrapper.unmount()
    resolveFile(bundleJSON)
    await flushPromises()

    expect(getGroups).toHaveBeenCalledTimes(groupLoadsBeforeUnmount)
    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(createChannel).not.toHaveBeenCalled()
    expect(updateChannel).not.toHaveBeenCalled()
  })
})
