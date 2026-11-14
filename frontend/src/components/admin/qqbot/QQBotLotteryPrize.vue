<template>
  <article class="min-w-0 space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700 sm:p-5" :data-testid="prefix + '-card-' + index">
    <div class="flex items-center justify-between gap-3">
      <h3 class="font-semibold">{{ t('qqbot.rewardEditor.prizeNumber', { number: index + 1 }) }}</h3>
      <button type="button" class="text-sm text-red-600 disabled:cursor-not-allowed disabled:opacity-40" :disabled="!removable" :aria-label="t('qqbot.rewardEditor.removePrize', { number: index + 1 })" @click="$emit('remove')">{{ t('qqbot.rewardEditor.deletePrize') }}</button>
    </div>
    <div class="grid gap-4 sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <label class="min-w-0 space-y-1 text-sm">{{ t('qqbot.prizeName') }}<input v-model.trim="prize.name" :data-testid="prefix + '-name-' + index" class="input" required maxlength="60" /></label>
      <label class="min-w-0 space-y-1 text-sm">{{ t('qqbot.probability') }}<input :value="prize.probability_bps / 100" :data-testid="guaranteed ? 'guarantee-probability-' + index : 'probability-' + index" class="input" type="number" min="0.01" max="100" step="0.01" required @input="setProbability" /></label>
    </div>
    <QQBotRewardItems :model-value="prize" :prefix="prefix + '-' + index" :amount-test-id="prefix + '-amount-' + index" :chances-test-id="prefix + '-chances-' + index" :empty-text="guaranteed ? 'qqbot.rewardEditor.guaranteeEmpty' : 'qqbot.rewardEditor.lotteryEmpty'" />
  </article>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { QQLotteryPrize } from '@/api/qqbot'
import QQBotRewardItems from './QQBotRewardItems.vue'

const prize = defineModel<QQLotteryPrize>({ required: true })
defineProps<{ index: number; prefix: string; guaranteed?: boolean; removable: boolean }>()
defineEmits<{ remove: [] }>()
const { t } = useI18n()
function setProbability(event: Event) {
  const value = (event.target as HTMLInputElement).valueAsNumber * 100
  const rounded = Math.round(value)
  prize.value.probability_bps = Math.abs(value - rounded) < 1e-8 ? rounded : Number.NaN
}
</script>
