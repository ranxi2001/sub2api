<template>
  <BaseDialog :show="show" :title="t('admin.accounts.upstreamBilling.newAPI.title')" width="wide" @close="close">
    <div class="space-y-5">
      <p v-if="loading" class="text-sm text-gray-500" role="status">{{ t('common.loading') }}</p>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <template v-if="config">
        <div class="break-all text-sm text-gray-500 dark:text-gray-400">{{ config.site_url }}</div>
        <form id="new-api-upstream-form" class="space-y-4" @submit.prevent="verify(true)">
          <fieldset :disabled="busy" class="space-y-4 disabled:opacity-70">
            <div class="grid gap-4 sm:grid-cols-[10rem_1fr]">
              <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
                {{ t('admin.accounts.upstreamBilling.newAPI.userId') }}
                <input v-model="userId" type="number" min="1" step="1" autocomplete="off" class="input mt-1 w-full" data-testid="new-api-user-id" />
              </label>
              <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
                {{ t('admin.accounts.upstreamBilling.newAPI.accessToken') }}
                <input v-model="accessToken" type="password" autocomplete="new-password" spellcheck="false" class="input mt-1 w-full" data-testid="new-api-access-token" :placeholder="canRetainCredential ? t('admin.accounts.upstreamBilling.newAPI.retainToken') : ''" />
              </label>
            </div>
            <p class="text-xs leading-relaxed text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.upstreamBilling.newAPI.tokenHelp') }}
              <a href="https://docs.newapi.pro/en/docs/guide/feature-guide/user/personal-setting" target="_blank" rel="noopener noreferrer" class="text-primary-600 underline dark:text-primary-400">{{ t('admin.accounts.upstreamBilling.newAPI.tokenHelpLink') }}</a>
            </p>
            <p v-if="canRetainCredential" class="text-xs text-gray-500">{{ t('admin.accounts.upstreamBilling.newAPI.retainTokenHint') }}</p>
            <fieldset class="space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
              <legend class="px-1 text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.accounts.upstreamBilling.newAPI.applyToPeers') }}</legend>
              <div class="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-200">
                {{ t('admin.accounts.upstreamBilling.newAPI.costConversion') }}
                <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.newAPICostConversion" :user-id="authStore.user?.id" />
              </div>
              <p class="text-xs text-gray-500">{{ t('admin.accounts.upstreamBilling.newAPI.costConversionHint') }}</p>
              <div v-for="candidate in config.accounts" :key="candidate.account_id" class="space-y-2 rounded-md border border-gray-100 p-2 dark:border-dark-700">
              <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-200">
                <input v-model="accountIds" :value="candidate.account_id" type="checkbox" :disabled="candidate.account_id === account?.id" class="mt-0.5 h-4 w-4 rounded border-gray-300" :data-testid="`new-api-account-${candidate.account_id}`" />
                <span class="min-w-0 break-words">{{ candidate.name }}
                  <span v-if="candidate.account_id === account?.id" class="text-xs text-gray-500"> · {{ t('admin.accounts.upstreamBilling.newAPI.currentAccount') }}</span>
                  <span v-if="candidate.configured_user_id != null" class="text-xs text-gray-500"> · {{ t('admin.accounts.upstreamBilling.newAPI.boundUser', { id: candidate.configured_user_id }) }}</span>
                </span>
              </label>
              <label v-if="accountIds.includes(candidate.account_id)" class="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
                <input v-model="applyUSDRate[candidate.account_id]" type="checkbox" class="h-4 w-4 rounded border-gray-300" :data-testid="`new-api-apply-usd-rate-${candidate.account_id}`" />
                {{ t('admin.accounts.upstreamBilling.newAPI.applyUSDRate') }}
                <span class="text-xs text-gray-500">{{ t('admin.accounts.upstreamBilling.newAPI.costPlatformFactor', { factor: conversionFactor(candidate) }) }}</span>
              </label>
              <p v-if="accountIds.includes(candidate.account_id) && candidate.cost_auto_sync_enabled === false" class="text-xs text-gray-500">{{ t('admin.accounts.upstreamBilling.newAPI.costSyncPaused') }}</p>
              </div>
            </fieldset>
          </fieldset>
        </form>
        <div v-if="result" class="space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-dark-800" aria-live="polite">
          <p class="text-sm font-medium text-gray-800 dark:text-gray-100" data-testid="new-api-wallet">{{ t('admin.accounts.upstreamBilling.newAPI.wallet', { amount: formatWallet(result.wallet.amount) }) }}</p>
          <p class="text-xs text-gray-500">{{ t('admin.accounts.upstreamBilling.newAPI.groupRatioHint') }}</p>
          <div v-for="row in result.accounts" :key="row.account_id" class="space-y-2 border-t border-gray-200 pt-3 text-sm dark:border-dark-600" :data-testid="`new-api-result-${row.account_id}`">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span class="font-medium text-gray-800 dark:text-gray-100">{{ row.name }}</span>
              <span :class="row.matched ? 'text-emerald-700 dark:text-emerald-400' : 'text-amber-700 dark:text-amber-400'">{{ row.matched ? t('admin.accounts.upstreamBilling.newAPI.matched') : t('admin.accounts.upstreamBilling.newAPI.needsSelection') }}</span>
              <span v-if="row.group" class="text-gray-600 dark:text-gray-300">{{ t('admin.accounts.upstreamBilling.newAPI.group', { name: row.group }) }}</span>
              <span class="text-gray-600 dark:text-gray-300">{{ row.group !== 'auto' && isFixedRate(row.rate) ? t('admin.accounts.upstreamBilling.newAPI.ratio', { value: row.rate }) : t('admin.accounts.upstreamBilling.newAPI.noFixedRatio') }}</span>
            </div>
            <p v-if="convertedCost(row) != null" class="text-emerald-700 dark:text-emerald-400" :data-testid="`new-api-cost-preview-${row.account_id}`">{{ t('admin.accounts.upstreamBilling.newAPI.costPreview', { rate: row.rate, scale: conversionFactor(row), cost: convertedCost(row) }) }}</p>
            <p v-else-if="row.cost_auto_sync_enabled === false" class="text-xs text-gray-500">{{ t('admin.accounts.upstreamBilling.newAPI.costSyncPaused') }}</p>
            <p v-else-if="row.matched" class="text-xs text-amber-700 dark:text-amber-400">{{ t('admin.accounts.upstreamBilling.newAPI.costUnavailable') }}</p>
            <label v-if="row.token_options?.length && (!row.matched || row.token_options.length > 1)" class="block text-sm text-gray-700 dark:text-gray-200">
              {{ t('admin.accounts.upstreamBilling.newAPI.chooseToken') }}
              <select v-model="tokenSelections[row.account_id]" :disabled="busy" class="input mt-1 w-full" :data-testid="`new-api-token-${row.account_id}`">
                <option :value="undefined">{{ t('admin.accounts.upstreamBilling.newAPI.chooseTokenPlaceholder') }}</option>
                <option v-for="option in row.token_options" :key="option.token_id" :value="option.token_id">{{ option.name }} · #{{ option.token_id }} · {{ option.group }} · {{ option.masked_key }}</option>
              </select>
            </label>
            <p v-if="row.error" class="text-xs text-red-600 dark:text-red-400">{{ rowError(row.error) }}</p>
          </div>
        </div>
        <div v-if="confirmRemove" class="space-y-3 rounded-lg border border-red-200 p-3 dark:border-red-900">
          <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.upstreamBilling.newAPI.removeConfirm') }}</p>
          <div class="flex flex-wrap gap-2">
            <button type="button" class="btn btn-danger" :disabled="busy" data-testid="new-api-confirm-remove" @click="remove">{{ t('common.confirm') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="confirmRemove = false">{{ t('common.cancel') }}</button>
          </div>
        </div>
      </template>
    </div>
    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-end gap-2">
        <button v-if="config?.configured" type="button" class="mr-auto text-sm text-red-600 hover:underline disabled:opacity-50 dark:text-red-400" :disabled="busy" data-testid="new-api-remove" @click="confirmRemove = true">{{ t('admin.accounts.upstreamBilling.newAPI.remove') }}</button>
        <button type="button" class="btn btn-secondary" data-testid="new-api-close" @click="close">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="!canVerify" data-testid="new-api-preview" @click="verify(false)">{{ t('admin.accounts.upstreamBilling.newAPI.preview') }}</button>
        <button type="submit" form="new-api-upstream-form" class="btn btn-primary" :disabled="!canVerify" data-testid="new-api-save" @click.prevent="verify(true)">{{ busy ? t('common.loading') : t('admin.accounts.upstreamBilling.newAPI.save') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import { deleteNewAPIUpstreamConfig, getNewAPIUpstreamConfig, previewNewAPIUpstreamConfig, saveNewAPIUpstreamConfig } from '@/api/admin/accounts'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import type { Account, NewAPIUpstreamConfig, NewAPIUpstreamConfigRequest, NewAPIUpstreamPreview } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'saved'): void }>()
const { t } = useI18n()
const authStore = useAuthStore()
const config = ref<NewAPIUpstreamConfig | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const userId = ref<string | number>('')
const accessToken = ref('')
const accountIds = ref<number[]>([])
const tokenSelections = ref<Record<string, number | undefined>>({})
const applyUSDRate = ref<Record<string, boolean>>({})
const result = ref<NewAPIUpstreamPreview | null>(null)
const confirmRemove = ref(false)
let session = 0

const canVerify = computed(() => !!config.value?.encryption_key_configured && !loading.value && !busy.value)
const canRetainCredential = computed(() => {
  const id = Number(userId.value)
  return !!config.value && (config.value.configured && config.value.user_id === id || config.value.accounts.some(account => account.configured_user_id === id))
})
const defaults = () => {
  const id = props.account?.id
  if (!config.value || id == null) return []
  const upstreamUser = Number(userId.value)
  return config.value.accounts.filter(account => account.account_id === id || Number.isSafeInteger(upstreamUser) && upstreamUser > 0 && account.configured_user_id === upstreamUser).map(account => account.account_id)
}
const invalidatePreview = () => { result.value = null; tokenSelections.value = {}; error.value = '' }
watch(userId, () => { accessToken.value = ''; invalidatePreview(); accountIds.value = defaults() }, { flush: 'sync' })
watch(accessToken, invalidatePreview, { flush: 'sync' })
watch(accountIds, invalidatePreview, { deep: true, flush: 'sync' })

const close = () => {
  session++
  accessToken.value = ''
  busy.value = false
  loading.value = false
  confirmRemove.value = false
  emit('close')
}
watch(() => [props.show, props.account?.id] as const, async ([show, id]) => {
  const currentSession = ++session
  accessToken.value = ''
  config.value = null
  applyUSDRate.value = {}
  userId.value = ''
  accountIds.value = []
  result.value = null
  tokenSelections.value = {}
  error.value = ''
  busy.value = false
  confirmRemove.value = false
  loading.value = show && id != null
  if (!show || id == null) return
  try {
    const loaded = await getNewAPIUpstreamConfig(id)
    if (session !== currentSession) return
    config.value = loaded
    applyUSDRate.value = Object.fromEntries(loaded.accounts.map(row => [row.account_id, row.apply_usd_rate !== false]))
    userId.value = loaded.user_id ?? ''
    accountIds.value = defaults()
    if (!loaded.encryption_key_configured) error.value = t('admin.accounts.upstreamBilling.newAPI.encryptionRequired')
  } catch (cause) {
    if (session === currentSession) error.value = errorMessage(cause, t('admin.accounts.upstreamBilling.newAPI.loadFailed'))
  } finally {
    if (session === currentSession) loading.value = false
  }
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { session++; accessToken.value = '' })

const buildRequest = (): NewAPIUpstreamConfigRequest | null => {
  const id = Number(userId.value)
  if (!Number.isSafeInteger(id) || id <= 0) {
    error.value = t('admin.accounts.upstreamBilling.newAPI.userIdRequired')
    return null
  }
  const token = accessToken.value.trim()
  if (!token && !canRetainCredential.value) {
    error.value = t('admin.accounts.upstreamBilling.newAPI.tokenRequired')
    return null
  }
  const selected = Object.fromEntries(Object.entries(tokenSelections.value).filter((entry): entry is [string, number] => typeof entry[1] === 'number' && Number.isSafeInteger(entry[1]) && entry[1] > 0))
  const costChanges = Object.fromEntries((config.value?.accounts ?? []).filter(row => accountIds.value.includes(row.account_id) && (applyUSDRate.value[row.account_id] !== false) !== (row.apply_usd_rate !== false)).map(row => [row.account_id, applyUSDRate.value[row.account_id] !== false]))
  return { user_id: id, ...(Object.keys(costChanges).length ? { apply_usd_rate: costChanges } : {}), ...(token ? { access_token: token } : {}), account_ids: [...new Set([props.account!.id, ...accountIds.value])], ...(Object.keys(selected).length ? { token_selections: selected } : {}) }
}
const verify = async (save: boolean) => {
  if (!canVerify.value || !props.account) return
  error.value = ''
  const request = buildRequest()
  if (!request) return
  const currentSession = session
  busy.value = true
  try {
    const verified = await (save ? saveNewAPIUpstreamConfig : previewNewAPIUpstreamConfig)(props.account.id, request)
    if (session !== currentSession) return
    result.value = verified
    if (save) {
      // A save response must confirm every requested account before showing success.
      if (request.account_ids.some(id => !verified.accounts.some(row => row.account_id === id && row.matched))) {
        error.value = t('admin.accounts.upstreamBilling.newAPI.saveIncomplete')
        return
      }
      accessToken.value = ''
      emit('saved')
      close()
    }
  } catch (cause) {
    if (session === currentSession) error.value = errorMessage(cause, t('admin.accounts.upstreamBilling.newAPI.verifyFailed'))
  } finally {
    if (session === currentSession) busy.value = false
  }
}
const remove = async () => {
  if (!confirmRemove.value || busy.value || !props.account) return
  const currentSession = session
  busy.value = true
  error.value = ''
  try {
    await deleteNewAPIUpstreamConfig(props.account.id)
    if (session !== currentSession) return
    accessToken.value = ''
    emit('saved')
    close()
  } catch (cause) {
    if (session === currentSession) error.value = errorMessage(cause, t('admin.accounts.upstreamBilling.newAPI.removeFailed'))
  } finally {
    if (session === currentSession) busy.value = false
  }
}
const conversionFactor = (row: {account_id: number; cn_provider?: boolean}) => row.cn_provider || applyUSDRate.value[row.account_id] === false ? 1 : 7
const convertedCost = (row: NewAPIUpstreamPreview['accounts'][number]): string | null => {
  const scale = conversionFactor(row)
  if (row.cost_auto_sync_enabled === false || !row.matched || row.group === 'auto' || !isFixedRate(row.rate) || ![1, 7].includes(scale)) return null
  const cost = row.rate! * scale
  return Number.isFinite(cost) && cost <= 1000000 ? Number(cost.toFixed(6)).toString() : null
}
const isFixedRate = (value?: number) => typeof value === 'number' && Number.isFinite(value) && value >= 0
const formatWallet = (amount: number) => Number.isFinite(amount) ? `$${amount.toFixed(2)} USD` : '-'
const rowError = (value: string) => {
  switch (value) {
    case 'ambiguous_token':
    case 'ambiguous_key': return t('admin.accounts.upstreamBilling.newAPI.needsSelection')
    case 'key_not_owned': return t('admin.accounts.upstreamBilling.newAPI.keyNotOwned')
    case 'token_selection_not_matched': return t('admin.accounts.upstreamBilling.newAPI.tokenSelectionNotMatched')
    case 'selected_key_verification_failed': return t('admin.accounts.upstreamBilling.newAPI.selectedKeyVerificationFailed')
    default: return t('admin.accounts.upstreamBilling.newAPI.accountVerificationFailed')
  }
}
function errorMessage(cause: unknown, fallback: string): string {
  switch (extractApiErrorCode(cause)) {
    case 'NEW_API_ENCRYPTION_KEY_REQUIRED': return t('admin.accounts.upstreamBilling.newAPI.encryptionRequired')
    case 'NEW_API_CONFIGURATION_INVALID': return t('admin.accounts.upstreamBilling.newAPI.verifyFailed')
    case 'NEW_API_VERIFICATION_FAILED': return t('admin.accounts.upstreamBilling.newAPI.upstreamVerificationFailed')
    default: return extractApiErrorMessage(cause, fallback)
  }
}
</script>
