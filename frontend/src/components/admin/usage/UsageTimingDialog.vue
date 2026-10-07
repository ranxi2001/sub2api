<template>
  <BaseDialog :show="!!record" :title="t('requestTiming.title')" width="extra-wide" placement="right" close-on-click-outside @close="$emit('close')">
    <div v-if="record" class="space-y-5">
      <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-800">
        <div class="flex flex-wrap justify-between gap-2"><strong>{{ record.model }}</strong><span>{{ formatDateTime(record.created_at) }}</span></div>
        <div class="mt-2 break-all font-mono text-xs text-gray-500">{{ record.request_id }}</div>
      </div>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-3">
        <div v-for="item in summaries" :key="item.label" class="rounded-xl border p-4" :class="TIMING_SURFACE[item.health]">
          <div class="text-sm text-gray-600 dark:text-gray-400">{{ item.label }}</div>
          <div class="mt-2 flex flex-wrap items-baseline justify-between gap-2" :class="TIMING_TEXT[item.health]">
            <strong class="text-xl tabular-nums">{{ ms(item.value) }}</strong>
            <span v-if="item.health !== 'neutral'" class="text-xs font-semibold">{{ healthLabel(item.health) }}</span>
          </div>
        </div>
      </div>
      <p v-if="loading" role="status">{{ t('requestTiming.loading') }}</p>
      <div v-else-if="error" role="alert"><p>{{ t('requestTiming.loadError') }}</p><button class="btn btn-secondary mt-2" @click="load">{{ t('requestTiming.retry') }}</button></div>
      <p v-else-if="!trace" class="rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800">{{ t('requestTiming.empty') }}</p>
      <template v-if="trace">
        <label v-if="traces.length > 1" class="block text-sm">{{ t('requestTiming.traceSelection') }}
          <select v-model="selected" class="input mt-2 w-full"><option v-for="(item, i) in traces" :key="item.trace_id" :value="i">{{ formatDateTime(item.started_at) }} · {{ ms(item.total_ms) }}</option></select>
        </label>
        <div class="rounded-xl border border-sky-200 bg-sky-50 p-4 dark:border-sky-900 dark:bg-sky-950/30">
          <h4 class="font-semibold text-sky-900 dark:text-sky-200">{{ t('requestTiming.health.guideTitle') }}</h4>
          <p class="mt-1 text-sm text-sky-800 dark:text-sky-300">{{ diagnosis }}</p>
          <p v-if="largestStage" class="mt-2 text-sm text-sky-800 dark:text-sky-300">{{ t('requestTiming.health.largest', { stage: label(largestStage.name), time: ms(largestStage.end_ms - largestStage.start_ms) }) }}</p>
          <div class="mt-3 flex flex-wrap gap-x-4 gap-y-2 text-xs">
            <span v-for="health in healthLevels" :key="health" class="inline-flex items-center gap-1.5 font-medium" :class="TIMING_TEXT[health]"><span class="h-2 w-2 rounded-full" :class="TIMING_BAR[health]" />{{ healthLabel(health) }}</span>
          </div>
          <details class="mt-3 text-xs text-sky-800 dark:text-sky-300"><summary class="cursor-pointer">{{ t('requestTiming.health.thresholdTitle') }}</summary><p class="mt-2 leading-relaxed">{{ t('requestTiming.health.thresholds') }}</p></details>
        </div>
        <p class="text-xs leading-relaxed text-gray-500 dark:text-gray-400">{{ t('requestTiming.scope') }}</p>
        <p class="text-sm"><strong>{{ t('usage.latencyTps') }} {{ formatUsageOutputTps(record) ?? '—' }}</strong> · {{ tpsUnavailableReason ? t(`usage.latencyTpsUnavailable.${tpsUnavailableReason}`) : t('requestTiming.tpsNote') }}</p>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('requestTiming.health.attemptGuide') }}</p>
        <p v-if="trace.truncated" class="text-amber-600">{{ t('requestTiming.truncated') }}</p>
        <div class="grid gap-4 lg:grid-cols-3">
          <section v-for="group in groups" :key="group.title" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
            <h4 class="mb-4 font-semibold">{{ group.title }}</h4>
            <dl class="space-y-3"><div v-for="row in group.rows" :key="row.name" class="flex justify-between gap-4 text-sm"><dt class="text-gray-500">{{ row.name }}</dt><dd class="shrink-0 text-right font-medium tabular-nums" :class="TIMING_TEXT[row.health]">{{ row.value }}<span v-if="row.health !== 'neutral'" class="mt-0.5 block text-[10px] font-normal">{{ healthLabel(row.health) }}</span></dd></div></dl>
          </section>
        </div>
        <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
          <h4 class="mb-3 font-semibold">{{ t('requestTiming.timeline') }}</h4>
          <p class="mb-3 text-xs text-gray-500">{{ t('requestTiming.overlap') }}</p>
          <div v-for="(span, i) in orderedSpans" :key="i" class="mb-3">
            <div class="flex justify-between gap-2 text-xs"><span>{{ label(span.name) }}</span><span :class="TIMING_TEXT[spanHealth(span)]">{{ ms(span.start_ms) }} → {{ ms(span.end_ms) }} · <strong>{{ ms(span.end_ms - span.start_ms) }}</strong></span></div>
            <div class="mt-1 h-2 overflow-hidden rounded bg-gray-100 dark:bg-dark-700"><div class="h-full rounded" :class="TIMING_BAR[spanHealth(span)]" :style="bar(span)" /></div>
          </div>
        </section>
        <section v-for="attempt in trace.attempts" :key="attempt.number" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
          <h4 class="font-semibold">{{ attemptTitle(attempt) }} · {{ t('requestTiming.account') }} #{{ attempt.account_id }} · {{ attempt.proxy_id > 0 ? `${t('requestTiming.proxy')} #${attempt.proxy_id}` : t('requestTiming.direct') }}</h4>
          <div class="mt-3 grid gap-x-8 gap-y-3 text-sm md:grid-cols-2">
            <div v-for="row in attemptRows(attempt)" :key="row.name" class="flex justify-between gap-4"><span class="text-gray-500">{{ row.name }}</span><span class="text-right font-medium tabular-nums" :class="TIMING_TEXT[row.health]">{{ row.value }}<span v-if="row.health !== 'neutral'" class="mt-0.5 block text-[10px] font-normal">{{ healthLabel(row.health) }}</span></span></div>
          </div>
        </section>
        <div class="break-all text-xs text-gray-500">Trace: {{ trace.trace_id }} · {{ t('requestTiming.retention', { days: retention }) }}</div>
      </template>
    </div>
  </BaseDialog>
</template>
<script setup lang="ts">
import { inject, computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { AdminUsageLog } from '@/types'
import { formatDateTime } from '@/utils/format'
import { formatUsageOutputTps, usageOutputTpsUnavailableReason } from '@/utils/usageTps'
import { timingHealth, stageScale, TIMING_TEXT, TIMING_SURFACE, TIMING_BAR, type TimingHealth, type TimingScale } from '@/utils/requestTimingHealth'
import { getUsageTiming, type RequestTiming, type TimingAttempt, type TimingSpan } from '@/api/admin/usageTiming'
import { observerUsageAPI } from '@/api/observerUsage'
import { observerUsageContext } from './observerUsageContext'
const observerMode = inject(observerUsageContext, false)
const props = defineProps<{ record: AdminUsageLog | null }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
const traces = ref<RequestTiming[]>([]), selected = ref(0), loading = ref(false), error = ref(false), retention = ref(30)
let controller: AbortController | undefined
const trace = computed(() => traces.value[selected.value])
const tpsUnavailableReason = computed(() => usageOutputTpsUnavailableReason(props.record))
const label = (name: string) => t(`requestTiming.fields.${name}`)
const ms = (value: number | null | undefined) => value == null ? t('requestTiming.missing') : value < 1 ? '<1ms' : value < 1000 ? `${value.toFixed(0)}ms` : `${(value / 1000).toFixed(2)}s`
const bytes = (value: number) => value < 0 ? t('requestTiming.missing') : `${(value / 1048576).toFixed(3)} MiB`
const yes = (value: boolean | null) => value == null ? t('requestTiming.missing') : t(value ? 'requestTiming.yes' : 'requestTiming.no')
const delta = (events: Record<string, number>, start: string, end: string) => events[start] == null || events[end] == null || events[end] < events[start] ? undefined : events[end] - events[start]
const duration = (name: string) => { const items = trace.value?.spans.filter(s => s.name === name); return items?.length ? items.reduce((n, s) => n + s.end_ms - s.start_ms, 0) : undefined }
const healthLevels: TimingHealth[] = ['good', 'warn', 'slow', 'critical', 'neutral']
const healthLabel = (health: TimingHealth) => t(`requestTiming.health.${health}`)
interface DetailRow { name: string; value: string; health: TimingHealth }
const row = (name: string, value: string, health: TimingHealth = 'neutral'): DetailRow => ({ name: label(name), value, health })
const timed = (name: string, value: number | null | undefined, scale: TimingScale = 'stage') => row(name, ms(value), timingHealth(value, scale))
const flag = (name: string, value: boolean | null | undefined, goodValue: boolean, negative: TimingHealth = 'warn') => row(name, yes(value ?? null), value == null ? 'neutral' : value === goodValue ? 'good' : negative)
const statusHealth = (status: number): TimingHealth => status >= 500 ? 'critical' : status >= 400 ? 'warn' : status >= 200 && status < 300 ? 'good' : 'neutral'
const summaries = computed(() => [
  { label: t('requestTiming.legacyFirst'), value: props.record?.first_token_ms, scale: 'first' as const },
  { label: t('requestTiming.legacyTotal'), value: props.record?.duration_ms, scale: 'total' as const },
  { label: t('requestTiming.requestTotal'), value: trace.value?.total_ms, scale: 'total' as const },
  { label: label('first_semantic'), value: trace.value?.events.first_semantic, scale: 'first' as const },
  { label: label('first_visible'), value: trace.value?.events.first_visible, scale: 'first' as const },
  { label: label('first_output_flush'), value: trace.value?.events.first_output_flush, scale: 'first' as const }
].map(item => ({ ...item, health: timingHealth(item.value, item.scale) })))
const diagnosis = computed(() => {
  const d = trace.value
  if (!d) return ''
  if (d.outcome === 'failed' || d.terminal === 'failed' || d.downstream_error || d.status >= 400) return t('requestTiming.health.failed')
  if (d.client_disconnect || d.canceled || d.terminal === 'incomplete') return t('requestTiming.health.interrupted')
  const first = d.events.first_visible ?? d.events.first_semantic
  if (first == null) return t('requestTiming.health.insufficient')
  return t(timingHealth(first, 'first') === 'good' ? 'requestTiming.health.firstGood' : 'requestTiming.health.firstSlow', { time: ms(first) })
})
const largestStage = computed(() => {
  const candidates = (trace.value?.spans ?? []).filter(s => stageScale(s.name) === 'stage')
  return candidates.reduce<TimingSpan | undefined>((best, s) => !best || s.end_ms - s.start_ms > best.end_ms - best.start_ms ? s : best, undefined)
})
const spanHealth = (span: TimingSpan) => timingHealth(span.end_ms - span.start_ms, stageScale(span.name))
const groups = computed(() => {
  const d = trace.value
  if (!d) return []
  const e = d.events
  return [
    { title: t('requestTiming.inbound'), rows: [
      timed('body_read_start', e.body_read_start, 'neutral'), timed('body_first_wait', delta(e, 'body_read_start', 'body_first_byte')),
      timed('body_receive', delta(e, 'body_first_byte', 'body_received')), timed('body_read_ms', e.body_read_start == null ? undefined : d.body_read_ms),
      row('body_bytes', bytes(d.body_bytes)), row('body_rate', e.body_read_start == null ? t('requestTiming.missing') : d.body_read_ms < 1 ? t('requestTiming.health.readTooBrief') : `${(d.body_bytes / 1048576 / (d.body_read_ms / 1000)).toFixed(2)} MiB/s`),
      flag('body_complete', e.body_read_start == null ? null : d.body_complete, true)
    ] },
    { title: t('requestTiming.internal'), rows: ['api_key_auth', 'model_allowlist', 'composite_routing', 'handler_body_read', 'security_audit', 'billing_check', 'user_queue', 'account_selection', 'account_queue', 'upstream_credentials', 'build_upstream_request', 'handler'].map(name => timed(name, duration(name), stageScale(name))) },
    { title: t('requestTiming.result'), rows: [
      row('status', String(d.status), statusHealth(d.status)), row('attempts', String(d.attempts.filter(a => a.kind !== 'http').length)),
      row('terminal', d.terminal ? t(`requestTiming.${d.terminal}`) : t('requestTiming.missing'), d.terminal === 'completed' ? 'good' : d.terminal === 'failed' ? 'critical' : d.terminal === 'incomplete' ? 'warn' : 'neutral'),
      row('outcome', d.outcome ? t(`requestTiming.${d.outcome}`) : t('requestTiming.missing'), d.outcome === 'success' ? 'good' : d.outcome === 'failed' ? 'critical' : d.outcome === 'client_disconnected' ? 'warn' : 'neutral'),
      flag('client_disconnect', d.client_disconnect, false), flag('canceled', d.canceled, false), flag('downstream_error', d.downstream_error, false, 'critical'), row('downstream_bytes', bytes(d.downstream_bytes)),
      timed('downstream_write_ms', d.downstream_write_ms, 'neutral'), row('ttft_mode', d.ttft_mode || t('requestTiming.missing'))
    ] }
  ]
})
const orderedSpans = computed(() => [...(trace.value?.spans ?? [])].sort((a, b) => a.start_ms - b.start_ms))
const bar = (span: TimingSpan) => { const total = Math.max(trace.value?.total_ms ?? 1, 1); return { marginLeft: `${Math.min(100, span.start_ms / total * 100)}%`, width: `${Math.max(0.2, (span.end_ms - span.start_ms) / total * 100)}%`, maxWidth: '100%' } }
function attemptTitle(a: TimingAttempt): string {
  const attempts = trace.value?.attempts ?? []
  const egress = attempts.filter(item => item.kind !== 'http')
  if (a.kind !== 'http') return `${t('requestTiming.attempt')} #${egress.findIndex(item => item.number === a.number) + 1}`
  const parent = egress.findIndex(item => item.number === a.parent) + 1
  const child = attempts.filter(item => item.kind === 'http' && item.parent === a.parent).findIndex(item => item.number === a.number) + 1
  return `${t('requestTiming.httpAttempt')} #${parent}.${child}`
}
function attemptRows(a: TimingAttempt): DetailRow[] {
  const e = a.events
  return [
    timed('attempt_total', a.end_ms == null ? undefined : a.end_ms - a.start_ms, 'neutral'), row('status', a.status > 0 ? String(a.status) : t('requestTiming.missing'), statusHealth(a.status)),
    row('transport_result', a.cleanup_canceled ? t('requestTiming.health.normalClose') : a.error === 'canceled' && !a.terminal ? t('requestTiming.health.legacyCancel') : a.error ? t(`requestTiming.${a.error}`) : t('requestTiming.health.noTransportError'), a.error ? (a.error === 'canceled' ? 'warn' : 'critical') : a.cleanup_canceled ? 'good' : 'neutral'),
    // A new connection and closing after the terminal event without EOF are normal.
    row('reused', yes(a.reused), a.reused === true ? 'good' : 'neutral'), timed('connection', delta(e, 'connection_start', 'connection_ready')),
    ...['dns', 'tcp', 'tls'].map(name => ({ name: name.toUpperCase(), value: ms(delta(e, `${name}_start`, `${name}_end`)), health: timingHealth(delta(e, `${name}_start`, `${name}_end`), 'stage') })),
    timed('request_write', delta(e, 'connection_ready', 'request_written')), timed('wait_first_byte', delta(e, 'request_written', 'first_byte'), 'first'),
    timed('first_semantic', e.first_semantic, 'neutral'), timed('first_visible', e.first_visible, 'neutral'),
    timed('response_headers', e.response_headers == null ? undefined : e.response_headers - a.start_ms, 'first'),
    timed('stream_transfer', a.end_ms == null || e.response_headers == null ? undefined : a.end_ms - e.response_headers, 'neutral'),
    row('request_bytes', bytes(a.request_bytes)), row('response_bytes', bytes(a.response_bytes)), row('body_eof', yes(a.body_eof))
  ]
}
async function load() {
  controller?.abort(); const current = new AbortController(); controller = current
  traces.value = []; selected.value = 0; error.value = false
  if (!props.record) { loading.value = false; return }
  loading.value = true
  try { const data = await (observerMode ? observerUsageAPI.getTiming : getUsageTiming)(props.record.id, current.signal); if (controller === current) { traces.value = data.traces; retention.value = data.retention_days } }
  catch { if (!current.signal.aborted && controller === current) error.value = true }
  finally { if (controller === current) loading.value = false }
}
watch(() => props.record?.id, load, { immediate: true })
onUnmounted(() => controller?.abort())
</script>
