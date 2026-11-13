<template>
  <section class="card space-y-4 p-5" data-testid="key-risk-panel">
    <p class="text-sm text-gray-500">{{ t('ipAudit.keyRisk.hint') }}</p>
    <p v-if="error && confirmId === null" role="alert" class="text-red-600">{{ error }}</p>
    <p v-if="message" role="status">{{ message }}</p>
    <form v-if="config" @submit.prevent="save">
      <fieldset :disabled="busy" class="space-y-4">
        <div class="flex flex-wrap gap-4">
          <label><input v-model="config.enabled" type="checkbox" /> {{ t('ipAudit.keyRisk.enabled') }}</label>
          <label><input v-model="config.age_email" type="checkbox" /> {{ t('ipAudit.keyRisk.ageEmail') }}</label>
          <label><input v-model="config.auto_email" type="checkbox" /> {{ t('ipAudit.keyRisk.autoEmail') }}</label>
        </div>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <label v-for="field in fields" :key="field.key" class="text-sm">{{ t(`ipAudit.keyRisk.${field.key}`) }}
            <input v-model.number="config[field.key]" type="number" :min="field.min" :max="field.max" required class="input mt-1 w-full" />
          </label>
        </div>
        <label class="block text-sm">{{ t('ipAudit.keyRisk.trusted') }}<textarea v-model="trusted" class="input mt-1 w-full" rows="3" /></label>
        <button type="submit" class="btn btn-primary">{{ t('common.save') }}</button>
        <button type="button" class="btn btn-secondary ml-2" @click="refresh">{{ t('common.refresh') }}</button>
        <button type="button" class="btn btn-secondary ml-2" @click="$emit('configure')">{{ t('ipAudit.settings') }}</button>
      </fieldset>
    </form>
    <template v-if="snapshot">
      <p class="text-sm">{{ t('ipAudit.keyRisk.lastScan') }}: {{ formatDate(snapshot.last_scan_at) }} · {{ t('ipAudit.keyRisk.delivery') }}: {{ snapshot.pending }} / {{ snapshot.sent }} / {{ snapshot.uncertain }}</p>
      <p v-if="snapshot.coverage_issue" role="alert" class="text-amber-700">{{ t(snapshot.coverage_issue.startsWith('coverage_incomplete') ? 'ipAudit.keyRisk.incomplete' : 'ipAudit.keyRisk.scanFailed') }}</p>
      <div class="overflow-x-auto"><table class="w-full text-left text-sm">
        <thead><tr><th class="p-2">{{ t('ipAudit.reason') }}</th><th class="p-2">{{ t('ipAudit.keyRisk.evidence') }}</th><th class="p-2">{{ t('ipAudit.state') }}</th><th class="p-2">{{ t('common.actions') }}</th></tr></thead>
        <tbody><tr v-for="row in snapshot.items" :key="row.id" class="border-t align-top">
          <td class="p-2">{{ t(`ipAudit.keyRisk.${row.rule}`) }}<p v-if="row.rule === 'cross_user_ip'"><code>{{ row.subject }}</code></p><p v-else><RiskIdentity :id="Number(row.subject)" :name="row.keys?.find(key => key.id === Number(row.subject))?.name" /></p><p>{{ formatDate(row.first_seen) }} → {{ formatDate(row.last_seen) }}</p></td>
          <td class="p-2"><p v-if="row.rule !== 'key_ip_spread'" class="text-gray-500">{{ t('ipAudit.keyRisk.retired') }}</p><div class="mt-2 space-y-1"><p class="text-xs text-gray-500">{{ t('ipAudit.keyRisk.users') }}</p><div v-for="id in row.evidence.user_ids" :key="id"><RiskIdentity :id="id" :name="row.users?.find(user => user.id === id)?.name" /></div></div><div class="mt-2 space-y-1"><p class="text-xs text-gray-500">{{ t('ipAudit.keyRisk.keys') }}</p><div v-for="id in row.evidence.key_ids" :key="id"><RiskIdentity :id="id" :name="row.keys?.find(key => key.id === id)?.name" /></div></div><p>{{ row.evidence.ips?.join(', ') }}<br />{{ row.evidence.requests }} / {{ row.evidence.window_minutes }} {{ t('ipAudit.keyRisk.minutes') }}</p></td>
          <td class="p-2">{{ t(`ipAudit.keyRisk.${row.state}`) }}
            <details v-if="row.recipients?.length" class="mt-2">
              <summary class="cursor-pointer">{{ t('ipAudit.keyRisk.noticeRecords') }}</summary>
              <p class="max-w-xs text-xs text-gray-500">{{ t('ipAudit.keyRisk.receiptHint') }}</p>
              <div v-for="recipient in row.recipients" :key="`${recipient.user_id}-${recipient.generation}`" class="mt-2 text-xs">
                <p><RiskIdentity :id="recipient.user_id" :name="recipient.user_name" /> · {{ t('ipAudit.keyRisk.episode', { n: recipient.generation }) }}</p>
                <p>{{ t('ipAudit.keyRisk.receiptCounts', { attempts: recipient.attempts, sent: recipient.sent, uncertain: recipient.uncertain }) }}</p>
                <p v-if="recipient.last_attempt">{{ t('ipAudit.keyRisk.lastAttempt') }}: {{ formatDate(recipient.last_attempt) }}</p>
                <p v-if="recipient.acknowledged_at">{{ t('ipAudit.keyRisk.acknowledged') }}: {{ formatDate(recipient.acknowledged_at) }}</p>
                <p v-else-if="config && recipient.attempts >= config.max_reminders">{{ t('ipAudit.keyRisk.limitReached') }}</p>
              </div>
            </details>
          </td>
          <td class="space-y-2 p-2">
            <button v-if="row.state === 'pending' && row.rule === 'key_ip_spread'" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="openConfirmation(row, 'notify')">{{ t('ipAudit.keyRisk.notify') }}</button>
            <div v-if="row.rule === 'key_ip_spread' && row.state === 'pending'" class="flex items-center gap-1">
              <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="openConfirmation(row, 'restrict')">{{ t('ipAudit.keyRisk.restrict') }}</button>
              <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.keyRiskSources" :user-id="adminId" />
            </div>
            <button v-if="row.state === 'pending' && row.rule === 'key_ip_spread'" class="btn btn-secondary btn-sm ml-2" :disabled="busy" @click="act(row.id, 'ignored')">{{ t('ipAudit.keyRisk.ignore') }}</button>
            <button v-if="row.rule === 'key_ip_spread'" class="btn btn-secondary btn-sm ml-2" :disabled="busy" @click="act(row.id, row.state === 'pending' ? 'resolved' : 'pending')">{{ t(row.state === 'pending' ? 'ipAudit.keyRisk.resolve' : 'ipAudit.keyRisk.reopen') }}</button>
          </td>
        </tr></tbody>
      </table></div>
      <p v-if="!snapshot.items.length">{{ t('ipAudit.empty') }}</p>
      <div class="flex gap-2"><button class="btn btn-secondary" :disabled="busy || !before" @click="refresh">{{ t('ipAudit.keyRisk.firstPage') }}</button><button class="btn btn-secondary" :disabled="busy || snapshot.items.length < 50" @click="next">{{ t('ipAudit.next') }}</button></div>
    </template>
    <BaseDialog
      :show="confirmId !== null"
      :title="t(confirmAction === 'restrict' ? 'ipAudit.keyRisk.restrict' : 'ipAudit.keyRisk.notify')"
      width="narrow"
      :close-on-escape="!busy"
      :show-close-button="!busy"
      @close="closeConfirmation"
    >
      <div class="space-y-3">
        <p v-if="confirmationTarget"><RiskIdentity :id="Number(confirmationTarget.subject)" :name="confirmationTarget.keys?.find(key => key.id === Number(confirmationTarget!.subject))?.name" /></p>
        <p>{{ t(confirmAction === 'restrict' ? 'ipAudit.keyRisk.restrictConfirm' : 'ipAudit.keyRisk.confirm') }}</p>
        <label v-if="confirmAction === 'restrict'" class="block text-sm">{{ t('ipAudit.keyRisk.allowedNetworks') }}
          <textarea v-model="allowedNetworks" data-testid="allowed-networks" class="input mt-1 w-full font-mono" rows="3" />
        </label>
        <p v-if="error" role="alert" class="text-red-600">{{ error }}</p>
      </div>
      <template #footer>
        <button type="button" data-testid="key-risk-confirm" class="btn btn-primary" :disabled="busy || (confirmAction === 'restrict' && !parseNetworks(allowedNetworks).length)" @click="act(confirmId!, confirmAction)">{{ t('ipAudit.keyRisk.confirmAction') }}</button>
        <button type="button" data-testid="key-risk-cancel" class="btn btn-secondary ml-2" :disabled="busy" @click="closeConfirmation">{{ t('common.cancel') }}</button>
      </template>
    </BaseDialog>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import RiskIdentity from './RiskIdentity.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useI18n } from 'vue-i18n'
import { keyRiskApi, type KeyRiskConfig, type KeyRiskSnapshot, type KeyRiskEvent } from './keyRiskApi'
defineProps<{ adminId?: number }>()
defineEmits<{ configure: [] }>()
const { t } = useI18n()
const config = ref<KeyRiskConfig>(), snapshot = ref<KeyRiskSnapshot>(), trusted = ref(''), busy = ref(false), error = ref(''), message = ref(''), before = ref(0), confirmId = ref<number | null>(null), confirmAction = ref<'notify' | 'restrict'>('notify')
const allowedNetworks = ref(''), restrictionGeneration = ref(0), confirmationTarget = ref<KeyRiskEvent>()
const fields = [ { key: 'max_reminders', min: 1, max: 10 }, { key: 'age_days', min: 1, max: 3650 }, { key: 'window_minutes', min: 1, max: 60 }, { key: 'ips_per_key', min: 2, max: 100 } ] as const
const formatDate = (value: string) => value.startsWith('0001') ? '—' : new Date(value).toLocaleString()
async function load() { const result = await keyRiskApi.get(before.value); snapshot.value = result; config.value = structuredClone(result.config); trusted.value = result.config.trusted_networks.join('\n') }
async function run(fn: () => Promise<void>) { if (busy.value) return; busy.value = true; error.value = ''; message.value = ''; try { await fn() } catch { error.value = t('ipAudit.keyRisk.error') } finally { busy.value = false } }
function refresh() { before.value = 0; void run(load) }
function next() { const rows = snapshot.value?.items; if (!rows?.length) return; before.value = rows[rows.length - 1]!.id; void run(load) }
function save() { void run(async () => { if (!config.value) return; await keyRiskApi.save({ ...config.value, trusted_networks: parseNetworks(trusted.value) }); await load(); message.value = t('ipAudit.keyRisk.saved') }) }
function parseNetworks(value: string) { return value.split(/[\n,]/).map(s => s.trim()).filter(Boolean) }
function openConfirmation(row: KeyRiskEvent, action: 'notify' | 'restrict') {
  confirmationTarget.value = row
  error.value = ''
  message.value = ''
  confirmId.value = row.id
  confirmAction.value = action
  restrictionGeneration.value = row.generation
  allowedNetworks.value = ''
}
function closeConfirmation() {
  if (busy.value) return
  confirmId.value = null
  confirmationTarget.value = undefined
}
function act(id: number, action: string) {
  void run(async () => {
    const result = action === 'restrict'
      ? await keyRiskApi.action(id, action, { generation: restrictionGeneration.value, confirm: true, allowed_networks: parseNetworks(allowedNetworks.value) })
      : await keyRiskApi.action(id, action)
    confirmId.value = null
    await load()
    message.value = t(action === 'notify' ? (result?.queued ? 'ipAudit.keyRisk.queued' : 'ipAudit.keyRisk.suppressed') : action === 'restrict' ? 'ipAudit.keyRisk.restrictSaved' : 'ipAudit.keyRisk.saved')
  })
}
onMounted(refresh)
</script>
