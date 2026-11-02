<template>
  <section
    v-if="pools.length"
    class="lottery-recharge-notice"
    :aria-label="t(`lottery.rechargeNotice.${source}Title`)"
  >
    <div class="notice-heading">
      <h2 class="flex min-w-0 items-center gap-2 text-sm font-semibold text-primary-800 dark:text-primary-200">
        <Icon name="gift" size="md" class="shrink-0 text-primary-600 dark:text-primary-400" />
        {{ t(`lottery.rechargeNotice.${source}Title`) }}
      </h2>
      <router-link to="/lottery" class="notice-link">
        {{ t('lottery.rechargeNotice.viewPools') }}<Icon name="arrowRight" size="sm" />
      </router-link>
    </div>
    <p class="text-xs leading-5 text-gray-600 dark:text-gray-400">
      {{ t(`lottery.rechargeNotice.${source}Hint`) }}
    </p>
    <ul class="mt-3 divide-y divide-primary-100 dark:divide-primary-800/40">
      <li v-for="pool in pools" :key="pool.id" class="notice-pool">
        <div class="min-w-0 flex-1">
          <h3 class="break-words text-sm font-medium text-gray-900 dark:text-gray-100">{{ pool.name }}</h3>
          <p class="mt-1 text-sm leading-6 text-primary-800 dark:text-primary-200">
            {{ t(lotteryRuleKey(pool), { amount: money(pool.threshold) }) }}
          </p>
          <p v-if="!pool.draw_enabled" class="mt-1 text-xs leading-5 text-amber-700 dark:text-amber-400">
            {{ t('lottery.rechargeNotice.drawPaused') }}
          </p>
          <p v-else-if="!pool.prizes.some(prize => prize.available)" class="mt-1 text-xs leading-5 text-amber-700 dark:text-amber-400">
            {{ t('lottery.noPrizes') }}
          </p>
          <p v-if="pool.debt > 0" class="mt-1 text-xs leading-5 text-amber-700 dark:text-amber-400">
            {{ t('lottery.rechargeNotice.debtHint', { count: pool.debt }) }}
          </p>
        </div>
        <div v-if="pool.mode === 'cumulative' || pool.available > 0" class="notice-progress">
          <p v-if="pool.mode === 'cumulative'">
            {{ t('lottery.progress') }}
            <span class="font-medium tabular-nums text-gray-700 dark:text-gray-200">{{ money(pool.progress) }} / {{ money(pool.threshold) }} USD</span>
          </p>
          <p v-if="pool.available > 0" class="font-medium text-primary-700 dark:text-primary-300">
            {{ t('lottery.pendingPool', { count: pool.available }) }}
          </p>
        </div>
      </li>
    </ul>
    <p v-if="pools.length > 1" class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">
      {{ t('lottery.rechargeNotice.multiplePools') }}
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { lotteryRuleKey } from '@/api/lottery'
import { useLotteryStore } from '@/stores/lottery'
import Icon from '@/components/icons/Icon.vue'

defineProps<{ source: 'recharge' | 'redeem' }>()

const { t, locale } = useI18n()
const lottery = useLotteryStore()
const pools = computed(() => lottery.enabled
  ? lottery.pools.filter(pool => pool.grant_enabled && (pool.mode === 'single' || pool.mode === 'cumulative'))
  : [])
const money = (amount: number) => amount.toLocaleString(locale.value, { maximumFractionDigits: 8 })

onMounted(() => { void lottery.refresh() })
</script>

<style scoped>
.lottery-recharge-notice {
  @apply min-w-0 border-y border-primary-200/70 bg-primary-50/70 px-4 py-3 dark:border-primary-800/50 dark:bg-primary-900/10;
}
.notice-heading { @apply flex min-w-0 flex-wrap items-center justify-between gap-x-4; }
.notice-link {
  @apply inline-flex min-h-11 shrink-0 items-center gap-1 text-xs font-medium text-primary-700 hover:text-primary-600 focus-visible:rounded focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-primary-300 dark:hover:text-primary-200;
}
.notice-pool { @apply flex min-w-0 flex-wrap items-start gap-x-5 gap-y-2 py-3 first:pt-0 last:pb-0; }
.notice-progress { @apply flex max-w-full flex-col gap-1 text-xs leading-5 text-gray-500 dark:text-gray-400 sm:text-right; }
.notice-progress span { @apply ml-1 inline-block; }
@media (max-width: 639px) {
  .notice-pool > div { width: 100%; }
}
</style>
