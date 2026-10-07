<template>
  <div
    class="flex w-full"
    :class="size === 'lg' ? 'h-5 gap-px sm:h-6 sm:gap-[2px]' : 'h-4 gap-px sm:h-5 sm:gap-[2px]'"
    role="group"
    :aria-label="label"
    data-testid="monitor-v3-bar"
  >
    <template v-for="(cell, index) in cells" :key="index">
      <button
        v-if="cell"
        type="button"
        class="min-w-0 flex-1 cursor-pointer rounded-[2px] transition-opacity hover:opacity-80 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-primary-400"
        :class="[MONITOR_V3_COLORS[cell.status], active === index ? 'ring-2 ring-gray-900/30 dark:ring-white/40' : '', collecting(cell, index) ? 'animate-pulse' : '']"
        :aria-label="cellLabel(cell)"
        data-testid="monitor-v3-cell"
        :data-status="cell.status"
        @mouseenter="emit('inspect', $event, cell, index)"
        @focus="emit('inspect', $event, cell, index)"
        @click="emit('inspect', $event, cell, index)"
        @mouseleave="emit('leave')"
        @blur="emit('leave')"
      />
      <span
        v-else
        class="min-w-0 flex-1 rounded-[2px]"
        :class="[MONITOR_V3_EMPTY_CELL, live && index === cells.length - 1 ? 'animate-pulse' : '']"
        :title="live && index === cells.length - 1 ? t('channelMonitorV3.page.collecting') : undefined"
        aria-hidden="true"
        data-testid="monitor-v3-cell-empty"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { MonitorV3Cell } from '@/api/channelMonitorV3'
import { MONITOR_V3_COLORS, MONITOR_V3_EMPTY_CELL, formatMonitorV3Rate, formatMonitorV3Slot } from './monitorV3'

// live marks the latest window, whose last slot is still filling with traffic.
const props = withDefaults(defineProps<{
  cells: Array<MonitorV3Cell | null>
  label: string
  intervalMinutes: number
  size?: 'lg' | 'md'
  active?: number | null
  live?: boolean
}>(), {
  size: 'md',
  active: null,
  live: false,
})
const emit = defineEmits<{
  (e: 'inspect', event: Event, cell: MonitorV3Cell, index: number): void
  (e: 'leave'): void
}>()
const { t } = useI18n()

// The slot still filling up has not reached the minimum yet: it is counting, not quiet.
function collecting(cell: MonitorV3Cell, index: number) {
  return props.live && index === props.cells.length - 1 && cell.status === 'insufficient'
}

function cellLabel(cell: MonitorV3Cell) {
  return `${formatMonitorV3Slot(cell.start, props.intervalMinutes)} · ${t(`channelMonitorV3.status.${cell.status}`)} · ${t('channelMonitorV3.tooltip.successRate', { value: formatMonitorV3Rate(cell.success_rate) })}`
}
</script>
