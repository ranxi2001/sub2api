<template>
  <section class="min-w-0 space-y-5 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900" data-testid="apikey-disposition">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2"><h2 class="text-lg font-semibold">{{ t('autoConfig.apikeyDisposition.title') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthAPIKeyDisposition" :user-id="userId" /></div>
      <label class="flex items-center gap-2 text-sm"><input v-model="model.enabled" data-testid="apikey-disposition-enabled" type="checkbox" role="switch" />{{ t('autoConfig.enable') }}</label>
    </header>
    <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.apikeyDisposition.hint') }}</p>
    <p v-if="!initialEnabled" class="text-sm text-amber-700 dark:text-amber-300">{{ t('autoConfig.apikeyDisposition.initialDisabled') }}</p>
    <div class="grid gap-4 sm:grid-cols-2">
      <label><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyDisposition.action') }}</span><select v-model="model.action" data-testid="apikey-disposition-action" class="input w-full"><option value="pause">{{ t('autoConfig.apikeyDisposition.pause') }}</option><option value="disable">{{ t('autoConfig.apikeyDisposition.disable') }}</option></select></label>
      <label><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyDisposition.delay') }}</span><input v-model.number="model.delay_minutes" data-testid="apikey-disposition-delay" type="number" min="0" max="43200" step="1" required class="input w-full" /></label>
      <label v-if="model.action === 'pause'"><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyDisposition.duration') }}</span><input v-model.number="model.pause_minutes" data-testid="apikey-disposition-duration" type="number" min="1" max="43200" step="1" required class="input w-full" /></label>
      <label><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyDisposition.timezone') }}</span><select v-model="model.timezone" class="input w-full"><option value="Asia/Shanghai">Asia/Shanghai (UTC+8)</option><option value="UTC">UTC</option><option v-if="!['Asia/Shanghai', 'UTC'].includes(model.timezone)" :value="model.timezone">{{ model.timezone }}</option></select></label>
    </div>
    <label class="block"><span class="mb-2 block text-sm font-medium">{{ t('autoConfig.apikeyDisposition.scope') }}</span><select v-model="model.scope" data-testid="apikey-disposition-scope" class="input w-full"><option value="accounts">{{ t('autoConfig.apikeyDisposition.manual') }}</option><option value="groups">{{ t('autoConfig.apikeyDisposition.allGroups') }}</option></select></label>
    <div v-if="model.scope === 'groups'" class="space-y-2">
      <p class="text-xs text-gray-500">{{ t('autoConfig.apikeyDisposition.groupHint') }}</p>
      <div class="max-h-52 space-y-2 overflow-auto rounded-xl border p-3 dark:border-dark-700">
        <label v-for="g in openAIGroups" :key="g.id" class="flex items-center gap-2 text-sm"><input v-model="model.group_ids" :value="g.id" :data-testid="'disposition-group-' + g.id" type="checkbox" />{{ g.name }} #{{ g.id }}</label>
        <label v-for="id in model.group_ids.filter(id => !openAIGroups.some(g => g.id === id))" :key="id" class="flex items-center gap-2 text-sm text-amber-700"><input v-model="model.group_ids" :value="id" type="checkbox" />#{{ id }} · {{ t('autoConfig.modelBilling.unavailableGroup') }}</label>
      </div>
    </div>
    <template v-else>
      <button type="button" class="text-sm text-primary-600 underline" :disabled="loading" @click="loadAccounts">{{ t('autoConfig.apikeyFallback.reloadAccounts') }}</button>
      <fieldset :disabled="loading || failed">
        <QualityAccountSelector v-model="model.account_ids" v-model:search="search" v-model:group="accountGroup" v-model:type="accountType" v-model:statuses="accountStatuses" v-model:scope="accountScope"
          search-id="apikey-disposition-search" :label="t('autoConfig.apikeyDisposition.accounts')" :selection-hint="t('autoConfig.apikeyDisposition.selectionHint')"
          :accounts="visibleAccounts" :groups="openAIGroups" :accounts-loading="loading" :selecting-accounts="false" :accounts-error="failed ? t('autoConfig.apikeyDisposition.accountsFailed') : ''"
          :account-page="accountPage" :account-pages="accountPages" :accounts-total="matchingAccounts.length" :bulk="false"
          hide-scope hide-type-filter hide-status-filter :disabled-reason="accountDisabledReason"
          @search="searchAccounts" @select-page="selectAccounts(visibleAccounts)" @select-all="selectAccounts(matchingAccounts)" @clear="model.account_ids = []">
          <template #account-details="{ account }"><small class="ml-1 text-gray-500">{{ t(account.schedulable ? 'autoConfig.apikeyFallback.schedulingOn' : 'autoConfig.apikeyFallback.schedulingOff') }}</small></template>
        </QualityAccountSelector>
      </fieldset>
      <div v-if="unavailableAccountIds.length && !loading && !failed" class="space-y-2 text-sm text-amber-700">
        <p>{{ t('autoConfig.apikeyDisposition.unavailableHint') }}</p>
        <label v-for="id in unavailableAccountIds" :key="id" class="flex items-center gap-2"><input v-model="model.account_ids" :value="id" type="checkbox" />#{{ id }}</label>
      </div>
    </template>
    <p class="rounded-xl bg-amber-50 p-3 text-sm leading-6 text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ t('autoConfig.apikeyDisposition.executionHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { list } from '@/api/admin/accounts'
import type { AccountListItem, Group } from '@/types'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import QualityAccountSelector from './QualityAccountSelector.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import type { OAuthAPIKeyDispositionConfig } from '@/utils/oauthAPIKeyDisposition'

const model = defineModel<OAuthAPIKeyDispositionConfig>({ required: true })
const props = defineProps<{ userId: number | null | undefined; groups: Group[]; platform: string; initialEnabled: boolean }>()
const { t } = useI18n()
const accounts = ref<AccountListItem[]>([])
const loading = ref(false), failed = ref(false)
const unavailableAccountIds = computed(() => model.value.account_ids.filter(id => !accounts.value.some(account => account.id === id)))
const search = ref(''), appliedSearch = ref(''), accountGroup = ref('')
const accountType = ref('apikey'), accountStatuses = ref(['active'])
const accountScope = ref<'all' | 'manual'>('manual')
const requestedPage = ref(1), pageSize = 20, maxAccounts = 1000
const openAIGroups = computed(() => props.groups.filter(group => group.platform === props.platform))
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
  return !model.value.account_ids.includes(account.id) && model.value.account_ids.length >= maxAccounts ? t('autoConfig.apikeyDisposition.selectionLimit') : ''
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
      const result = await list(page, 100, { platform: props.platform, type: 'apikey', lite: '1', sort_by: 'id', sort_order: 'asc' })
      if (!alive || generation !== current) return
      if (result.page !== page || !Number.isSafeInteger(result.pages) || result.pages < 0 || !Number.isSafeInteger(result.total) || result.total < 0) throw new Error('Incomplete account list')
      for (const account of result.items) found.set(account.id, account)
      if (page >= result.pages) {
        if (found.size < result.total) throw new Error('Incomplete account list')
        break
      }
      if (!result.items.length) throw new Error('Incomplete account list')
    }
    if (alive && generation === current) accounts.value = [...found.values()].filter(account =>
      account.platform === props.platform && account.type === 'apikey' && account.parent_account_id == null
    )
  } catch {
    if (alive && generation === current) failed.value = true
  } finally {
    if (alive && generation === current) loading.value = false
  }
}

function validationError(): string | null {
  if (!model.value.enabled || model.value.scope === 'groups') return null
  if (loading.value || failed.value) return 'autoConfig.apikeyDisposition.accountsFailed'
  if (unavailableAccountIds.value.length) return 'autoConfig.apikeyDisposition.unavailableHint'
  return null
}
defineExpose({ validationError })
watch(() => props.platform, () => { accounts.value = []; accountGroup.value = ''; generation++; loading.value = false })
watch(() => [props.platform, model.value.enabled, model.value.scope], () => {
  if (model.value.enabled && model.value.scope === 'accounts') void loadAccounts()
})
onMounted(() => { if (model.value.enabled && model.value.scope === 'accounts') void loadAccounts() })
onBeforeUnmount(() => { alive = false; generation++ })
</script>
