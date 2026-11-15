<template>
  <section class="card space-y-4 p-5">
    <h3 class="font-semibold">{{ t('coupon.usage') }} · {{ total }}</h3>
    <p class="text-sm text-gray-500">{{ t('coupon.usageHint') }}</p>
    <form class="flex flex-wrap gap-3" @submit.prevent="page = 1; load()">
      <input v-model="search" :placeholder="t('coupon.usageSearch')" :aria-label="t('coupon.usageSearch')" class="input min-w-48 flex-1" />
      <select v-model="status" :aria-label="t('coupon.status')" class="input w-auto">
        <option value="">{{ t('coupon.all') }}</option>
        <option v-for="s in ['reserved', 'used', 'released', 'conflict', 'honored']" :key="s" :value="s">{{ t('coupon.' + s) }}</option>
      </select>
      <button class="btn btn-secondary" :disabled="loading">{{ t('common.search') }}</button>
    </form>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="loading" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
    <div v-else class="overflow-x-auto">
      <table class="w-full min-w-[680px] text-left text-sm">
        <thead><tr class="border-b dark:border-dark-700">
          <th class="p-3">{{ t('coupon.name') }}</th><th v-if="admin" class="p-3">{{ t('coupon.user') }}</th>
          <th class="p-3">{{ t('coupon.order') }}</th><th class="p-3">{{ t('coupon.status') }}</th>
          <th class="p-3">{{ t('coupon.benefit') }}</th><th class="p-3">{{ t('coupon.usedAt') }}</th>
        </tr></thead>
        <tbody><tr v-for="r in rows" :key="r.order_id" class="border-b dark:border-dark-700">
          <td class="p-3">#{{ r.coupon_id }} {{ r.name }}</td><td v-if="admin" class="p-3">{{ r.user_email || '#' + r.user_id }}</td>
          <td class="p-3">#{{ r.order_id }}<p class="text-xs text-gray-500">{{ t('payment.status.' + r.order_status.toLowerCase(), r.order_status) }}</p></td>
          <td class="p-3" :class="r.status === 'conflict' ? 'text-amber-600' : ''">{{ t('coupon.' + r.status) }}</td>
          <td class="p-3">{{ r.currency }} {{ r.discount_amount.toFixed(2) }}<p v-if="r.bonus_amount">+ USD {{ r.bonus_amount.toFixed(2) }}</p></td>
          <td class="whitespace-nowrap p-3">{{ r.used_at ? new Date(r.used_at).toLocaleString() : '—' }}<p class="text-xs text-gray-500">{{ new Date(r.created_at).toLocaleString() }}</p></td>
        </tr></tbody>
      </table>
      <p v-if="!rows.length" class="p-5 text-center text-gray-500">{{ t('coupon.noCoupons') }}</p>
    </div>
    <Pagination :page="page" :total="total" :page-size="20" @update:page="page = $event; load()" />
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Pagination from '@/components/common/Pagination.vue'
import { adminCouponAPI, couponAPI, type CouponUsage } from '@/api/coupons'
const props = defineProps<{ admin?: boolean }>()
const { t } = useI18n()
const rows = ref<CouponUsage[]>([]), page = ref(1), total = ref(0), search = ref(''), status = ref(''), error = ref(''), loading = ref(false)
let version = 0
async function load() {
  const v = ++version; loading.value = true; error.value = ''
  try {
    const { data } = await (props.admin ? adminCouponAPI : couponAPI).usage({ page: page.value, page_size: 20, search: search.value, status: status.value })
    if (v === version) { rows.value = data.items; total.value = data.total }
  } catch (e) { if (v === version) error.value = (e as Error).message }
  finally { if (v === version) loading.value = false }
}
onMounted(load)
onBeforeUnmount(() => { version++ })
</script>
