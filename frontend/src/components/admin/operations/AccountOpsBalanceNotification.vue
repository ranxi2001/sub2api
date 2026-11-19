<template>
  <div class="mt-2 text-xs text-gray-500">
    <div class="flex items-start gap-2">
      <label class="flex items-start gap-2" :title="t('accountOps.accountBalanceNotificationHint')">
        <input :checked="enabled" type="checkbox" :disabled="busy || disabled" :data-testid="`balance-notification-${account.account_id}`" @change="save($event)" />
        <span>{{ account.account_name }} · {{ t('accountOps.accountBalanceNotification') }}</span>
      </label>
      <NewFeatureBadge v-if="userId != null" :notice-id="NEW_FEATURE_NOTICES.accountOpsBalanceNotification" :user-id="userId" />
    </div>
    <span v-if="error" role="alert" class="block text-red-600">{{ error }}</span>
  </div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { setAccountOpsBalanceLowNotification, type AccountOpsConfig, type AccountOpsThresholdAccount } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ account: AccountOpsThresholdAccount; config: AccountOpsConfig; disabled?: boolean; userId?: number | null }>()
const emit = defineEmits<{ saved: [config: AccountOpsConfig] }>()
const { t } = useI18n()
const busy = ref(false), error = ref('')
const enabled = computed(() => !props.config.balance_low_disabled_account_ids?.includes(props.account.account_id))
let alive = true
onBeforeUnmount(() => { alive = false })
async function save(event: Event) {
  const checkbox = event.target as HTMLInputElement
  const value = checkbox.checked
  checkbox.checked = enabled.value
  if (busy.value || props.disabled) return
  const accountID = props.account.account_id
  busy.value = true; error.value = ''
  try {
    const config = await setAccountOpsBalanceLowNotification(accountID, value)
    if (alive && props.account.account_id === accountID) emit('saved', config)
  } catch (cause) {
    if (alive && props.account.account_id === accountID) error.value = extractApiErrorMessage(cause, t('accountOps.saveFailed'))
  } finally { if (alive) busy.value = false }
}
</script>
