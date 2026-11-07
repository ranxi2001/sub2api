<template>
  <section class="rounded-lg border border-amber-200 bg-amber-50/40 p-4 dark:border-amber-900 dark:bg-amber-950/20" aria-labelledby="key-risk-notices-title">
    <div class="flex items-center justify-between gap-3">
      <h2 id="key-risk-notices-title" class="font-medium">{{ t('ipAudit.keyRisk.userTitle') }}</h2>
      <button class="btn btn-secondary btn-sm" :disabled="busy" @click="load(0)">{{ t('common.refresh') }}</button>
    </div>
    <p class="mt-2 text-sm text-gray-600 dark:text-gray-300">{{ t('ipAudit.keyRisk.userHint') }}</p>
    <p v-if="error" role="alert" class="mt-2 text-sm text-red-600">{{ t('ipAudit.keyRisk.error') }}</p>
    <p v-if="message" role="status" class="mt-2 text-sm">{{ message }}</p>
    <p v-if="!busy && !error && !items.length" class="mt-2 text-sm">{{ t('ipAudit.keyRisk.noNotices') }}</p>
    <ul class="mt-3 space-y-3">
      <li v-for="item in items" :key="`${item.event_id}-${item.generation}`" class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-800">
        <p class="font-medium">{{ t(`ipAudit.keyRisk.${item.rule}`) }} · #{{ item.event_id }}</p>
        <div class="my-1 space-y-1 text-sm"><p class="text-xs text-gray-500">{{ t('ipAudit.keyRisk.keys') }}</p><div v-for="id in item.key_ids" :key="id"><RiskIdentity :id="id" :name="item.keys?.find(key => key.id === id)?.name" /></div><p v-if="!item.key_ids.length">—</p></div>
        <p class="text-sm text-gray-500">{{ new Date(item.last_seen).toLocaleString() }} · {{ t('ipAudit.keyRisk.attempts', { count: item.attempts, max: item.max_reminders }) }}</p>
        <p v-if="item.acknowledged_at" class="mt-2 text-sm" role="status">{{ t('ipAudit.keyRisk.acknowledged') }} · {{ new Date(item.acknowledged_at).toLocaleString() }}</p>
        <p v-else-if="item.state !== 'pending'" class="mt-2 text-sm">{{ t(`ipAudit.keyRisk.${item.state}`) }}</p>
        <template v-else>
          <p v-if="item.attempts >= item.max_reminders" class="mt-2 text-sm">{{ t('ipAudit.keyRisk.limitReached') }}</p>
          <button class="btn btn-secondary btn-sm mt-2" :disabled="busy" @click="confirming = item">{{ t('ipAudit.keyRisk.acknowledge') }}</button>
        </template>
        <div v-if="confirming?.event_id === item.event_id" class="mt-3 space-y-2 rounded border border-amber-300 p-3" role="group" :aria-label="t('ipAudit.keyRisk.acknowledge')">
          <p class="text-sm">{{ t('ipAudit.keyRisk.ackConfirm') }}</p>
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-primary btn-sm" :disabled="busy" data-test="confirm-risk" @click="acknowledge(item)">{{ t('ipAudit.keyRisk.ackSubmit') }}</button>
            <button class="btn btn-secondary btn-sm" :disabled="busy" @click="confirming = null">{{ t('common.cancel') }}</button>
          </div>
        </div>
      </li>
    </ul>
    <div v-if="before || items.length === 50" class="mt-3 flex gap-2">
      <button class="btn btn-secondary btn-sm" :disabled="busy || !before" @click="load(0)">{{ t('ipAudit.keyRisk.firstPage') }}</button>
      <button class="btn btn-secondary btn-sm" :disabled="busy || items.length < 50" @click="load(items[items.length - 1]!.event_id)">{{ t('ipAudit.next') }}</button>
    </div>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import RiskIdentity from './RiskIdentity.vue'
import { useI18n } from 'vue-i18n'
import { userKeyRiskApi, type UserKeyRiskNotice } from './keyRiskApi'
const { t } = useI18n()
const items = ref<UserKeyRiskNotice[]>([])
const busy = ref(false), error = ref(false), message = ref(''), before = ref(0)
const confirming = ref<UserKeyRiskNotice | null>(null)
async function load(cursor: number) {
  if (busy.value) return
  busy.value = true; error.value = false; message.value = ''; confirming.value = null
  try { items.value = (await userKeyRiskApi.list(cursor)).items; before.value = cursor }
  catch { error.value = true }
  finally { busy.value = false }
}
async function acknowledge(item: UserKeyRiskNotice) {
  if (busy.value) return
  busy.value = true; error.value = false; message.value = ''
  try {
    await userKeyRiskApi.acknowledge(item)
    confirming.value = null
    message.value = t('ipAudit.keyRisk.acknowledged')
    items.value = (await userKeyRiskApi.list(before.value)).items
  } catch { error.value = true }
  finally { busy.value = false }
}
onMounted(() => load(0))
</script>
