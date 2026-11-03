<template>
  <BaseDialog
    :show="show"
    :title="t('apiKeyModelPolicy.title')"
    width="wide"
    :z-index="100000030"
    :close-on-escape="!saving"
    :show-close-button="!saving"
    @close="close"
  >
    <form v-if="apiKey" id="api-key-model-policy-form" class="space-y-5" @submit.prevent="save">
      <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <p class="font-medium text-gray-900 dark:text-white">{{ apiKey.name }}</p>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('apiKeyModelPolicy.scopeHint') }}</p>
      </div>

      <fieldset :disabled="saving" class="space-y-3">
        <legend class="input-label">{{ t('apiKeyModelPolicy.modelMapping') }}</legend>
        <p class="text-sm text-gray-500 dark:text-dark-300">{{ t('apiKeyModelPolicy.mappingHint') }}</p>
        <p v-if="rows.length === 0" class="text-sm text-gray-500">{{ t('apiKeyModelPolicy.noMappings') }}</p>
        <div v-for="row in rows" :key="row.id" class="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_auto_1fr_auto] sm:items-end" data-test="model-mapping-row">
          <label class="block min-w-0">
            <span class="input-label">{{ t('apiKeyModelPolicy.requestedModel') }}</span>
            <input v-model="row.source" class="input font-mono" type="text" required maxlength="200" placeholder="gpt-6.1-sol" data-test="mapping-source" />
          </label>
          <span aria-hidden="true" class="hidden pb-2.5 text-gray-400 sm:block">→</span>
          <label class="block min-w-0">
            <span class="input-label">{{ t('apiKeyModelPolicy.targetModel') }}</span>
            <input v-model="row.target" class="input font-mono" type="text" required maxlength="200" placeholder="gpt-5.6-sol" data-test="mapping-target" />
          </label>
          <button type="button" class="btn btn-secondary" :aria-label="t('apiKeyModelPolicy.removeMapping')" @click="removeRow(row.id)">
            {{ t('common.remove') }}
          </button>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="rows.length >= 100" data-test="add-mapping" @click="addRow">
          {{ t('apiKeyModelPolicy.addMapping') }}
        </button>
      </fieldset>

      <div>
        <label for="api-key-reasoning-effort" class="input-label">{{ t('apiKeyModelPolicy.reasoningEffort') }}</label>
        <select id="api-key-reasoning-effort" v-model="reasoningEffort" class="input" :disabled="saving" data-test="reasoning-effort">
          <option value="">{{ t('apiKeyModelPolicy.followClient') }}</option>
          <option v-for="effort in efforts" :key="effort" :value="effort">{{ effort }}</option>
        </select>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-300">{{ t('apiKeyModelPolicy.effortHint') }}</p>
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button>
        <button type="submit" form="api-key-model-policy-form" class="btn btn-primary" :disabled="saving || !apiKey">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { ApiKey } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; apiKey: ApiKey | null }>()
const emit = defineEmits<{ close: []; updated: [key: ApiKey] }>()
const { t } = useI18n()
const appStore = useAppStore()
const efforts = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra']
const rows = ref<Array<{ id: number; source: string; target: string }>>([])
const reasoningEffort = ref('')
const saving = ref(false)
const error = ref('')
let nextRowID = 0
let generation = 0

watch(() => [props.show, props.apiKey?.id] as const, () => {
  generation++
  saving.value = false
  error.value = ''
  rows.value = Object.entries(props.apiKey?.model_mapping ?? {}).map(([source, target]) => ({ id: nextRowID++, source, target }))
  reasoningEffort.value = props.apiKey?.reasoning_effort ?? ''
}, { immediate: true })

onUnmounted(() => { generation++ })

function addRow() {
  if (rows.value.length < 100) rows.value.push({ id: nextRowID++, source: '', target: '' })
}

function removeRow(id: number) {
  rows.value = rows.value.filter(row => row.id !== id)
}

function close() {
  if (!saving.value) emit('close')
}

function errorMessage(cause: unknown): string {
  if (!cause || typeof cause !== 'object') return t('apiKeyModelPolicy.saveFailed')
  const failure = cause as { message?: unknown; response?: { data?: { message?: unknown; detail?: unknown } } }
  // apiClient normally normalizes failures to plain objects; retain server
  // details for an unnormalized Axios error as well.
  for (const message of [failure.response?.data?.message, failure.response?.data?.detail, failure.message]) {
    if (typeof message === 'string' && message.trim()) return message
  }
  return t('apiKeyModelPolicy.saveFailed')
}

async function save() {
  if (!props.apiKey || saving.value) return
  error.value = ''
  const mappings = new Map<string, string>()
  // Unicode properties match Go's unicode.IsSpace and unicode.IsControl,
  // including C1 controls that JavaScript's whitespace class omits.
  const validModel = (model: string) => model.length > 0 && new TextEncoder().encode(model).length <= 200 && !/[\p{White_Space}\p{Cc}]/u.test(model)
  if (rows.value.length > 100) {
    error.value = t('apiKeyModelPolicy.tooManyMappings')
    return
  }
  for (const row of rows.value) {
    if (!validModel(row.source) || !validModel(row.target)) {
      error.value = t('apiKeyModelPolicy.invalidModel')
      return
    }
    if (mappings.has(row.source)) {
      error.value = t('apiKeyModelPolicy.duplicateModel', { model: row.source })
      return
    }
    mappings.set(row.source, row.target)
  }
  const keyID = props.apiKey.id
  const currentGeneration = generation
  saving.value = true
  try {
    const result = await adminAPI.apiKeys.updateApiKeyModelPolicy(keyID, {
      model_mapping: Object.fromEntries(mappings),
      reasoning_effort: reasoningEffort.value
    })
    if (generation !== currentGeneration) return
    appStore.showSuccess(t('apiKeyModelPolicy.saved'))
    emit('updated', result.api_key)
    emit('close')
  } catch (cause: unknown) {
    if (generation !== currentGeneration) return
    error.value = errorMessage(cause)
  } finally {
    if (generation === currentGeneration) saving.value = false
  }
}
</script>
