<template>
  <section class="card overflow-hidden p-5" aria-labelledby="harvest-controls-title">
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 id="harvest-controls-title" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t(`${prefix}.title`) }}</h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.description`) }}</p>
      </div>
      <div v-if="draft" class="flex flex-wrap items-center gap-2">
        <span v-if="dirty" class="rounded-full bg-amber-50 px-2.5 py-1 text-[11px] text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">{{ t(`${prefix}.unsaved`) }}</span>
        <button data-testid="save-controls" type="submit" form="harvest-controls-form" class="btn btn-primary btn-sm" :disabled="saving || !dirty">{{ t(`${prefix}.save`) }}</button>
        <button data-testid="restore-defaults" type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="restoreDefaults">{{ t(`${prefix}.defaults`) }}</button>
        <button v-if="dirty" type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="discard">{{ t(`${prefix}.discard`) }}</button>
      </div>
    </div>
    <p v-if="error" role="alert" class="mb-3 text-sm text-red-600">{{ error }}</p>
    <p v-if="!draft" class="text-sm text-gray-500">{{ t(`${prefix}.loading`) }}</p>
    <form v-else id="harvest-controls-form" class="space-y-4" @submit.prevent="save">
      <fieldset :disabled="saving" class="space-y-4">
        <div class="rounded-xl border border-gray-100 p-4 dark:border-dark-700">
          <label class="mb-3 block text-sm">{{ t('admin.harvestFlow.edgeLabel') }}
            <input v-model="draft.edge_ip" data-testid="edge-ip" class="input mt-2 max-w-xs" placeholder="172.64.155.209" />
          </label>
          <p class="mb-3 text-xs text-gray-500">{{ t('admin.harvestFlow.edgeHint') }}</p>
          <label class="mb-3 block text-sm">{{ t('admin.harvestFlow.transportLabel') }}
            <select v-model="draft.transport" class="input mt-2 max-w-xs"><option value="sse">SSE</option><option value="websocket">WebSocket</option></select>
          </label>
          <label class="block text-sm">{{ t('admin.harvestFlow.gatewayLabel') }}
            <input v-model="draft.target_gateway" data-testid="target-gateway" class="input mt-2 max-w-xs" placeholder="unified-88 / any" required />
          </label>
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.harvestFlow.gatewayHint') }}</p>
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.harvestFlow.nativeHint') }}</p>
        </div>
        <div class="grid gap-3 lg:grid-cols-2">
          <div class="space-y-3 rounded-2xl border border-gray-100 p-4 dark:border-dark-700">
            <label class="flex items-start gap-3">
              <input v-model="draft.node_memory_enabled" data-testid="memory-toggle" type="checkbox" class="mt-1 rounded text-primary-600" :disabled="!remote?.available && !draft.node_memory_enabled" />
              <span>
                <span class="text-sm font-medium text-gray-900 dark:text-white">{{ t(`${prefix}.enabled`) }}</span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.enabledHint`) }}</span>
              </span>
            </label>
            <label class="flex items-start gap-3">
              <input v-model="draft.ticket_first_scheduling" data-testid="ticket-first-toggle" type="checkbox" class="mt-1 rounded text-primary-600" />
              <span>
                <span class="text-sm font-medium text-gray-900 dark:text-white">{{ t(`${prefix}.ticketFirst`) }}</span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.ticketFirstHint`) }}</span>
              </span>
            </label>
          </div>
          <div class="rounded-2xl border border-gray-100 p-4 dark:border-dark-700">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <label class="flex min-w-0 flex-wrap items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                {{ t(`${prefix}.speed`) }}
                <select :value="selectedPreset" data-testid="speed-preset" class="input w-36" @change="selectPreset">
                  <option v-for="preset in ['slow', 'standard', 'fast', 'burst', 'custom']" :key="preset" :value="preset">{{ t(`${prefix}.presets.${preset}`) }}</option>
                </select>
              </label>
              <button
                type="button"
                data-testid="advanced-toggle"
                class="rounded-full px-2.5 py-1 text-[11px]"
                :class="advanced ? 'bg-primary-600 text-white' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
                :aria-expanded="advanced"
                @click="advanced = !advanced"
              >
                {{ t(`${prefix}.advanced`) }}
              </button>
            </div>
            <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.summary`, { round: draft.speed.round_interval_seconds, gap: draft.speed.probe_interval_seconds, budget: draft.speed.max_requests_per_round, tries: draft.speed.max_node_attempts }) }}</p>
          </div>
        </div>
        <p v-if="remote && !remote.available" class="text-xs text-amber-600">{{ t(`${prefix}.unavailable`) }} {{ remote.availability_reason }}</p>
        <p v-if="remote?.settings_error" role="alert" class="text-xs text-amber-600">{{ remote.settings_error }}</p>
        <div v-show="advanced" data-testid="advanced-fields" class="grid gap-3 rounded-2xl border border-gray-100 p-4 dark:border-dark-700 sm:grid-cols-2 lg:grid-cols-3">
          <label v-for="field in speedFields" :key="field.key" class="text-xs text-gray-600 dark:text-gray-300">
            {{ t(`${prefix}.fields.${field.key}`) }}
            <input v-model.number="draft.speed[field.key]" :data-testid="field.key" type="number" :min="field.min" :max="field.max" :step="field.step" required class="input mt-1 w-full" />
            <span class="mt-1 block text-gray-400">{{ field.min }}–{{ field.max }}</span>
          </label>
        </div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.costHint`) }}</p>
        <p v-if="remote && !remote.configured" class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.legacyHint`) }}</p>
      </fieldset>
    </form>
    <div v-if="live" class="mt-4 grid grid-cols-2 gap-3 border-t border-gray-100 pt-4 dark:border-dark-700 lg:grid-cols-4">
      <div class="rounded-2xl border border-gray-100 bg-gray-50/80 p-3 dark:border-dark-700 dark:bg-dark-800/40">
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.budget`) }}</p>
        <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ live.requests_used }} / {{ live.request_budget }}</p>
      </div>
      <div class="rounded-2xl border border-gray-100 bg-gray-50/80 p-3 dark:border-dark-700 dark:bg-dark-800/40">
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.nextRound`) }}</p>
        <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ live.running ? t(`${prefix}.running`) : clock(live.next_round_at) }}</p>
      </div>
      <div class="rounded-2xl border border-gray-100 bg-gray-50/80 p-3 dark:border-dark-700 dark:bg-dark-800/40">
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.currentNode`) }}</p>
        <p class="mt-1 truncate text-sm font-semibold text-gray-900 dark:text-white">{{ live.current_node || '—' }}</p>
      </div>
      <div class="rounded-2xl border border-gray-100 bg-gray-50/80 p-3 dark:border-dark-700 dark:bg-dark-800/40">
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`${prefix}.selection`) }}</p>
        <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ selectionLabel }}</p>
      </div>
      <p v-if="live.degraded_reason" class="text-xs text-amber-600 col-span-2 lg:col-span-4">{{ t(`${prefix}.degraded`) }} {{ live.degraded_reason }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCodexHarvestControls, saveCodexHarvestControls, type CodexHarvestControls, type CodexHarvestControlSnapshot, type CodexHarvestRuntime, type CodexHarvestSpeed } from '@/api/admin/codexHarvest'
import { useAppStore } from '@/stores'

const props = defineProps<{ refreshKey?: string; runtime?: CodexHarvestRuntime }>()
const emit = defineEmits<{ saved: [] }>()
const { t, te } = useI18n()
const appStore = useAppStore()
const prefix = 'admin.harvestFlow.controls'
const remote = ref<CodexHarvestControlSnapshot | null>(null)
const draft = ref<CodexHarvestControls | null>(null)
const saved = ref<CodexHarvestControls | null>(null)
const saving = ref(false)
const advanced = ref(false)
const error = ref('')
let requestId = 0
let disposed = false
const copy = (v: CodexHarvestControls): CodexHarvestControls => ({ ...v, edge_ip: v.edge_ip || '', target_gateway: v.target_gateway || 'unified-88', transport: v.transport || 'sse', ticket_first_scheduling: !!v.ticket_first_scheduling, speed: { ...v.speed } })
const defaultSpeedFields: { key: keyof CodexHarvestSpeed; min: number; max: number; step: number }[] = [
  { key: 'round_interval_seconds', min: 1, max: 3600, step: 1 },
  { key: 'probe_interval_seconds', min: 0, max: 60, step: 1 },
  { key: 'attempt_timeout_seconds', min: 1, max: 120, step: 1 },
  { key: 'cooldown_seconds', min: 1, max: 3600, step: 1 },
  { key: 'max_requests_per_round', min: 1, max: 100, step: 1 },
  { key: 'max_node_attempts', min: 1, max: 10, step: 1 },
  { key: 'refresh_before_seconds', min: 60, max: 1800, step: 1 }
]
const speedFields = computed(() => defaultSpeedFields.map(field => {
  const bound = remote.value?.bounds?.[field.key]
  return bound ? { ...field, min: bound.min, max: bound.max } : field
}))
const same = (a: CodexHarvestControls | null, b: CodexHarvestControls | null) => !!a && !!b && a.version === b.version && a.edge_ip === b.edge_ip && a.target_gateway === b.target_gateway && a.transport === b.transport && a.node_memory_enabled === b.node_memory_enabled && a.ticket_first_scheduling === b.ticket_first_scheduling && speedFields.value.every(f => a.speed[f.key] === b.speed[f.key])
const dirty = computed(() => !!draft.value && !same(draft.value, saved.value))
const live = computed(() => props.runtime || remote.value?.runtime)
const selectionLabel = computed(() => {
  const reason = live.value?.selection_reason
  if (!reason) return '—'
  const key = `${prefix}.reasons.${reason}`
  return te(key) ? t(key) : reason
})
const selectedPreset = computed(() => Object.entries(remote.value?.presets || {}).find(([, speed]) => speedFields.value.every(f => speed[f.key] === draft.value?.speed[f.key]))?.[0] || 'custom')
const clock = (value?: string | null) => value ? new Date(value).toLocaleTimeString() : '—'

async function refresh() {
  if (saving.value || disposed) return
  const id = ++requestId
  try {
    const data = await getCodexHarvestControls()
    if (disposed || id !== requestId || saving.value) return
    const preserve = dirty.value
    remote.value = data
    saved.value = copy(data.settings)
    if (!preserve) draft.value = copy(data.settings)
    error.value = ''
  } catch {
    if (!disposed && id === requestId) error.value = t(`${prefix}.loadFailed`)
  }
}

function selectPreset(event: Event) {
  const preset = (event.target as HTMLSelectElement).value
  if (draft.value && remote.value?.presets[preset]) draft.value.speed = { ...remote.value.presets[preset] }
  if (preset === 'custom') advanced.value = true
}
function restoreDefaults() { if (remote.value) draft.value = copy(remote.value.defaults) }
function discard() { if (saved.value) draft.value = copy(saved.value) }

async function save() {
  if (!draft.value || saving.value) return
  const id = ++requestId
  saving.value = true
  error.value = ''
  try {
    const data = await saveCodexHarvestControls(copy(draft.value))
    if (disposed || id !== requestId) return
    saved.value = copy(data)
    draft.value = copy(data)
    if (remote.value) remote.value.configured = true
    advanced.value = false
    appStore.showSuccess(t(`${prefix}.saveSuccess`))
    emit('saved')
  } catch {
    if (!disposed && id === requestId) {
      error.value = t(`${prefix}.saveFailed`)
      appStore.showError(error.value)
    }
  } finally {
    if (!disposed && id === requestId) saving.value = false
  }
}

watch(() => props.refreshKey, () => { void refresh() })
onMounted(() => { void refresh() })
onBeforeUnmount(() => { disposed = true; requestId++ })
</script>
