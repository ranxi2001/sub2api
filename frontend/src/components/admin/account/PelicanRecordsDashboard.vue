<template>
  <div class="fixed inset-0 z-[60] overflow-y-auto bg-[#f6f8f7] p-4 dark:bg-dark-950 sm:p-6" role="dialog" aria-modal="true" :aria-label="t('admin.accounts.pelicanTest.history')" @keydown.esc.stop="selected = null">
    <div class="mx-auto max-w-[1800px]">
      <header class="mb-4 flex items-center justify-between gap-4">
        <div>
          <h2 class="text-xl font-semibold">{{ t('admin.accounts.pelicanTest.history') }}</h2>
          <p class="text-sm text-gray-500">{{ t('admin.accounts.pelicanTest.dashboardHint') }}</p>
          <p class="text-xs text-gray-500" data-testid="record-count">{{ t('admin.accounts.pelicanTest.recordCount', { count: cards.length }) }}</p>
        </div>
        <div class="flex gap-2"><button type="button" class="btn btn-secondary" :disabled="refreshing" @click="refresh">{{ t('common.refresh') }}</button>
        <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button></div>
      </header>
      <p v-if="loadError" role="alert" class="mb-4 text-sm text-red-600">{{ loadError }}</p>
      <div v-if="loading" class="py-20 text-center text-sm text-gray-500">{{ t('common.loading') }}...</div>
      <div v-else-if="cards.length" class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        <article v-for="card in visibleCards" :key="card.key" class="relative overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm transition hover:border-primary-300 hover:shadow-md dark:border-dark-700 dark:bg-dark-800" data-testid="pelican-record-card">
          <div class="p-4">
            <h3 class="font-semibold">{{ card.account.name }}</h3>
            <p class="mt-1 text-xs text-gray-500">#{{ card.account.id }}<span v-if="card.resultId"> · {{ t('admin.accounts.pelicanTest.recordId') }} #{{ card.resultId }}</span></p>
            <div class="mt-3 flex justify-between gap-2 text-xs">
              <span :class="statusClass(card.record)" data-testid="record-status">{{ statusLabel(card.record) }}</span>
              <span class="text-gray-500">{{ duration(card.record.durationMs) }} · {{ sourceLabel(card.record.source) }}</span>
            </div>
            <div class="mt-2 space-y-1 text-xs text-gray-500">
              <p>{{ card.record.modelId || '—' }} / {{ card.record.reasoningEffort || '—' }}</p>
              <p>{{ t('admin.accounts.pelicanTest.generatedAt') }}：{{ formatDate(card.record.startedAt) }}</p>
            </div>
            <div class="mt-3 aspect-[4/3] overflow-hidden rounded-xl bg-gray-50">
              <iframe v-if="card.record.html" :srcdoc="card.record.html" class="pointer-events-none h-full w-full border-0" tabindex="-1" sandbox="allow-scripts" referrerpolicy="no-referrer" :title="card.account.name" />
              <p v-else-if="!card.loaded && !card.loadError" class="p-4 text-sm text-gray-500">{{ t('common.loading') }}...</p>
              <pre v-else-if="card.record.output && !card.record.html" class="whitespace-pre-wrap break-words p-4 text-sm">{{ card.record.output }}</pre>
              <p v-else class="p-4 text-sm text-red-500">{{ card.loadError || card.record.error || t('admin.accounts.pelicanTest.invalidHtml') }}</p>
            </div>
            <p v-if="card.record.error && !card.record.verdict" class="mt-2 line-clamp-2 break-words text-xs text-red-500">{{ card.record.error }}</p>
          </div>
          <div class="border-t border-gray-100 px-4 py-3 text-sm text-primary-600 dark:border-dark-700">{{ t('admin.accounts.pelicanTest.preview') }}</div>
          <button type="button" class="absolute inset-0 z-10 rounded-2xl focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500" :aria-label="`${card.account.name} · ${t('admin.accounts.pelicanTest.preview')}`" @click="openRecord(card)" />
        </article>
      </div>
      <div v-else class="rounded-xl border border-dashed border-gray-300 bg-white py-20 text-center text-sm text-gray-500 dark:bg-dark-800">{{ t('admin.accounts.pelicanTest.noHistory') }}</div>
      <button v-if="cards.length > visibleLimit" type="button" class="btn btn-secondary mt-4" data-testid="load-more" @click="showMore">{{ t('admin.accounts.pelicanTest.moreRecords') }}</button>
    </div>
    <div v-if="selected" class="fixed inset-0 z-[70] flex items-center justify-center bg-black/70 p-4" data-testid="record-detail" @click.self="selected = null">
      <div class="flex h-full w-full max-w-6xl flex-col overflow-hidden rounded-xl bg-white dark:bg-dark-800">
        <header class="flex items-center justify-between gap-3 border-b px-4 py-3">
          <div class="text-sm">
            <strong>{{ selected.account.name }}</strong>
            <p>{{ sourceLabel(selected.record.source) }} · {{ selected.record.modelId || '—' }} / {{ selected.record.reasoningEffort || '—' }} · {{ duration(selected.record.durationMs) }}</p>
            <p class="text-xs text-gray-500">{{ formatDate(selected.record.startedAt) }}</p>
          </div>
          <button type="button" class="btn btn-secondary" @click="selected = null">{{ t('common.close') }}</button>
        </header>
        <iframe v-if="selected.record.html" :srcdoc="selected.record.html" class="min-h-0 w-full flex-1 border-0" sandbox="allow-scripts" referrerpolicy="no-referrer" :title="selected.account.name" />
        <p v-else-if="!selected.loaded && !selected.loadError" class="p-4">{{ t('common.loading') }}...</p>
        <pre v-else class="overflow-auto whitespace-pre-wrap p-4 text-sm">{{ detailText(selected) }}</pre>
      </div>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { scheduledTestsAPI } from '@/api/admin/scheduledTests'
import type { PelicanHistoryResult } from '@/api/admin/scheduledTests'
import { extractPelicanHtml } from '@/utils/pelicanHtml'
import { isTextAnswerKind, stateProbeVerdict, type StateProbeVerdict } from '@/utils/intelligenceTest'
import type { Account, AccountListItem, PelicanTestConfig, ScheduledTestResult } from '@/types'

interface ManualRun {
  id?: string
  html?: string
  output?: string
  error?: string
  status?: string
  durationMs?: number
  startedAt?: string
  modelId?: string
  reasoningEffort?: string
  questionKind?: PelicanTestConfig['question_kind']
}
interface ManualRecord {
  id?: string
  createdAt: string
  modelId: string
  reasoningEffort: string
  questionKind?: PelicanTestConfig['question_kind']
  runs: ManualRun[]
}
interface DisplayRecord {
  source: 'manual' | 'scheduled'
  startedAt?: string
  durationMs?: number
  modelId?: string
  reasoningEffort?: string
  status: string
  // 定时探针结果的判定；有判定时优先展示判定和探针摘要，不展示原始错误标记。
  verdict?: StateProbeVerdict
  html: string
  output: string
  error: string
}
interface Card {
  key: string
  account: Pick<AccountListItem, 'id' | 'name'>
  record: DisplayRecord
  planId?: number
  resultId?: number
  loaded: boolean
  loadError?: string
}
const props = defineProps<{ accounts: AccountListItem[]; account: Account | null; manualRecord: ManualRecord | null }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
const loading = ref(true)
const refreshing = ref(false)
const selected = ref<Card | null>(null)
const cards = ref<Card[]>([])
const loadError = ref('')
const visibleLimit = ref(24)
const visibleCards = computed(() => cards.value.slice(0, visibleLimit.value))
let alive = true
let timer: ReturnType<typeof setTimeout> | undefined
const inFlight = new Map<string, Promise<void>>()
function sourceLabel(source: DisplayRecord['source']) { return t(`admin.accounts.pelicanTest.${source === 'manual' ? 'sourceManual' : 'sourceScheduled'}`) }
function formatDate(value?: string) {
  if (!value || !Number.isFinite(Date.parse(value))) return '—'
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value))
}
const verdictKeys: Record<StateProbeVerdict, string> = { healthy: 'verdictHealthy', degraded: 'verdictDegraded', inconclusive: 'verdictInconclusive' }
function statusLabel(record: DisplayRecord) {
  if (record.verdict) return t(`admin.accounts.pelicanTest.probe.${verdictKeys[record.verdict]}`)
  return t(record.status === 'success' ? 'admin.accounts.pelicanTest.success' : 'admin.accounts.pelicanTest.failed')
}
function statusClass(record: DisplayRecord) {
  if (record.verdict === 'inconclusive') return 'text-gray-500'
  if (record.verdict) return record.verdict === 'healthy' ? 'text-emerald-600' : 'text-red-500'
  return record.status === 'success' ? 'text-emerald-600' : 'text-red-500'
}
function detailText(card: Card) {
  if (card.loadError) return card.loadError
  if (card.record.verdict) return card.record.output || card.record.error
  return card.record.error || card.record.output
}
function duration(value?: number) { return typeof value === 'number' && Number.isFinite(value) ? `${(value / 1000).toFixed(1)} s` : '—' }
function timestamp(record: DisplayRecord) { return record.startedAt ? Date.parse(record.startedAt) || 0 : 0 }
function manualCards(server: PelicanHistoryResult[]): Card[] {
  const accounts = new Map<number, Card['account']>(props.accounts.map(account => [account.id, account]))
  for (const result of server) accounts.set(result.account_id, { id: result.account_id, name: result.account_name })
  if (props.account) accounts.set(props.account.id, props.account)
  const saved = new Map<number, ManualRecord[]>()
  // Read only Pelican keys, across all account pages; never inspect auth storage.
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      const match = key?.match(/^sub2api-pelican-test:(\d+)$/)
      if (!match || !key) continue
      try {
        const records = JSON.parse(localStorage.getItem(key) || '[]')
        if (Array.isArray(records)) saved.set(Number(match[1]), records)
      } catch { /* Ignore only this corrupt record. */ }
    }
  } catch { /* The current in-memory result still works when storage is unavailable. */ }
  if (props.account && props.manualRecord) {
    saved.set(props.account.id, [props.manualRecord, ...(saved.get(props.account.id) || [])])
  }
  const results = new Map<string, Card>()
  for (const [id, records] of saved) {
    const account = accounts.get(id) || { id, name: `#${id}` }
    for (const record of records) {
      if (!Array.isArray(record?.runs)) continue
      record.runs.forEach((run, index) => {
        if (!run || typeof run !== 'object') return
        const key = `manual:${id}:${record.id || record.createdAt}:${run.id || index}`
        if (results.has(key)) return
        const output = run.output || run.html || ''
        const html = isTextAnswerKind(run.questionKind || record.questionKind) ? '' : extractPelicanHtml(output)
        results.set(key, { key, account, loaded: true, record: {
          source: 'manual', startedAt: run.startedAt || record.createdAt, durationMs: run.durationMs,
          modelId: run.modelId || record.modelId, reasoningEffort: run.reasoningEffort || record.reasoningEffort,
          status: run.status || (html ? 'success' : 'error'), html, output, error: run.error || ''
        } })
      })
    }
  }
  return [...results.values()]
}
function serverRecord(result: ScheduledTestResult): DisplayRecord {
  return { source: 'scheduled', startedAt: result.started_at, durationMs: result.latency_ms,
    modelId: result.pelican_config?.model_id, reasoningEffort: result.pelican_config?.reasoning_effort,
    status: result.status, verdict: stateProbeVerdict(result) ?? undefined, output: result.response_text || '',
    html: isTextAnswerKind(result.pelican_config?.question_kind) ? '' : extractPelicanHtml(result.response_text || ''), error: result.error_message }
}
async function loadBody(card: Card) {
  if (card.loaded || !card.planId || !card.resultId || !alive) return
  const pending = inFlight.get(card.key)
  if (pending) return pending
  const work = (async () => {
    try {
      const result = await scheduledTestsAPI.getResult(card.planId!, card.resultId!)
      if (alive) { card.record = serverRecord(result); card.loaded = true; card.loadError = '' }
    } catch {
      if (alive) card.loadError = t('admin.accounts.pelicanTest.recordLoadError')
    }
  })()
  inFlight.set(card.key, work)
  try { await work } finally { inFlight.delete(card.key) }
}
async function hydrateVisible() {
  const visible = visibleCards.value
  let cursor = 0
  await Promise.all(Array.from({ length: Math.min(4, visible.length) }, async () => {
    while (alive && cursor < visible.length) await loadBody(visible[cursor++])
  }))
}
async function openRecord(card: Card) { selected.value = card; await loadBody(card) }
async function showMore() { visibleLimit.value += 24; await hydrateVisible() }
async function refresh() {
  if (!alive || refreshing.value) return
  refreshing.value = true
  try {
    const server: PelicanHistoryResult[] = []
    let cursor = 0
    do {
      const page = await scheduledTestsAPI.listPelicanHistory(cursor)
      if (!alive) return
      server.push(...page.items)
      if (page.next_cursor && cursor && page.next_cursor >= cursor) throw new Error('Invalid history cursor')
      cursor = page.next_cursor
    } while (cursor)
    const previous = new Map(cards.value.map(card => [card.key, card]))
    const merged = new Map(manualCards(server).map(card => [card.key, card]))
    for (const result of server) {
      const key = `scheduled:${result.id}`
      const existing = previous.get(key)
      merged.set(key, existing || { key, account: { id: result.account_id, name: result.account_name },
        record: serverRecord(result), planId: result.plan_id, resultId: result.id, loaded: false })
    }
    cards.value = [...merged.values()].sort((a, b) => timestamp(b.record) - timestamp(a.record) || (b.resultId || 0) - (a.resultId || 0))
    loadError.value = ''
  } catch {
    if (alive) {
      // Keep already loaded history, and show available manual records even on initial API failure.
      if (!cards.value.length) cards.value = manualCards([])
      loadError.value = t('admin.accounts.pelicanTest.historyLoadError')
    }
  } finally {
    if (alive) loading.value = false
  }
  await hydrateVisible()
  refreshing.value = false
}
async function poll() {
  await refresh()
  if (alive) timer = setTimeout(poll, 15000)
}
onMounted(poll)
onBeforeUnmount(() => { alive = false; clearTimeout(timer) })
</script>
