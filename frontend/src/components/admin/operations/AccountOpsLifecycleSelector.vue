<template>
  <label class="mt-2 block text-xs text-gray-500">
    <span>{{ account.account_name }} · {{ t('accountOps.oauthLifecycle') }} <NewFeatureBadge v-if="userId != null" :notice-id="NEW_FEATURE_NOTICES.accountOpsOAuthLifecycle" :user-id="userId" /></span>
    <select :value="account.oauth_lifecycle || 'temporary'" :disabled="busy || disabled" class="input mt-1 w-full text-xs" :aria-label="`${account.account_name} ${t('accountOps.oauthLifecycle')}`" data-testid="oauth-lifecycle" @change="save(($event.target as HTMLSelectElement).value)">
      <option value="temporary">{{ t('accountOps.oauthTemporary') }}</option>
      <option value="long_term">{{ t('accountOps.oauthLongTerm') }}</option>
    </select>
    <span v-if="error" role="alert" class="block text-red-600">{{ error }}</span>
  </label>
</template>
<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { setAccountOpsOAuthLifecycle, type AccountOpsThresholdAccount, type AccountOpsOAuthLifecycle } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'
const props = defineProps<{ account: AccountOpsThresholdAccount; disabled?: boolean; userId?: number | null }>()
const emit = defineEmits<{ updated: [] }>()
const { t } = useI18n()
const busy = ref(false), error = ref('')
let alive = true
onBeforeUnmount(() => { alive = false })
async function save(value: string) {
  if (busy.value || props.disabled || !['temporary', 'long_term'].includes(value)) return
  busy.value = true; error.value = ''
  try { await setAccountOpsOAuthLifecycle(props.account.account_id, value as AccountOpsOAuthLifecycle); if (alive) emit('updated') }
  catch (cause) { if (alive) error.value = extractApiErrorMessage(cause, t('accountOps.saveFailed')) }
  finally { if (alive) busy.value = false }
}
</script>
