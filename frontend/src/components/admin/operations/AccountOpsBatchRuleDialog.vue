<template>
  <BaseDialog
    :show="show"
    :title="t('accountOps.batchRuleTitle')"
    :width="isMixed ? 'wide' : 'normal'"
    @close="close"
  >
    <form id="account-ops-batch-rule-form" class="space-y-5" @submit.prevent="save">
      <div class="space-y-3">
        <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
          {{ isMixed ? t('accountOps.batchMixedRuleScope') : t('accountOps.batchRuleScope', { count: accounts.length }) }}
        </p>
        <p class="rounded-lg bg-amber-50 p-3 text-xs leading-5 text-amber-800 dark:bg-amber-950/20 dark:text-amber-300">
          {{ t('accountOps.batchReplaceHint') }}
        </p>
        <p v-if="!validSelection" role="alert" class="text-sm text-red-600 dark:text-red-400">
          {{ t('accountOps.batchInvalidSelection') }}
        </p>
        <p v-else-if="mixedUnits" role="alert" class="text-sm text-red-600 dark:text-red-400">
          {{ t('accountOps.batchMixedUnits') }}
        </p>
      </div>

      <div class="grid gap-5" :class="{ 'sm:grid-cols-2': isMixed }">
        <fieldset
          v-for="section in sections"
          :key="section.metric"
          :disabled="busy"
          class="min-w-0 space-y-5"
          :class="{ 'rounded-xl border border-gray-200 p-4 dark:border-dark-700': isMixed }"
        >
          <legend class="px-1 text-sm font-semibold text-gray-900 dark:text-gray-100">
            {{ t(section.metric === 'quota' ? 'accountOps.batchQuotaRules' : 'accountOps.batchBalanceRules') }}
          </legend>
          <details class="rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
            <summary class="cursor-pointer text-xs text-gray-600 dark:text-gray-300">
              {{ t('accountOps.selectedAccounts', { count: section.accounts.length }) }}
            </summary>
            <ul class="mt-3 max-h-32 space-y-2 overflow-y-auto">
              <li v-for="account in section.accounts" :key="account.account_id" class="break-words text-sm">
                <span class="font-medium">{{ account.account_name }}</span>
                <span class="ml-2 text-xs text-gray-500">
                  #{{ account.account_id }}<template v-if="account.type === 'apikey'"> · {{ account.unit }}</template>
                </span>
              </li>
            </ul>
          </details>
          <label class="flex items-center justify-between text-sm">
            <span>{{ t('accountOps.ruleEnabled') }}</span>
            <input
              v-model="section.form.enabled"
              type="checkbox"
              :data-testid="`${section.testIdPrefix}-enabled`"
            />
          </label>
          <label class="block space-y-2 text-sm">
            <span>{{ t('accountOps.thresholdValue') }}</span>
            <div class="flex items-center gap-2">
              <input
                v-model="section.form.threshold"
                type="number"
                :min="section.metric === 'quota' ? 0.01 : 0"
                :max="section.metric === 'quota' ? 100 : 1e12"
                step="any"
                class="input w-full"
                :data-testid="`${section.testIdPrefix}-threshold`"
              />
              <span class="shrink-0 text-gray-500">
                {{ section.metric === 'quota' ? '%' : mixedUnits ? '—' : unit }}
              </span>
            </div>
          </label>
          <label v-if="section.metric === 'quota'" class="block space-y-2 text-sm">
            <span>{{ t('accountOps.quotaWindow') }}</span>
            <select
              v-model="section.form.window"
              class="input w-full"
              :data-testid="`${section.testIdPrefix}-window`"
            >
              <option value="any">{{ t('accountOps.anyWindow') }}</option>
              <option v-for="window in commonWindows" :key="window.window" :value="window.window">
                {{ window.label }}
              </option>
            </select>
          </label>
          <div class="space-y-3 border-y border-gray-200 py-4 dark:border-dark-700">
            <p class="text-sm font-medium">{{ t('accountOps.transitionNotices') }}</p>
            <label class="flex items-start gap-3 text-sm">
              <input
                v-model="section.form.notifyAlert"
                type="checkbox"
                class="mt-1"
                :data-testid="`${section.testIdPrefix}-notify-alert`"
              />
              <span>
                {{ t('accountOps.notifyAlert') }}
                <small class="mt-1 block text-xs leading-5 text-gray-500">
                  {{ t('accountOps.notifyAlertHint') }}
                </small>
              </span>
            </label>
            <label class="flex items-start gap-3 text-sm">
              <input
                v-model="section.form.notifyRecovery"
                type="checkbox"
                class="mt-1"
                :data-testid="`${section.testIdPrefix}-notify-recovery`"
              />
              <span>
                {{ t('accountOps.notifyRecovery') }}
                <small class="mt-1 block text-xs leading-5 text-gray-500">
                  {{ t('accountOps.notifyRecoveryHint') }}
                </small>
              </span>
            </label>
          </div>
          <p class="text-xs leading-5 text-gray-500">
            {{ t(section.metric === 'quota' ? 'accountOps.quotaOnceHint' : 'accountOps.balanceOnceHint') }}
          </p>
        </fieldset>
      </div>
    </form>
    <template #footer>
      <button
        type="button"
        class="btn btn-secondary"
        data-testid="cancel-batch-rules"
        @click="close"
      >
        {{ t('common.cancel') }}
      </button>
      <button
        type="submit"
        form="account-ops-batch-rule-form"
        class="btn btn-primary"
        data-testid="save-batch-rules"
        :disabled="busy || !validSelection || mixedUnits"
      >
        {{ busy ? t('common.loading') : t('accountOps.saveBatchRules', { count: accounts.length }) }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { saveAccountOpsRuleGroups, saveAccountOpsRulesBatch } from '@/api/admin/accountOps'
import type { AccountOpsConfig, AccountOpsRuleBatchGroup, AccountOpsRuleInput, AccountOpsThresholdAccount } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'

interface RuleForm {
  enabled: boolean
  threshold: string | number
  window: string
  notifyAlert: boolean
  notifyRecovery: boolean
}
interface RuleSection {
  metric: 'balance' | 'quota'
  form: RuleForm
  accounts: AccountOpsThresholdAccount[]
  testIdPrefix: string
}

const props = defineProps<{ show: boolean; accounts: AccountOpsThresholdAccount[]; config?: AccountOpsConfig }>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'saved', config: AccountOpsConfig): void
  (event: 'error', message: string): void
}>()
const { t } = useI18n()
const newForm = (): RuleForm => ({
  enabled: true, threshold: '', window: 'any', notifyAlert: true, notifyRecovery: true
})
const balanceForm = reactive(newForm())
const quotaForm = reactive(newForm())
const busy = ref(false)
const balanceAccounts = computed(() => props.accounts.filter(account => account.type === 'apikey'))
const quotaAccounts = computed(() => props.accounts.filter(account => account.type === 'oauth'))
const isMixed = computed(() => balanceAccounts.value.length > 0 && quotaAccounts.value.length > 0)
const validSelection = computed(() => props.accounts.length > 0
  && props.accounts.length === balanceAccounts.value.length + quotaAccounts.value.length)
const unit = computed(() => balanceAccounts.value[0]?.unit ?? '')
const mixedUnits = computed(() => balanceAccounts.value.some(account => account.unit !== unit.value))
const commonWindows = computed(() => (quotaAccounts.value[0]?.usage_windows ?? [])
  .filter(window => window.window !== 'any'
    && quotaAccounts.value.every(account => account.usage_windows.some(candidate => candidate.window === window.window))))
const sections = computed<RuleSection[]>(() => {
  const result: RuleSection[] = []
  if (balanceAccounts.value.length) result.push({
    metric: 'balance', form: balanceForm, accounts: balanceAccounts.value,
    testIdPrefix: isMixed.value ? 'batch-balance-rule' : 'batch-rule'
  })
  if (quotaAccounts.value.length) result.push({
    metric: 'quota', form: quotaForm, accounts: quotaAccounts.value,
    testIdPrefix: isMixed.value ? 'batch-quota-rule' : 'batch-rule'
  })
  return result
})
const scopeKey = computed(() => JSON.stringify(props.accounts.map(account => ({
  id: account.account_id,
  type: account.type,
  unit: account.type === 'apikey' ? account.unit : undefined,
  windows: account.type === 'oauth' ? account.usage_windows.map(window => window.window) : undefined
}))))
let sequence = 0

watch(() => [props.show, scopeKey.value] as const, () => {
  sequence++
  busy.value = false
  Object.assign(balanceForm, newForm())
  Object.assign(quotaForm, newForm())
  const balance = props.config?.balance_thresholds?.find(rule => rule.account_id === balanceAccounts.value[0]?.account_id)
  if (balance) Object.assign(balanceForm, { enabled: balance.enabled, threshold: balance.threshold, notifyAlert: balance.notify_alert ?? true, notifyRecovery: balance.notify_recovery ?? true })
  const quota = props.config?.quota_thresholds?.find(rule => rule.account_id === quotaAccounts.value[0]?.account_id)
  if (quota) Object.assign(quotaForm, { enabled: quota.enabled, threshold: quota.threshold_percent, window: quota.window, notifyAlert: quota.notify_alert ?? true, notifyRecovery: quota.notify_recovery ?? true })
}, { immediate: true, flush: 'pre' })

function close() {
  sequence++
  busy.value = false
  emit('close')
}

onBeforeUnmount(() => { sequence++ })

function validThreshold(section: RuleSection): boolean {
  const value = Number(section.form.threshold)
  const quota = section.metric === 'quota'
  return String(section.form.threshold).trim() !== '' && Number.isFinite(value)
    && value >= (quota ? Number.MIN_VALUE : 0) && value <= (quota ? 100 : 1e12)
}

function buildRule(section: RuleSection): AccountOpsRuleInput {
  const value = Number(section.form.threshold)
  return {
    metric: section.metric,
    enabled: section.form.enabled,
    notify_alert: section.form.notifyAlert,
    notify_recovery: section.form.notifyRecovery,
    ...(section.metric === 'quota'
      ? { threshold_percent: value, window: section.form.window }
      : { threshold: value, unit: unit.value })
  }
}

async function save() {
  if (!props.show || busy.value) return
  if (!validSelection.value) {
    emit('error', t('accountOps.batchInvalidSelection'))
    return
  }
  if (mixedUnits.value) {
    emit('error', t('accountOps.batchMixedUnits'))
    return
  }
  if (!sections.value.every(validThreshold)) {
    emit('error', t('accountOps.invalidThreshold'))
    return
  }

  const groups: AccountOpsRuleBatchGroup[] = sections.value.map(section => ({
    account_ids: section.accounts.map(account => account.account_id),
    rule: buildRule(section)
  }))
  const current = sequence
  busy.value = true
  try {
    const config = groups.length === 1
      ? await saveAccountOpsRulesBatch(groups[0]!.account_ids, groups[0]!.rule)
      : await saveAccountOpsRuleGroups(groups)
    if (sequence === current) {
      emit('saved', config)
      close()
    }
  } catch (error) {
    if (sequence === current) emit('error', extractApiErrorMessage(error, t('accountOps.saveFailed')))
  } finally {
    if (sequence === current) busy.value = false
  }
}
</script>
