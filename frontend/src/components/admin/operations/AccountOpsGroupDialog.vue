<template>
  <BaseDialog :show="show" :title="t('accountOps.accountGroups')" width="wide" @close="close">
    <div class="space-y-4">
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
        <div class="mt-4 grid max-h-48 gap-2 overflow-y-auto sm:grid-cols-2">
          <label v-for="account in accounts" :key="account.account_id" class="flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-dark-800">
            <input type="checkbox" :checked="group.account_ids.includes(account.account_id)" :data-testid="`account-group-member-${group.id}-${account.account_id}`" @change="toggleMember(group.id, account.account_id, ($event.target as HTMLInputElement).checked)" />
            <span class="min-w-0 truncate">{{ account.account_name }} <span class="text-xs text-gray-500">#{{ account.account_id }}</span></span>
          </label>
        </div>
      </div>
      <button type="button" class="btn btn-secondary" data-testid="add-account-group" @click="addGroup">{{ t('accountOps.addAccountGroup') }}</button>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="close">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" data-testid="save-account-groups" :disabled="busy" @click="save">{{ busy ? t('common.loading') : t('accountOps.saveAccountGroups') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { saveAccountOpsGroups } from '@/api/admin/accountOps'
import type { AccountOpsConfig, AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; groups: AccountOpsGroup[]; accounts: AccountOpsThresholdAccount[] }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'saved', config: AccountOpsConfig): void; (event: 'error', message: string): void }>()
const { t } = useI18n()
const draftGroups = ref<AccountOpsGroup[]>([])
const busy = ref(false)
const error = ref('')
let sequence = 0

watch(() => [props.show, props.groups] as const, () => {
  sequence++
  error.value = ''
  draftGroups.value = props.groups.map(group => ({ ...group, account_ids: Array.isArray(group.account_ids) ? [...group.account_ids] : [] }))
}, { immediate: true, deep: true })

function close() {
  sequence++
  busy.value = false
  emit('close')
}

function addGroup() {
  const id = `manual-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
  draftGroups.value.push({ id, name: '', default_name: t('accountOps.newAccountGroup'), provider: 'manual', site: '', mode: 'manual', account_ids: [] })
}

function removeGroup(groupID: string) {
  draftGroups.value = draftGroups.value.filter(group => group.id !== groupID)
}

function toggleMember(groupID: string, accountID: number, checked: boolean) {
  for (const group of draftGroups.value) {
    group.account_ids = group.account_ids.filter(id => id !== accountID)
  }
  if (checked) draftGroups.value.find(group => group.id === groupID)?.account_ids.push(accountID)
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
