<template><div class="space-y-1"><select :value="modelValue || (required ? '' : 0)" :required="required" class="input" :aria-label="t('coupon.template')" @change="emit('update:modelValue', Number(($event.target as HTMLSelectElement).value))"><option :value="required ? '' : 0">{{ t(required ? 'qqbot.rewardEditor.selectCoupon' : 'coupon.noTemplate') }}</option><option v-if="modelValue && !items.some(c => c.id === modelValue)" :value="modelValue" disabled>{{ t('coupon.unavailableTemplate', { id: modelValue }) }}</option><option v-for="c in items" :key="c.id" :value="c.id">#{{ c.id }} {{ c.name }} · {{ c.currency }}</option></select><p v-if="error" role="alert" class="text-xs text-red-600">{{ error }}</p><RouterLink to="/admin/orders/coupons" class="text-xs text-primary-600">{{ t('coupon.adminTitle') }}</RouterLink></div></template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminCouponAPI, type CouponTemplate } from '@/api/coupons'
defineProps<{ modelValue?: number; required?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: number] }>()
const { t } = useI18n(), items = ref<CouponTemplate[]>([]), error = ref('')
onMounted(async () => { try { items.value = (await adminCouponAPI.templates()).data.filter(c => c.enabled) } catch (e) { error.value = (e as Error).message } })
</script>
