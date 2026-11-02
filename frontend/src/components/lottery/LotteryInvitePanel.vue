<template>
  <section class="mt-4 min-w-0 border-t border-gray-200 pt-4 dark:border-dark-700" :aria-label="t('lottery.invitation')">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <h3 class="flex items-center gap-2 text-sm font-semibold"><Icon name="gift" size="sm" class="text-primary-600" />{{ t('lottery.invitation') }}</h3>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t(lotteryRuleKey(pool), lotteryRuleParams(pool)) }}</p>
        <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t(pool.invite_config?.reward_to === 'both' ? 'lottery.rewardBoth' : 'lottery.rewardInviter') }}</p>
      </div>
      <div v-if="summary" class="flex gap-5 text-xs text-gray-500 dark:text-gray-400">
        <div v-for="metric in metrics" :key="metric" class="text-center"><strong class="block text-lg font-semibold tabular-nums text-gray-900 dark:text-gray-100">{{ summary[metric] }}</strong>{{ t(`lottery.${metric}`) }}</div>
      </div>
    </div>
    <div v-if="summary" class="mt-3 flex min-w-0 gap-2">
      <input :value="link" readonly class="input min-w-0 flex-1 text-xs" :aria-label="t('lottery.inviteLink')" @focus="($event.target as HTMLInputElement).select()" />
      <button class="btn btn-secondary !h-11 !w-11 shrink-0 !p-2" :title="t(copied ? 'lottery.copied' : 'lottery.copyLink')" :aria-label="t(copied ? 'lottery.copied' : 'lottery.copyLink')" @click="copyLink"><Icon :name="copied ? 'check' : 'clipboard'" size="sm" /></button>
    </div>
    <p v-if="summary?.invited && pool.mode === 'invite_recharge'" class="mt-3 text-xs text-primary-700 dark:text-primary-400">{{ t('lottery.invitedProgress', { amount: money(summary.incoming_progress), threshold: money(pool.threshold) }) }}</p>
    <p v-if="pool.mode === 'invite_visit' && pool.invite_config?.reward_to === 'both'" class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('lottery.visitRecipientNote') }}</p>
    <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('lottery.inviteLimits', { limit: pool.invite_config?.daily_limit, ip: pool.invite_config?.ip_daily_limit }) }}</p>
    <p v-if="error" role="alert" class="mt-2 text-xs text-red-600">{{ error }} <button class="underline" @click="load">{{ t('lottery.refresh') }}</button></p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { lotteryAPI, lotteryRuleKey, lotteryRuleParams, type LotteryPool, type LotteryInviteSummary } from '@/api/lottery'

const props = defineProps<{ pool: LotteryPool }>()
const { t, te, locale } = useI18n()
const summary = ref<LotteryInviteSummary | null>(null)
const error = ref(''), copied = ref(false)
const metrics = ['visits', 'registered', 'qualified'] as const
const link = computed(() => summary.value ? new URL(summary.value.path, window.location.origin).href : '')
const money = (amount: number) => new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }).format(amount)
let generation = 0
async function load() {
  const current = ++generation
  error.value = ''
  copied.value = false
  summary.value = null
  try {
    const result = await lotteryAPI.invite(props.pool.id)
    if (current === generation) summary.value = result
  } catch (e) {
    const key = `lottery.errors.${(e as { reason?: string }).reason}`
    if (current === generation) error.value = te(key) ? t(key) : t('lottery.error')
  }
}
async function copyLink() {
  try { await navigator.clipboard.writeText(link.value); copied.value = true }
  catch { error.value = t('lottery.copyFailed') }
}
watch(() => props.pool, load, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>
