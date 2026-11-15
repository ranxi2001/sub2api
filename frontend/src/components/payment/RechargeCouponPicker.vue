<template>
  <section class="card space-y-3 p-5" data-testid="recharge-coupon-picker">
    <div class="flex items-center justify-between gap-3"><label for="recharge-coupon" class="font-medium">{{ t('coupon.select') }}</label><RouterLink to="/coupons" class="text-sm text-primary-600">{{ t('coupon.title') }}</RouterLink></div>
    <select id="recharge-coupon" v-model.number="selected" class="input" :disabled="disabled || loading"><option :value="0">{{ t('coupon.none') }}</option><option v-if="selected && !chosen" :value="selected" disabled>{{ t('coupon.unavailableSelected') }}</option><option v-for="c in coupons" :key="c.id" :value="c.id" :disabled="c.snapshot.currency !== currency || amount < c.snapshot.min_amount">{{ c.snapshot.name }} · {{ c.snapshot.currency }} ≥ {{ c.snapshot.min_amount }}</option></select>
    <p v-if="chosen" class="text-sm text-gray-500">{{ t(chosen.snapshot.stackable ? 'coupon.stackYes' : 'coupon.stackNo') }} · {{ t('coupon.expires') }} {{ new Date(chosen.expires_at).toLocaleString() }}</p>
    <p v-if="quoting" role="status" class="text-sm text-gray-500">{{ t('coupon.quoteLoading') }}</p><p v-if="error" role="alert" class="text-sm text-red-600">{{ error }} <button type="button" class="underline" @click="retry">{{ t('common.refresh') }}</button></p>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { couponAPI, type UserCoupon, type CouponQuote } from '@/api/coupons'
const props = defineProps<{ amount: number; method: string; currency: string; disabled?: boolean; initialId?: number }>()
const emit = defineEmits<{ change: [id: number, quote: CouponQuote | null, pending: boolean] }>()
const { t } = useI18n()
const coupons = ref<UserCoupon[]>([]), selected = ref(props.initialId && props.initialId > 0 ? props.initialId : 0), loading = ref(false), quoting = ref(false), error = ref('')
const chosen = computed(() => coupons.value.find(c => c.id === selected.value))
let listFailed = false, unmounted = false
let version = 0, timer: ReturnType<typeof setTimeout> | undefined
function refresh() {
  const v = ++version; if (timer) clearTimeout(timer); error.value = ''; quoting.value = false
  if (!selected.value) { emit('change', 0, null, false); return }
  emit('change', selected.value, null, true); quoting.value = true
  timer = setTimeout(async () => {
    try { const { data } = await couponAPI.quote(selected.value, props.amount, props.method); if (v === version) emit('change', selected.value, data, false) }
    catch (e) { if (v === version) { error.value = (e as Error).message || t('coupon.quoteError'); emit('change', selected.value, null, false) } }
    finally { if (v === version) quoting.value = false }
  }, 250)
}
watch(() => [selected.value, props.amount, props.method, props.currency], refresh, { flush: 'sync', immediate: true })
async function loadCoupons() {
  loading.value = true; error.value = ''; listFailed = false
  try {
    const list: UserCoupon[] = []; let page = 1
    while (true) { const { data } = await couponAPI.mine({ status: 'available', page_size: 100, page }); list.push(...data.items); if (list.length >= data.total || !data.items.length) break; page++ }
    if (unmounted) return
    coupons.value = list
    if (selected.value && !list.some(c => c.id === selected.value)) { version++; if (timer) clearTimeout(timer); quoting.value = false; error.value = t('coupon.unavailableSelected'); emit('change', selected.value, null, false) }
  } catch (e) { listFailed = true; error.value = (e as Error).message } finally { loading.value = false }
}
function retry() { if (listFailed) void loadCoupons(); else refresh() }
onMounted(loadCoupons)
onBeforeUnmount(() => { unmounted = true; version++; if (timer) clearTimeout(timer) })
</script>
