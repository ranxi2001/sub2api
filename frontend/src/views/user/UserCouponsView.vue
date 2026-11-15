<template>
  <AppLayout><div class="space-y-6">
    <section class="card space-y-3 p-6"><div class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('coupon.title') }}</h2><button class="btn btn-secondary" :disabled="loading" @click="load">{{ t('common.refresh') }}</button></div><p class="text-sm text-gray-500">{{ t('coupon.rules') }}</p></section>
    <div class="flex items-center justify-between gap-3"><select v-model="status" class="input max-w-xs" :aria-label="t('coupon.status')" @change="page = 1; load()"><option value="">{{ t('coupon.all') }}</option><option v-for="s in statuses" :key="s" :value="s">{{ t('coupon.' + s) }}</option></select><span class="shrink-0 text-sm text-gray-500">{{ t('coupon.total', { count: total }) }}</span></div>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="loading" class="p-10 text-center">{{ t('common.loading') }}</p>
    <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <article v-for="c in items" :key="c.id" class="card flex flex-col gap-3 border-t-4 border-primary-500 p-5" data-testid="coupon-card">
        <div class="flex items-start justify-between gap-3"><h3 class="font-semibold">{{ c.snapshot.name }}</h3><span class="rounded bg-gray-100 px-2 py-1 text-xs dark:bg-dark-700">{{ t('coupon.' + c.status) }}</span></div>
        <p class="text-lg font-semibold text-primary-600 dark:text-primary-400">{{ rule(c.snapshot) }}</p>
        <p v-if="c.snapshot.max_discount > 0 && c.snapshot.kind !== 'bonus'" class="text-sm">{{ t('coupon.maxDiscountHint', { currency: c.snapshot.currency, amount: c.snapshot.max_discount }) }}</p>
        <p class="text-sm text-gray-500">{{ t(c.snapshot.stackable ? 'coupon.stackYes' : 'coupon.stackNo') }}</p>
        <p class="text-sm">{{ t('coupon.expires') }}：{{ date(c.expires_at) }}</p>
        <p class="text-xs text-gray-500">{{ t('coupon.source') }}：{{ t('coupon.sources.' + c.source, c.source) }} · #{{ c.id }}</p>
        <p v-if="c.order_id" class="text-sm">{{ t('coupon.order') }}：#{{ c.order_id }} · {{ c.order_status ? t('payment.status.' + c.order_status.toLowerCase(), c.order_status) : '' }}</p>
        <p v-if="c.used_at" class="text-xs text-gray-500">{{ t('coupon.usedAt') }}：{{ date(c.used_at) }}</p>
        <RouterLink v-if="c.status === 'available'" :to="{ path: '/purchase', query: { coupon: c.id } }" class="btn btn-primary mt-auto">{{ t('coupon.use') }}</RouterLink>
      </article>
      <p v-if="!items.length" class="col-span-full p-10 text-center text-gray-500">{{ t('coupon.noCoupons') }}</p>
    </div>
    <Pagination v-if="total > 0" :page="page" :page-size="20" :total="total" @update:page="page = $event; load()" />
  <CouponUsageHistory /></div></AppLayout>
</template>
<script setup lang="ts">
import CouponUsageHistory from '@/components/payment/CouponUsageHistory.vue'
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import { couponAPI, type UserCoupon, type CouponTemplate } from '@/api/coupons'
const { t } = useI18n()
const items = ref<UserCoupon[]>([]), status = ref(''), page = ref(1), total = ref(0), loading = ref(false), error = ref('')
const statuses = ['available', 'reserved', 'used', 'expired', 'revoked']
let version = 0
function rule(c: CouponTemplate) { return t('coupon.' + ({ fixed: 'ruleFixed', percent: 'rulePercent', bonus: 'ruleBonus' }[c.kind]), { currency: c.currency, min: c.min_amount, value: c.value }) }
function date(value: string) { return new Date(value).toLocaleString() }
async function load() {
  const v = ++version; loading.value = true; error.value = ''
  try { const { data } = await couponAPI.mine({ page: page.value, page_size: 20, status: status.value }); if (v === version) { items.value = data.items; total.value = data.total } }
  catch (e) { if (v === version) error.value = (e as Error).message }
  finally { if (v === version) loading.value = false }
}
onMounted(load)
onBeforeUnmount(() => { version++ })
</script>
