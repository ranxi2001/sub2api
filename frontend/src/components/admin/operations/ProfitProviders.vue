<template>
  <section class="card space-y-3 p-4" aria-labelledby="wallet-title" data-testid="profit-providers">
    <header class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2"><h2 id="wallet-title" class="font-semibold">{{ t('profitOps.walletTitle') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.providerProfit" :user-id="auth.user?.id" /></div>
      <button type="button" class="btn btn-secondary" :disabled="busy" @click="load(page)">{{ t('matureProviders.refreshList') }}</button>
    </header>
    <p class="text-sm text-gray-500">{{ t('profitOps.walletNote') }}</p>
    <p v-if="loading" role="status">{{ t('matureProviders.loading') }}</p>
    <p v-else-if="loadError" role="alert" class="text-red-600">{{ t('profitOps.walletLoadFailed') }}</p>
    <template v-else-if="result">
      <p v-if="!result.items.length">{{ t('profitOps.providersEmpty') }}</p>
      <div v-else class="overflow-x-auto">
        <table class="w-full min-w-[760px] text-left text-sm">
          <caption class="sr-only">{{ t('profitOps.walletTitle') }}</caption>
          <thead><tr><th v-for="key in columns" :key="key" scope="col" class="p-2">{{ t(key) }}</th></tr></thead>
          <tbody>
            <template v-for="p in result.items" :key="p.provider_id">
              <tr class="border-t align-top dark:border-dark-700" :data-testid="`wallet-${p.provider_id}`">
                <th scope="row" class="p-2 font-medium">{{ p.provider_name }} #{{ p.provider_id }}<span class="block text-xs font-normal">{{ p.site_type === 'sub2api' ? 'Sub2API' : 'New API' }}</span></th>
                <td class="p-2"><button v-if="p.channel_id" type="button" class="text-primary-600 underline" @click="emit('filter', p.channel_id)">{{ channelName(p) }}</button><span v-else>{{ channelName(p) }}</span></td>
                <td class="p-2 tabular-nums">{{ amount(p, 'today_spend') }}</td><td class="p-2 tabular-nums">{{ amount(p, 'total_spend') }}</td>
                <td class="p-2"><time v-if="p.observed_at" :datetime="p.observed_at">{{ p.observed_at }}</time><span v-else>{{ t('matureProviders.never') }}</span><span v-if="olderReading(p)" class="block text-xs">{{ t('profitOps.olderReading') }}</span><span class="block text-xs text-gray-500">{{ t(`matureProviders.${p.status}`) }}</span></td>
                <td class="p-2"><div v-if="auth.isAdmin" class="flex flex-wrap gap-2">
                  <button type="button" class="btn btn-secondary btn-sm" :aria-expanded="editing === p.provider_id" :disabled="busy" @click="begin(p)">{{ t('profitOps.linkChannel') }}</button>
                  <button v-if="p.site_type === 'sub2api'" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="sync(p)">{{ t('profitOps.syncCharges') }}</button>
                </div></td>
              </tr>
              <tr v-if="editing === p.provider_id"><td :colspan="columns.length" class="p-2">
                <ProfitChannelLink :provider="p" :current-channel="channelName(p)" @cancel="editing = null" @busy="writing = $event" @reread="linkResult" />
              </td></tr>
            </template>
          </tbody>
        </table>
      </div>
      <div v-if="result.total > result.page_size || page > 1" class="flex flex-wrap items-center gap-3 text-sm">
        <button type="button" class="btn btn-secondary" :disabled="busy || page <= 1" @click="load(page - 1)">{{ t('profitOps.previous') }}</button>
        <span>{{ t('matureProviders.page') }} {{ page }} · {{ t('matureProviders.total') }}: {{ result.total }}</span>
        <button type="button" class="btn btn-secondary" :disabled="busy || page * result.page_size >= result.total" @click="load(page + 1)">{{ t('profitOps.next') }}</button>
      </div>
      <p v-if="auth.isAdmin && result.items.some(p => p.site_type === 'sub2api')" class="break-words text-xs text-gray-500">{{ t('profitOps.syncWindow') }} {{ from }} ≤ t &lt; {{ until }}</p>
    </template>
    <p v-if="message" role="status" class="text-sm">{{ message }}</p>
    <p v-if="actionError" role="alert" class="text-sm text-red-600">{{ actionError }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { listProfitProviders, syncProfitCharges, type ProfitProvider, type ProfitProviderPage } from '@/api/admin/profitOperations'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import ProfitChannelLink from './ProfitChannelLink.vue'

const props = defineProps<{ from: string; until: string }>()
const emit = defineEmits<{ filter: [id: number]; changed: [] }>()
const { t, locale } = useI18n(), auth = useAuthStore()
const columns = ['matureProviders.name', 'profitOps.profitChannel', 'profitOps.walletToday', 'profitOps.walletLifetime', 'matureProviders.observed', 'upstreamEntities.actions']
const result = ref<ProfitProviderPage | null>(null), page = ref(1), readAt = ref(0)
const loading = ref(false), loadError = ref(false), writing = ref(false), busy = computed(() => loading.value || writing.value)
const editing = ref<number | null>(null), message = ref(''), actionError = ref('')
const lifetime = new AbortController()
function channelName(p: ProfitProvider) {
  return p.channel_id ? `${p.channel_name || t('profitOps.unknown')} #${p.channel_id}` : t('profitOps.noLink')
}
function amount(p: ProfitProvider, field: 'today_spend' | 'total_spend') {
  const value = p.metrics?.[field]
  if (value == null || !Number.isFinite(value)) return t('profitOps.unknown')
  const unit = p.metrics.currency === 'quota_units' ? t('profitOps.quotaUnits') : p.metrics.currency || t('matureProviders.currencyUnknown')
  return `${value.toLocaleString(locale.value, { maximumSignificantDigits: 12 })} ${unit}`
}
function olderReading(p: ProfitProvider) {
  return p.observed_at && readAt.value - Date.parse(p.observed_at) >= 86_400_000
}
async function load(next: number) {
  if (busy.value || lifetime.signal.aborted) return
  editing.value = null; message.value = ''; actionError.value = ''; loading.value = true; loadError.value = false
  try {
    result.value = await listProfitProviders(next, lifetime.signal)
    page.value = result.value.page; readAt.value = Date.now()
  } catch { result.value = null; loadError.value = !lifetime.signal.aborted }
  finally { loading.value = false }
}
function begin(p: ProfitProvider) {
  message.value = ''; actionError.value = ''; editing.value = editing.value === p.provider_id ? null : p.provider_id
}
async function linkResult(providerId: number, channelId: number, state: 'written' | 'conflict' | 'failed') {
  await load(page.value)
  if (lifetime.signal.aborted) return
  if (state === 'written' && result.value?.items.some(p => p.provider_id === providerId && p.channel_id === channelId)) message.value = t('upstreamEntities.saved')
  else actionError.value = t(state === 'conflict' ? 'profitOps.linkConflict' : 'profitOps.linkUnconfirmed')
  emit('changed')
}
async function sync(p: ProfitProvider) {
  if (busy.value || !auth.isAdmin || p.site_type !== 'sub2api') return
  editing.value = null; writing.value = true; actionError.value = ''
  const dates = { from: props.from, until: props.until }
  message.value = t('profitOps.syncing', { provider: p.provider_name })
  try {
    const counts = await syncProfitCharges(p.provider_id, dates)
    if (lifetime.signal.aborted) return
    if (![counts?.matched, counts?.unmatched].every(n => Number.isSafeInteger(n) && n >= 0)) throw new Error('Missing sync counts')
    message.value = t('profitOps.syncResult', { provider: p.provider_name, ...dates, ...counts })
    emit('changed')
  } catch { message.value = ''; actionError.value = t('profitOps.syncFailed', { provider: p.provider_name }) }
  finally { writing.value = false }
}
onMounted(() => load(1))
onBeforeUnmount(() => lifetime.abort())
</script>
