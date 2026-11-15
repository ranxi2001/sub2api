<template>
  <section class="min-w-0 space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-dark-800">
    <h3 class="text-sm font-medium">{{ t('qqbot.rewardEditor.items') }}</h3>
    <div class="grid items-start gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <label class="block min-w-0 space-y-1 text-sm">{{ t('qqbot.rewardEditor.type') }}
        <select v-model="reward.type" :data-testid="fieldId('reward-type')" class="input" @change="changeType">
          <option v-for="kind in allowedTypes ?? types" :key="kind" :value="kind">{{ t('qqbot.rewardEditor.types.' + kind) }}</option>
        </select>
      </label>
      <label v-if="reward.type === 'balance'" class="block min-w-0 space-y-1 text-sm">{{ t('qqbot.rewardEditor.amount') }}<input v-model.number="reward.amount" :data-testid="fieldId('reward-amount')" type="number" min="0" max="1000" step="0.00000001" required class="input" /></label>
      <label v-else class="block min-w-0 space-y-1 text-sm">{{ t(reward.type === 'chances' ? 'qqbot.rewardEditor.chancesQuantity' : reward.type === 'coupon' ? 'qqbot.rewardEditor.couponQuantity' : 'qqbot.rewardEditor.quantity') }}<input v-model.number="reward.quantity" :data-testid="fieldId(reward.type === 'chances' ? 'chances' : 'reward-quantity')" type="number" min="0" :max="reward.type === 'coupon' ? 100 : 1000" step="1" required class="input" /></label>
      <div v-if="reward.type === 'coupon'" class="min-w-0 space-y-1 text-sm"><span>{{ t('coupon.template') }}</span><CouponTemplateSelect v-model="reward.coupon_template_id" required /></div>
      <template v-if="reward.type === 'physical' || reward.type === 'other'">
        <label class="block space-y-1">{{ t('qqbot.redemption.prize') }}<input v-model="reward.prize_name" :data-testid="fieldId('prize-name')" maxlength="100" :required="reward.quantity > 0" class="input" /></label>
        <label class="block space-y-1">{{ t('qqbot.redemption.instructions') }}<textarea v-model="reward.claim_instructions" :data-testid="fieldId('instructions')" maxlength="500" rows="2" class="input" /></label>
      </template>
    </div>
    <p v-if="reward.type === 'coupon'" class="text-xs text-gray-500">{{ t('coupon.costHint') }}</p>
    <p class="text-xs text-gray-500">{{ t(reward.type === 'coupon' ? 'coupon.qqHelp' : 'qqbot.growth.rewardHelp') }}</p>
  </section>
</template>
<script setup lang="ts">
import CouponTemplateSelect from './CouponTemplateSelect.vue'
import { useI18n } from 'vue-i18n'
import type { QQGrowthReward, QQRewardType } from '@/api/qqbot'
const { t } = useI18n()
const reward = defineModel<QQGrowthReward>({ required: true })
const props = defineProps<{ prefix: string; index?: number; allowedTypes?: QQRewardType[] }>()
const types: QQRewardType[] = ['chances', 'balance', 'physical', 'other', 'coupon']
function fieldId(name: string) { return props.prefix + '-' + name + (props.index === undefined ? '' : '-' + props.index) }
function changeType() {
  reward.value = { type: reward.value.type, amount: 0, quantity: ['physical', 'other', 'coupon'].includes(reward.value.type) ? 1 : 0, prize_name: '', claim_instructions: '' }
}
</script>
