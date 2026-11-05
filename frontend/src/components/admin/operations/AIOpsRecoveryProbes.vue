<template>
  <section class="card space-y-4 p-5" data-testid="ai-recovery-probes">
    <header class="flex flex-wrap items-center justify-between gap-3"><h3 class="font-semibold">{{ t('aiOpsControl.probes.title') }}</h3><button type="button" class="btn btn-secondary" :disabled="submitting || loading" data-testid="ai-probe-refresh" @click="refresh">{{ t('aiOpsControl.probes.refresh') }}</button></header>
    <p class="text-sm leading-6 text-gray-500">{{ t('aiOpsControl.probes.hint') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-300">{{ error }}</p>
    <p v-if="needsRefresh && !error" role="alert" class="text-sm text-amber-700 dark:text-amber-300">{{ t('aiOpsControl.probes.refreshRequired') }}</p>
    <div class="grid gap-3 sm:grid-cols-2">
      <label><span class="mb-1 block text-xs text-gray-500">{{ t('aiOpsControl.probes.account') }}</span><select v-model.number="accountId" class="input w-full" :disabled="submitting || disabled" data-testid="ai-probe-account"><option :value="0">{{ t('aiOpsControl.probes.choose') }}</option><option v-for="account in policy.accounts" :key="account.account_id" :value="account.account_id">#{{ account.account_id }}</option></select></label>
      <label><span class="mb-1 block text-xs text-gray-500">{{ t('aiOpsControl.probes.model') }}</span><input v-model.trim="model" type="text" maxlength="100" class="input w-full" :disabled="submitting || disabled" data-testid="ai-probe-model" /></label>
    </div>
    <label class="flex items-start gap-2 text-sm"><input v-model="consent" type="checkbox" class="mt-0.5" :disabled="submitting || disabled" data-testid="ai-probe-consent" /><span>{{ t('aiOpsControl.probes.consent') }}</span></label>
    <div class="flex flex-wrap items-center gap-3">
      <button type="button" class="btn btn-primary" :disabled="!canProbe" data-testid="ai-probe-start" @click="start">{{ t('aiOpsControl.probes.start') }}</button>
      <button v-if="polling" type="button" class="btn btn-secondary" data-testid="ai-probe-stop-polling" @click="stopPolling(true)">{{ t('aiOpsControl.probes.stop') }}</button>
      <button v-else-if="hasPending" type="button" class="btn btn-secondary" :disabled="loading" data-testid="ai-probe-resume-polling" @click="startPolling">{{ t('aiOpsControl.probes.resume') }}</button>
    </div>
    <p v-if="pollMessage" class="text-sm text-amber-700 dark:text-amber-300">{{ pollMessage }}</p>
    <p v-if="!probes.length" class="text-sm text-gray-500">{{ t('aiOpsControl.probes.empty') }}</p>
    <article v-for="probe in probes" :key="probe.id" class="space-y-2 rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-700" data-testid="ai-probe-record">
      <div class="flex flex-wrap justify-between gap-2"><strong>#{{ probe.account_id }} · {{ probe.model }}</strong><span>{{ t(`aiOpsControl.probes.status.${probe.status}`) }}</span></div>
      <p class="text-xs text-gray-500">{{ t('aiOpsControl.created') }}：{{ time(probe.created_at) }} · {{ t('aiOpsControl.expires') }}：{{ time(probe.expires_at) }}</p>
      <p v-if="probe.effort || probe.protocol" class="text-xs text-gray-500">{{ t('aiOpsControl.effort') }}：{{ probe.effort || t('aiOpsControl.unknown') }} · {{ t('aiOpsControl.protocol') }}：{{ probe.protocol || t('aiOpsControl.unknown') }}</p>
      <p v-if="probe.reason_code" class="break-words text-xs text-gray-500">{{ reasonLabel(probe.reason_code) }}</p>
      <p v-if="probe.status === 'passed'" class="text-xs text-gray-500">{{ t('aiOpsControl.probes.passedHint') }}</p>
      <p class="break-all font-mono text-xs text-gray-400">{{ probe.id }}</p>
    </article>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { aiOpsControlAPI, type AIOpsPolicy, type AIOpsRecoveryProbe } from '@/api/admin/aiOpsControl'

const props = defineProps<{ policy: AIOpsPolicy; disabled?: boolean }>()
const { t, te, locale } = useI18n()
const auth = useAuthStore()
const probes = ref<AIOpsRecoveryProbe[]>([])
const accountId = ref(0), model = ref(''), consent = ref(false)
const submitting = ref(false), loading = ref(false), polling = ref(false), needsRefresh = ref(false)
const error = ref(''), pollMessage = ref('')
const hasPending = computed(() => probes.value.some(item => item.status === 'reserved'))
const canProbe = computed(() => auth.isAdmin && props.policy.control_enabled && props.policy.mode !== 'disabled' && !props.disabled && !submitting.value && !loading.value && !needsRefresh.value && !hasPending.value && consent.value && props.policy.accounts.some(item => item.account_id === accountId.value) && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,99}$/.test(model.value))
let alive = true, readGeneration = 0, pollGeneration = 0
let readController: AbortController | undefined, submitController: AbortController | undefined
let timer: ReturnType<typeof setTimeout> | undefined
let deadline = 0
watch([accountId, model, () => props.policy.revision], () => { consent.value = false })

function time(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale.value, { timeZoneName: 'short' }) : '—'
}
function reasonLabel(code: string): string { return te(`aiOpsControl.reasons.${code}`) ? t(`aiOpsControl.reasons.${code}`) : code }
async function read(manual: boolean): Promise<boolean> {
  readController?.abort()
  const controller = new AbortController(), generation = ++readGeneration
  readController = controller
  loading.value = true
  try {
    const history = await aiOpsControlAPI.probes(controller.signal)
    if (!alive || controller.signal.aborted || generation !== readGeneration) return false
    const pendingIds = new Set(probes.value.filter(item => item.status === 'reserved').map(item => item.id))
    probes.value = history.items
    if (manual) needsRefresh.value = false
    else if (history.items.some(item => item.status === 'unknown' && pendingIds.has(item.id))) needsRefresh.value = true
    error.value = ''
    return true
  } catch {
    if (alive && !controller.signal.aborted && generation === readGeneration) {
      error.value = t('aiOpsControl.probes.loadFailed')
      needsRefresh.value = true
    }
    return false
  } finally { if (alive && generation === readGeneration) loading.value = false }
}
function stopPolling(showMessage = false): void {
  pollGeneration++
  if (timer != null) clearTimeout(timer)
  timer = undefined
  polling.value = false
  readController?.abort(); readGeneration++
  loading.value = false
  if (showMessage) pollMessage.value = t('aiOpsControl.probes.stopped')
}
function startPolling(): void {
  stopPolling()
  if (!alive || !hasPending.value) return
  polling.value = true; pollMessage.value = ''; deadline = Date.now() + 90000
  const generation = pollGeneration
  timer = setTimeout(() => poll(generation), 5000)
}
async function poll(generation: number): Promise<void> {
  if (!alive || generation !== pollGeneration) return
  if (Date.now() >= deadline) { stopPolling(); pollMessage.value = t('aiOpsControl.probes.expired'); return }
  const ok = await read(false)
  if (!alive || generation !== pollGeneration) return
  if (!ok || !hasPending.value) { stopPolling(); return }
  timer = setTimeout(() => poll(generation), 5000)
}
async function refresh(): Promise<void> {
  if (!auth.isAdmin || submitting.value) return
  stopPolling(); pollMessage.value = ''
  if (await read(true)) startPolling()
}
async function start(): Promise<void> {
  if (!canProbe.value) return
  submitting.value = true; error.value = ''; pollMessage.value = ''; consent.value = false
  submitController = new AbortController()
  try {
    const probe = await aiOpsControlAPI.probe(accountId.value, model.value, submitController.signal)
    if (!alive) return
    probes.value = [probe, ...probes.value.filter(item => item.id !== probe.id)]
    if (probe.status === 'unknown') needsRefresh.value = true
    startPolling()
  } catch {
    if (alive) { error.value = t('aiOpsControl.probes.failed'); needsRefresh.value = true }
  } finally { if (alive) submitting.value = false }
}
onMounted(() => { if (auth.isAdmin) void refresh() })
onBeforeUnmount(() => { alive = false; stopPolling(); submitController?.abort() })
</script>
