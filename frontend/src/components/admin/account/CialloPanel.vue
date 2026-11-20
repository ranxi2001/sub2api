<template>
  <section class="space-y-4" data-testid="ciallo-panel">
    <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('qualityOps.ciallo.hint') }}</p>
    <TestModelSelect id="ciallo-model" :model-value="modelId" :options="modelOptions"
      :label="t('admin.accounts.pelicanTest.model')" hint="" :disabled="running"
      @update:model-value="emit('update:modelId', $event)" />
    <label class="block space-y-1">
      <span class="input-label">{{ t('admin.accounts.pelicanTest.reasoning') }}</span>
      <select v-model="effort" class="input" :disabled="running">
        <option value="">{{ t('modelTrace.providerDefault') }}</option>
        <option v-for="level in reasoningLevels" :key="level" :value="level">{{ level }}</option>
      </select>
    </label>
    <p v-if="account.platform !== 'openai'" class="text-sm">{{ t('qualityOps.ciallo.unsupported') }}</p>
    <button v-if="running" class="btn btn-secondary" @click="controller?.abort()">{{ t('common.cancel') }}</button>
    <button v-else class="btn btn-primary" :disabled="!modelId.trim() || account.platform !== 'openai'" @click="start">{{ t('qualityOps.ciallo.start') }}</button>
    <p v-if="failure" role="alert" class="text-sm">{{ t('qualityOps.ciallo.unassessable') }} · {{ t('qualityOps.ciallo.' + failure) }}</p>
    <p v-if="saveFailed" role="alert">{{ t('qualityOps.ciallo.storage') }}</p>
    <ol class="space-y-2" aria-live="polite">
      <li v-for="item in history" :key="item.started_at" class="space-y-1 rounded-lg border p-3 text-sm dark:border-dark-600">
        <strong>{{ t('qualityOps.ciallo.' + item.outcome) }}</strong>
        <p v-if="item.greeting"><code>{{ item.greeting }}</code></p>
        <p v-if="item.reason">{{ t('qualityOps.ciallo.' + item.reason) }}</p>
        <p>{{ item.model }} → {{ item.mapped_model }} · {{ item.reasoning_effort || t('modelTrace.providerDefault') }} · {{ item.protocol }}</p>
        <p class="text-xs">{{ item.template_version }} / {{ item.judge_version }} · {{ new Date(item.started_at).toLocaleString() }} · {{ item.duration_ms }} ms</p>
      </li>
    </ol>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { buildApiUrl } from '@/api/client'
import { ADMIN_UI_REQUEST_HEADER } from '@/api/adminUIRequest'
import type { Account } from '@/types'
import TestModelSelect from './TestModelSelect.vue'

interface CialloResult {
  model: string
  mapped_model: string
  reasoning_effort: string
  protocol: string
  template_version: string
  judge_version: string
  started_at: string
  duration_ms: number
  outcome: 'matched' | 'unmatched' | 'unassessable'
  reason?: string
  greeting?: string
}

const props = defineProps<{ account: Account; modelId: string; modelOptions: Array<{value: string; label: string}>; reasoningLevels: string[] }>()
const emit = defineEmits<{ (event: 'update:modelId', value: string): void; (event: 'running', value: boolean): void }>()
const { t } = useI18n()
const auth = useAuthStore()
const key = computed(() => `sub2api-ciallo:${auth.user?.id ?? 'admin'}:${props.account.id}`)
const history = ref<CialloResult[]>([])
const effort = ref('')
const failure = ref('')
const saveFailed = ref(false)
const running = ref(false)
let controller: AbortController | undefined

watch(() => props.modelId, () => { effort.value = '' })
watch(key, () => {
  controller?.abort()
  history.value = []
  failure.value = ''
  saveFailed.value = false
  try {
    const items = JSON.parse(localStorage.getItem(key.value) || '[]')
    if (Array.isArray(items)) history.value = items.filter(item => item?.judge_version && item?.started_at).slice(0, 5)
  } catch { saveFailed.value = true }
}, { immediate: true })

async function start() {
  if (running.value || props.account.platform !== 'openai' || !props.modelId.trim()) return
  const active = new AbortController()
  const storageKey = key.value
  controller = active
  running.value = true
  emit('running', true)
  failure.value = ''
  try {
    const response = await fetch(buildApiUrl(`/admin/accounts/${props.account.id}/pelican-test`), {
      method: 'POST',
      headers: { Authorization: `Bearer ${localStorage.getItem('auth_token') || ''}`, 'Content-Type': 'application/json', [ADMIN_UI_REQUEST_HEADER]: '1' },
      body: JSON.stringify({ prompt_mode: 'ciallo', model_id: props.modelId.trim(), reasoning_effort: effort.value }),
      signal: active.signal
    })
    const body = await response.json()
    if (active.signal.aborted || storageKey !== key.value) return
    if (!response.ok || !body.data?.judge_version) {
      failure.value = response.status === 409 ? 'busy' : body.reason === 'CIALLO_UNSUPPORTED' ? 'unsupported' : 'request_failed'
      return
    }
    history.value = [body.data as CialloResult, ...history.value].slice(0, 5)
    try { localStorage.setItem(storageKey, JSON.stringify(history.value)); saveFailed.value = false } catch { saveFailed.value = true }
  } catch {
    if (storageKey === key.value) failure.value = active.signal.aborted ? 'cancelled' : 'request_failed'
  } finally {
    running.value = false
    emit('running', false)
    controller = undefined
  }
}
onBeforeUnmount(() => { controller?.abort(); emit('running', false) })
</script>
