<template>
  <label class="mt-4 block text-sm font-medium text-gray-700 dark:text-gray-300">
    {{ t('payment.stripePaymentMethod') }}
    <select class="input mt-2 w-full" :value="modelValue" data-testid="stripe-subtype" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
      <option v-for="(rates, method) in methods" :key="method" :value="method">
        {{ t(`payment.methods.${method}`, method) }} · {{ rateLabel(rates) }}
      </option>
    </select>
  </label>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { MethodFeeRates } from '@/types/payment'
import { paymentFeeRange } from './fees'

defineProps<{ modelValue: string; methods: Record<string, MethodFeeRates> }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
function rateLabel(rates: MethodFeeRates): string {
  const { min, max } = paymentFeeRange(rates)
  return `${t('payment.fee')} ${min === max ? min : `${min}–${max}`}%`
}
</script>
