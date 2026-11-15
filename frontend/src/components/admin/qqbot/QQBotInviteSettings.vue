<template>
  <div class="space-y-6">
    <p class="text-sm text-gray-500">{{ t('qqbot.inviteHelp') }}</p>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
    <p v-if="dirty" class="text-sm text-amber-700">{{ t('qqbot.workspace.unsaved') }}</p>
    <form v-if="form" class="card space-y-5 p-6" @submit.prevent="save">
      <h2 class="text-lg font-semibold">{{ t('qqbot.inviteSettings') }}</h2>
      <div class="space-y-2">
        <div class="flex items-center justify-between gap-4">
          <span id="invite-enabled-label" class="font-medium">{{ t('qqbot.enableInvite') }}</span>
          <Toggle v-model="form.enabled" data-testid="invite-enabled" aria-labelledby="invite-enabled-label" :disabled="busy" />
        </div>
        <p class="text-sm text-gray-500">{{ t('qqbot.inviteEnabledHelp') }}</p>
      </div>
      <QQBotRewardItems :model-value="form" prefix="invite" :allowed-types="['balance', 'coupon']" amount-field="invite_reward" amount-test-id="invite-reward" />
      <label class="block space-y-1">{{ t('qqbot.inviteDaily') }}<input v-model.number="form.invite_daily_limit" data-testid="invite-daily-limit" class="input" type="number" min="0" max="100000" step="1" required /></label>
      <div class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
        <h3 class="font-medium">{{ t('qqbot.rechargeEligibility') }}</h3>
        <p class="text-sm text-gray-500">{{ t('qqbot.inviteRechargeEligibilityHelp') }}</p>
        <label class="block space-y-1">{{ t('qqbot.rechargeWindowDays') }}<input v-model.number="form.recharge_window_days" data-testid="invite-recharge-window-days" type="number" min="1" max="3650" step="1" required class="input" /></label>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="space-y-1">
            {{ t('qqbot.minimumLifetimeRecharge') }}
            <input v-model.number="form.min_recharge_total" data-testid="invite-min-recharge-total" class="input" type="number" min="0" max="1000000000" step="0.01" required />
          </label>
          <label class="space-y-1">
            {{ t('qqbot.minimumRecentRecharge', { days: form.recharge_window_days }) }}
            <input v-model.number="form.min_recharge_30d" data-testid="invite-min-recharge-30d" class="input" type="number" min="0" max="1000000000" step="0.01" required />
          </label>
        </div>
      </div>
      <button type="submit" class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button>
    </form>
    <p v-else-if="!error">{{ t('common.loading') }}</p>
  </div>
</template>

<script setup lang="ts">
import QQBotRewardItems from './QQBotRewardItems.vue'
import { useQQDraft } from './useQQDraft'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQInviteConfig } from '@/api/qqbot'
import Toggle from '@/components/common/Toggle.vue'
import { validCouponReward, validRewardAmount } from './rewardItems'

const { t } = useI18n()
const form = ref<QQInviteConfig | null>(null)
const { dirty, markSaved } = useQQDraft(form)
const busy = ref(false)
const error = ref('')
const notice = ref('')

async function save() {
  if (!form.value || busy.value) return
  error.value = ''; notice.value = ''
  if (!validRewardAmount(form.value.invite_reward)) { error.value = t('qqbot.invalidRange'); return }
  if (!validCouponReward(form.value)) { error.value = t('qqbot.rewardEditor.invalidCoupon'); return }
  busy.value = true
  error.value = ''; notice.value = ''
  if (![form.value.min_recharge_total, form.value.min_recharge_30d].every(n => Number.isFinite(n) && n >= 0 && n <= 1_000_000_000 && Number(n.toFixed(2)) === n)) {
    error.value = t('qqbot.invalidRechargeThreshold')
    busy.value = false
    return
  }
  if (!Number.isInteger(form.value.recharge_window_days) || form.value.recharge_window_days < 1 || form.value.recharge_window_days > 3650) {
    error.value = t('qqbot.invalidRechargeWindow')
    busy.value = false
    return
  }
  try {
    form.value = await qqbotAPI.saveInvite({ ...form.value })
    markSaved(); notice.value = t('common.saved')
  } catch (e) {
    error.value = (e as Error).message || t('common.error')
  } finally {
    busy.value = false
  }
}
onMounted(async () => {
  try {
    const config = await qqbotAPI.inviteConfig()
    form.value = {
      ...config,
      coupon_template_id: config.coupon_template_id ?? 0,
      coupon_quantity: config.coupon_quantity ?? 0,
      enabled: typeof config.enabled === 'boolean' ? config.enabled : config.invite_reward > 0 && config.invite_daily_limit > 0,
      recharge_window_days: config.recharge_window_days || 30
    }
  }
  catch (e) { error.value = (e as Error).message || t('common.error') }
})
</script>
