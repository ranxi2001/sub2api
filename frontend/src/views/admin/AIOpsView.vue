<template>
  <AppLayout>
    <div v-if="authStore.isAdmin" class="mx-auto max-w-5xl space-y-5">
      <SmartOpsNav />
      <header class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div class="flex flex-wrap items-center gap-3">
            <h1 class="text-2xl font-semibold text-gray-900 dark:text-gray-100">{{ t('aiOps.title') }}</h1>
            <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.aiOpsCodex" :user-id="authStore.user?.id" />
            <span class="rounded-full bg-sky-50 px-3 py-1 text-xs text-sky-800 dark:bg-sky-900/20 dark:text-sky-200">{{ t('aiOps.observeOnly') }}</span>
          </div>
          <p class="mt-2 text-sm leading-6 text-gray-500">{{ t('aiOps.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="loading || submitting" data-testid="ai-ops-refresh" @click="refresh">{{ t('aiOps.refresh') }}</button>
      </header>

      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>

      <section class="card space-y-4 p-5" aria-labelledby="ai-ops-start-title">
        <h2 id="ai-ops-start-title" class="font-semibold">{{ t('aiOps.runDiagnostic') }}</h2>
        <p v-if="!status && loading" class="text-sm text-gray-500">{{ t('aiOps.loading') }}</p>
        <p v-else-if="status && !status.enabled" class="text-sm text-gray-600 dark:text-gray-300" data-testid="ai-ops-disabled">{{ t('aiOps.disabled') }}</p>
        <p v-else-if="status && !status.configured" class="text-sm text-gray-600 dark:text-gray-300">{{ t('aiOps.notConfigured') }}</p>
        <template v-if="status?.enabled && status.configured">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <p class="text-sm text-gray-700 dark:text-gray-200">{{ status.group_id == null ? t('aiOps.noGroup') : t('aiOps.group', { id: status.group_id }) }} · {{ t('aiOps.windowMinutes', { count: status.window_minutes }) }}</p>
            <span class="rounded-full px-3 py-1 text-xs" :class="needsRefreshAfterSubmission || diagnosisRunning || cooldownSeconds > 0 ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200' : 'bg-emerald-50 text-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200'" data-testid="ai-ops-state">{{ t(needsRefreshAfterSubmission ? 'aiOps.awaitingConfirmation' : diagnosisRunning ? 'aiOps.inProgress' : cooldownSeconds > 0 ? 'aiOps.coolingDown' : 'aiOps.ready') }}</span>
          </div>
          <p class="text-sm leading-6 text-gray-500" data-testid="ai-ops-readonly">{{ t('aiOps.noChanges') }}</p>
          <p v-if="diagnosisRunning" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t('aiOps.running') }}</p>
          <p v-if="cooldownSeconds > 0" role="status" class="text-sm text-amber-700 dark:text-amber-300" data-testid="ai-ops-cooldown">{{ t('aiOps.cooldown', { duration: cooldownDuration, time: formatTime(new Date(cooldownUntil).toISOString()) }) }}</p>
          <p v-if="needsRefreshAfterSubmission" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t('aiOps.refreshBeforeRetry') }}</p>
          <div class="flex flex-wrap items-center justify-between gap-4 border-t border-gray-100 pt-4 dark:border-dark-700">
            <label class="flex min-w-0 items-start gap-2 text-sm"><input v-model="costConsent" type="checkbox" class="mt-0.5" :disabled="submitting || diagnosisRunning" data-testid="ai-ops-cost-consent" /><span>{{ t('aiOps.costConsent') }}</span></label>
            <button type="button" class="btn btn-primary w-full sm:w-auto" :disabled="!canStart" data-testid="ai-ops-start" @click="createRun">{{ submitting ? t('aiOps.starting') : t('aiOps.start') }}</button>
          </div>
          <details class="text-xs text-gray-500" data-testid="ai-ops-run-settings">
            <summary class="cursor-pointer">{{ t('aiOps.runSettings') }}</summary>
            <div class="mt-3 space-y-2 leading-5">
              <p>{{ t('aiOps.interval', { count: status.min_interval_seconds }) }} · {{ t('aiOps.dailyLimit', { count: status.daily_run_limit }) }}</p>
              <p>{{ t('aiOps.budgetHint') }}</p>
              <p data-testid="ai-ops-automatic">{{ automaticIntervalLabel }}</p>
              <p v-if="status.last_run_at">{{ t('aiOps.lastRun', { time: formatTime(status.last_run_at) }) }}</p>
            </div>
          </details>
        </template>
      </section>

      <section class="min-w-0 space-y-4" aria-live="polite" aria-labelledby="ai-ops-report-title">
        <p v-if="detailError" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ detailError }}</p>
        <p v-if="loadingDetail && !selected" class="card p-5 text-sm text-gray-500">{{ t('aiOps.loading') }}</p>
        <div v-else-if="!selected" class="card space-y-2 p-5">
          <h2 id="ai-ops-report-title" class="font-semibold">{{ t('aiOps.report') }}</h2>
          <p class="text-sm leading-6 text-gray-500">{{ runs.length ? t('aiOps.selectRun') : t('aiOps.empty') }}</p>
        </div>
        <template v-if="selected">
          <div class="flex flex-wrap items-center justify-between gap-3" data-testid="ai-ops-run">
            <div class="flex flex-wrap items-center gap-3"><h2 id="ai-ops-report-title" class="font-semibold">{{ t('aiOps.report') }}</h2><span class="rounded-full bg-gray-100 px-3 py-1 text-xs text-gray-700 dark:bg-dark-800 dark:text-gray-200">{{ t(`aiOps.status.${selected.status}`) }}</span></div>
            <button v-if="isPending(selected) && polling" type="button" class="btn btn-secondary btn-sm" data-testid="ai-ops-stop-polling" @click="stopPolling(true)">{{ t('aiOps.stopPolling') }}</button>
            <button v-else-if="isPending(selected)" type="button" class="btn btn-secondary btn-sm" data-testid="ai-ops-resume-polling" @click="startPolling">{{ t('aiOps.resumePolling') }}</button>
          </div>
          <p v-if="isPending(selected)" class="rounded-lg bg-sky-50 p-4 text-sm text-sky-800 dark:bg-sky-900/15 dark:text-sky-200">{{ t('aiOps.pending') }}</p>
          <p v-if="pollMessage" class="text-sm text-amber-700 dark:text-amber-300" data-testid="ai-ops-poll-message">{{ pollMessage }}</p>
          <p v-if="selected.status === 'failed' || selected.status === 'interrupted'" role="status" class="rounded-lg bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/15 dark:text-amber-200">{{ t('aiOps.runFailed') }}</p>
          <AIOpsReport :run="selected" />
        </template>
      </section>

      <details class="card p-5" data-testid="ai-ops-history">
        <summary class="cursor-pointer text-sm font-semibold">{{ t('aiOps.history') }} <span class="ml-2 font-normal text-gray-500">{{ t('aiOps.historyCount', { shown: runs.length, total }) }}</span></summary>
        <div class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <p v-if="!runs.length" class="text-sm leading-6 text-gray-500">{{ loading ? t('aiOps.loading') : t('aiOps.emptyHistory') }}</p>
          <button v-for="run in runs" :key="run.id" type="button" class="min-w-0 rounded-lg border p-3 text-left text-sm" :class="selectedId === run.id ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/10' : 'border-gray-200 dark:border-dark-700'" :title="run.id" :aria-pressed="selectedId === run.id" data-testid="ai-ops-history-run" @click="selectRun(run.id)">
            <span class="block font-medium">{{ t(`aiOps.status.${run.status}`) }}</span>
            <time class="mt-1 block text-xs leading-5 text-gray-500" :datetime="run.created_at">{{ formatTime(run.created_at) }}</time>
          </button>
        </div>
      </details>

      <AIOpsControls :run="selected" compact />
    </div>
    <p v-else role="alert">{{ t('aiOps.unavailable') }}</p>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { aiOpsAPI, type AIOpsRun, type AIOpsStatus } from '@/api/admin/aiOps'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import AIOpsControls from '@/components/admin/operations/AIOpsControls.vue'
import AIOpsReport from '@/components/admin/operations/AIOpsReport.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'

const { t, locale } = useI18n()
const authStore = useAuthStore()
const status = ref<AIOpsStatus | null>(null)
const runs = ref<AIOpsRun[]>([])
const total = ref(0)
const selectedId = ref('')
const selected = ref<AIOpsRun | null>(null)
const loading = ref(false)
const loadingDetail = ref(false)
const submitting = ref(false)
const costConsent = ref(false)
const error = ref('')
const detailError = ref('')
const pollMessage = ref('')
const polling = ref(false)
const needsRefreshAfterSubmission = ref(false)
const clock = ref(Date.now())
const acceptedRunAt = ref(0)
const diagnosisRunning = computed(() => Boolean(status.value?.running || (selected.value && isPending(selected.value))))
const cooldownUntil = computed(() => {
  const interval = status.value?.min_interval_seconds ?? 0
  if (!Number.isFinite(interval) || interval <= 0) return 0
  const timestamps = [acceptedRunAt.value, Date.parse(status.value?.last_run_at ?? ''),
    ...runs.value.filter(run => run.group_id === status.value?.group_id).map(run => Date.parse(run.created_at))]
  const latest = Math.max(0, ...timestamps.filter(Number.isFinite))
  return latest > 0 ? latest + interval * 1000 : 0
})
const cooldownSeconds = computed(() => Math.max(0, Math.ceil((cooldownUntil.value - clock.value) / 1000)))
const cooldownDuration = computed(() => `${Math.floor(cooldownSeconds.value / 60)}:${String(cooldownSeconds.value % 60).padStart(2, '0')}`)
const canStart = computed(() => authStore.isAdmin && status.value?.enabled && status.value.configured && !diagnosisRunning.value && cooldownSeconds.value === 0 && !loading.value && !submitting.value && costConsent.value && !needsRefreshAfterSubmission.value)
const automaticIntervalLabel = computed(() => {
  const seconds = status.value?.automatic_interval_seconds ?? 0
  if (seconds <= 0) return t('aiOps.automaticDisabled')
  return seconds % 60 === 0
    ? t('aiOps.automaticMinutes', { count: seconds / 60 })
    : t('aiOps.automaticSeconds', { count: seconds })
})

let alive = true
let overviewController: AbortController | undefined
let detailController: AbortController | undefined
let createController: AbortController | undefined
let detailGeneration = 0
let pollingGeneration = 0
let pollingDeadline = 0
let pollingTimer: ReturnType<typeof setTimeout> | undefined
let clockTimer: ReturnType<typeof setInterval> | undefined
const POLL_INTERVAL_MS = 5000
const POLL_DURATION_MS = 5 * 60 * 1000

function isPending(run: AIOpsRun): boolean {
  return run.status === 'queued' || run.status === 'collecting' || run.status === 'analyzing'
}

function formatTime(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString(locale.value, { timeZoneName: 'short' })
}

async function loadOverview(): Promise<boolean> {
  overviewController?.abort()
  const controller = new AbortController()
  overviewController = controller
  const [statusResult, listResult] = await Promise.allSettled([aiOpsAPI.status(controller.signal), aiOpsAPI.list(controller.signal)])
  if (!alive || controller.signal.aborted) return false
  if (statusResult.status === 'fulfilled') status.value = statusResult.value
  else status.value = null
  if (listResult.status === 'fulfilled') {
    runs.value = listResult.value.items ?? []
    total.value = listResult.value.total
  }
  if (statusResult.status === 'fulfilled' && listResult.status === 'fulfilled') needsRefreshAfterSubmission.value = false
  const ok = statusResult.status === 'fulfilled' && (listResult.status === 'fulfilled' || !statusResult.value.enabled)
  if (!ok) error.value = t('aiOps.loadFailed')
  return ok
}

async function fetchDetail(id: string): Promise<boolean> {
  detailController?.abort()
  const controller = new AbortController()
  detailController = controller
  const generation = ++detailGeneration
  loadingDetail.value = true
  try {
    const run = await aiOpsAPI.get(id, controller.signal)
    if (!alive || controller.signal.aborted || generation !== detailGeneration || selectedId.value !== id) return false
    selected.value = run
    runs.value = runs.value.map(item => item.id === run.id ? { ...item, status: run.status, finished_at: run.finished_at } : item)
    detailError.value = ''
    return true
  } catch {
    if (alive && !controller.signal.aborted && generation === detailGeneration) detailError.value = t('aiOps.detailFailed')
    return false
  } finally {
    if (alive && generation === detailGeneration) loadingDetail.value = false
  }
}

function stopPolling(showMessage = false): void {
  pollingGeneration++
  if (pollingTimer != null) clearTimeout(pollingTimer)
  pollingTimer = undefined
  polling.value = false
  detailController?.abort()
  detailGeneration++
  loadingDetail.value = false
  if (showMessage) pollMessage.value = t('aiOps.pollingStopped')
}

function startPolling(): void {
  stopPolling()
  if (!alive || !selected.value || !isPending(selected.value)) return
  pollMessage.value = ''
  polling.value = true
  pollingDeadline = Date.now() + POLL_DURATION_MS
  const generation = pollingGeneration
  pollingTimer = setTimeout(() => poll(generation), POLL_INTERVAL_MS)
}

async function poll(generation: number): Promise<void> {
  if (!alive || generation !== pollingGeneration || !polling.value) return
  if (Date.now() >= pollingDeadline) {
    stopPolling()
    pollMessage.value = t('aiOps.pollingExpired')
    return
  }
  const ok = await fetchDetail(selectedId.value)
  if (!alive || generation !== pollingGeneration) return
  if (!ok) {
    stopPolling()
    pollMessage.value = t('aiOps.pollingFailed')
    return
  }
  if (selected.value && !isPending(selected.value)) {
    stopPolling()
    await loadOverview()
    return
  }
  pollingTimer = setTimeout(() => poll(generation), POLL_INTERVAL_MS)
}

async function selectRun(id: string): Promise<void> {
  stopPolling()
  pollMessage.value = ''
  detailError.value = ''
  selectedId.value = id
  selected.value = null
  if (await fetchDetail(id)) startPolling()
}

async function refresh(): Promise<void> {
  if (!authStore.isAdmin || submitting.value) return
  stopPolling()
  error.value = ''
  loading.value = true
  try {
    await loadOverview()
    if (!alive) return
    const id = runs.value.find(run => run.id === selectedId.value)?.id ?? runs.value[0]?.id
    if (id) await selectRun(id)
    else { selectedId.value = ''; selected.value = null }
  } finally {
    if (alive) loading.value = false
  }
}

async function createRun(): Promise<void> {
  if (!canStart.value) return
  stopPolling()
  error.value = ''
  detailError.value = ''
  pollMessage.value = ''
  submitting.value = true
  costConsent.value = false
  createController = new AbortController()
  try {
    const run = await aiOpsAPI.create(createController.signal)
    if (!alive) return
    const createdAt = Date.parse(run.created_at)
    acceptedRunAt.value = Math.max(acceptedRunAt.value, Number.isFinite(createdAt) ? createdAt : Date.now())
    selectedId.value = run.id
    selected.value = run
    if (!runs.value.some(item => item.id === run.id)) total.value++
    runs.value = [run, ...runs.value.filter(item => item.id !== run.id)]
    if (status.value) status.value.running = isPending(run)
    startPolling()
    await loadOverview()
  } catch (value) {
    if (alive) {
      const httpStatus = (value as { status?: number } | null)?.status
      error.value = t(httpStatus === 429 ? 'aiOps.rateLimited' : httpStatus === 409 ? 'aiOps.stateChanged' : 'aiOps.startFailed')
      needsRefreshAfterSubmission.value = true
    }
  } finally {
    if (alive) submitting.value = false
  }
}

onMounted(() => {
  if (authStore.isAdmin) {
    clockTimer = setInterval(() => { clock.value = Date.now() }, 1000)
    void refresh()
  }
})
onBeforeUnmount(() => {
  alive = false
  stopPolling()
  overviewController?.abort()
  createController?.abort()
  if (clockTimer != null) clearInterval(clockTimer)
})
</script>
