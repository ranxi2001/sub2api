<template>
  <section class="min-w-0 space-y-3" :data-testid="prefix + '-rewards'">
    <h3 class="text-sm font-medium">{{ t('qqbot.rewardEditor.items') }}</h3>
    <p v-if="!kinds.length" class="rounded-lg border border-dashed border-gray-200 p-4 text-sm text-gray-500 dark:border-dark-600">{{ t(emptyText) }}</p>
    <div v-for="(kind, index) in kinds" :key="kind" class="space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-dark-800">
      <div class="grid items-start gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto]">
        <label class="min-w-0 space-y-1 text-sm">
          <span>{{ t('qqbot.rewardEditor.type') }}</span>
          <select :value="kind" :data-testid="prefix + '-reward-type-' + index" class="input" @change="changeKind(index, $event)">
            <option v-for="option in allowedTypes" :key="option" :value="option" :disabled="option !== kind && kinds.includes(option)">{{ t('qqbot.rewardEditor.types.' + option) }}</option>
          </select>
        </label>
        <template v-if="kind === 'balance'">
          <label class="min-w-0 space-y-1 text-sm">
            <span>{{ t(range ? 'qqbot.minAmount' : 'qqbot.rewardEditor.amount') }}</span>
            <input :value="reward[amountField]" :data-testid="amountTestId || prefix + '-amount'" type="number" min="0" max="1000" step="0.00000001" required class="input" @input="setNumber(amountField, $event)" />
          </label>
          <label v-if="range" class="min-w-0 space-y-1 text-sm">
            <span>{{ t('qqbot.maxAmount') }}</span>
            <input :value="reward.max_amount" :data-testid="maxAmountTestId || prefix + '-max-amount'" type="number" min="0" max="1000" step="0.00000001" required class="input" @input="setNumber('max_amount', $event)" />
          </label>
          <span v-else class="hidden lg:block" />
        </template>
        <template v-else-if="kind === 'coupon'">
          <div class="min-w-0 space-y-1 text-sm">
            <span>{{ t('coupon.template') }}</span>
            <CouponTemplateSelect v-model="reward.coupon_template_id" required />
          </div>
          <label class="min-w-0 space-y-1 text-sm">
            <span>{{ t('qqbot.rewardEditor.couponQuantity') }}</span>
            <input :value="reward.coupon_quantity" :data-testid="prefix + '-coupon-quantity'" type="number" min="1" max="100" step="1" required class="input" @input="setNumber('coupon_quantity', $event)" />
          </label>
        </template>
        <template v-else>
          <label class="min-w-0 space-y-1 text-sm">
            <span>{{ t('qqbot.rewardEditor.chancesQuantity') }}</span>
            <input :value="reward.chances" :data-testid="chancesTestId || prefix + '-chances'" type="number" min="0" max="1000" step="1" required class="input" @input="setNumber('chances', $event)" />
          </label>
          <span class="hidden lg:block" />
        </template>
        <button type="button" :data-testid="prefix + '-remove-reward-' + index" class="justify-self-start text-sm text-red-600 sm:mt-7" :aria-label="t('qqbot.rewardEditor.remove', { type: t('qqbot.rewardEditor.types.' + kind) })" @click="remove(index)">{{ t('common.delete') }}</button>
      </div>
      <p v-if="kind === 'coupon'" class="text-xs text-gray-500">{{ t('coupon.costHint') }}</p>
      <slot v-if="kind === 'balance'" name="balance-extra" />
    </div>
    <button type="button" :data-testid="prefix + '-add-reward'" class="btn btn-secondary" :disabled="kinds.length >= allowedTypes.length" @click="add">{{ t('qqbot.rewardEditor.add') }}</button>
    <p v-if="kinds.length > 1" class="text-xs text-gray-500">{{ t('qqbot.rewardEditor.bundleHelp') }}</p>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import CouponTemplateSelect from './CouponTemplateSelect.vue'
import type { RewardKind, RewardAmountField, RewardValues } from './rewardItems'

const reward = defineModel<RewardValues>({ required: true })
const props = withDefaults(defineProps<{
  prefix: string
  allowedTypes?: RewardKind[]
  amountField?: RewardAmountField
  range?: boolean
  emptyText?: string
  amountTestId?: string
  maxAmountTestId?: string
  chancesTestId?: string
}>(), {
  allowedTypes: () => ['balance', 'chances', 'coupon'],
  amountField: 'amount',
  emptyText: 'qqbot.rewardEditor.empty'
})
const { t } = useI18n()
const emit = defineEmits<{ remove: [kind: RewardKind] }>()
const kinds = ref<RewardKind[]>([])
watch(reward, value => {
  kinds.value = props.allowedTypes.filter(kind => kind === 'balance'
    ? Boolean(value[props.amountField] || (props.range && value.max_amount))
    : kind === 'chances' ? Boolean(value.chances) : Boolean(value.coupon_quantity || value.coupon_template_id))
}, { immediate: true })

function setNumber(field: keyof RewardValues, event: Event) {
  reward.value[field] = (event.target as HTMLInputElement).valueAsNumber
}
function clear(kind: RewardKind) {
  if (kind === 'balance') {
    reward.value[props.amountField] = 0
    if (props.range) reward.value.max_amount = 0
  } else if (kind === 'chances') reward.value.chances = 0
  else { reward.value.coupon_template_id = 0; reward.value.coupon_quantity = 0 }
  emit('remove', kind)
}
function initialize(kind: RewardKind) {
  if (kind === 'balance') {
    reward.value[props.amountField] = props.range ? 0.1 : 1
    if (props.range) reward.value.max_amount = 0.5
  } else if (kind === 'chances') reward.value.chances = 1
  else { reward.value.coupon_template_id = 0; reward.value.coupon_quantity = 1 }
}
function add() {
  const kind = props.allowedTypes.find(type => !kinds.value.includes(type))
  if (!kind) return
  kinds.value.push(kind)
  initialize(kind)
}
function remove(index: number) {
  clear(kinds.value[index]!)
  kinds.value.splice(index, 1)
}
function changeKind(index: number, event: Event) {
  const kind = (event.target as HTMLSelectElement).value as RewardKind
  if (!props.allowedTypes.includes(kind) || kinds.value.includes(kind)) return
  clear(kinds.value[index]!)
  kinds.value[index] = kind
  initialize(kind)
}
</script>
