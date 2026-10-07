<template>
  <div class="space-y-5 sm:space-y-6" data-testid="monitor-v3-page">
    <div class="flex flex-wrap items-center justify-end gap-2">
      <slot name="actions" />
      <button
        type="button"
        class="inline-flex items-center gap-1.5 rounded-lg bg-gray-900 px-4 py-2 text-sm font-semibold text-white shadow-sm transition hover:bg-gray-700 disabled:opacity-60 dark:bg-white dark:text-gray-900 dark:hover:bg-gray-200"
        :disabled="loading"
        data-testid="monitor-v3-refresh"
        @click="emit('refresh')"
      >
        <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
        {{ t('channelMonitorV3.page.refresh') }}
      </button>
    </div>

    <div v-if="!status && loading" class="space-y-4" aria-busy="true">
      <div class="h-40 animate-pulse rounded-2xl bg-gray-100 dark:bg-dark-800" />
      <div class="h-96 animate-pulse rounded-2xl bg-gray-100 dark:bg-dark-800" />
    </div>

    <template v-else-if="status">
      <section
        v-if="status.featured"
        class="rounded-2xl border border-gray-200/80 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-6 xl:p-8"
        data-testid="monitor-v3-featured"
      >
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="truncate text-lg font-bold text-gray-900 dark:text-white sm:text-xl" :title="status.featured.description || status.featured.name">{{ status.featured.name }}</h2>
            <div class="mt-2 flex flex-wrap items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
              <StatusDot :status="status.featured.status" :label="t(`channelMonitorV3.status.${status.featured.status}`)" />
              <span data-testid="monitor-v3-featured-status">{{ t(`channelMonitorV3.headline.${status.featured.status}`) }}</span>
              <span v-if="multiplier(status.featured)" class="ml-1 rounded-md bg-gray-100 px-1.5 py-px font-mono text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ multiplier(status.featured) }}</span>
            </div>
          </div>
          <div class="shrink-0 text-right">
            <div class="text-3xl font-bold tabular-nums tracking-tight text-gray-900 dark:text-white sm:text-4xl" data-testid="monitor-v3-featured-availability">
              {{ formatMonitorV3Availability(status.featured.availability) }}
            </div>
            <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ t(`channelMonitorV3.page.availabilityRange.r${status.availability_range}`) }}</div>
          </div>
        </div>
        <StatusBar
          class="mt-4"
          size="lg"
          live
          :cells="status.featured.cells"
          :interval-minutes="status.interval_minutes"
          :label="t('channelMonitorV3.page.historyOf', { name: status.featured.name })"
          :active="activeIndex(status.featured)"
          @inspect="(event, cell, index) => openDetail(event, status!.featured!, cell, index)"
          @leave="scheduleClose"
        />
        <div class="mt-2 flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
          <span>{{ t('channelMonitorV3.page.statsSince', { time: formatMonitorV3Full(status.availability_since), zone: timezoneLabel }) }}</span>
          <span>
            <template v-if="status.featured.requests">{{ t('channelMonitorV3.page.requestCount', { count: status.featured.requests }) }} · </template>
            <span data-testid="monitor-v3-data-through">{{ freshness }}</span>
          </span>
        </div>
      </section>

      <section
        class="rounded-2xl border border-gray-200/80 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-6 xl:p-8"
        data-testid="monitor-v3-system"
      >
        <header class="flex flex-wrap items-center gap-x-6 gap-y-3">
          <h2 class="text-lg font-bold text-gray-900 dark:text-white sm:text-xl">{{ t('channelMonitorV3.page.systemStatus') }}</h2>
          <div class="flex flex-wrap items-center gap-1 text-xs text-gray-600 sm:gap-2 sm:text-sm dark:text-gray-300">
            <button
              type="button"
              class="grid h-8 w-8 shrink-0 place-items-center rounded-md transition hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-30 dark:hover:bg-dark-700"
              :disabled="loading || !status.window.has_older"
              :aria-label="t('channelMonitorV3.page.older')"
              data-testid="monitor-v3-older"
              @click="emit('navigate', olderEnd)"
            >
              <Icon name="chevronLeft" size="xs" />
            </button>
            <span class="whitespace-nowrap tabular-nums" data-testid="monitor-v3-window">{{ windowRange.from }} - {{ windowRange.to }}</span>
            <button
              type="button"
              class="grid h-8 w-8 shrink-0 place-items-center rounded-md transition hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-30 dark:hover:bg-dark-700"
              :disabled="loading || status.window.latest"
              :aria-label="t('channelMonitorV3.page.newer')"
              data-testid="monitor-v3-newer"
              @click="emit('navigate', newerEnd)"
            >
              <Icon name="chevronRight" size="xs" />
            </button>
            <button
              v-if="!status.window.latest"
              type="button"
              class="ml-1 text-primary-600 hover:underline dark:text-primary-400"
              data-testid="monitor-v3-latest"
              @click="emit('navigate', null)"
            >
              {{ t('channelMonitorV3.page.backToLatest') }}
            </button>
          </div>
          <div class="flex w-full flex-wrap items-center gap-x-4 gap-y-2 text-xs text-gray-500 dark:text-gray-400 xl:ml-auto xl:w-auto">
            <span v-for="item in legend" :key="item" class="inline-flex items-center gap-1" aria-hidden="true">
              <i class="h-2.5 w-2.5 rounded-[2px]" :class="item === 'empty' ? MONITOR_V3_EMPTY_CELL : MONITOR_V3_COLORS[item]" />{{ t(`channelMonitorV3.legend.${item}`) }}
            </span>
            <span class="inline-flex cursor-help text-gray-400" :title="rules" tabindex="0" :aria-label="rules" data-testid="monitor-v3-rules">
              <Icon name="questionCircle" size="xs" />
            </span>
          </div>
        </header>

        <div v-if="!status.categories.length" class="py-14 text-center text-sm text-gray-500 dark:text-gray-400" data-testid="monitor-v3-empty">
          {{ t('channelMonitorV3.page.empty') }}
        </div>
        <div v-else class="mt-6 grid grid-cols-1 gap-6 xl:grid-cols-2 xl:gap-8">
          <div
            v-for="{ category, fullWidth } in layout"
            :key="category.id"
            class="min-w-0 rounded-xl border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700/70 dark:bg-dark-900/30 sm:p-5"
            :class="fullWidth ? 'xl:col-span-2' : ''"
            data-testid="monitor-v3-category"
          >
            <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 text-sm">
              <div class="flex min-w-0 items-center gap-1.5">
                <span class="truncate text-base font-semibold text-gray-900 dark:text-white">{{ category.name }}</span>
                <span
                  v-if="category.description"
                  class="inline-flex text-gray-400 dark:text-gray-500"
                  :title="category.description"
                  tabindex="0"
                  :aria-label="category.description"
                >
                  <Icon name="infoCircle" size="xs" />
                </span>
                <button
                  type="button"
                  class="inline-flex shrink-0 items-center gap-1 rounded px-1 py-1 text-xs text-gray-500 transition hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700"
                  :aria-expanded="!collapsed.has(category.id)"
                  data-testid="monitor-v3-category-toggle"
                  @click="toggle(category.id)"
                >
                  {{ t('channelMonitorV3.page.componentCount', { count: category.components.length }) }}
                  <Icon :name="collapsed.has(category.id) ? 'chevronDown' : 'chevronUp'" size="xs" />
                </button>
              </div>
              <span class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.page.availabilityValue', { value: formatMonitorV3Availability(category.availability) }) }}</span>
            </div>
            <div v-show="!collapsed.has(category.id)" class="mt-4 space-y-5 sm:space-y-6">
              <div v-for="component in category.components" :key="component.id" data-testid="monitor-v3-component">
                <div class="flex flex-col gap-1.5 text-sm sm:flex-row sm:items-center sm:justify-between sm:gap-3">
                  <div class="flex min-w-0 items-center gap-1.5">
                    <StatusDot size="md" :status="component.status" :label="t(`channelMonitorV3.status.${component.status}`)" />
                    <span class="min-w-0 break-words font-medium text-gray-800 dark:text-gray-100" :title="component.description || component.name">{{ component.name }}</span>
                    <span v-if="multiplier(component)" class="shrink-0 rounded-md bg-gray-100 px-1.5 py-px font-mono text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ multiplier(component) }}</span>
                  </div>
                  <span class="shrink-0 tabular-nums text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.page.availabilityValue', { value: formatMonitorV3Availability(component.availability) }) }}</span>
                </div>
                <StatusBar
                  class="mt-2.5"
                  :cells="component.cells"
                  :interval-minutes="status.interval_minutes"
                  :label="t('channelMonitorV3.page.historyOf', { name: component.name })"
                  :active="activeIndex(component)"
                  :live="status.window.latest"
                  @inspect="(event, cell, index) => openDetail(event, component, cell, index)"
                  @leave="scheduleClose"
                />
              </div>
            </div>
          </div>
        </div>
      </section>

      <footer class="flex flex-col items-center gap-3 pb-2 pt-1 text-center">
        <button
          type="button"
          class="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-4 py-2 text-sm font-medium text-gray-700 shadow-sm transition hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:bg-dark-700"
          data-testid="monitor-v3-incidents"
          @click="emit('open-incidents')"
        >
          <Icon name="document" size="xs" />
          {{ t('channelMonitorV3.page.incidents') }}
          <span v-if="status.open_incidents" class="rounded-full bg-[#f2715a] px-1.5 text-xs font-semibold leading-5 text-white">{{ status.open_incidents }}</span>
        </button>
        <p class="text-xs text-gray-500 dark:text-gray-400">Powered by <strong class="font-semibold text-gray-700 dark:text-gray-200">{{ siteName }}</strong></p>
        <p class="max-w-3xl whitespace-pre-line text-xs leading-relaxed text-gray-500 dark:text-gray-400" data-testid="monitor-v3-footer-note">
          {{ status.footer_note || t('channelMonitorV3.page.defaultFooter') }}
        </p>
      </footer>
    </template>

    <Teleport to="body">
      <div
        v-if="detail && status"
        :id="tooltipId"
        ref="tooltipElement"
        role="tooltip"
        class="fixed z-[100] w-64 max-w-[calc(100vw-24px)] rounded-xl border border-gray-200 bg-white text-xs text-gray-800 shadow-xl dark:border-dark-600 dark:bg-dark-900 dark:text-gray-100"
        :style="{ left: placement.left + 'px', top: placement.top + 'px', visibility: placement.ready ? 'visible' : 'hidden' }"
        data-testid="monitor-v3-tooltip"
        @mouseenter="cancelClose"
        @mouseleave="scheduleClose"
      >
        <div class="px-3 pb-2 pt-2.5">
          <p class="text-[11px] text-gray-500 dark:text-gray-400">{{ formatMonitorV3Slot(detail.cell.start, status.interval_minutes) }}</p>
          <div class="mt-1.5 flex items-center justify-between gap-3">
            <span class="inline-flex items-center gap-1.5 font-medium">
              <StatusDot :status="detail.cell.status" />
              {{ t(`channelMonitorV3.status.${detail.cell.status}`) }}
            </span>
            <span class="font-mono text-[11px] text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.tooltip.successRate', { value: formatMonitorV3Rate(detail.cell.success_rate) }) }}</span>
          </div>
          <p class="mt-1 flex items-center justify-between gap-3 text-[11px] text-gray-500 dark:text-gray-400">
            <span>{{ t('channelMonitorV3.tooltip.ttft') }}</span>
            <span class="font-mono">{{ formatMonitorV3Bucket(detail.cell.ttft_p50_ms) }}</span>
          </p>
        </div>
        <div v-if="note(detail)" class="border-t border-gray-100 px-3 py-2 dark:border-dark-700" data-testid="monitor-v3-tooltip-note">
          <p class="text-[11px] font-semibold" :class="detail.cell.status === 'down' ? 'text-[#e2553f]' : detail.cell.status === 'degraded' ? 'text-[#c88a00]' : 'text-gray-500 dark:text-gray-400'">
            {{ t(detail.cell.status === 'down' ? 'channelMonitorV3.tooltip.error' : 'channelMonitorV3.tooltip.note') }}
          </p>
          <p class="mt-0.5 text-gray-700 dark:text-gray-200">{{ note(detail) }}</p>
        </div>
        <div class="flex items-center justify-between gap-2 border-t border-gray-100 px-3 py-1.5 text-[10px] text-gray-400 dark:border-dark-700 dark:text-gray-500">
          <span class="truncate">{{ detail.component.name }}<template v-if="detail.component.model"> · {{ detail.component.model }}</template></span>
          <span v-if="detail.cell.requests" class="shrink-0" data-testid="monitor-v3-tooltip-volume">{{ t('channelMonitorV3.tooltip.volume', { requests: detail.cell.requests, errors: detail.cell.errors || 0, ignored: detail.cell.ignored_errors || 0 }) }}</span>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import type { MonitorV3Cell, MonitorV3ComponentStatus, MonitorV3StatusPage } from '@/api/channelMonitorV3'
import StatusBar from './StatusBar.vue'
import StatusDot from './StatusDot.vue'
import {
  MONITOR_V3_COLORS,
  MONITOR_V3_EMPTY_CELL,
  formatMonitorV3Availability,
  formatMonitorV3Bucket,
  formatMonitorV3Full,
  formatMonitorV3Multiplier,
  formatMonitorV3Offset,
  formatMonitorV3Rate,
  formatMonitorV3Slot,
  monitorV3CategoryLayout,
  monitorV3TimezoneOffsetHours,
  monitorV3WindowRange,
} from './monitorV3'

const props = defineProps<{ status: MonitorV3StatusPage | null; loading: boolean }>()
const emit = defineEmits<{
  (e: 'refresh'): void
  (e: 'navigate', end: number | null): void
  (e: 'open-incidents'): void
}>()
const { t, te } = useI18n()
const appStore = useAppStore()

const legend = ['operational', 'degraded', 'down', 'empty'] as const
const siteName = computed(() => appStore.siteName || 'Sub2API')
const layout = computed(() => monitorV3CategoryLayout(props.status?.categories || []))
const windowRange = computed(() => (props.status
  ? monitorV3WindowRange(props.status.window.start, props.status.window.end, props.status.generated_at)
  : { from: '—', to: '—' }))
const timezoneLabel = computed(() => {
  const hours = monitorV3TimezoneOffsetHours()
  return hours === 8 ? t('channelMonitorV3.page.beijingTime') : formatMonitorV3Offset(hours)
})
const freshness = computed(() => (props.status?.data_through
  ? t('channelMonitorV3.page.dataThrough', { time: formatMonitorV3Full(props.status.data_through) })
  : t('channelMonitorV3.page.noData')))
const rules = computed(() => (props.status
  ? t('channelMonitorV3.page.rules', {
    down: formatMonitorV3Rate(props.status.down_error_rate),
    degraded: formatMonitorV3Rate(props.status.degraded_error_rate),
    ttft: props.status.degraded_ttft_ms / 1000,
    min: props.status.min_requests,
  })
  : ''))

const intervalSeconds = computed(() => (props.status?.interval_minutes || 1) * 60)
/** The older window ends one slot before the current one starts. */
const olderEnd = computed(() => (props.status ? Math.floor(new Date(props.status.window.start).getTime() / 1000) - intervalSeconds.value : null))
const newerEnd = computed(() => {
  if (!props.status) return null
  const end = Math.floor(new Date(props.status.window.end).getTime() / 1000)
  return end + (props.status.cells - 1) * intervalSeconds.value
})

const collapsed = reactive(new Set<number>())
function toggle(id: number) {
  if (collapsed.has(id)) collapsed.delete(id)
  else collapsed.add(id)
}

function multiplier(component: MonitorV3ComponentStatus) {
  return formatMonitorV3Multiplier(component.multiplier)
}

/** What the tooltip explains below the numbers, if anything. */
function note({ component, cell, index }: Detail) {
  if (cell.status === 'insufficient') {
    const live = component === props.status?.featured || props.status?.window.latest
    if (live && index === component.cells.length - 1) return t('channelMonitorV3.page.collecting')
    return t('channelMonitorV3.tooltip.insufficient', { min: props.status?.min_requests ?? 1 })
  }
  if (cell.status === 'operational') return ''
  if (cell.top_error) {
    const key = `channelMonitorV3.errors.${cell.top_error}`
    return te(key) ? t(key) : t('channelMonitorV3.errors.other')
  }
  return cell.status === 'degraded' ? t('channelMonitorV3.tooltip.slow') : ''
}

type Detail = { component: MonitorV3ComponentStatus; cell: MonitorV3Cell; index: number }
const tooltipId = useId()
const detail = shallowRef<Detail | null>(null)
const anchor = shallowRef<HTMLElement | null>(null)
const tooltipElement = ref<HTMLElement | null>(null)
const placement = ref({ left: 0, top: 0, ready: false })
let closeTimer: ReturnType<typeof setTimeout> | undefined

function activeIndex(component: MonitorV3ComponentStatus) {
  return detail.value?.component === component ? detail.value.index : null
}
function cancelClose() {
  if (closeTimer) clearTimeout(closeTimer)
  closeTimer = undefined
}
function closeDetail() {
  cancelClose()
  detail.value = null
  anchor.value = null
}
function scheduleClose() {
  cancelClose()
  // Leave time to move onto the tooltip (which cancels this) to read it.
  closeTimer = setTimeout(closeDetail, 120)
}
function openDetail(event: Event, component: MonitorV3ComponentStatus, cell: MonitorV3Cell, index: number) {
  cancelClose()
  anchor.value = event.currentTarget as HTMLElement
  detail.value = { component, cell, index }
  placement.value = { ...placement.value, ready: false }
  void nextTick(position)
}
function position() {
  if (!anchor.value || !tooltipElement.value) return
  const rect = anchor.value.getBoundingClientRect()
  const box = tooltipElement.value.getBoundingClientRect()
  const left = Math.max(12, Math.min(window.innerWidth - box.width - 12, rect.left + rect.width / 2 - box.width / 2))
  const above = rect.top - box.height - 8
  const top = above >= 12 ? above : Math.min(window.innerHeight - box.height - 12, rect.bottom + 8)
  placement.value = { left, top, ready: true }
}
function onOutsidePointer(event: Event) {
  if (!(event.target instanceof Node)) return
  if (!anchor.value?.contains(event.target) && !tooltipElement.value?.contains(event.target)) closeDetail()
}
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') closeDetail()
}
function onViewportChange(event: Event) {
  if (event.target instanceof Node && tooltipElement.value?.contains(event.target)) return
  closeDetail()
}
watch(() => props.status, closeDetail)
onMounted(() => {
  document.addEventListener('pointerdown', onOutsidePointer, true)
  document.addEventListener('keydown', onKeydown)
  window.addEventListener('resize', onViewportChange)
  window.addEventListener('scroll', onViewportChange, true)
})
onBeforeUnmount(() => {
  cancelClose()
  document.removeEventListener('pointerdown', onOutsidePointer, true)
  document.removeEventListener('keydown', onKeydown)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('scroll', onViewportChange, true)
})
</script>
