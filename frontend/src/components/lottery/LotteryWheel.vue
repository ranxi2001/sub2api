<template>
  <div class="mx-auto mb-5 w-full max-w-sm">
    <div class="lottery-wheel" role="img" :aria-label="t('lottery.prizes')">
      <svg viewBox="0 0 400 400" class="wheel-disc" :style="{ transform: `rotate(${rotation}deg)`, transitionDuration: `${duration}ms` }">
        <circle cx="200" cy="200" r="197" class="wheel-rim" />
        <circle cx="200" cy="200" r="188" class="wheel-empty" />
        <g v-for="segment in segments" :key="segment.prize.id" :style="{ '--segment-color': segment.color }">
          <title>{{ segment.prize.name }} · {{ percent(segment.prize.probability) }}</title>
          <path :d="sectorPath(segment.start, segment.end)" class="wheel-sector" />
          <g v-if="hasLabel(segment)" :transform="`translate(${point(segment.center).x} ${point(segment.center).y})`">
            <g class="wheel-label" :style="{ transform: `rotate(${-rotation}deg)`, transitionDuration: `${duration}ms` }">
              <foreignObject x="-12" y="-42" width="24" height="24" aria-hidden="true">
                <Icon :name="segment.prize.kind === 'balance' ? 'creditCard' : segment.prize.kind === 'subscription' ? 'sparkles' : 'gift'" class="h-6 w-6" />
              </foreignObject>
              <text y="0" text-anchor="middle" class="wheel-name">{{ shortName(segment.prize.name) }}</text>
              <text y="25" text-anchor="middle" class="wheel-value" :textLength="detail(segment.prize).length > 10 ? 100 : undefined" lengthAdjust="spacingAndGlyphs">{{ detail(segment.prize) }}</text>
            </g>
          </g>
        </g>
      </svg>
      <div class="wheel-center"><Icon name="gift" class="h-8 w-8 text-emerald-700 dark:text-emerald-300" /></div>
      <div class="wheel-pointer"></div>
    </div>
    <div v-if="smallSegments.length" class="mt-2 flex flex-wrap justify-center gap-x-4 gap-y-2" :aria-label="t('lottery.prizes')">
      <span v-for="segment in smallSegments" :key="segment.prize.id" class="flex min-w-0 items-center gap-1.5 text-xs text-gray-600 dark:text-gray-300" :title="segment.prize.name">
        <span class="h-2 w-2 shrink-0 rounded-full" :style="{ background: segment.color }"></span>
        <span class="break-words">{{ segment.prize.name }}</span>
        <span class="shrink-0 tabular-nums text-gray-500">{{ percent(segment.prize.probability) }}</span>
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { LotteryPrize } from '@/api/lottery'
import { sectorPath, wheelRotation, wheelSegments } from './wheel'

const props = defineProps<{ prizes: LotteryPrize[] }>()
const { t, locale } = useI18n()
const segments = computed(() => wheelSegments(props.prizes))
const hasLabel = (segment: { start: number; end: number }) => segment.end - segment.start >= 42
const smallSegments = computed(() => segments.value.filter(s => !hasLabel(s)))
const shortName = (name: string) => Array.from(name).length > 8 ? Array.from(name).slice(0, 7).join('') + '…' : name
const percent = (n: number) => new Intl.NumberFormat(locale.value, { style: 'percent', maximumFractionDigits: 4 }).format(n)
const detail = (p: LotteryPrize) => p.kind === 'balance'
  ? `$${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }).format(p.amount)}`
  : p.kind === 'subscription' ? t('lottery.days', { days: p.validity_days }) : t('lottery.textPrize')
const point = (angle: number) => ({ x: 200 + 129 * Math.sin(angle * Math.PI / 180), y: 200 - 129 * Math.cos(angle * Math.PI / 180) })
const rotation = ref(0)
const duration = ref(0)
let timer: ReturnType<typeof setTimeout> | undefined
let finish: (() => void) | undefined

async function spinTo(id: number) {
  const segment = segments.value.find(s => s.prize.id === id)
  if (!segment) return
  duration.value = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 0 : 2600
  await nextTick()
  rotation.value = wheelRotation(rotation.value, segment.center)
  await new Promise<void>(resolve => {
    finish = resolve
    timer = setTimeout(resolve, duration.value + 50)
  })
}

onUnmounted(() => {
  clearTimeout(timer)
  finish?.()
})
defineExpose({ spinTo })
</script>

<style scoped>
.lottery-wheel {
  position: relative;
  width: min(100%, 360px);
  aspect-ratio: 1;
  margin: 12px auto 16px;
}
.wheel-disc { width: 100%; height: 100%; }
.wheel-disc, .wheel-label {
  transition-property: transform;
  transition-timing-function: cubic-bezier(0.12, 0.7, 0.12, 1);
}
.wheel-rim { fill: #ffffff; stroke: #e5e7eb; stroke-width: 2px; }
.wheel-empty { fill: #f3f4f6; }
.wheel-sector {
  fill: color-mix(in srgb, var(--segment-color) 16%, white);
  stroke: white;
  stroke-width: 2px;
}
.wheel-label { color: var(--segment-color); fill: var(--segment-color); }
.wheel-name { font-size: 14px; font-weight: 600; }
.wheel-value { font-size: 21px; font-weight: 700; }
.wheel-center {
  position: absolute;
  inset: 39%;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: white;
  box-shadow: 0 0 0 7px #ffffff80, 0 3px 14px #0000000d;
}
.wheel-pointer {
  position: absolute;
  left: calc(50% - 11px);
  top: -3px;
  width: 0;
  height: 0;
  border-left: 11px solid transparent;
  border-right: 11px solid transparent;
  border-top: 29px solid #065f46;
  filter: drop-shadow(0 2px 2px #00000022);
}
.dark .wheel-rim { fill: #292929; stroke: #404040; }
.dark .wheel-empty { fill: #262626; }
.dark .wheel-sector { fill: color-mix(in srgb, var(--segment-color) 24%, #171717); stroke: #292929; }
.dark .wheel-label { color: color-mix(in srgb, var(--segment-color) 35%, white); fill: color-mix(in srgb, var(--segment-color) 35%, white); }
.dark .wheel-center { background: #292929; box-shadow: 0 0 0 7px #29292990; }
.dark .wheel-pointer { border-top-color: #6ee7b7; }
</style>
