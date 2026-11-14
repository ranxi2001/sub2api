<template>
  <BaseDialog :show="show" :title="t(config.recipient ? 'accountOps.editEmail' : 'accountOps.addEmail')" width="normal" @close="close">
    <form id="account-ops-email-form" class="space-y-4" @submit.prevent="save">
      <fieldset :disabled="busy" class="space-y-4">
        <label class="block space-y-2 text-sm" for="account-ops-email-name">
          <span>{{ t('accountOps.channelName') }}</span>
          <input id="account-ops-email-name" v-model.trim="name" type="text" maxlength="80" autocomplete="off" :placeholder="t('accountOps.channelNamePlaceholder')" class="input w-full" />
          <span class="block text-xs text-gray-500">{{ t('accountOps.channelNameHint') }}</span>
        </label>
        <label class="block space-y-2 text-sm" for="account-ops-email">
          <span>{{ t('accountOps.recipient') }} <NewFeatureBadge v-if="userId != null" :notice-id="NEW_FEATURE_NOTICES.accountOpsMultipleEmails" :user-id="userId" /></span>
          <textarea id="account-ops-email" v-model.trim="recipient" rows="4" maxlength="5120" required autocomplete="off" spellcheck="false" placeholder="ops@example.com&#10;oncall@example.com" class="input w-full" />
        </label>
        <p class="text-xs leading-5 text-gray-500">{{ t('accountOps.recipientHint') }}</p>
        <div v-if="recipient" class="flex items-center justify-between gap-2 text-xs text-gray-500">
          <span>{{ t(smtpConfigured ? 'accountOps.smtpReady' : 'accountOps.smtpMissing') }}</span>
          <a href="/admin/settings" class="text-primary-600">{{ t('accountOps.mailSettings') }}</a>
        </div>
      </fieldset>
    </form>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="close">{{ t('common.cancel') }}</button>
      <button type="submit" form="account-ops-email-form" :disabled="busy" class="btn btn-primary">{{ t(busy ? 'common.loading' : 'accountOps.saveChannel') }}</button>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { saveAccountOpsNotificationSettings } from '@/api/admin/accountOps'
import type { AccountOpsConfig } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; config: AccountOpsConfig; smtpConfigured: boolean; userId?: number | null }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'saved', config: AccountOpsConfig): void; (event: 'error', message: string): void }>()
const { t } = useI18n()
const busy = ref(false), recipient = ref(''), name = ref('')
let sequence = 0
watch(() => props.show, () => {
  sequence++
  busy.value = false
  recipient.value = props.show ? props.config.recipient.split(/[,;\n]+/).map(value => value.trim()).filter(Boolean).join('\n') : ''
  name.value = props.show ? props.config.email_name ?? '' : ''
}, { immediate: true })
const close = () => { sequence++; busy.value = false; recipient.value = ''; name.value = ''; emit('close') }
onBeforeUnmount(() => { sequence++; recipient.value = '' })
async function save() {
  if (busy.value) return
  if (!recipient.value) { emit('error', t('accountOps.emailRequired')); return }
  const current = sequence
  busy.value = true
  try {
    const config = await saveAccountOpsNotificationSettings({ recipient: recipient.value, email_name: name.value })
    if (current === sequence) { emit('saved', config); close() }
  } catch (e) {
    if (current === sequence) emit('error', extractApiErrorMessage(e, t('accountOps.saveFailed')))
  } finally {
    if (current === sequence) busy.value = false
  }
}
</script>
