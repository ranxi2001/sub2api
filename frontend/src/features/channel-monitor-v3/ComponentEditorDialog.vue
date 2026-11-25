<template>
  <BaseDialog
    :show="show"
    :title="component ? t('channelMonitorV3.editor.editTitle') : t('channelMonitorV3.editor.createTitle')"
    width="wide"
    @close="emit('close')"
  >
    <form id="monitor-v3-component-form" class="space-y-4" data-testid="monitor-v3-component-form" @submit.prevent="save">
      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label class="input-label" for="monitor-v3-name">{{ t('channelMonitorV3.editor.name') }}</label>
          <input id="monitor-v3-name" v-model="form.name" class="input" maxlength="100" :placeholder="t('channelMonitorV3.editor.namePlaceholder')" data-testid="monitor-v3-name" />
        </div>
        <div>
          <label class="input-label">{{ t('channelMonitorV3.editor.category') }}</label>
          <Select v-model="form.category_id" :options="categoryOptions" :aria-label="t('channelMonitorV3.editor.category')" />
        </div>
      </div>
      <div>
        <label class="input-label" for="monitor-v3-description">{{ t('channelMonitorV3.editor.description') }}</label>
        <input id="monitor-v3-description" v-model="form.description" class="input" maxlength="500" :placeholder="t('channelMonitorV3.editor.descriptionPlaceholder')" />
      </div>
      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label class="input-label">{{ t('channelMonitorV3.editor.group') }}</label>
          <Select :model-value="form.group_id" :options="groupOptions" searchable :placeholder="t('channelMonitorV3.editor.groupPlaceholder')" :aria-label="t('channelMonitorV3.editor.group')" data-testid="monitor-v3-group" @update:model-value="changeGroup" />
          <p class="mt-1 text-xs text-gray-400">{{ t('channelMonitorV3.editor.groupHint') }}</p>
        </div>
        <div>
          <div class="mb-1 flex items-center gap-2">
            <label for="monitor-v3-model" class="text-sm font-medium">{{ t('channelMonitorV3.editor.model') }}</label>
            <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.monitorV3ComponentModels" :user-id="auth.user?.id" />
          </div>
          <input id="monitor-v3-model" v-model.trim="query" type="search" class="input" maxlength="100" :disabled="!form.group_id" :placeholder="t('channelMonitorV3.editor.modelPlaceholder')" />
          <div v-if="options.length" class="mt-1 max-h-36 overflow-y-auto rounded border border-gray-200 p-2 dark:border-dark-600">
            <label v-for="model in options" :key="model" class="flex cursor-pointer items-center gap-2 py-1 text-xs">
              <input v-model="form.models" type="checkbox" :value="model" />
              <span class="break-all font-mono">{{ model }}</span>
            </label>
          </div>
          <p v-if="candidateError" class="mt-1 text-xs text-amber-600" role="status">{{ t('channelMonitorV3.editor.candidatesFailed') }}</p>
          <p class="mt-1 text-xs text-gray-400">{{ t('channelMonitorV3.editor.modelHint') }}</p>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('channelMonitorV3.editor.threshold') }}</label>
        <Select v-model="form.degraded_ttft_ms" :options="thresholdOptions" :aria-label="t('channelMonitorV3.editor.threshold')" data-testid="monitor-v3-threshold" />
        <p class="mt-1 text-xs text-gray-400">{{ t('channelMonitorV3.editor.thresholdHint') }}</p>
      </div>

      <div>
        <label class="input-label">{{ t('channelMonitorV3.editor.visibility') }}</label>
        <div class="flex flex-wrap gap-2" role="radiogroup">
          <button
            v-for="visibility in visibilities"
            :key="visibility"
            type="button"
            role="radio"
            :aria-checked="form.visibility === visibility"
            class="rounded-lg border px-3 py-1.5 text-xs font-medium transition"
            :class="form.visibility === visibility ? 'border-primary-400 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-900/20 dark:text-primary-300' : 'border-gray-200 text-gray-600 hover:border-gray-300 dark:border-dark-600 dark:text-gray-300'"
            :data-testid="`monitor-v3-visibility-${visibility}`"
            @click="form.visibility = visibility"
          >
            {{ t(`channelMonitorV3.visibility.${visibility}`) }}
          </button>
        </div>
        <p class="mt-1 text-xs text-gray-400">{{ t(`channelMonitorV3.visibility.${form.visibility}Hint`) }}</p>
      </div>

      <div class="grid gap-3 sm:grid-cols-2">
        <label class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 px-3 py-2.5 dark:border-dark-600">
          <span>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('channelMonitorV3.editor.showMultiplier') }}</span>
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.editor.showMultiplierHint') }}</span>
          </span>
          <Toggle v-model="form.show_multiplier" />
        </label>
        <label class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 px-3 py-2.5 dark:border-dark-600">
          <span>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('channelMonitorV3.editor.enabled') }}</span>
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.editor.enabledHint') }}</span>
          </span>
          <Toggle v-model="form.enabled" />
        </label>
      </div>
      <p v-if="error" class="text-sm text-red-600 dark:text-red-400" role="alert">{{ error }}</p>
    </form>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="submit" form="monitor-v3-component-form" class="btn btn-primary" :disabled="saving || !canSubmit" data-testid="monitor-v3-save">{{ t('common.save') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import { getModelAllowlistCandidates } from '@/api/admin/groups'
import type { AdminGroup } from '@/types'
import {
  MONITOR_V3_TTFT_THRESHOLDS,
  createComponent,
  updateComponent,
  type MonitorV3Category,
  type MonitorV3Component,
  type MonitorV3ComponentInput,
  type MonitorV3Visibility,
} from '@/api/channelMonitorV3'
import { extractApiErrorMessage } from '@/utils/apiError'
import { platformLabel } from '@/utils/platformColors'
import { componentModels, formatMonitorV3Multiplier } from './monitorV3'

const props = defineProps<{
  show: boolean
  component: MonitorV3Component | null
  categories: MonitorV3Category[]
  groups: AdminGroup[]
  defaultCategoryId: number | null
  inheritedTtftMs: number
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved', value: MonitorV3Component): void }>()
const { t } = useI18n()
const auth = useAuthStore()

const visibilities: MonitorV3Visibility[] = ['group', 'public']

function blank(): MonitorV3ComponentInput & { models: string[] } {
  return {
    category_id: props.defaultCategoryId,
    name: '',
    description: '',
    group_id: 0,
    model: '',
    models: [],
    degraded_ttft_ms: 0,
    show_multiplier: true,
    visibility: 'group',
    enabled: true,
  }
}

const form = reactive(blank())
const candidates = ref<string[]>([])
const query = ref('')
const candidateError = ref(false)
const options = computed(() => {
  const all = [...new Set([...form.models, ...candidates.value])]
  const search = query.value.toLowerCase()
  const matches = all.filter((model) => model.toLowerCase().includes(search))
  if (query.value && !all.includes(query.value)) matches.push(query.value)
  return matches
})

function changeGroup(value: unknown) {
  const id = Number(value)
  if (form.group_id === id) return
  form.group_id = id
  form.models = []
  query.value = ''
}

watch(() => [props.show, form.group_id] as const, async ([open, id], _, onCleanup) => {
  let current = true
  onCleanup(() => { current = false })
  candidates.value = []
  candidateError.value = false
  if (!open || !id) return
  try {
    const models = await getModelAllowlistCandidates(id)
    if (current) candidates.value = models
  } catch {
    if (current) candidateError.value = true
  }
}, { immediate: true })
const saving = ref(false)
const error = ref('')

watch(() => props.show, (open) => {
  if (!open) return
  const source = props.component
  Object.assign(form, source
    ? {
        category_id: source.category_id, name: source.name, description: source.description, group_id: source.group_id,
        model: source.model, models: [...componentModels(source)], degraded_ttft_ms: source.degraded_ttft_ms, show_multiplier: source.show_multiplier,
        visibility: source.visibility, enabled: source.enabled,
      }
    : blank())
  query.value = ''
  error.value = ''
}, { immediate: true })

const canSubmit = computed(() => form.name.trim() !== '' && form.group_id > 0)
const categoryOptions = computed(() => [
  { value: null, label: t('channelMonitorV3.editor.noCategory') },
  ...props.categories.map((category) => ({ value: category.id, label: category.name })),
])
const groupOptions = computed(() => {
  const options = props.groups.map((group) => ({
    value: group.id,
    label: `${group.name} · ${platformLabel(group.platform)} · ${formatMonitorV3Multiplier(group.rate_multiplier)}`,
  }))
  // A component may point at a group that was disabled since; keep it selectable.
  const current = props.component
  if (current && !options.some((option) => option.value === current.group_id)) {
    options.unshift({ value: current.group_id, label: current.group_name || `#${current.group_id}` })
  }
  return options
})
const thresholdOptions = computed(() => [
  { value: 0, label: t('channelMonitorV3.editor.thresholdInherit', { seconds: props.inheritedTtftMs / 1000 }) },
  ...MONITOR_V3_TTFT_THRESHOLDS.map((ms) => ({ value: ms, label: t('channelMonitorV3.admin.seconds', { count: ms / 1000 }) })),
])

function payload(): MonitorV3ComponentInput {
  return { ...form, name: form.name.trim(), description: form.description.trim(), model: form.models[0] ?? '', models: [...form.models] }
}

async function save() {
  if (!canSubmit.value) return
  saving.value = true
  error.value = ''
  try {
    const saved = props.component ? await updateComponent(props.component.id, payload()) : await createComponent(payload())
    emit('saved', saved)
  } catch (err: unknown) {
    error.value = extractApiErrorMessage(err, t('channelMonitorV3.editor.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
