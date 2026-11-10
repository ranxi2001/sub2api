<template>
  <section class="space-y-5 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900" data-testid="apikey-fallback">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2"><h2 class="text-lg font-semibold">{{ t('autoConfig.apikeyFallback.title') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.openaiAPIKeyFallback" :user-id="userId" /></div>
      <label class="flex items-center gap-2 text-sm"><input v-model="model.enabled" data-testid="apikey-fallback-enabled" type="checkbox" role="switch" class="h-4 w-4 rounded text-primary-600" />{{ t('autoConfig.enable') }}</label>
    </header>
    <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.apikeyFallback.hint') }}</p>
    <div class="grid min-w-0 gap-4 sm:grid-cols-2">
      <label class="sm:col-span-2"><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyFallback.metric') }}</span><select v-model="model.metric" data-testid="apikey-fallback-metric" class="input w-full"><option value="sla">{{ t('autoConfig.apikeyFallback.metrics.sla') }}</option><option value="health_score">{{ t('autoConfig.apikeyFallback.metrics.health_score') }}</option></select></label>
      <label><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyFallback.threshold') }}</span><div class="flex items-center gap-2"><input v-model.number="model.threshold" data-testid="apikey-fallback-threshold" type="number" min="0" max="100" step="any" required class="input min-w-0 w-full" /><span class="shrink-0 text-sm text-gray-500">{{ model.metric === 'sla' ? '%' : t('autoConfig.apikeyFallback.scoreUnit') }}</span></div></label>
      <label><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyFallback.window') }}</span><input v-model.number="model.window_minutes" data-testid="apikey-fallback-window" type="number" min="1" max="1440" step="1" required class="input w-full" /></label>
    </div>
    <div class="flex justify-end"><button type="button" data-testid="apikey-fallback-reload-accounts" class="text-sm text-primary-600 underline" :disabled="loading" @click="loadAccounts">{{ t('autoConfig.apikeyFallback.reloadAccounts') }}</button></div>
    <p v-if="loading" class="text-sm text-gray-500" data-testid="apikey-fallback-loading">{{ t('common.loading') }}</p>
    <p v-if="failed" role="alert" data-testid="apikey-fallback-accounts-error" class="text-sm text-red-600">{{ t('autoConfig.apikeyFallback.accountsFailed') }}</p>
    <fieldset :disabled="loading || failed" class="min-w-0">
      <QualityAccountSelector v-model="model.account_ids" v-model:search="search" v-model:group="accountGroup" v-model:type="accountType" v-model:statuses="accountStatuses" v-model:scope="accountScope"
        data-testid="apikey-fallback-account-picker" search-id="apikey-fallback-account-search"
        :label="t('autoConfig.apikeyFallback.accounts')" :selection-hint="t('autoConfig.apikeyFallback.selectionHint')"
        :accounts="visibleAccounts" :groups="openAIGroups" :accounts-loading="loading" :selecting-accounts="false" accounts-error=""
        :account-page="accountPage" :account-pages="accountPages" :accounts-total="matchingAccounts.length" :bulk="false"
        hide-scope hide-type-filter hide-status-filter :disabled-reason="accountDisabledReason"
        @search="searchAccounts" @select-page="selectAccounts(visibleAccounts)" @select-all="selectAccounts(matchingAccounts)" @clear="model.account_ids = []">
        <template #account-details="{ account }"><small class="ml-1 text-gray-500">{{ t(account.schedulable ? 'autoConfig.apikeyFallback.schedulingOn' : 'autoConfig.apikeyFallback.schedulingOff') }}</small></template>
      </QualityAccountSelector>
    </fieldset>
    <div v-if="unavailableAccountIds.length && !loading && !failed" class="space-y-2 text-sm text-amber-700 dark:text-amber-300" data-testid="apikey-fallback-unavailable">
      <p>{{ t('autoConfig.apikeyFallback.unavailableHint') }}</p>
      <label v-for="id in unavailableAccountIds" :key="id" class="flex items-center gap-2"><input v-model="model.account_ids" :value="id" :data-testid="'apikey-fallback-unavailable-' + id" type="checkbox" class="h-4 w-4 rounded text-primary-600" /><span>#{{ id }} · {{ t('autoConfig.apikeyFallback.unavailableAccount') }}</span></label>
    </div>
    <p v-if="failed && model.account_ids.length" class="break-words text-sm text-amber-700 dark:text-amber-300" data-testid="apikey-fallback-retained-ids">{{ t('autoConfig.apikeyFallback.retainedIds') }} {{ model.account_ids.map(id => '#' + id).join(', ') }}</p>
    <p class="rounded-xl bg-amber-50 p-3 text-sm leading-6 text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ t('autoConfig.apikeyFallback.recoveryHint') }}</p>
    <div class="space-y-2 border-t border-gray-100 pt-4 text-sm text-gray-500 dark:border-dark-700" data-testid="apikey-fallback-status">
      <div class="flex flex-wrap items-center justify-between gap-2"><h3 class="font-medium">{{ t('autoConfig.apikeyFallback.status') }}</h3><button type="button" data-testid="apikey-fallback-refresh-status" class="text-primary-600 underline" :disabled="statusLoading" @click="$emit('refresh-status')">{{ t('autoConfig.apikeyFallback.refreshStatus') }}</button></div>
      <p v-if="statusFailed" role="alert" class="text-red-600">{{ t('autoConfig.apikeyFallback.statusFailed') }}</p>
      <template v-if="status?.last_checked_at">
        <p>{{ t('autoConfig.apikeyFallback.lastChecked') }} {{ status.last_checked_at }}</p>
        <p v-if="status.last_metric_checked_at" data-testid="apikey-fallback-metric-checked-at">{{ t('autoConfig.apikeyFallback.lastMetricChecked') }} {{ status.last_metric_checked_at }}</p>
        <p>{{ t('autoConfig.apikeyFallback.oauthAvailable') }} {{ status.oauth_available_count }} / {{ status.oauth_account_count }}</p>
        <p>{{ t('autoConfig.apikeyFallback.metrics.' + status.metric) }} {{ status.metric_value == null ? '—' : status.metric_value.toFixed(2) + (status.metric === 'sla' ? '%' : t('autoConfig.apikeyFallback.scoreUnit')) }} · {{ status.window_minutes }} {{ t('autoConfig.apikeyFallback.minutes') }} · {{ t('autoConfig.apikeyFallback.samples') }} {{ status.samples }}</p>
        <p v-if="status.last_trigger_reason">{{ t('autoConfig.apikeyFallback.lastTrigger') }} {{ t('autoConfig.apikeyFallback.reasons.' + status.last_trigger_reason) }}</p>
        <p v-if="status.last_enabled_at">{{ t('autoConfig.apikeyFallback.lastEnabled') }} {{ status.last_enabled_at }}</p>
        <p v-if="status.last_error_category" class="text-amber-700 dark:text-amber-300">{{ t('autoConfig.apikeyFallback.errors.' + status.last_error_category) }}</p>
      </template>
      <p v-else>{{ t('autoConfig.apikeyFallback.notChecked') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { list } from '@/api/admin/accounts'
import type { AccountListItem, Group } from '@/types'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import QualityAccountSelector from './QualityAccountSelector.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import type { OpenAIAPIKeyFallbackConfig, OpenAIAPIKeyFallbackStatus } from '@/utils/openaiAPIKeyFallback'

const model = defineModel<OpenAIAPIKeyFallbackConfig>({ required: true })
const props = defineProps<{ userId: number | null | undefined; groups: Group[]; status?: OpenAIAPIKeyFallbackStatus | null; statusLoading: boolean; statusFailed: boolean }>()
defineEmits<{ 'refresh-status': [] }>()
const { t } = useI18n()
const accounts = ref<AccountListItem[]>([])
const loading = ref(true), failed = ref(false)
const unavailableAccountIds = computed(() => model.value.account_ids.filter(id => !accounts.value.some(account => account.id === id)))
const search = ref(''), appliedSearch = ref(''), accountGroup = ref('')
const accountType = ref('apikey'), accountStatuses = ref(['active'])
const accountScope = ref<'all' | 'manual'>('manual')
const requestedPage = ref(1), pageSize = 20, maxAccounts = 100
const openAIGroups = computed(() => props.groups.filter(group => group.platform === 'openai'))
const matchingAccounts = computed(() => {
  const query = appliedSearch.value.trim().toLowerCase()
  return accounts.value.filter(account => {
    const groupIds = account.group_ids ?? []
    if (accountGroup.value === 'ungrouped' && groupIds.length) return false
    if (accountGroup.value && accountGroup.value !== 'ungrouped' && !groupIds.includes(Number(accountGroup.value))) return false
    return !query || account.name.toLowerCase().includes(query) || String(account.id).includes(query.replace(/^#/, ''))
  })
})
const accountPages = computed(() => Math.max(1, Math.ceil(matchingAccounts.value.length / pageSize)))
const accountPage = computed(() => Math.min(requestedPage.value, accountPages.value))
const visibleAccounts = computed(() => matchingAccounts.value.slice((accountPage.value - 1) * pageSize, accountPage.value * pageSize))
let alive = true, generation = 0

function searchAccounts(page: number) {
  appliedSearch.value = search.value
  requestedPage.value = page
}

function accountDisabledReason(account: AccountListItem): string {
  return !model.value.account_ids.includes(account.id) && model.value.account_ids.length >= maxAccounts ? t('autoConfig.apikeyFallback.selectionLimit') : ''
}

function selectAccounts(candidates: AccountListItem[]) {
  if (loading.value || failed.value) return
  const ids = new Set(model.value.account_ids)
  for (const account of candidates) {
    if (ids.size >= maxAccounts) break
    ids.add(account.id)
  }
  model.value.account_ids = [...ids]
}

async function loadAccounts() {
  const current = ++generation
  loading.value = true; failed.value = false
  try {
    const found = new Map<number, AccountListItem>()
    for (let page = 1; ; page++) {
      // The list API's "active" filter also requires schedulable=true. Fallback
      // must include paused accounts, so filter the raw status after loading.
      const result = await list(page, 100, { platform: 'openai', type: 'apikey', lite: '1', sort_by: 'id', sort_order: 'asc' })
      if (!alive || generation !== current) return
      if (result.page !== page || !Number.isSafeInteger(result.pages) || result.pages < 0 || !Number.isSafeInteger(result.total) || result.total < 0) throw new Error('Incomplete account list')
      for (const account of result.items) found.set(account.id, account)
      if (page >= result.pages) {
        if (found.size < result.total) throw new Error('Incomplete account list')
        break
      }
      if (!result.items.length) throw new Error('Incomplete account list')
    }
    const now = Date.now() / 1000
    if (alive && generation === current) accounts.value = [...found.values()].filter(account =>
      account.platform === 'openai' && account.type === 'apikey' && account.status === 'active' && account.parent_account_id == null &&
      (account.expires_at == null || Number.isFinite(account.expires_at) && Number.isFinite(new Date(account.expires_at * 1000).getTime()) && account.expires_at > now)
    )
  } catch {
    if (alive && generation === current) failed.value = true
  } finally {
    if (alive && generation === current) loading.value = false
  }
}

function validationError(): string | null {
  if (!model.value.enabled) return null
  if (loading.value || failed.value) return 'autoConfig.apikeyFallback.accountsFailed'
  if (unavailableAccountIds.value.length) return 'autoConfig.apikeyFallback.unavailableHint'
  return null
}
defineExpose({ validationError })
onMounted(loadAccounts)
onBeforeUnmount(() => { alive = false; generation++ })
</script>
