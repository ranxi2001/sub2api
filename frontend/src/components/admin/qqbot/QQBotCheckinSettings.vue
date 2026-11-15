<template>
  <div class="space-y-6">
    <p class="text-sm text-gray-500">{{ t('qqbot.checkinHelp') }}</p>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
    <p v-if="dirty" class="text-sm text-amber-700">{{ t('qqbot.workspace.unsaved') }}</p>
    <form v-if="form" class="card space-y-5 p-6" @submit.prevent="save">
      <label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" />{{ t('qqbot.enableCheckin') }}</label>
      <QQBotRewardItems :model-value="form" prefix="checkin" :allowed-types="['balance', 'coupon']" amount-field="min_amount" range amount-test-id="min-amount" max-amount-test-id="max-amount" @remove="resetDistribution">
        <template #balance-extra>
          <fieldset class="space-y-3 rounded-lg border p-4 dark:border-dark-700" :disabled="busy">
            <legend class="font-medium">{{ t('qqbot.checkinDistributionTitle') }}</legend>
            <p class="text-sm text-gray-500">{{ t('qqbot.checkinDistributionHelp') }}</p>
            <div class="grid gap-4 sm:grid-cols-3">
              <label class="space-y-1">{{ t('qqbot.checkinLowerHalf') }}<input v-model.number="lowerHalfPercent" data-testid="lower-half-percent" type="number" min="0" max="100" step="0.01" required class="input" /></label>
              <label class="space-y-1">{{ t('qqbot.checkinNextQuarter') }}<input v-model.number="nextQuarterPercent" data-testid="next-quarter-percent" type="number" min="0" max="100" step="0.01" required class="input" /></label>
              <label class="space-y-1">{{ t('qqbot.checkinUpperQuarter') }}<input v-model.number="upperQuarterPercent" data-testid="upper-quarter-percent" type="number" min="0" max="100" step="0.01" required class="input" /></label>
            </div>
            <p class="text-sm" :class="distributionValid ? 'text-gray-500' : 'text-red-600'" data-testid="probability-total">{{ t('qqbot.checkinDistributionTotal', { total: distributionTotal }) }}</p>
          </fieldset>
        </template>
      </QQBotRewardItems>
      <div class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
        <h2 class="font-medium">{{ t('qqbot.rechargeEligibility') }}</h2>
        <p class="text-sm text-gray-500">{{ t('qqbot.rechargeEligibilityHelp') }}</p>
        <label class="block space-y-1">{{ t('qqbot.rechargeWindowDays') }}<input v-model.number="form.recharge_window_days" data-testid="recharge-window-days" type="number" min="1" max="3650" step="1" required class="input" /></label>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="space-y-1">{{ t('qqbot.minimumLifetimeRecharge') }}<input v-model.number="form.min_recharge_total" data-testid="min-recharge-total" type="number" min="0" max="1000000000" step="0.01" required class="input" /></label>
          <label class="space-y-1">{{ t('qqbot.minimumRecentRecharge', { days: form.recharge_window_days }) }}<input v-model.number="form.min_recharge_30d" data-testid="min-recharge-30d" type="number" min="0" max="1000000000" step="0.01" required class="input" /></label>
        </div>
      </div>
      <fieldset class="min-w-0 space-y-4 rounded-xl border border-primary-200 p-4 dark:border-primary-900" :disabled="busy" data-testid="checkin-milestones">
        <legend class="px-1 font-semibold">{{ t('qqbot.milestones.title') }}</legend>
        <p class="text-sm text-gray-500">{{ t('qqbot.milestones.help') }}</p>
        <div class="flex flex-wrap gap-2">
          <button v-for="days in [7, 30]" :key="days" type="button" class="btn btn-secondary" :data-testid="'milestone-preset-' + days" :disabled="form.milestones.length >= maxMilestones" @click="addMilestone(days)">{{ t('qqbot.milestones.preset', { days }) }}</button>
        </div>
        <article v-for="(milestone, index) in form.milestones" :key="milestone.id" class="space-y-4 rounded-lg border bg-white p-4 dark:border-dark-700 dark:bg-dark-800" data-testid="checkin-milestone">
          <div class="flex items-center justify-between gap-3">
            <label class="flex items-center gap-2 text-sm"><input v-model="milestone.enabled" type="checkbox" :data-testid="'milestone-enabled-' + index" />{{ t('qqbot.milestones.enabled') }}</label>
            <button type="button" class="text-sm text-red-600" :data-testid="'milestone-remove-' + index" :aria-label="t('qqbot.milestones.remove', { number: index + 1 })" @click="form.milestones.splice(index, 1)">{{ t('common.delete') }}</button>
          </div>
          <div class="grid gap-4 sm:grid-cols-3">
            <label class="space-y-1">{{ t('qqbot.milestones.name') }}<input v-model="milestone.name" :data-testid="'milestone-name-' + index" maxlength="40" required class="input" /></label>
            <label class="space-y-1">{{ t('qqbot.milestones.kind') }}<select v-model="milestone.kind" class="input" :disabled="persistedMilestoneIDs.has(milestone.id) && milestone.repeat" @change="milestone.repeat = false" :data-testid="'milestone-kind-' + index"><option value="checkin_total">{{ t('qqbot.growth.kinds.checkin_total') }}</option><option value="checkin_streak">{{ t('qqbot.growth.kinds.checkin_streak') }}</option></select></label>
            <label class="space-y-1">{{ t('qqbot.milestones.days') }}<input v-model.number="milestone.days" :data-testid="'milestone-days-' + index" type="number" min="1" max="3650" step="1" required class="input" /></label>
          </div>
          <label v-if="milestone.kind === 'checkin_total'" class="flex items-center gap-2 text-sm"><input v-model="milestone.repeat" type="checkbox" :disabled="persistedMilestoneIDs.has(milestone.id)" :title="persistedMilestoneIDs.has(milestone.id) ? t('qqbot.milestones.repeatLocked') : undefined" :data-testid="'milestone-repeat-' + index" />{{ t('qqbot.milestones.repeat') }}</label>
          <QQBotGrowthRewardFields v-model="milestone.reward" prefix="milestone" :index="index" :allowed-types="milestone.reward.type === 'balance' || milestone.reward.type === 'chances' ? ['chances', 'balance', 'coupon'] : undefined" />
        </article>
        <p v-if="!form.milestones.length" class="rounded-lg border border-dashed p-4 text-sm text-gray-500 dark:border-dark-700">{{ t('qqbot.milestones.empty') }}</p>
        <button type="button" class="btn btn-secondary" data-testid="add-checkin-milestone" :disabled="form.milestones.length >= maxMilestones" @click="addMilestone(7)">{{ t('qqbot.milestones.add') }}</button>
        <p class="text-sm text-gray-500">{{ t('qqbot.milestones.identityHelp') }}</p>
        <p class="text-xs text-gray-500">{{ t('qqbot.milestones.repeatLocked') }}</p>
      </fieldset>
      <fieldset class="space-y-3 rounded-lg border p-4 dark:border-dark-700" :disabled="busy">
        <legend class="font-medium">{{ t('qqbot.growth.streakTitle') }}</legend>
        <p class="text-sm text-gray-500">{{ t('qqbot.growth.streakHelp') }}</p>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block space-y-1">{{ t('qqbot.growth.streakDays') }}<input v-model.number="form.streak_days" data-testid="streak-days" type="number" min="1" max="3650" step="1" required class="input" /></label>
        </div>
        <QQBotGrowthRewardFields v-model="form.streak_reward" prefix="streak" />
      </fieldset>
      <p class="rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800">{{ t('qqbot.checkinRangeHelp') }}</p>
      <p class="text-sm text-gray-500">{{ t('qqbot.checkinCommand') }}</p>
      <button type="submit" class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button>
    </form>
    <p v-else-if="!error">{{ t('common.loading') }}</p>
  </div>
</template>
<script setup lang="ts">
import QQBotRewardItems from './QQBotRewardItems.vue'
import { useQQDraft } from './useQQDraft'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QQCheckinConfig, QQCheckinRewardDistribution } from '@/api/qqbot'
import { loadCheckinConfig, saveCheckinSettings, validCheckinDistribution } from './checkinSettings'
import QQBotGrowthRewardFields from './QQBotGrowthRewardFields.vue'
import { normalizeGrowthReward, validGrowthReward } from './growthReward'
import { validCouponReward, validRewardAmount, type RewardKind } from './rewardItems'
const { t } = useI18n()
const maxMilestones = 32
const form = ref<QQCheckinConfig | null>(null)
const persistedMilestoneIDs = ref(new Set<string>())
const { dirty, markSaved } = useQQDraft(form)
const busy = ref(false)
const error = ref('')
const notice = ref('')
function resetDistribution(kind: RewardKind) {
  if (kind === 'balance' && form.value) form.value.reward_distribution = { lower_half_bps: 7000, next_quarter_bps: 2000, upper_quarter_bps: 1000 }
}
function probabilityPercent(key: keyof QQCheckinRewardDistribution) {
  return computed({
    get: () => (form.value?.reward_distribution?.[key] ?? 0) / 100,
    set: (value: number | string) => {
      if (!form.value?.reward_distribution) return
      const percent = typeof value === 'number' ? value : Number.NaN
      const bps = Math.round(percent * 100)
      form.value.reward_distribution[key] = Number.isFinite(percent) && Math.abs(percent * 100 - bps) < 1e-8 ? bps : Number.NaN
    }
  })
}
const lowerHalfPercent = probabilityPercent('lower_half_bps')
const nextQuarterPercent = probabilityPercent('next_quarter_bps')
const upperQuarterPercent = probabilityPercent('upper_quarter_bps')
const distributionValid = computed(() => validCheckinDistribution(form.value?.reward_distribution))
const distributionTotal = computed(() => {
  const total = lowerHalfPercent.value + nextQuarterPercent.value + upperQuarterPercent.value
  return Number.isFinite(total) ? total.toFixed(2) : '—'
})
function addMilestone(days: number) {
  if (!form.value || busy.value || form.value.milestones.length >= maxMilestones) return
  form.value.milestones.push({ id: 'milestone_' + crypto.randomUUID().replace(/-/g, ''), name: t('qqbot.milestones.defaultName', { days }), kind: 'checkin_total', days, reward: normalizeGrowthReward(undefined, 1), enabled: false })
}
async function save() {
  if (!form.value || busy.value) return
  error.value = ''; notice.value = ''
  if (!distributionValid.value) { error.value = t('qqbot.invalidCheckinDistribution'); return }
  if (!validRewardAmount(form.value.min_amount) || !validRewardAmount(form.value.max_amount) || form.value.min_amount > form.value.max_amount) { error.value = t('qqbot.invalidRange'); return }
  if (!validCouponReward(form.value)) { error.value = t('qqbot.rewardEditor.invalidCoupon'); return }
  if (![form.value.min_recharge_total, form.value.min_recharge_30d].every(n => Number.isFinite(n) && n >= 0 && n <= 1_000_000_000 && Number(n.toFixed(2)) === n)) { error.value = t('qqbot.invalidRechargeThreshold'); return }
  if (!Number.isInteger(form.value.recharge_window_days) || form.value.recharge_window_days < 1 || form.value.recharge_window_days > 3650) { error.value = t('qqbot.invalidRechargeWindow'); return }
  if (!Number.isInteger(form.value.streak_days) || form.value.streak_days < 1 || form.value.streak_days > 3650) {
    error.value = t('qqbot.growth.invalidStreakDays')
    return
  }
  if (!validGrowthReward(form.value.streak_reward)) {
    error.value = t('qqbot.growth.invalidReward')
    return
  }
  if (form.value.milestones.length > maxMilestones || !form.value.milestones.every(milestone =>
    milestone.name.trim() && [...milestone.name].length <= 40 && ![...milestone.name].some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)
    && ['checkin_total', 'checkin_streak'].includes(milestone.kind) && (!milestone.repeat || milestone.kind === 'checkin_total') && Number.isInteger(milestone.days) && milestone.days >= 1 && milestone.days <= 3650
    && validGrowthReward(milestone.reward))) {
    error.value = t('qqbot.milestones.invalid'); return
  }
  busy.value = true
  try { form.value = await saveCheckinSettings(form.value); persistedMilestoneIDs.value = new Set(form.value.milestones.map(milestone => milestone.id)); markSaved(); notice.value = t('common.saved') }
  catch (e) { error.value = (e as Error).message || t('common.error') }
  finally { busy.value = false }
}
onMounted(async () => {
  try { form.value = await loadCheckinConfig(); persistedMilestoneIDs.value = new Set(form.value.milestones.map(milestone => milestone.id)) }
  catch (e) { error.value = (e as Error).message || t('common.error') }
})
</script>
