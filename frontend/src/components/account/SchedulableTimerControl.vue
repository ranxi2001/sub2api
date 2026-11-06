<template>
  <div class="mb-1 min-w-0 text-left text-xs" @click.stop>
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" data-test="timer-manage" class="text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="busy || loading || switching" :aria-expanded="expanded" @click="begin()">{{ t('admin.accounts.timer.manage') }}</button>
      <NewFeatureBadge v-if="canManageTimers && noticeUserId != null" :notice-id="NEW_FEATURE_NOTICES.accountDailyScheduling" :user-id="noticeUserId" compact />
    </div>
    <p v-if="dailyWindow" data-test="timer-daily-window" class="mt-1 max-w-72 whitespace-normal break-words text-gray-600 dark:text-dark-300">{{ dailyWindow }}</p>
    <p v-if="error && !expanded" class="mt-1 max-w-80 whitespace-normal text-red-600" role="alert">{{ error }}</p>
    <button v-if="loadFailed" type="button" class="text-primary-600 underline" @click="load">{{ t('admin.accounts.timer.reload') }}</button>
    <BaseDialog v-if="expanded" :show="expanded" :title="accountName ? `${t('admin.accounts.timer.manage')} · ${accountName}` : t('admin.accounts.timer.manage')" width="normal" :close-on-escape="!busy && !switching" :show-close-button="!busy && !switching" @close="expanded = false">
      <div data-test="timer-form" class="max-w-full space-y-3 whitespace-normal text-sm">
      <p v-if="error" class="text-red-600" role="alert">{{ error }}</p>
      <button v-if="loadFailed" type="button" data-test="timer-reload" class="text-primary-600 underline" @click="begin(action)">{{ t('admin.accounts.timer.reload') }}</button>
      <fieldset v-if="canManageTimers" :disabled="busy || switching || loadFailed" class="min-w-0 space-y-2">
        <legend class="mb-2 font-medium">{{ t('admin.accounts.timer.manage') }}</legend>
        <label class="block">{{ t('admin.accounts.timer.planType') }}
          <select v-model="planType" data-test="timer-plan-type" class="input mt-1 w-full text-xs">
            <option value="daily">{{ t('admin.accounts.timer.daily') }}</option>
            <option value="once">{{ t('admin.accounts.timer.once') }}</option>
          </select>
        </label>
        <div v-if="form.mode === 'daily'" class="space-y-2">
          <div class="flex items-center gap-1"><span>{{ t('admin.accounts.timer.daily') }}</span><NewFeatureBadge v-if="noticeUserId != null" :notice-id="NEW_FEATURE_NOTICES.accountDailyScheduling" :user-id="noticeUserId" compact /></div>
          <div class="grid grid-cols-2 gap-2">
            <label>{{ t('admin.accounts.timer.openTime') }}<input v-model="form.openTime" data-test="timer-open-time" type="time" step="60" class="input mt-1 w-full text-xs" /></label>
            <label>{{ t('admin.accounts.timer.closeTime') }}<input v-model="form.closeTime" data-test="timer-close-time" type="time" step="60" class="input mt-1 w-full text-xs" /></label>
          </div>
          <p data-test="timer-draft-window" class="text-primary-700 dark:text-primary-300">{{ windowText(form.openTime, form.closeTime, form.timezone) }}</p>
          <p data-test="timer-mode-hint" class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.timer.dailyHint') }}</p>
        </div>
        <template v-else>
          <label class="block">{{ t('admin.accounts.timer.action') }}
            <select v-model="action" data-test="timer-action" class="input mt-1 w-full text-xs" @change="editOneShot(action)">
              <option value="open">{{ t('admin.accounts.timer.openAction') }}</option>
              <option value="close">{{ t('admin.accounts.timer.closeAction') }}</option>
            </select>
          </label>
          <label class="block">{{ t('admin.accounts.timer.mode') }}
            <select v-model="form.mode" data-test="timer-mode" class="input mt-1 w-full text-xs">
              <option value="relative">{{ t('admin.accounts.timer.relative') }}</option>
              <option value="absolute">{{ t('admin.accounts.timer.absolute') }}</option>
            </select>
          </label>
          <div v-if="form.mode === 'relative'" class="space-y-1">
            <div class="grid grid-cols-2 gap-2">
              <label>{{ t('admin.accounts.timer.hours') }}<input v-model.number="form.hours" type="number" min="0" step="1" :max="Math.floor(maxMinutes / 60)" class="input mt-1 w-full text-xs" /></label>
              <label>{{ t('admin.accounts.timer.minutes') }}<input v-model.number="form.minutes" type="number" min="0" step="1" :max="maxMinutes" class="input mt-1 w-full text-xs" /></label>
            </div>
            <button type="button" class="text-primary-600 underline" @click="form.hours = 8; form.minutes = 0">{{ t('admin.accounts.timer.preset') }}</button>
            <p class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.timer.relativeHint') }}</p>
          </div>
          <div v-else class="space-y-1">
            <label class="block">{{ t('admin.accounts.timer.date') }}<input v-model="form.date" type="date" class="input mt-1 w-full text-xs" /></label>
            <label class="block">{{ t('admin.accounts.timer.time') }}<input v-model="form.time" data-test="timer-time" type="time" step="60" class="input mt-1 w-full text-xs" /></label>
            <p class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.timer.absoluteHint') }}</p>
          </div>
        </template>
        <label class="block">{{ t('admin.accounts.timer.timezone') }}<input v-model.trim="form.timezone" data-test="timer-zone" :aria-label="t('admin.accounts.timer.timezone')" class="input mt-1 w-full text-xs" placeholder="Asia/Shanghai" /></label>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="text-primary-600 underline" @click="form.timezone = 'Asia/Shanghai'">{{ t('admin.accounts.timer.beijing') }}</button>
          <button type="button" class="text-primary-600 underline" @click="form.timezone = 'UTC'">UTC</button>
        </div>
        <p class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.timer.zoneHint') }}</p>
        <button type="button" data-test="timer-preview" class="btn btn-secondary w-full text-xs" @click="previewTime">{{ t('admin.accounts.timer.preview') }}</button>
        <div v-if="preview || dailyPreview" class="space-y-2 rounded bg-primary-50 p-2 dark:bg-dark-700" aria-live="polite">
          <template v-if="dailyPreview">
            <p>{{ t('admin.accounts.timer.next') }}</p>
            <p data-test="timer-preview-open">{{ t('admin.accounts.timer.openAction') }} · {{ previewText(dailyPreview.open) }}</p>
            <p data-test="timer-preview-close">{{ t('admin.accounts.timer.closeAction') }} · {{ previewText(dailyPreview.close) }}</p>
          </template>
          <p v-else-if="preview" data-test="timer-preview-value">{{ previewText(preview) }}</p>
          <label v-if="form.mode === 'absolute'" class="flex items-start gap-2"><input v-model="confirmed" data-test="timer-confirm" type="checkbox" class="mt-0.5" />{{ t('admin.accounts.timer.confirm') }}</label>
          <button type="button" data-test="timer-save" class="btn btn-primary w-full text-xs" :disabled="form.mode === 'absolute' && !confirmed" @click="save">{{ t(`admin.accounts.timer.${form.mode === 'daily' ? 'saveDaily' : 'save'}`) }}</button>
        </div>
      </fieldset>
      <p v-if="canManageTimers" class="text-gray-500 dark:text-dark-400">{{ t(`admin.accounts.timer.${form.mode === 'daily' ? 'dailyPolicy' : 'policy'}`, { seconds: snapshot?.poll_seconds ?? 30, grace: (snapshot?.open_grace_seconds ?? 900) / 60, days: maxMinutes / 1440 }) }}</p>
      <section v-if="pending.length" class="space-y-2 border-t border-gray-200 pt-2 dark:border-dark-600">
        <h4 class="font-medium">{{ t('admin.accounts.timer.pending') }}</h4>
        <div v-for="task in pending" :key="task.action" class="break-words" aria-live="polite">
          <p v-if="task.recurrence === 'daily'" :data-test="`timer-daily-${task.action}`">{{ t(`admin.accounts.timer.${task.action}Action`) }} · {{ t('admin.accounts.timer.dailyAt', { time: task.daily_time || task.local_time }) }} · {{ task.timezone }}<br />{{ t('admin.accounts.timer.next') }} {{ taskLocalTime(task) }} · {{ countdown(task) }}</p>
          <p v-else>{{ t(`admin.accounts.timer.${task.action}Action`) }} · {{ taskLocalTime(task) }} · {{ countdown(task) }}</p>
          <div class="flex gap-2">
            <button type="button" :data-test="`timer-edit-${task.action}`" class="text-primary-600 hover:underline" :disabled="busy || switching" @click="begin(task.action)">{{ t('admin.accounts.timer.edit') }}</button>
            <button type="button" :data-test="`timer-cancel-${task.action}`" class="text-red-600 hover:underline" :disabled="busy || switching" @click="cancel(task)">{{ t('admin.accounts.timer.cancel') }}</button>
          </div>
          <p v-if="task.reason" class="text-amber-600">{{ reasonText(task.reason) }}</p>
        </div>
        <p class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.timer.cancelHint') }}</p>
      </section>
      <section v-if="schedulable !== undefined" class="space-y-2 border-t border-gray-200 pt-2 dark:border-dark-600">
        <p class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.timer.manualOverrideHint') }}</p>
        <button type="button" data-test="timer-immediate" class="btn btn-secondary w-full text-xs" :disabled="busy || loading || switching" @click="emit('toggle')">{{ t(`admin.accounts.timer.${schedulable ? 'immediateOff' : 'immediateOn'}`) }}</button>
      </section>
      <details v-if="snapshot?.history.length" class="text-gray-500 dark:text-dark-400">
        <summary class="cursor-pointer">{{ t('admin.accounts.timer.history') }} · {{ t(`admin.accounts.timer.results.${snapshot.history[0]?.result}`) }}</summary>
        <p v-for="(item, index) in snapshot.history" :key="index" class="mt-1 break-words">{{ t(`admin.accounts.timer.${item.action}Action`) }} · {{ localTime(item.execute_at, item.timezone) }}<br />{{ t('admin.accounts.timer.actual') }} {{ localTime(item.actual_at, item.timezone) }} · {{ t(`admin.accounts.timer.results.${item.result}`) }}<span v-if="item.reason"> · {{ reasonText(item.reason) }}</span></p>
      </details>
      <button type="button" class="text-gray-500 underline" :disabled="busy || switching" @click="expanded = false">{{ t('admin.accounts.timer.collapse') }}</button>
      </div>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { schedulableTimersAPI, type SchedulableTimer, type TimerAction, type TimerInput, type TimerPreview, type TimerSnapshot } from '@/api/admin/schedulableTimers'

const props = withDefaults(defineProps<{ accountId: number; accountName?: string; refreshKey?: number; noticeUserId?: number; schedulable?: boolean; switching?: boolean; canManageTimers?: boolean }>(), { canManageTimers: true })
const emit = defineEmits<{ executed: []; toggle: [] }>()
const { t } = useI18n()
const app = useAppStore()
const snapshot = ref<TimerSnapshot | null>(null)
const pending = computed(() => snapshot.value?.tasks.filter(task => task.status === 'pending') ?? [])
const dailyWindow = computed(() => {
  const open = pending.value.find(task => task.action === 'open' && task.recurrence === 'daily')
  const close = pending.value.find(task => task.action === 'close' && task.recurrence === 'daily')
  return open && close && open.timezone === close.timezone && open.daily_time && close.daily_time
    ? windowText(open.daily_time, close.daily_time, open.timezone) : ''
})
const maxMinutes = computed(() => snapshot.value?.max_delay_minutes ?? 366 * 1440)
const expanded = ref(false)
const action = ref<TimerAction>('open')
const editVersions = reactive({ open: 0, close: 0 })
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const loadFailed = ref(false)
const preview = ref<TimerPreview | null>(null)
const dailyPreview = ref<{ open: TimerPreview; close: TimerPreview } | null>(null)
const confirmed = ref(false)
const form = reactive({ mode: 'daily' as TimerInput['mode'], hours: 8, minutes: 0, date: '', time: '08:00', openTime: '08:00', closeTime: '01:00', timezone: 'Asia/Shanghai' })
const planType = computed({
  get: () => form.mode === 'daily' ? 'daily' : 'once',
  set: (value: string) => { if (value === 'daily') form.mode = 'daily'; else editOneShot(action.value) }
})
let epoch = 0
let received = 0
const now = ref(0)
let interval: ReturnType<typeof setInterval> | undefined
let refreshInterval: ReturnType<typeof setInterval> | undefined
let disposed = false
let loadSequence = 0

watch(form, () => { preview.value = null; dailyPreview.value = null; confirmed.value = false }, { flush: 'sync' })
watch(action, () => { preview.value = null; confirmed.value = false }, { flush: 'sync' })
watch(() => props.refreshKey, () => { expanded.value = false; void load() })
function reasonText(reason: string) { return t(`admin.accounts.timer.errors.${reason}`) }
function showError(err: unknown) {
  const code = (err as { response?: { data?: { reason?: string } } })?.response?.data?.reason?.replace(/^TIMER_/, '')
  const known = ['timezone', 'datetime', 'dst', 'duration', 'mode', 'past', 'confirmation', 'version', 'conflict', 'request', 'account_deleted']
  error.value = reasonText(code && known.includes(code) ? code : 'internal')
  app.showError(error.value)
}
async function load() {
  if (!props.canManageTimers) return
  const sequence = ++loadSequence
  loading.value = true
  try {
    const data = await schedulableTimersAPI.list(props.accountId)
    if (disposed || sequence !== loadSequence) return
    const executed = data.tasks.some(task => snapshot.value?.tasks.some(previous =>
      previous.action === task.action && previous.status === 'pending' &&
      ((task.status === 'executed' && previous.version === task.version) ||
       (previous.recurrence === 'daily' && task.recurrence === 'daily' && task.version > previous.version && task.execute_at !== previous.execute_at && task.actual_at !== null))))
    snapshot.value = data
    if (executed) emit('executed')
    epoch = Date.parse(data.server_now)
    received = performance.now()
    now.value = epoch
    if (loadFailed.value) error.value = ''
    loadFailed.value = false
  } catch {
    if (disposed || sequence !== loadSequence) return
    loadFailed.value = true
    error.value = reasonText('internal')
  } finally { if (sequence === loadSequence) loading.value = false }
}
function localParts(timestamp: string, timezone: string) {
  const parts = new Intl.DateTimeFormat('en-GB', { timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZoneName: 'longOffset' }).formatToParts(new Date(timestamp))
  const value = (key: string) => parts.find(part => part.type === key)?.value ?? ''
  return { date: `${value('year')}-${value('month')}-${value('day')}`, time: `${value('hour')}:${value('minute')}`, offset: value('timeZoneName').replace('GMT', 'UTC') }
}
function localTime(timestamp: string, timezone: string) {
  try { const local = localParts(timestamp, timezone); return `${local.date} ${local.time} · ${timezone} · ${local.offset}` }
  catch { return `${timestamp} · ${timezone}` }
}
function taskLocalTime(task: SchedulableTimer) {
  return task.local_date && task.local_time && task.offset
    ? `${task.local_date} ${task.local_time} · ${task.timezone} · UTC${task.offset}` : localTime(task.execute_at, task.timezone)
}
function previewText(value: TimerPreview) { return `${value.local_date} ${value.local_time} · ${value.timezone} · UTC${value.offset}` }
function windowText(open: string, close: string, timezone: string) {
  return t(`admin.accounts.timer.${close <= open ? 'dailyWindowNextDay' : 'dailyWindow'}`, { open, close, timezone })
}
function countdown(task: SchedulableTimer) {
  const minutes = Math.ceil((Date.parse(task.execute_at) - now.value) / 60000)
  return minutes <= 0 ? t('admin.accounts.timer.awaiting') : t('admin.accounts.timer.remaining', { hours: Math.floor(minutes / 60), minutes: minutes % 60 })
}
function editOneShot(value: TimerAction) {
  action.value = value
  const existing = snapshot.value?.tasks.find(task => task.action === value && task.status === 'pending' && task.recurrence !== 'daily')
  if (existing) {
    const local = existing.local_date && existing.local_time ? { date: existing.local_date, time: existing.local_time } : localParts(existing.execute_at, existing.timezone)
    Object.assign(form, { mode: 'absolute', hours: 0, minutes: 0, date: local.date, time: local.time, timezone: existing.timezone })
  } else Object.assign(form, { mode: 'relative', hours: 8, minutes: 0, date: '', time: '08:00' })
}
async function begin(value?: TimerAction) {
  if (expanded.value && value === undefined) { expanded.value = false; return }
  if (!props.canManageTimers) { expanded.value = true; return }
  await load()
  if (loadFailed.value) { expanded.value = true; return }
  for (const selected of ['open', 'close'] as const) editVersions[selected] = snapshot.value?.tasks.find(task => task.action === selected)?.version ?? 0
  const dailyOpen = pending.value.find(task => task.action === 'open' && task.recurrence === 'daily')
  const dailyClose = pending.value.find(task => task.action === 'close' && task.recurrence === 'daily')
  const selected = value ? pending.value.find(task => task.action === value) : pending.value.find(task => task.recurrence !== 'daily')
  if (selected && selected.recurrence !== 'daily') editOneShot(selected.action)
  else Object.assign(form, { mode: 'daily', openTime: dailyOpen?.daily_time || '08:00', closeTime: dailyClose?.daily_time || '01:00', timezone: dailyOpen?.timezone || dailyClose?.timezone || 'Asia/Shanghai' })
  error.value = ''; preview.value = null; dailyPreview.value = null; confirmed.value = false; expanded.value = true
}
function input(): TimerInput {
  if (form.mode === 'daily') return { mode: 'daily', time: form.openTime, timezone: form.timezone }
  return { mode: form.mode, hours: form.mode === 'relative' ? form.hours : 0, minutes: form.mode === 'relative' ? form.minutes : 0, date: form.mode === 'absolute' ? form.date : '', time: form.mode === 'absolute' ? form.time : '', timezone: form.timezone }
}
async function previewTime() {
  const request = input()
  if (request.mode === 'relative' && (!Number.isInteger(request.hours) || !Number.isInteger(request.minutes) || request.hours < 0 || request.minutes < 0 || request.hours * 60 + request.minutes <= 0 || request.hours * 60 + request.minutes > maxMinutes.value)) { error.value = reasonText('duration'); return }
  if (request.mode === 'daily' && form.openTime === form.closeTime) { error.value = reasonText('conflict'); return }
  busy.value = true; error.value = ''; preview.value = null; dailyPreview.value = null; confirmed.value = false
  try {
    if (request.mode === 'daily') {
      const [open, close] = await Promise.all([
        schedulableTimersAPI.preview(props.accountId, request),
        schedulableTimersAPI.preview(props.accountId, { mode: 'daily', time: form.closeTime, timezone: form.timezone })
      ])
      dailyPreview.value = { open, close }
    } else preview.value = await schedulableTimersAPI.preview(props.accountId, request)
  } catch (err) { showError(err) }
  finally { busy.value = false }
}
async function save() {
  if (form.mode === 'daily' ? !dailyPreview.value : !preview.value || (form.mode === 'absolute' && !confirmed.value)) return
  busy.value = true; error.value = ''
  try {
    if (form.mode === 'daily') {
      await schedulableTimersAPI.saveDailyWindow(props.accountId, { open_time: form.openTime, close_time: form.closeTime, timezone: form.timezone, expected_open_version: editVersions.open, expected_close_version: editVersions.close })
    } else {
      const request = input()
      if (request.mode === 'absolute') request.date = preview.value!.local_date
      await schedulableTimersAPI.save(props.accountId, { ...request, action: action.value, expected_version: editVersions[action.value], confirmed_at: request.mode === 'absolute' ? preview.value!.execute_at : undefined })
    }
    app.showSuccess(t('admin.accounts.timer.saved')); expanded.value = false; await load()
  } catch (err) { showError(err) }
  finally { busy.value = false }
}
async function cancel(task: SchedulableTimer) {
  busy.value = true; error.value = ''
  try { await schedulableTimersAPI.cancel(props.accountId, task.action, task.version); app.showSuccess(t('admin.accounts.timer.cancelled')); await load() }
  catch (err) { showError(err) }
  finally { busy.value = false }
}
onMounted(() => {
  if (!props.canManageTimers) return
  void load()
  interval = setInterval(() => { now.value = epoch + performance.now() - received }, 1000)
  refreshInterval = setInterval(() => { if (!busy.value && document.visibilityState === 'visible') void load() }, 30000)
})
onUnmounted(() => { disposed = true; loadSequence++; clearInterval(interval); clearInterval(refreshInterval) })
</script>
