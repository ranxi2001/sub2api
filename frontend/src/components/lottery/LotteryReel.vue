<template>
  <div class="lottery-reels" :class="{ 'is-multiple': count > 1, 'is-five': count === 5 }" :aria-busy="spinning">
    <div v-for="(row, index) in rows" :key="index" class="reel-row" :data-row="index" @mouseleave="tooltip = null">
      <div class="reel-pointer" aria-hidden="true"></div>
      <div
        class="reel-window" role="img" :tabindex="row.cards.length && !spinning ? 0 : -1"
        :aria-label="row.done && row.winner ? t('lottery.reelWinner', { name: row.winner.name }) : t('lottery.prizes')"
        :aria-describedby="tooltip?.row === index ? `reel-detail-${index}` : undefined"
        @focus="showCenteredPrize(index)" @blur="tooltip = null" @keydown.esc="tooltip = null"
      >
        <div v-if="row.cards.length" :ref="el => setTrack(el, index)" class="reel-track" :style="{ transform: `translate3d(${-row.offset}px, 0, 0)` }" aria-hidden="true">
          <div
            v-for="(prize, position) in row.cards" :key="position"
            class="reel-card" :class="{ 'is-winner': row.done && position === WINNER_INDEX }"
            :data-prize-id="prize.id" :data-position="position"
            :style="{ '--prize-color': prizeColor(prize, prizes) }"
            @mouseenter="!spinning && (tooltip = { row: index, prize })"
          >
            <Icon v-if="row.done && position === WINNER_INDEX" name="checkCircle" class="reel-check" />
            <PrizeIcon :kind="prize.kind" :color="prizeColor(prize, prizes)" />
            <div class="reel-copy">
              <strong class="reel-value" :class="{ 'is-long': detail(prize).length > 10 }">{{ detail(prize) }}</strong>
              <span class="reel-name">{{ prize.name }}</span>
            </div>
          </div>
        </div>
        <p v-else class="reel-empty">{{ t('lottery.noPrizes') }}</p>
      </div>
      <span v-if="count > 1" class="reel-number" aria-hidden="true">{{ index + 1 }}</span>
      <div v-if="tooltip?.row === index" :id="`reel-detail-${index}`" class="reel-tooltip" role="tooltip">
        <strong>{{ detail(tooltip.prize) }}</strong><span>{{ tooltip.prize.name }}</span>
      </div>
    </div>
    <p class="sr-only" role="status">{{ announcement }}</p>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PrizeIcon from './PrizeIcon.vue'
import type { LotteryPrize } from '@/api/lottery'
import { createReel, landingFraction, prizeColor, reelOffset, reelKeyframes, REEL_DURATION, REEL_STAGGER, WINNER_INDEX, type ReelPrize } from './reel'

const props = withDefaults(defineProps<{ prizes: LotteryPrize[]; count?: number }>(), { count: 1 })
const { t, locale } = useI18n()
type Row = { cards: ReelPrize[]; offset: number; done: boolean; winner?: ReelPrize }
const rows = ref<Row[]>([])
const spinning = ref(false)
const announcement = ref('')
const tooltip = ref<{ row: number; prize: ReelPrize } | null>(null)
const tracks: (HTMLElement | null)[] = []
const active = new Set<() => void>()
const startOffset = 1000
let disposed = false
let currentSpin: Promise<void> | undefined

function setTrack(el: Element | ComponentPublicInstance | null, index: number) {
  tracks[index] = el instanceof HTMLElement ? el : null
}
function reset() {
  tooltip.value = null
  rows.value = Array.from({ length: props.count }, () => ({ cards: createReel(props.prizes), offset: startOffset, done: false }))
  announcement.value = ''
}
watch(() => props.count, () => { if (!spinning.value) reset() })
watch(() => props.prizes, () => {
  if (!spinning.value && !rows.value.some(row => row.winner)) reset()
}, { immediate: true })
const detail = (p: ReelPrize) => p.kind === 'balance'
  ? `$${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }).format(p.amount)}`
  : p.kind === 'subscription' ? t('lottery.days', { days: p.validity_days }) : t('lottery.textPrize')

function showCenteredPrize(index: number) {
  const row = rows.value[index]
  const track = tracks[index]
  if (!track || spinning.value) return
  const width = track.firstElementChild?.getBoundingClientRect().width || 1
  const gap = Number.parseFloat(getComputedStyle(track).gap) || 8
  // One keyboard stop per reel; focusing it reveals the prize under the pointer.
  const prize = row.winner ?? row.cards[Math.floor(row.offset / (width + gap))]
  if (prize) tooltip.value = { row: index, prize }
}

async function animate(winners: ReelPrize[]) {
  if (disposed || !winners.length) return
  tooltip.value = null
  spinning.value = true
  announcement.value = t('lottery.drawing')
  rows.value = Array.from({ length: props.count }, (_, i) => ({ cards: createReel(props.prizes, winners[i]), offset: startOffset, done: false, winner: winners[i] }))
  await nextTick()
  if (disposed) return
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  await Promise.all(rows.value.map((row, index) => {
    const track = tracks[index]
    const card = track?.firstElementChild as HTMLElement | null
    if (!row.winner || !track || !card) return Promise.resolve()
    const width = card.getBoundingClientRect().width
    const gap = Number.parseFloat(getComputedStyle(track).gap) || 8
    const destination = reelOffset(WINNER_INDEX, width, gap, landingFraction())
    return new Promise<void>(resolve => {
      let animation: Animation | undefined
      let timer: ReturnType<typeof setTimeout> | undefined
      let settled = false
      const finish = () => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        // Write the final transform before cancelling the temporary animation.
        track.style.transform = `translate3d(${-destination}px, 0, 0)`
        row.offset = destination
        row.done = true
        animation?.cancel()
        active.delete(finish)
        resolve()
      }
      active.add(finish)
      if (reduced || typeof track.animate !== 'function') return finish()
      try {
        animation = track.animate(reelKeyframes(startOffset, destination, width), {
          duration: REEL_DURATION, delay: index * REEL_STAGGER, easing: 'linear', fill: 'both',
        })
        animation.finished.then(finish, finish)
        timer = setTimeout(finish, REEL_DURATION + index * REEL_STAGGER + 250)
      } catch { finish() }
    })
  }))
  spinning.value = false
  announcement.value = winners.map(p => t('lottery.reelWinner', { name: p.name })).join(', ')
}
function spinTo(winners: ReelPrize[]) {
  if (currentSpin) return currentSpin
  currentSpin = animate(winners).finally(() => { currentSpin = undefined })
  return currentSpin
}
onBeforeUnmount(() => {
  disposed = true
  active.forEach(finish => finish())
})
defineExpose({ spinTo })
</script>

<style scoped>
/* Widths stay fixed per mode so resizing never changes a saved landing offset. */
.lottery-reels { --reel-bg: #f0f2f4; --card-width: 148px; --card-height: 136px; display: grid; gap: 8px; letter-spacing: 0; }
.reel-row { position: relative; min-width: 0; padding-top: 6px; }
.reel-window { position: relative; display: flex; align-items: center; height: calc(var(--card-height) + 24px); overflow: hidden; border: 1px solid #e2e5e9; border-radius: 8px; background: var(--reel-bg); box-shadow: inset 0 1px 4px #11182706; }
.reel-window:focus-visible { outline: 2px solid #0d9488; outline-offset: 2px; }
.reel-window::before, .reel-window::after { content: ''; position: absolute; z-index: 2; top: 0; bottom: 0; width: 40px; pointer-events: none; }
.reel-window::before { left: 0; background: linear-gradient(to right, var(--reel-bg), transparent); }
.reel-window::after { right: 0; background: linear-gradient(to left, var(--reel-bg), transparent); }
.reel-track { position: relative; left: 50%; display: flex; gap: 8px; flex-shrink: 0; will-change: transform; }
.reel-card { position: relative; width: var(--card-width); height: var(--card-height); flex-shrink: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; padding: 10px; overflow: hidden; border: 1px solid color-mix(in srgb, var(--prize-color) 24%, #e5e7eb); border-bottom: 3px solid var(--prize-color); border-radius: 6px; background: #fff; box-shadow: 0 1px 2px #11182704; }
.reel-card :deep(.prize-icon) { width: 42px; height: 42px; border-radius: 7px; }
.reel-card :deep(.prize-icon svg) { width: 26px; height: 26px; }
.reel-card :deep(.prize-icon-dot) { display: none; }
.reel-copy { width: 100%; min-width: 0; display: flex; flex-direction: column; gap: 3px; text-align: center; }
.reel-value { display: block; max-width: 100%; font-size: 23px; line-height: 28px; color: var(--prize-color); font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.reel-name { display: block; max-width: 100%; font-size: 12px; line-height: 18px; color: #606a78; white-space: nowrap; text-overflow: ellipsis; overflow: hidden; }
.reel-pointer { position: absolute; z-index: 4; left: 50%; top: 0; transform: translateX(-50%); border-left: 7px solid transparent; border-right: 7px solid transparent; border-top: 14px solid #dca326; filter: drop-shadow(0 1px 1px #00000025); }
.reel-number { position: absolute; left: 7px; top: 10px; z-index: 3; color: #6b7280; font-size: 12px; line-height: 16px; font-weight: 600; font-variant-numeric: tabular-nums; }
.reel-check { position: absolute; top: 5px; right: 5px; width: 14px; height: 14px; color: #b77908; }
.reel-card.is-winner { border-color: #d59a17; background: #fffcf4; box-shadow: inset 0 0 0 1px #d59a17, 0 1px 8px #d59a1714; }
.reel-empty { width: 100%; padding: 8px; text-align: center; color: #6b7280; font-size: 12px; }
.is-multiple { --card-width: 184px; --card-height: 64px; gap: 6px; }
.is-multiple .reel-window { height: calc(var(--card-height) + 16px); }
.is-multiple .reel-card { flex-direction: row; justify-content: flex-start; gap: 10px; padding: 8px 16px 8px 10px; border-bottom-width: 2px; }
.is-multiple .reel-copy { flex: 1; text-align: left; gap: 1px; }
.is-multiple .reel-card :deep(.prize-icon) { width: 34px; height: 34px; border-radius: 6px; }
.is-multiple .reel-card :deep(.prize-icon svg) { width: 22px; height: 22px; }
.is-multiple .reel-value { font-size: 19px; line-height: 24px; }
.is-multiple .reel-pointer { border-left-width: 5px; border-right-width: 5px; border-top-width: 12px; }
.is-multiple .reel-check { width: 12px; height: 12px; top: 4px; right: 3px; }
.is-five { --card-width: 176px; --card-height: 52px; gap: 5px; }
.is-five .reel-window { height: calc(var(--card-height) + 14px); }
.is-five .reel-card { gap: 8px; padding-top: 5px; padding-bottom: 5px; }
.is-five .reel-card :deep(.prize-icon) { width: 30px; height: 30px; }
.is-five .reel-card :deep(.prize-icon svg) { width: 20px; height: 20px; }
.is-five .reel-value { font-size: 17px; line-height: 21px; }
.reel-value.is-long { font-size: 12px; }
.reel-tooltip { position: absolute; z-index: 10; bottom: calc(100% + 4px); left: 50%; transform: translateX(-50%); width: max-content; max-width: min(320px, 100%); display: grid; gap: 3px; padding: 8px 12px; border: 1px solid #e5e7eb; border-radius: 6px; background: #fff; color: #374151; font-size: 12px; line-height: 18px; overflow-wrap: anywhere; box-shadow: 0 4px 16px #00000014; pointer-events: none; }
.dark .lottery-reels { --reel-bg: #171717; }
.dark .reel-window { border-color: #303030; }
.dark .reel-card { background: #242424; border-color: color-mix(in srgb, var(--prize-color) 65%, #343434); }
.dark .reel-value { color: color-mix(in srgb, var(--prize-color) 55%, white); }
.dark .reel-name { color: #b5bbc4; }
.dark .reel-number { color: #9ca3af; }
.dark .reel-card.is-winner { border-color: #ddb246; background: #2d291f; box-shadow: inset 0 0 0 1px #ddb246; }
.dark .reel-tooltip { border-color: #404040; background: #262626; color: #e5e7eb; }
@media (max-width: 639px) {
  .reel-window::before, .reel-window::after { width: 20px; }
}
</style>
