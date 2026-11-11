<template>
  <BaseDialog
    :show="show"
    :title="t('admin.channels.bulkEdit.title')"
    width="extra-wide"
    :close-on-escape="!saving"
    :show-close-button="!saving"
    @close="requestClose"
  >
    <form id="bulk-edit-channel-form" class="space-y-5" @submit.prevent="submit">
      <div class="rounded-lg bg-primary-50 p-4 dark:bg-primary-900/20">
        <p class="text-sm font-medium text-primary-800 dark:text-primary-200">
          {{ t('admin.channels.bulkEdit.selectionInfo', { count: pendingTargets.length }) }}
        </p>
        <p class="mt-1 text-xs text-primary-700 dark:text-primary-300">
          {{ t('admin.channels.bulkEdit.hint') }}
        </p>
        <p class="mt-2 break-words text-xs text-gray-600 dark:text-gray-400">
          {{ pendingTargets.map(channel => channel.name).join('、') }}
        </p>
      </div>

      <fieldset :disabled="saving || readingFile" class="space-y-5">
        <div>
          <label class="flex items-center gap-2 text-sm font-medium">
            <input v-model="enableStatus" type="checkbox" class="rounded border-gray-300 text-primary-600" data-testid="bulk-status-enabled" />
            {{ t('admin.channels.form.status') }}
          </label>
          <Select v-if="enableStatus" v-model="status" :options="statusOptions" class="mt-2" data-testid="bulk-status" />
        </div>

        <div>
          <label class="flex items-center gap-2 text-sm font-medium">
            <input v-model="enableRestriction" type="checkbox" class="rounded border-gray-300 text-primary-600" data-testid="bulk-restriction-enabled" />
            {{ t('admin.channels.form.restrictModels') }}
          </label>
          <label v-if="enableRestriction" class="mt-2 flex items-center gap-2 text-sm">
            <input v-model="restrictModels" type="checkbox" class="rounded border-gray-300 text-primary-600" data-testid="bulk-restrict-models" />
            {{ t('admin.channels.form.restrictModelsHint') }}
          </label>
        </div>

        <div>
          <label class="flex items-center gap-2 text-sm font-medium">
            <input v-model="enableBillingSource" type="checkbox" class="rounded border-gray-300 text-primary-600" data-testid="bulk-billing-enabled" />
            {{ t('admin.channels.form.billingModelSource') }}
          </label>
          <Select v-if="enableBillingSource" v-model="billingSource" :options="billingOptions" class="mt-2" data-testid="bulk-billing-source" />
        </div>

        <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
          <label class="flex items-center gap-2 text-sm font-medium">
            <input v-model="enablePricing" type="checkbox" class="rounded border-gray-300 text-primary-600" data-testid="bulk-pricing-enabled" />
            {{ t('admin.channels.form.modelPricing') }}
          </label>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.channels.bulkEdit.pricingHint') }}</p>

          <div v-if="enablePricing" class="mt-3 space-y-3">
            <div v-if="pricingById" class="space-y-2" data-testid="bulk-matched-pricing">
              <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.channels.bulkEdit.matchedPricingHint') }}</p>
              <div v-for="channel in pendingTargets" :key="channel.id" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
                <span class="text-sm font-medium">{{ channel.name }}</span>
                <span class="ml-2 text-xs text-gray-500 dark:text-gray-400">
                  {{ pricingById[channel.id]?.name }} · {{ pricingById[channel.id]?.platforms.map(section => platformLabel(section.platform)).join(' / ') }}
                </span>
              </div>
            </div>
            <template v-else>
              <div class="flex flex-wrap items-end gap-3">
                <div class="min-w-0 flex-1">
                  <label class="input-label" for="bulk-channel-pricing-template">{{ t('admin.channels.bulkEdit.template') }}</label>
                  <select id="bulk-channel-pricing-template" v-model="templateId" class="input" @change="loadSourceTemplate">
                    <option v-for="channel in sourceChannels" :key="channel.id" :value="channel.id">{{ channel.name }}</option>
                  </select>
                </div>
                <label class="btn btn-secondary cursor-pointer">
                  {{ t('admin.channels.pricingTransfer.import') }}
                  <input type="file" accept=".json,application/json" class="sr-only" :aria-label="t('admin.channels.pricingTransfer.import')" data-testid="bulk-pricing-import" @change="importPricing" />
                </label>
              </div>
              <div class="flex flex-wrap gap-2 border-b border-gray-200 pb-2 dark:border-dark-600">
                <button
                  v-for="section in pricingPlatforms"
                  :key="section.platform"
                  type="button"
                  class="rounded-md px-3 py-1.5 text-sm"
                  :class="activePlatform === section.platform ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300' : 'text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700'"
                  @click="activePlatform = section.platform"
                >
                  {{ platformLabel(section.platform) }}
                </button>
              </div>
              <p v-if="pricingPlatforms.length === 0" class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.channels.bulkEdit.noPricing') }}</p>
              <div v-for="section in pricingPlatforms" v-show="activePlatform === section.platform" :key="section.platform" class="space-y-2">
                <div class="flex justify-end">
                  <button type="button" class="btn btn-secondary btn-sm" data-testid="bulk-add-pricing" @click="addPricingEntry(section)">
                    + {{ t('common.add') }}
                  </button>
                </div>
                <p v-if="section.entries.length === 0" class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.channels.bulkEdit.emptyPlatform') }}</p>
                <PricingEntryCard :notice-user-id="noticeUserId"
                  v-for="(entry, index) in section.entries"
                  :key="index"
                  :entry="entry"
                  :platform="section.platform"
                  enable-time-pricing
                  enable-tier-multipliers
                  @update="section.entries.splice(index, 1, $event)"
                  @remove="section.entries.splice(index, 1)"
                />
              </div>
            </template>
          </div>
        </div>
      </fieldset>

      <div v-if="failures.length" class="rounded-lg border border-red-200 bg-red-50 p-3 dark:border-red-900 dark:bg-red-900/20" data-testid="bulk-channel-failures">
        <p class="text-sm font-medium text-red-700 dark:text-red-300">{{ t('admin.channels.bulkEdit.partial', { success: completedCount, failed: failures.length }) }}</p>
        <ul class="mt-2 space-y-1 text-xs text-red-700 dark:text-red-300">
          <li v-for="failure in failures" :key="failure.id">{{ failure.name }} — {{ failure.message }}</li>
        </ul>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="requestClose">{{ t('common.cancel') }}</button>
        <button
          type="submit"
          form="bulk-edit-channel-form"
          class="btn btn-primary"
          :disabled="saving || readingFile || !hasChanges || pendingTargets.length === 0"
          data-testid="bulk-channel-submit"
        >
          {{ saving ? t('common.saving') : failures.length ? t('admin.channels.bulkEdit.retryFailed') : t('admin.channels.bulkEdit.apply', { count: pendingTargets.length }) }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { Channel, ChannelModelPricing, UpdateChannelRequest } from '@/api/admin/channels'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import PricingEntryCard from './PricingEntryCard.vue'
import {
  createDefaultTimePricingForm,
  formIntervalsToAPI,
  formReasoningEffortMultipliersToAPI,
  formTimePricingToAPI,
  mTokToPerToken,
} from './types'
import { MAX_PRICING_FILE_BYTES } from '@/views/admin/channelPricingTransfer'
import {
  channelPricingToPlatforms,
  parsePricingBundle,
  serializePricingBundle,
  type PricingBundle,
  type PricingPlatform,
} from '@/views/admin/channelPricingBundle'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { platformLabel as catalogPlatformLabel } from '@/utils/platformColors'

const props = defineProps<{
  noticeUserId?: number | null
  show: boolean
  channels: Channel[]
  initialPricing?: PricingBundle
  initialPricingById?: Record<number, PricingBundle>
}>()
const emit = defineEmits<{
  close: []
  updated: [successfulIds: number[]]
}>()
const { t } = useI18n()
const appStore = useAppStore()
type Target = Pick<Channel, 'id' | 'name'>
const sourceChannels = ref<Channel[]>([])
const pendingTargets = ref<Target[]>([])
const failures = ref<Array<Target & { message: string }>>([])
const completedCount = ref(0)
const saving = ref(false)
const readingFile = ref(false)
const enableStatus = ref(false)
const enableRestriction = ref(false)
const enableBillingSource = ref(false)
const enablePricing = ref(false)
const status = ref('active')
const restrictModels = ref(false)
const billingSource = ref('channel_mapped')
const pricingPlatforms = ref<PricingPlatform[]>([])
const pricingById = ref<Record<number, PricingBundle> | null>(null)
const activePlatform = ref('')
const templateId = ref<number | null>(null)
let generation = 0

const hasChanges = computed(() => enableStatus.value || enableRestriction.value || enableBillingSource.value || enablePricing.value)
const statusOptions = computed(() => [
  { value: 'active', label: t('admin.channels.statusActive') },
  { value: 'disabled', label: t('admin.channels.statusDisabled') },
])
const billingOptions = computed(() => [
  { value: 'channel_mapped', label: t('admin.channels.form.billingModelSourceChannelMapped') },
  { value: 'requested', label: t('admin.channels.form.billingModelSourceRequested') },
  { value: 'upstream', label: t('admin.channels.form.billingModelSourceUpstream') },
  { value: 'response_model', label: t('admin.channels.form.billingModelSourceResponse') },
])

function platformLabel(platform: string) {
  return t(`admin.groups.platforms.${platform}`, catalogPlatformLabel(platform))
}

function notifyError(error: unknown) {
  const message = error instanceof Error ? error.message : ''
  appStore.showError(message.startsWith('admin.channels.') ? t(message) : extractApiErrorMessage(error, t('admin.channels.bulkEdit.failed')))
}

function normalizeBundle(bundle: PricingBundle): PricingBundle {
  return parsePricingBundle(serializePricingBundle(bundle.name, bundle.platforms, t), t)
}

function setPricing(bundle: PricingBundle) {
  const normalized = normalizeBundle(bundle)
  pricingPlatforms.value = normalized.platforms
  activePlatform.value = normalized.platforms[0]?.platform ?? ''
}

function loadSourceTemplate() {
  const source = sourceChannels.value.find(channel => channel.id === templateId.value)
  if (!source) return
  try {
    setPricing({ name: source.name, platforms: channelPricingToPlatforms(source.model_pricing || []) })
  } catch (error) {
    pricingPlatforms.value = []
    activePlatform.value = ''
    notifyError(error)
  }
}

function addPricingEntry(section: PricingPlatform) {
  section.entries.push({
    models: [],
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_write_1h_price: null,
    cache_write_12h_price: null,
    cache_read_price: null,
    fast_multiplier: null,
    flex_multiplier: null,
    reasoning_effort_multipliers: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: createDefaultTimePricingForm(),
  })
}

watch(() => props.show, show => {
  generation++
  if (!show || saving.value) return
  sourceChannels.value = JSON.parse(JSON.stringify(props.channels)) as Channel[]
  pendingTargets.value = sourceChannels.value.map(({ id, name }) => ({ id, name }))
  failures.value = []
  completedCount.value = 0
  enableStatus.value = false
  enableRestriction.value = false
  enableBillingSource.value = false
  enablePricing.value = !!props.initialPricing || !!props.initialPricingById
  status.value = 'active'
  restrictModels.value = false
  billingSource.value = 'channel_mapped'
  pricingById.value = null
  pricingPlatforms.value = []
  activePlatform.value = ''
  readingFile.value = false
  templateId.value = sourceChannels.value[0]?.id ?? null
  try {
    if (props.initialPricingById) {
      const next: Record<number, PricingBundle> = {}
      for (const target of pendingTargets.value) {
        const bundle = props.initialPricingById[target.id]
        if (!bundle) throw new Error('admin.channels.bulkEdit.noPricing')
        next[target.id] = normalizeBundle(bundle)
      }
      pricingById.value = next
    } else if (props.initialPricing) {
      setPricing(props.initialPricing)
    } else {
      loadSourceTemplate()
    }
  } catch (error) {
    enablePricing.value = false
    notifyError(error)
  }
}, { immediate: true })

onUnmounted(() => { generation++ })

async function importPricing(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || saving.value || readingFile.value) return
  const version = generation
  readingFile.value = true
  try {
    if (file.size > MAX_PRICING_FILE_BYTES) throw new Error('admin.channels.pricingTransfer.invalidFile')
    const bundle = parsePricingBundle(await file.text(), t)
    if (version !== generation || !props.show) return
    setPricing(bundle)
    enablePricing.value = true
  } catch (error) {
    if (version === generation) notifyError(error)
  } finally {
    if (version === generation) readingFile.value = false
  }
}

function pricingToAPI(platforms: PricingPlatform[]): ChannelModelPricing[] {
  return platforms.flatMap(section => section.entries.map(entry => ({
    platform: section.platform,
    models: [...entry.models],
    billing_mode: entry.billing_mode,
    input_price: mTokToPerToken(entry.input_price),
    output_price: mTokToPerToken(entry.output_price),
    cache_write_price: mTokToPerToken(entry.cache_write_price),
    cache_write_1h_price: mTokToPerToken(entry.cache_write_1h_price),
    cache_write_12h_price: mTokToPerToken(entry.cache_write_12h_price),
    cache_read_price: mTokToPerToken(entry.cache_read_price),
    image_input_price: mTokToPerToken(entry.image_input_price),
    image_output_price: mTokToPerToken(entry.image_output_price),
    per_request_price: entry.per_request_price == null || entry.per_request_price === '' ? null : Number(entry.per_request_price),
    fast_multiplier: entry.fast_multiplier == null || entry.fast_multiplier === '' ? null : Number(entry.fast_multiplier),
    flex_multiplier: entry.flex_multiplier == null || entry.flex_multiplier === '' ? null : Number(entry.flex_multiplier),
    reasoning_effort_multipliers: formReasoningEffortMultipliersToAPI(entry.reasoning_effort_multipliers),
    intervals: formIntervalsToAPI(entry.intervals),
    time_pricing: formTimePricingToAPI(entry.time_pricing),
  })))
}

function withoutPricingIDs(pricing: ChannelModelPricing): ChannelModelPricing {
  const { id: _id, ...rest } = pricing
  return {
    ...rest,
    models: [...pricing.models],
    intervals: (pricing.intervals || []).map(({ id: _intervalId, ...interval }) => ({ ...interval })),
  }
}

async function submit() {
  if (saving.value || readingFile.value || !hasChanges.value || pendingTargets.value.length === 0) return
  const targets = pendingTargets.value.map(target => ({ ...target }))
  const updates: UpdateChannelRequest = {}
  if (enableStatus.value) updates.status = status.value
  if (enableRestriction.value) updates.restrict_models = restrictModels.value
  if (enableBillingSource.value) updates.billing_model_source = billingSource.value

  const templates = new Map<number, PricingBundle>()
  try {
    if (enablePricing.value) {
      for (const target of targets) {
        const bundle = pricingById.value?.[target.id] ?? { name: '', platforms: pricingPlatforms.value }
        const normalized = normalizeBundle(bundle)
        if (!normalized.platforms.length) throw new Error('admin.channels.bulkEdit.noPricing')
        templates.set(target.id, normalized)
      }
    }
  } catch (error) {
    notifyError(error)
    return
  }

  saving.value = true
  const successfulIds: number[] = []
  const failed: Array<Target & { message: string }> = []
  try {
    for (const target of targets) {
      try {
        const update: UpdateChannelRequest = { ...updates }
        const template = templates.get(target.id)
        if (template) {
          const current = await adminAPI.channels.getById(target.id)
          const replacedPlatforms = new Set<string>(template.platforms.map(section => section.platform))
          update.model_pricing = [
            ...(current.model_pricing || []).filter(pricing => !replacedPlatforms.has(pricing.platform || 'anthropic')).map(withoutPricingIDs),
            ...pricingToAPI(template.platforms),
          ]
        }
        await adminAPI.channels.update(target.id, update)
        successfulIds.push(target.id)
      } catch (error) {
        failed.push({ ...target, message: extractApiErrorMessage(error, t('admin.channels.bulkEdit.failed')) })
      }
    }
    completedCount.value += successfulIds.length
    pendingTargets.value = failed.map(({ id, name }) => ({ id, name }))
    failures.value = failed
    if (successfulIds.length) emit('updated', successfulIds)
    if (failed.length) {
      appStore.showError(t('admin.channels.bulkEdit.partial', { success: completedCount.value, failed: failed.length }))
    } else {
      appStore.showSuccess(t('admin.channels.bulkEdit.success', { count: completedCount.value }))
      emit('close')
    }
  } finally {
    saving.value = false
  }
}

function requestClose() {
  if (saving.value) return
  generation++
  emit('close')
}
</script>
