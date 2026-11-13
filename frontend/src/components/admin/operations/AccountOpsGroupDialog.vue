<template>
  <BaseDialog :show="show" :title="t('accountOps.accountGroups')" width="wide" @close="close">
    <fieldset :disabled="busy" class="min-w-0 space-y-4">
      <p class="text-sm leading-6 text-gray-500">{{ t('accountOps.accountGroupsHint') }}</p>
      <div v-if="!draftGroups.length" class="rounded-lg border border-dashed border-gray-300 p-6 text-center text-sm text-gray-500 dark:border-dark-600">
        {{ t('accountOps.noAccountGroups') }}
      </div>
      <div v-for="group in draftGroups" :key="group.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <label class="min-w-0 flex-1 space-y-1">
            <span class="text-xs font-medium text-gray-500">{{ t('accountOps.groupName') }}</span>
            <input v-model="group.name" class="input w-full" :placeholder="group.default_name" :data-testid="`account-group-name-${group.id}`" />
          </label>
          <div class="flex items-start gap-3 pt-5 text-right text-xs text-gray-500">
            <div>
              <p>{{ t('accountOps.manualGroup') }}</p>
              <p class="mt-1 max-w-56 truncate" :title="group.site || group.id">{{ group.site || group.id }}</p>
            </div>
            <button type="button" class="text-red-600 hover:underline disabled:opacity-40" :disabled="busy" :data-testid="`delete-account-group-${group.id}`" @click="removeGroup(group.id)">{{ t('accountOps.deleteAccountGroup') }}</button>
          </div>
        </div>
        <button type="button" class="mt-3 text-sm text-primary-600" :aria-expanded="activeGroupID === group.id" :aria-controls="activeGroupID === group.id ? pickerID : undefined" :data-testid="`edit-account-group-${group.id}`" @click="editMembers(group.id)">
          {{ t('accountOps.editGroupMembers') }} · {{ t('accountOps.groupMemberCount', { count: group.account_ids.length }) }}
        </button>
        <QualityAccountSelector v-if="activeGroupID === group.id" :id="pickerID" v-model="selectedAccounts" v-model:search="search" v-model:group="accountGroup" v-model:type="accountType"
          v-model:statuses="statuses" v-model:scope="scope" class="mt-4" data-testid="account-ops-member-picker"
          :accounts="pageAccounts" :groups="filterGroups" :accounts-loading="false" :selecting-accounts="busy" accounts-error=""
          :account-page="page" :account-pages="pages" :accounts-total="filteredAccounts.length" :bulk="false"
          hide-scope hide-status-filter :type-options="typeOptions" :disabled-reason="disabledReason" :search-id="`${pickerID}-search`"
          :label="t('accountOps.searchGroupMembers')" :selection-hint="t('accountOps.groupSelectionHint')"
          @search="goToPage" @select-page="selectAccounts(pageAccounts)" @select-all="selectAccounts(filteredAccounts)" @clear="selectedAccounts = []">
          <template #account-details="{ account }">
            <span class="ml-1 text-xs text-gray-500">· {{ membership.get(account.id)?.name || membership.get(account.id)?.default_name || t('qualityOps.ungroupedAccounts') }}</span>
          </template>
        </QualityAccountSelector>
      </div>
      <button type="button" class="btn btn-secondary" data-testid="add-account-group" @click="addGroup">{{ t('accountOps.addAccountGroup') }}</button>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    </fieldset>
    <template #footer>
      <button type="button" class="btn btn-secondary" data-testid="cancel-account-groups" @click="close">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" data-testid="save-account-groups" :disabled="busy" @click="save">{{ busy ? t('common.loading') : t('accountOps.saveAccountGroups') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import QualityAccountSelector from './QualityAccountSelector.vue'
import { saveAccountOpsGroups } from '@/api/admin/accountOps'
import type { AccountOpsConfig, AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'
import { qualityAccountTypeOptions } from '@/utils/qualityAccountFilter'

const props = defineProps<{ show: boolean; groups: AccountOpsGroup[]; accounts: AccountOpsThresholdAccount[] }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'saved', config: AccountOpsConfig): void; (event: 'error', message: string): void }>()
const { t } = useI18n()
const draftGroups = ref<AccountOpsGroup[]>([])
const busy = ref(false)
const error = ref('')
const pickerID = `account-ops-members-${useId()}`
const activeGroupID = ref('')
const search = ref('')
const accountGroup = ref('')
const accountType = ref('')
const statuses = ref<string[]>([])
const scope = ref<'all' | 'manual'>('manual')
const page = ref(1)
const pageSize = 50
const memberLimit = 1000 // validateAccountOpsGroups server limit.
const typeOptions = qualityAccountTypeOptions.filter(option => option.value === 'oauth' || option.value === 'apikey')
// Threshold accounts have no scheduling status or admin distribution-group fields.
const rows = computed(() => props.accounts.map(account => ({ id: account.account_id, name: account.account_name, type: account.type })))
type SelectorAccount = (typeof rows.value)[number]
const availableIDs = computed(() => new Set(rows.value.map(account => account.id)))
const activeGroup = computed(() => draftGroups.value.find(group => group.id === activeGroupID.value))
const membership = computed(() => {
  const result = new Map<number, AccountOpsGroup>()
  for (const group of draftGroups.value) for (const id of group.account_ids) result.set(id, group)
  return result
})
const filterGroups = computed(() => draftGroups.value.map(group => ({ id: group.id, name: group.name || group.default_name })))
const filteredAccounts = computed(() => {
  const query = search.value.trim().toLowerCase()
  const idQuery = query.replace(/^#/, '')
  return rows.value.filter(account => {
    const owner = membership.value.get(account.id)
    return (!query || account.name.toLowerCase().includes(query) || String(account.id) === idQuery)
      && (!accountType.value || account.type === accountType.value)
      && (!accountGroup.value || (accountGroup.value === 'ungrouped' ? !owner : owner?.id === accountGroup.value))
  })
})
const pages = computed(() => Math.max(1, Math.ceil(filteredAccounts.value.length / pageSize)))
const pageAccounts = computed(() => filteredAccounts.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const selectedAccounts = computed({
  get: () => activeGroup.value?.account_ids ?? [],
  set: (ids: number[]) => {
    const target = activeGroup.value
    if (!props.show || busy.value || !target) return
    const previous = new Set(target.account_ids)
    // Retain hidden/unavailable existing IDs; never add an ID absent from the DTO.
    const next = [...new Set(ids)].filter(id => previous.has(id) || availableIDs.value.has(id))
    if (next.length > memberLimit) {
      error.value = t('accountOps.groupMemberLimit', { count: memberLimit })
      return
    }
    const added = new Set(next.filter(id => !previous.has(id)))
    for (const group of draftGroups.value) {
      if (group.id !== target.id) group.account_ids = group.account_ids.filter(id => !added.has(id))
    }
    target.account_ids = next
    error.value = ''
  },
})
let sequence = 0

// The parent polls groups every 30 seconds. Snapshot on opening only, so a
// background refresh cannot discard edits or invalidate an in-flight save.
watch(() => props.show, show => {
  sequence++
  busy.value = false
  error.value = ''
  if (!show) return
  draftGroups.value = props.groups.map(group => ({ ...group, account_ids: Array.isArray(group.account_ids) ? [...group.account_ids] : [] }))
  editMembers(draftGroups.value[0]?.id ?? '')
}, { immediate: true })

watch([search, accountGroup, accountType], () => { page.value = 1 }, { flush: 'sync' })
watch(pages, count => { page.value = Math.min(page.value, count) }, { flush: 'sync' })
onBeforeUnmount(() => { sequence++ })

function editMembers(groupID: string) {
  activeGroupID.value = groupID
  search.value = ''; accountGroup.value = ''; accountType.value = ''; page.value = 1
}

function goToPage(value: number) {
  page.value = Math.max(1, Math.min(value, pages.value))
}

function disabledReason() {
  // No availability restriction is present in the threshold-account contract.
  return ''
}

function selectAccounts(accounts: SelectorAccount[]) {
  selectedAccounts.value = [...selectedAccounts.value, ...accounts.map(account => account.id)]
}

function close() {
  sequence++
  busy.value = false
  emit('close')
}

function addGroup() {
  if (busy.value) return
  const id = `manual-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
  draftGroups.value.push({ id, name: '', default_name: t('accountOps.newAccountGroup'), provider: 'manual', site: '', mode: 'manual', account_ids: [] })
  editMembers(id)
}

function removeGroup(groupID: string) {
  if (busy.value) return
  draftGroups.value = draftGroups.value.filter(group => group.id !== groupID)
  if (activeGroupID.value === groupID) editMembers(draftGroups.value[0]?.id ?? '')
  if (accountGroup.value === groupID) accountGroup.value = ''
}

async function save() {
  if (!props.show || busy.value) return
  const manualMissing = draftGroups.value.some(group => !group.name.trim())
  if (manualMissing) {
    error.value = t('accountOps.manualGroupNameRequired')
    return
  }
  const current = ++sequence
  busy.value = true
  error.value = ''
  try {
    const config = await saveAccountOpsGroups(draftGroups.value.map(group => ({ id: group.id, name: group.name.trim(), provider: group.provider, site: group.site, account_ids: [...group.account_ids] })))
    if (current === sequence) emit('saved', config)
  } catch (cause) {
    if (current === sequence) {
      error.value = extractApiErrorMessage(cause, t('accountOps.saveFailed'))
      emit('error', error.value)
    }
  } finally {
    if (current === sequence) busy.value = false
  }
}
</script>
