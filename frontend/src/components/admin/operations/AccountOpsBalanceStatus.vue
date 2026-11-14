<template>
  <div class="space-y-3">
    <div v-for="account in accounts" :key="account.account_id" class="max-w-sm text-xs" :data-testid="`balance-status-${account.account_id}`">
      <p v-if="accounts.length > 1" class="mb-1 font-medium">{{ account.account_name }}</p>
      <p v-if="account.balance_status === 'ok' && account.balance != null" class="font-mono">{{ account.balance.toFixed(2) }} {{ account.unit }}</p>
      <template v-else>
        <p class="text-amber-700 dark:text-amber-400">{{ t(`accountOps.observations.${account.balance_status}`) }}</p>
        <p v-if="account.last_known_balance != null" class="mt-1 text-gray-500">{{ t('accountOps.lastKnownBalance', { value: account.last_known_balance.toFixed(2), unit: account.unit }) }}</p>
      </template>
      <p v-if="account.probe_enabled === false" class="mt-1 text-amber-700 dark:text-amber-400">{{ t('accountOps.collectionDisabled') }}</p>
      <p v-else-if="account.balance_status !== 'ok'" class="mt-1 text-gray-500">{{ t(account.balance_status === 'unsupported' ? 'accountOps.balanceUnsupportedHint' : 'accountOps.balancePendingHint') }}</p>
      <p v-if="account.received_at" class="mt-1 text-gray-500">{{ t('accountOps.sampleTime') }} {{ formatDateTime(account.received_at) }}</p>
      <button type="button" class="mt-2 text-primary-600 hover:underline disabled:opacity-50" :disabled="disabled || busy !== null" :data-testid="`refresh-balance-${account.account_id}`" @click="refresh(account)">
        {{ busy === account.account_id ? t('common.loading') : t(account.probe_enabled === false ? 'accountOps.enableCollection' : 'accountOps.refreshBalance') }}
      </button>
    </div>
    <p v-if="feedback" role="status" class="max-w-sm text-xs text-amber-700 dark:text-amber-400">{{ feedback }}</p>
  </div>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUpstreamBillingProbeSettings, probeUpstreamBilling, setUpstreamBillingProbeEnabled } from '@/api/admin/accounts'
import type { AccountOpsThresholdAccount } from '@/api/admin/accountOps'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ accounts: AccountOpsThresholdAccount[]; disabled?: boolean }>()
const emit = defineEmits<{ (event: 'updated'): void }>()
const { t } = useI18n()
const busy = ref<number | null>(null), feedback = ref('')
let alive = true
onBeforeUnmount(() => { alive = false })
async function refresh(account: AccountOpsThresholdAccount) {
  if (props.disabled || busy.value !== null) return
  busy.value = account.account_id
  feedback.value = ''
  try {
    if (account.probe_enabled === false) {
      const settings = await getUpstreamBillingProbeSettings()
      if (!alive) return
      if (!settings.enabled) {
        feedback.value = t('accountOps.collectionGloballyDisabled')
        return
      }
      await setUpstreamBillingProbeEnabled(account.account_id, true)
      if (!alive) return
    }
    const result = await probeUpstreamBilling(account.account_id)
    if (!alive) return
    // A 200 transport response can still contain a failed/unsupported balance.
    const balance = result.snapshot?.balance
    if (result.error || balance?.status !== 'ok') {
      feedback.value = t(balance?.status === 'unsupported' ? 'accountOps.balanceUnsupportedHint' : 'accountOps.balanceRefreshFailed')
    }
  } catch (error) {
    if (alive) feedback.value = extractApiErrorMessage(error, t('accountOps.balanceRefreshFailed'))
  } finally {
    if (alive) { busy.value = null; emit('updated') }
  }
}
</script>
