<template>
  <div class="space-y-6">
    <p class="text-sm text-gray-500">{{ t('qqbot.lotteryHelp') }}</p>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
    <p v-if="dirty" class="text-sm text-amber-700">{{ t('qqbot.workspace.unsaved') }}</p>
    <form v-if="form" class="card space-y-5 p-6" @submit.prevent="save">
      <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('qqbot.lotteryRules') }}</h2><label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" />{{ t('common.enabled') }}</label></div>
      <label class="block space-y-1">{{ t('qqbot.activityName') }}<input v-model.trim="form.title" class="input" maxlength="60" required /></label>
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="space-y-1">{{ t('qqbot.rechargeStep') }}<input v-model.number="form.recharge_step" data-testid="recharge-step" class="input" type="number" min="0.01" max="1000000000" step="0.01" required /></label>
        <label class="space-y-1">{{ t('qqbot.chancesPerStep') }}<input v-model.number="form.chances_per_step" data-testid="chances-per-step" class="input" type="number" min="1" max="1000" step="1" required /></label>
      </div>
      <details class="rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800">
        <summary class="cursor-pointer font-medium">{{ t('qqbot.rewardEditor.rechargeRules') }}</summary>
        <div class="mt-3 space-y-2 text-gray-500"><p>{{ t('qqbot.historicalRechargeHelp') }}</p><p>{{ t('qqbot.lotteryRuleChangeHelp') }}</p></div>
      </details>
      <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ t('qqbot.prizePool') }}</h2><span :class="probabilityTotal === 10000 ? 'text-green-600' : 'text-red-600'">{{ t('qqbot.probabilityTotal', { total: (probabilityTotal / 100).toFixed(2) }) }}</span></div>
      <QQBotLotteryPrize v-for="(prize, index) in form.prizes" :key="index" :model-value="prize" :index="index" prefix="prize" :removable="form.prizes.length > 1" @remove="form.prizes.splice(index, 1)" />
      <button type="button" data-testid="add-lottery-prize" class="btn btn-secondary" :disabled="form.prizes.length >= 20" @click="addPrize">{{ t('qqbot.addPrize') }}</button>
      <p class="text-sm text-gray-500">{{ t('qqbot.prizeHelp') }}</p>
      <p class="rounded-lg bg-primary-50 p-3 text-sm text-primary-700 dark:bg-primary-950 dark:text-primary-300">{{ t('qqbot.chancePrizeExample') }}</p>
      <section class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700" data-testid="guarantee-settings">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ t('qqbot.guaranteeTitle') }}</h2>
          <label class="flex items-center gap-2"><input v-model="form.guarantee_enabled" data-testid="guarantee-enabled" type="checkbox" />{{ t('qqbot.enableGuarantee') }}</label>
        </div>
        <p class="text-sm text-gray-500">{{ t('qqbot.guaranteeHelp') }}</p>
        <template v-if="form.guarantee_enabled">
          <label class="block space-y-1">{{ t('qqbot.guaranteeEvery') }}<input v-model.number="form.guarantee_every" data-testid="guarantee-every" class="input" type="number" min="1" max="1000000" step="1" required /></label>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="font-semibold">{{ t('qqbot.guaranteePrizePool') }}</h3>
            <span :class="guaranteeProbabilityTotal === 10000 ? 'text-green-600' : 'text-red-600'">{{ t('qqbot.probabilityTotal', { total: (guaranteeProbabilityTotal / 100).toFixed(2) }) }}</span>
          </div>
          <QQBotLotteryPrize v-for="(prize, index) in form.guarantee_prizes" :key="index" :model-value="prize" :index="index" prefix="guarantee" guaranteed :removable="form.guarantee_prizes.length > 1" @remove="form.guarantee_prizes.splice(index, 1)" />
          <button type="button" data-testid="add-guarantee-prize" class="btn btn-secondary" :disabled="form.guarantee_prizes.length >= 20" @click="addGuaranteePrize">{{ t('qqbot.addPrize') }}</button>
          <p class="text-sm text-gray-500">{{ t('qqbot.guaranteePrizeHelp') }}</p>
        </template>
      </section>
      <p class="text-sm text-gray-500">{{ t('qqbot.lotteryCommand') }}</p>
      <p class="rounded-lg bg-blue-50 p-3 text-sm text-blue-800 dark:bg-blue-950 dark:text-blue-200">{{ t('qqbot.workspace.lotteryCost', { amount: expectedCost.toFixed(6), draws: 1000, cost: (expectedCost * 1000).toFixed(2) }) }}</p>
      <p class="text-sm text-gray-500" data-testid="lottery-chances-expected">{{ t('qqbot.expectedChanceReward', { count: expectedChances.toFixed(3) }) }}</p>
      <button type="submit" class="btn btn-primary" :disabled="saving || probabilityTotal !== 10000 || (form.guarantee_enabled && guaranteeProbabilityTotal !== 10000)">{{ t('common.save') }}</button>
    </form>
    <p v-else-if="!error">{{ t('common.loading') }}</p>
    <section class="card space-y-4 p-6">
      <h2 class="text-lg font-semibold">{{ t('qqbot.chanceLookup') }}</h2>
      <form class="flex flex-wrap gap-3" @submit.prevent="lookup"><label class="min-w-0 flex-1"><span class="sr-only">{{ t('qqbot.userID') }}</span><input v-model="userID" class="input" :placeholder="t('qqbot.userID')" type="number" min="1" step="1" required /></label><button class="btn btn-secondary" :disabled="lookingUp">{{ t('common.search') }}</button></form>
      <p v-if="lookupError" role="alert" class="text-red-600">{{ lookupError }}</p>
      <dl v-if="state" class="grid grid-cols-2 gap-4 text-sm sm:grid-cols-4">
        <div><dt class="text-gray-500">{{ t('qqbot.eligibleRecharge') }}</dt><dd class="mt-1 font-semibold">${{ state.recharge_total.toFixed(2) }}</dd></div>
        <div><dt class="text-gray-500">{{ t('qqbot.earnedChances') }}</dt><dd class="mt-1 font-semibold">{{ state.earned }}</dd></div>
        <div><dt class="text-gray-500">{{ t('qqbot.directRechargeChances') }}</dt><dd class="mt-1 font-semibold">{{ state.bonus_chances || 0 }}</dd></div>
        <div><dt class="text-gray-500">{{ t('qqbot.usedChances') }}</dt><dd class="mt-1 font-semibold">{{ state.used }}</dd></div>
        <div><dt class="text-gray-500">{{ t('qqbot.remainingChances') }}</dt><dd class="mt-1 font-semibold">{{ state.remaining }}</dd></div>
      </dl>
      <p v-if="state" class="text-sm text-gray-500">{{ t('qqbot.nextRecharge', { amount: state.next_recharge.toFixed(2) }) }}</p>
      <p v-if="state && state.draws_until_guarantee > 0" class="text-sm text-primary-600 dark:text-primary-400">{{ t('qqbot.guaranteeProgress', { every: state.guarantee_every, count: state.draws_until_guarantee }) }}</p>
    </section>

  </div>
</template>
<script setup lang="ts">
import QQBotLotteryPrize from './QQBotLotteryPrize.vue'
import { useQQDraft } from './useQQDraft'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQLotteryConfig, type QQLotteryState } from '@/api/qqbot'
import { validCouponReward } from './rewardItems'
const { t } = useI18n()
const form = ref<QQLotteryConfig | null>(null)
const { dirty, markSaved } = useQQDraft(form)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const expectedCost = computed(() => {
  if (!form.value) return 0
  const mean = (pool: QQLotteryConfig['prizes']) => pool.reduce((sum, p) => sum + p.amount * p.probability_bps / 10000, 0)
  const normal = mean(form.value.prizes)
  return form.value.guarantee_enabled && form.value.guarantee_every > 0 ? (normal * (form.value.guarantee_every - 1) + mean(form.value.guarantee_prizes)) / form.value.guarantee_every : normal
})
const expectedChances = computed(() => {
  if (!form.value) return 0
  const mean = (pool: QQLotteryConfig['prizes']) => pool.reduce((sum, p) => sum + (p.chances ?? 0) * p.probability_bps / 10000, 0)
  const normal = mean(form.value.prizes)
  return form.value.guarantee_enabled && form.value.guarantee_every > 0 ? (normal * (form.value.guarantee_every - 1) + mean(form.value.guarantee_prizes)) / form.value.guarantee_every : normal
})
const probabilityTotal = computed(() => form.value?.prizes.reduce((sum, p) => sum + p.probability_bps, 0) ?? 0)
const guaranteeProbabilityTotal = computed(() => form.value?.guarantee_prizes.reduce((sum, p) => sum + p.probability_bps, 0) ?? 0)
const userID = ref('')
const state = ref<QQLotteryState | null>(null)
const lookingUp = ref(false)
const lookupError = ref('')
function message(e: unknown) { return (e as Error).message || t('common.error') }
function validPrize(prize: QQLotteryConfig['prizes'][number], guaranteed = false) {
  const coupons = prize.coupon_quantity ?? 0
  if (!validCouponReward(prize)) return false
  const chances = prize.chances ?? 0
  return Boolean(prize.name.trim()) && [...prize.name].length <= 60
    && Number.isFinite(prize.amount) && prize.amount >= 0 && prize.amount <= 1000 && Math.abs(prize.amount * 1e8 - Math.round(prize.amount * 1e8)) <= 0.001
    && Number.isInteger(chances) && chances >= 0 && chances <= 1000
    && Number.isInteger(prize.probability_bps) && prize.probability_bps >= 1 && prize.probability_bps <= 10000
    && (!guaranteed || prize.amount > 0 || chances > 0 || coupons > 0)
}
function addPrize() { form.value?.prizes.push({ name: '', amount: 0, probability_bps: Math.max(1, 10000 - probabilityTotal.value) }) }
function addGuaranteePrize() { form.value?.guarantee_prizes.push({ name: '', amount: 0, probability_bps: Math.max(1, 10000 - guaranteeProbabilityTotal.value) }) }
function normalizeConfig(config: QQLotteryConfig): QQLotteryConfig {
  return {
    ...config,
    guarantee_enabled: config.guarantee_enabled ?? false,
    guarantee_every: config.guarantee_every || 10,
    guarantee_prizes: config.guarantee_prizes?.length ? config.guarantee_prizes : [{ name: t('qqbot.defaultGuaranteePrize'), amount: 1, probability_bps: 10000 }]
  }
}
async function save() {
  if (!form.value || saving.value || probabilityTotal.value !== 10000) return
  error.value = ''; notice.value = ''
  if (form.value.prizes.length < 1 || form.value.prizes.length > 20 || !form.value.prizes.every(prize => validPrize(prize))) { error.value = t('qqbot.invalidLotteryPrizes'); return }
  if (form.value.guarantee_enabled) {
    if (!Number.isInteger(form.value.guarantee_every) || form.value.guarantee_every < 1 || form.value.guarantee_every > 1000000) {
      error.value = t('qqbot.invalidGuaranteeEvery'); return
    }
    if (guaranteeProbabilityTotal.value !== 10000 || form.value.guarantee_prizes.length < 1 || form.value.guarantee_prizes.length > 20 || !form.value.guarantee_prizes.every(prize => validPrize(prize, true))) {
      error.value = t('qqbot.invalidGuaranteePrizes'); return
    }
  }
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const config = await qqbotAPI.saveLottery({
      ...form.value,
      guarantee_every: Number.isInteger(form.value.guarantee_every) && form.value.guarantee_every >= 1 && form.value.guarantee_every <= 1000000 ? form.value.guarantee_every : 0,
      prizes: form.value.prizes.map(p => ({ ...p })),
      guarantee_prizes: form.value.guarantee_prizes.map(p => ({ ...p }))
    })
    form.value = normalizeConfig(config); markSaved(); notice.value = t('common.saved'); state.value = null
  }
  catch (e) { error.value = message(e) }
  finally { saving.value = false }
}
async function lookup() {
  lookingUp.value = true; lookupError.value = ''; state.value = null
  try { state.value = await qqbotAPI.lotteryState(Number(userID.value)) }
  catch (e) { lookupError.value = message(e) }
  finally { lookingUp.value = false }
}
onMounted(async () => {
  try { form.value = normalizeConfig(await qqbotAPI.lotteryConfig()) }
  catch (e) { error.value = message(e) }
})
</script>
