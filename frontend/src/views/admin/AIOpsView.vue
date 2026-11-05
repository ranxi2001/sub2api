<template>
  <AppLayout>
    <div v-if="authStore.isAdmin" class="space-y-6">
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
        <button class="btn btn-secondary" :disabled="loading || submitting" data-testid="ai-ops-refresh" @click="refresh">{{ t('aiOps.refresh') }}</button>
      </header>

      <p class="rounded-xl border border-sky-200 bg-sky-50 p-4 text-sm leading-6 text-sky-900 dark:border-sky-900 dark:bg-sky-900/15 dark:text-sky-200" data-testid="ai-ops-readonly">{{ t('aiOps.noChanges') }}</p>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>

      <section class="card space-y-4 p-5">
        <p v-if="!status && loading" class="text-sm text-gray-500">{{ t('aiOps.loading') }}</p>
        <p v-else-if="status && !status.enabled" class="text-sm text-gray-600 dark:text-gray-300" data-testid="ai-ops-disabled">{{ t('aiOps.disabled') }}</p>
        <p v-else-if="status && !status.configured" class="text-sm text-gray-600 dark:text-gray-300">{{ t('aiOps.notConfigured') }}</p>
        <template v-if="status?.enabled && status.configured">
          <div>
            <h2 class="font-semibold">{{ t('aiOps.scope') }}</h2>
            <p class="mt-2 text-sm">{{ status.group_id == null ? t('aiOps.noGroup') : t('aiOps.group', { id: status.group_id }) }} · {{ t('aiOps.windowMinutes', { count: status.window_minutes }) }}</p>
            <p class="mt-1 text-xs leading-5 text-gray-500">{{ t('aiOps.interval', { count: status.min_interval_seconds }) }} · {{ t('aiOps.dailyLimit', { count: status.daily_run_limit }) }}</p>
            <p class="mt-1 text-xs leading-5 text-gray-500" data-testid="ai-ops-automatic">{{ automaticIntervalLabel }}</p>
            <p v-if="status.last_run_at" class="mt-1 text-xs text-gray-500">{{ t('aiOps.lastRun', { time: formatTime(status.last_run_at) }) }}</p>
          </div>
          <p v-if="status.running" class="text-sm text-amber-700 dark:text-amber-300">{{ t('aiOps.running') }}</p>
          <div class="flex flex-wrap items-center gap-4">
            <label class="flex items-start gap-2 text-sm"><input v-model="costConsent" type="checkbox" class="mt-0.5" :disabled="submitting" data-testid="ai-ops-cost-consent" /><span>{{ t('aiOps.costConsent') }}</span></label>
            <button class="btn btn-primary" :disabled="!canStart" data-testid="ai-ops-start" @click="createRun">{{ submitting ? t('aiOps.starting') : t('aiOps.start') }}</button>
          </div>
        </template>
      </section>

      <AIOpsControls :run="selected" />

      <div class="grid items-start gap-6 lg:grid-cols-[260px_minmax(0,1fr)]">
        <aside class="card p-4">
          <h2 class="font-semibold">{{ t('aiOps.history') }}</h2>
          <p class="mb-4 mt-1 text-xs text-gray-500">{{ t('aiOps.historyCount', { shown: runs.length, total }) }}</p>
          <p v-if="!runs.length" class="text-sm leading-6 text-gray-500">{{ loading ? t('aiOps.loading') : t('aiOps.empty') }}</p>
          <button v-for="run in runs" :key="run.id" class="mb-2 w-full rounded-lg border p-3 text-left text-sm" :class="selectedId === run.id ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/10' : 'border-gray-200 dark:border-dark-700'" :aria-pressed="selectedId === run.id" data-testid="ai-ops-history-run" @click="selectRun(run.id)">
            <span class="block font-medium">{{ t(`aiOps.status.${run.status}`) }}</span>
            <time class="mt-1 block text-xs text-gray-500" :datetime="run.created_at">{{ formatTime(run.created_at) }}</time>
            <span class="mt-1 block truncate font-mono text-xs text-gray-400" :title="run.id">{{ run.id }}</span>
          </button>
        </aside>

        <section class="min-w-0 space-y-5" aria-live="polite">
          <p v-if="detailError" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ detailError }}</p>
          <p v-if="loadingDetail && !selected" class="card p-5 text-sm text-gray-500">{{ t('aiOps.loading') }}</p>
          <p v-else-if="!selected" class="card p-5 text-sm text-gray-500">{{ t('aiOps.selectRun') }}</p>
          <template v-if="selected">
            <div class="card space-y-3 p-5" data-testid="ai-ops-run">
              <div class="flex flex-wrap items-center justify-between gap-3">
                <h2 class="font-semibold">{{ t(`aiOps.status.${selected.status}`) }}</h2>
                <button v-if="isPending(selected) && polling" class="btn btn-secondary btn-sm" data-testid="ai-ops-stop-polling" @click="stopPolling(true)">{{ t('aiOps.stopPolling') }}</button>
                <button v-else-if="isPending(selected)" class="btn btn-secondary btn-sm" data-testid="ai-ops-resume-polling" @click="startPolling">{{ t('aiOps.resumePolling') }}</button>
              </div>
              <dl class="grid gap-3 text-sm sm:grid-cols-2">
                <div class="min-w-0"><dt class="text-xs text-gray-500">{{ t('aiOps.runId') }}</dt><dd class="mt-1 break-all font-mono text-xs">{{ selected.id }}</dd></div>
                <div><dt class="text-xs text-gray-500">{{ t('aiOps.startedAt') }}</dt><dd class="mt-1">{{ formatTime(selected.created_at) }}</dd></div>
                <div v-if="selected.finished_at"><dt class="text-xs text-gray-500">{{ t('aiOps.finishedAt') }}</dt><dd class="mt-1">{{ formatTime(selected.finished_at) }}</dd></div>
                <div class="sm:col-span-2"><dt class="text-xs text-gray-500">{{ t('aiOps.observedWindow') }}</dt><dd class="mt-1">{{ formatTime(selected.window.start) }} → {{ formatTime(selected.window.end) }}</dd></div>
              </dl>
              <p v-if="isPending(selected)" class="text-sm text-gray-500">{{ t('aiOps.pending') }}</p>
              <p v-if="pollMessage" class="text-sm text-amber-700 dark:text-amber-300" data-testid="ai-ops-poll-message">{{ pollMessage }}</p>
              <p v-if="selected.status === 'failed' || selected.status === 'interrupted'" class="text-sm text-amber-700 dark:text-amber-300">{{ t('aiOps.runFailed') }}</p>
            </div>

            <template v-if="selected.result">
              <div class="card space-y-3 p-5"><h2 class="font-semibold">{{ t('aiOps.summary') }}</h2><p class="whitespace-pre-wrap break-words text-sm leading-6" data-testid="ai-ops-summary">{{ selected.result.summary }}</p></div>

              <div class="card space-y-4 p-5">
                <h2 class="font-semibold">{{ t('aiOps.findings') }}</h2>
                <p v-if="!selected.result.findings.length" class="text-sm text-gray-500">{{ t('aiOps.noFindings') }}</p>
                <article v-for="(finding, index) in selected.result.findings" :key="index" class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-700" data-testid="ai-ops-finding">
                  <div class="flex flex-wrap gap-2 text-xs"><span class="rounded bg-gray-100 px-2 py-1 dark:bg-dark-800">{{ t(`aiOps.category.${finding.category}`) }}</span><span :class="severityClass(finding.severity)" class="rounded px-2 py-1">{{ t(`aiOps.severity.${finding.severity}`) }}</span></div>
                  <p class="whitespace-pre-wrap break-words text-sm leading-6">{{ finding.description }}</p>
                  <p class="whitespace-pre-wrap break-words text-sm leading-6"><strong>{{ t('aiOps.recommendation') }}：</strong>{{ finding.recommendation }}</p>
                  <p v-if="finding.account_ids.length" class="text-xs text-gray-500">{{ t('aiOps.accounts') }}：{{ finding.account_ids.map(id => `#${id}`).join(', ') }}</p>
                  <p class="break-words text-xs text-gray-500">{{ t('aiOps.evidenceRefs') }}：{{ finding.evidence_ids.join(', ') || '—' }}</p>
                </article>
              </div>

              <div class="card space-y-4 p-5">
                <h2 class="font-semibold">{{ t('aiOps.proposals') }}</h2>
                <p v-if="!selected.result.proposals.length" class="text-sm text-gray-500">{{ t('aiOps.noProposals') }}</p>
                <article v-for="(proposal, index) in selected.result.proposals" :key="index" class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-700" data-testid="ai-ops-proposal">
                  <div class="flex flex-wrap items-center gap-2"><strong class="text-sm">{{ t(`aiOps.action.${proposal.action}`) }}</strong><span class="rounded bg-gray-100 px-2 py-1 text-xs dark:bg-dark-800">{{ t('aiOps.notExecuted') }}</span></div>
                  <p v-if="proposal.account_id != null" class="text-sm">{{ t('aiOps.account', { id: proposal.account_id }) }}<span v-if="proposal.proposed_value != null"> · {{ t('aiOps.proposedValue', { value: proposal.proposed_value }) }}</span></p>
                  <p class="whitespace-pre-wrap break-words text-sm leading-6">{{ proposal.reason }}</p>
                  <p class="break-words text-xs text-gray-500">{{ t('aiOps.evidenceRefs') }}：{{ proposal.evidence_ids.join(', ') || '—' }}</p>
                </article>
              </div>

              <div v-if="selected.result.limitations.length" class="card space-y-3 p-5"><h2 class="font-semibold">{{ t('aiOps.limitations') }}</h2><ul class="list-disc space-y-2 pl-5 text-sm leading-6"><li v-for="(limitation, index) in selected.result.limitations" :key="index" class="whitespace-pre-wrap break-words">{{ limitation }}</li></ul></div>
            </template>

            <div class="card space-y-3 p-5">
              <h2 class="font-semibold">{{ t('aiOps.evidence') }}</h2>
              <p v-if="!selected.evidence.length" class="text-sm text-gray-500">{{ t('aiOps.evidenceEmpty') }}</p>
              <details v-for="evidence in selected.evidence" :key="evidence.id" class="rounded-lg border border-gray-200 p-3 dark:border-dark-700" data-testid="ai-ops-evidence">
                <summary class="cursor-pointer break-words text-sm"><span class="font-mono">{{ evidence.id }}</span> · {{ evidence.source }} · {{ t(`aiOps.evidenceStatus.${evidence.status}`) }}</summary>
                <p class="mt-3 text-xs text-gray-500">{{ t('aiOps.collectedAt', { time: formatTime(evidence.collected_at) }) }}</p>
                <pre class="mt-3 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-800">{{ JSON.stringify(evidence.metrics, null, 2) }}</pre>
              </details>
            </div>
          </template>
        </section>
      </div>
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
const canStart = computed(() => authStore.isAdmin && status.value?.enabled && status.value.configured && !status.value.running && !loading.value && !submitting.value && costConsent.value && !needsRefreshAfterSubmission.value)
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

function severityClass(severity: string): string {
  if (severity === 'critical') return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
  if (severity === 'warning') return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
  return 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-300'
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
    needsRefreshAfterSubmission.value = false
  }
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
  createController = new AbortController()
  try {
    const run = await aiOpsAPI.create(createController.signal)
    if (!alive) return
    costConsent.value = false
    selectedId.value = run.id
    selected.value = run
    if (!runs.value.some(item => item.id === run.id)) total.value++
    runs.value = [run, ...runs.value.filter(item => item.id !== run.id)]
    if (status.value) status.value.running = isPending(run)
    startPolling()
    await loadOverview()
  } catch {
    if (alive) {
      error.value = t('aiOps.startFailed')
      needsRefreshAfterSubmission.value = true
    }
  } finally {
    if (alive) submitting.value = false
  }
}

onMounted(() => { if (authStore.isAdmin) void refresh() })
onBeforeUnmount(() => {
  alive = false
  stopPolling()
  overviewController?.abort()
  createController?.abort()
})
</script>
