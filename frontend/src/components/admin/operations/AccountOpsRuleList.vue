<template>
  <section
    class="rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
    data-testid="account-ops-rules"
  >
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <div>
        <h3 class="text-sm font-semibold">{{ t('accountOps.accountRules') }}</h3>
        <p class="mt-1 text-xs text-gray-500">{{ t('accountOps.rulesOnceSummary') }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <input v-model="search" class="input w-48 text-sm" :placeholder="t('accountOps.search')" :aria-label="t('accountOps.search')" />
        <select v-model="type" class="input w-auto text-sm" :aria-label="t('accountOps.accountType')">
          <option value="all">{{ t('accountOps.allAccounts') }}</option>
          <option value="apikey">API Key</option>
          <option value="oauth">OAuth</option>
          <option value="mixed">API Key + OAuth</option>
        </select>
        <select v-model="state" class="input w-auto text-sm" :aria-label="t('accountOps.ruleState')">
          <option value="all">{{ t('accountOps.allStates') }}</option>
          <option v-for="value in states" :key="value" :value="value">{{ t(`accountOps.ruleStates.${value}`) }}</option>
        </select>
      </div>
    </div>
    <slot name="global-settings" />
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-3 dark:border-dark-700">
      <div class="min-w-0 space-y-1">
        <p class="text-sm font-medium" aria-live="polite">
          {{ t('accountOps.selectedGroups', { count: selectedGroups.length }) }}
          <span v-if="selectedTypes.length" class="ml-2 text-xs text-gray-500">{{ selectedTypes.join(' / ') }}</span>
        </p>
        <p class="text-xs leading-5 text-gray-500">{{ t('accountOps.batchSelectionHint') }}</p>
      </div>
      <div class="flex shrink-0 items-center gap-3">
        <button v-if="selectedGroups.length" type="button" class="text-sm text-gray-500 hover:underline" :disabled="busy" @click="clearSelection">{{ t('accountOps.clearSelection') }}</button>
        <button type="button" class="btn btn-secondary text-sm" data-testid="batch-edit-rules" :disabled="selectionUnavailable || !selectedGroups.length || !selectedAccounts.length" @click="editBatch">{{ t('accountOps.batchEditRules') }}</button>
      </div>
    </div>
    <div class="overflow-x-auto">
      <table class="w-full min-w-[820px] text-left text-sm">
        <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800">
          <tr>
            <th class="w-12 py-3 pl-5 pr-2">
              <input type="checkbox" data-testid="select-visible-groups" :aria-label="t('accountOps.selectVisibleGroups')" :checked="allVisibleSelected" :indeterminate="selectedGroups.length > 0 && !allVisibleSelected" :disabled="selectionUnavailable || !filtered.length" @change="selectVisible(($event.target as HTMLInputElement).checked)" />
            </th>
            <th class="px-3 py-3">{{ t('accountOps.accountGroup') }}</th>
            <th class="px-4 py-3">{{ t('accountOps.metric') }}</th>
            <th class="px-4 py-3">{{ t('accountOps.currentValue') }}</th>
            <th class="px-4 py-3">{{ t('accountOps.ruleSummary') }}</th>
            <th class="px-5 py-3 text-right">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in filtered" :key="row.group.id" class="border-t border-gray-100 dark:border-dark-700" :data-testid="`rule-row-${row.group.id}`">
            <td class="py-4 pl-5 pr-2">
              <input type="checkbox" :data-testid="`select-group-${row.group.id}`" :aria-label="t('accountOps.selectGroup', { name: row.group.name })" :checked="selectedIds.has(row.group.id)" :disabled="selectionUnavailable" @change="selectGroup(row.group.id, ($event.target as HTMLInputElement).checked)" />
            </td>
            <td class="px-3 py-4">
              <p class="font-medium text-gray-900 dark:text-gray-100">{{ row.group.name }}</p>
              <p class="mt-1 text-xs text-gray-500">{{ row.group.site || row.group.provider || '—' }} · {{ t('accountOps.groupMemberCount', { count: row.accounts.length }) }}</p>
              <p class="mt-1 max-w-72 truncate text-xs text-gray-500" :title="row.memberNames">{{ row.memberNames || t('accountOps.noAccountGroups') }}</p>
            </td>
            <td class="px-4 py-4 text-xs text-gray-500">
              <span v-if="row.balanceAccounts.length" class="block">{{ t('accountOps.balanceMetric') }}</span>
              <span v-if="row.quotaAccounts.length" class="mt-1 block">{{ t('accountOps.quotaMetric') }}</span>
              <span v-if="!row.balanceAccounts.length && !row.quotaAccounts.length">—</span>
              <span v-if="row.quotaWindow" class="mt-1 block">{{ row.quotaWindow }}</span>
            </td>
            <td class="px-4 py-4">
              <p v-if="row.balanceValue" class="font-mono text-xs">{{ row.balanceValue }}</p>
              <p v-if="row.quotaValue" class="mt-1 font-mono text-xs">{{ row.quotaValue }}</p>
              <span class="mt-1 inline-block text-xs" :class="row.state === 'warning' ? 'text-amber-700 dark:text-amber-400' : row.state === 'normal' ? 'text-emerald-700 dark:text-emerald-400' : 'text-gray-500'">{{ t(`accountOps.ruleStates.${row.state}`) }}</span>
            </td>
            <td class="px-4 py-4">
              <template v-if="row.balanceRule || row.quotaRule">
                <p v-if="row.balanceRule" class="text-xs">≤ {{ row.balanceRule.threshold.toFixed(2) }} {{ row.balanceRule.unit }}</p>
                <p v-if="row.quotaRule" class="mt-1 text-xs">≥ {{ row.quotaRule.threshold_percent }}%</p>
                <p class="mt-1 text-xs text-gray-500">{{ policy(row) }}</p>
              </template>
              <span v-else class="text-xs text-gray-400">{{ t('accountOps.ruleStates.unconfigured') }}</span>
            </td>
            <td class="px-5 py-4">
              <div class="flex items-center justify-end gap-4">
                <label v-if="row.balanceRule || row.quotaRule" class="flex items-center gap-1.5 text-xs text-gray-500">
                  <input type="checkbox" :checked="row.balanceRule?.enabled ?? row.quotaRule?.enabled" :disabled="busy" :aria-label="`${row.group.name} ${t('accountOps.ruleEnabled')}`" @change="emit('toggle', row.group, row.accounts, ($event.target as HTMLInputElement).checked)" />
                  {{ t('accountOps.channelEnabled') }}
                </label>
                <button type="button" class="text-sm text-primary-600 hover:underline" :data-testid="`edit-rule-${row.group.id}`" @click="editRow(row)">{{ t('common.edit') }}</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="loading" role="status" class="p-8 text-center text-sm text-gray-500">{{ t('common.loading') }}</p>
    <p v-else-if="!filtered.length && !error" class="p-8 text-center text-sm text-gray-500">{{ t('accountOps.noThresholdAccounts') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountOpsConfig, AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'

interface GroupRow {
  group: AccountOpsGroup
  accounts: AccountOpsThresholdAccount[]
  balanceAccounts: AccountOpsThresholdAccount[]
  quotaAccounts: AccountOpsThresholdAccount[]
  balanceRule?: NonNullable<AccountOpsConfig['balance_thresholds']>[number]
  quotaRule?: NonNullable<AccountOpsConfig['quota_thresholds']>[number]
  balanceValue: string
  quotaValue: string
  quotaWindow: string
  memberNames: string
  state: string
}

const props = withDefaults(defineProps<{ accounts: AccountOpsThresholdAccount[]; groups?: AccountOpsGroup[]; config: AccountOpsConfig; loading: boolean; ready: boolean; error: string; busy?: boolean }>(), { groups: () => [] })
const emit = defineEmits<{ (event: 'batch-edit', accounts: AccountOpsThresholdAccount[]): void; (event: 'edit-group', group: AccountOpsGroup): void; (event: 'toggle', group: AccountOpsGroup, accounts: AccountOpsThresholdAccount[], enabled: boolean): void }>()
const { t } = useI18n()
const search = ref(''), type = ref('all'), state = ref('all'), selectedIds = ref(new Set<string>())
const states = ['warning', 'normal', 'unknown', 'unconfigured', 'disabled']
const accountByID = computed(() => new Map(props.accounts.map(account => [account.account_id, account])))
const groups = computed(() => {
  const result = props.groups.filter(group => group.mode === 'manual').map(group => ({ ...group, account_ids: Array.isArray(group.account_ids) ? [...group.account_ids] : [] }))
  const known = new Set(result.map(group => group.id))
  for (const account of props.accounts) {
    if (account.group_mode !== 'manual' || !account.group_id || known.has(account.group_id)) continue
    result.push({ id: account.group_id, name: account.group_name || account.group_id, default_name: account.group_name || account.group_id, provider: account.platform, site: account.platform, mode: 'manual', account_ids: [] })
    known.add(account.group_id)
  }
  return result
})
function metricState(account: AccountOpsThresholdAccount, rule: GroupRow['balanceRule'] | GroupRow['quotaRule'] | undefined): string {
  if (!rule) return 'unconfigured'
  if (!rule.enabled) return 'disabled'
  if ('unit' in rule) {
    if (account.balance_status !== 'ok' || account.balance == null || rule.unit !== account.unit) return 'unknown'
    return account.balance <= rule.threshold ? 'warning' : 'normal'
  }
  const selected = rule.window || 'any'
  const windows = account.usage_windows.filter(item => selected === 'any' || selected === '' || selected === item.window)
  const valid = windows.filter(item => item.status === 'ok' && item.used_percent != null && Number.isFinite(item.used_percent))
  if (!windows.length || valid.length !== windows.length) return 'unknown'
  return valid.some(item => item.used_percent! >= rule.threshold_percent) ? 'warning' : 'normal'
}
function metricValue(account: AccountOpsThresholdAccount, rule: GroupRow['balanceRule'] | GroupRow['quotaRule'] | undefined): string {
  if (!rule) return ''
  if ('unit' in rule) return account.balance_status === 'ok' && account.balance != null ? `${account.balance.toFixed(2)} ${account.unit}` : t(`accountOps.observations.${account.balance_status}`)
  const selected = rule.window || 'any'
  const current = account.usage_windows.find(item => (selected === 'any' || selected === '' || selected === item.window) && item.status === 'ok' && item.used_percent != null)
  return current ? `${current.used_percent!.toFixed(1)}%` : t('accountOps.observations.unknown')
}
const rows = computed<GroupRow[]>(() => groups.value.map(group => {
  const accounts = group.account_ids.map(id => accountByID.value.get(id)).filter((account): account is AccountOpsThresholdAccount => !!account)
  const balanceAccounts = accounts.filter(account => account.type === 'apikey')
  const quotaAccounts = accounts.filter(account => account.type === 'oauth')
  const balanceRule = balanceAccounts.map(account => props.config.balance_thresholds?.find(rule => rule.account_id === account.account_id)).find((rule): rule is NonNullable<GroupRow['balanceRule']> => !!rule)
  const quotaRule = quotaAccounts.map(account => props.config.quota_thresholds?.find(rule => rule.account_id === account.account_id)).find((rule): rule is NonNullable<GroupRow['quotaRule']> => !!rule)
  const statesForGroup = [...balanceAccounts.map(account => metricState(account, balanceRule)), ...quotaAccounts.map(account => metricState(account, quotaRule))]
  const rowState = statesForGroup.includes('warning') ? 'warning' : statesForGroup.includes('unknown') ? 'unknown' : statesForGroup.length > 0 && statesForGroup.every(value => value === 'disabled') ? 'disabled' : statesForGroup.length > 0 && statesForGroup.every(value => value === 'unconfigured') ? 'unconfigured' : 'normal'
  return { group, accounts, balanceAccounts, quotaAccounts, balanceRule, quotaRule, balanceValue: balanceRule && balanceAccounts[0] ? metricValue(balanceAccounts[0], balanceRule) : '', quotaValue: quotaRule && quotaAccounts[0] ? metricValue(quotaAccounts[0], quotaRule) : '', quotaWindow: quotaRule?.window && quotaRule.window !== 'any' ? quotaRule.window : '', memberNames: accounts.map(account => account.account_name).join('、'), state: rowState }
}))
const filtered = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return rows.value.filter(row => {
    const hasType = type.value === 'all' || (type.value === 'apikey' && row.balanceAccounts.length > 0 && row.quotaAccounts.length === 0) || (type.value === 'oauth' && row.quotaAccounts.length > 0 && row.balanceAccounts.length === 0) || (type.value === 'mixed' && row.balanceAccounts.length > 0 && row.quotaAccounts.length > 0)
    const matches = !needle || `${row.group.name} ${row.group.id} ${row.group.site} ${row.memberNames}`.toLowerCase().includes(needle)
    return hasType && (state.value === 'all' || row.state === state.value) && matches
  })
})
const selectionUnavailable = computed(() => props.busy || props.loading || !props.ready || !!props.error)
const selectedGroups = computed(() => filtered.value.filter(row => selectedIds.value.has(row.group.id)))
const selectedAccounts = computed(() => {
  const unique = new Map<number, AccountOpsThresholdAccount>()
  for (const row of selectedGroups.value) for (const account of row.accounts) unique.set(account.account_id, account)
  return [...unique.values()]
})
const selectedTypes = computed(() => [...(selectedAccounts.value.some(account => account.type === 'apikey') ? ['API Key'] : []), ...(selectedAccounts.value.some(account => account.type === 'oauth') ? ['OAuth'] : [])])
const allVisibleSelected = computed(() => filtered.value.length > 0 && filtered.value.every(row => selectedIds.value.has(row.group.id)))
const policy = (row: GroupRow) => {
  const rules = [row.balanceRule, row.quotaRule].filter(Boolean)
  const alert = rules.some(rule => rule?.notify_alert ?? true), recovery = rules.some(rule => rule?.notify_recovery ?? true)
  return t(alert ? (recovery ? 'accountOps.policyBoth' : 'accountOps.policyAlert') : (recovery ? 'accountOps.policyRecovery' : 'accountOps.policyRecord'))
}
function clearSelection() { selectedIds.value = new Set() }
watch([type, state], clearSelection, { flush: 'sync' })
watch(filtered, visible => { const visibleIDs = new Set(visible.map(row => row.group.id)); selectedIds.value = new Set([...selectedIds.value].filter(id => visibleIDs.has(id))) }, { flush: 'sync' })
function selectGroup(id: string, checked: boolean) { if (selectionUnavailable.value) return; const next = new Set(selectedIds.value); if (checked) next.add(id); else next.delete(id); selectedIds.value = next }
function selectVisible(checked: boolean) { if (selectionUnavailable.value) return; selectedIds.value = new Set(checked ? filtered.value.map(row => row.group.id) : []) }
function editRow(row: GroupRow) {
  if (selectionUnavailable.value) return
  if (row.accounts.length) emit('batch-edit', row.accounts)
  else emit('edit-group', row.group)
}
function editBatch() { if (!selectionUnavailable.value && selectedAccounts.value.length) emit('batch-edit', selectedAccounts.value) }
defineExpose({ clearSelection })
</script>
