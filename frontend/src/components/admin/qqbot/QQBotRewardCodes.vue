<template>
  <div class="space-y-6">
    <section class="card space-y-4 p-5">
      <h2 class="text-lg font-semibold">{{ t('qqbot.redemption.title') }}</h2>
      <p class="text-sm text-gray-500">{{ t('qqbot.redemption.help') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="notice" role="status" class="text-sm text-green-600">{{ notice }}</p>
    </section>

    <section class="card space-y-4 p-5">
      <h2 class="text-lg font-semibold">{{ t('qqbot.redemption.records') }}</h2>
      <form class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_180px_auto]" @submit.prevent="page = 1; load()">
        <input v-model="search" class="input min-w-0 flex-1" :placeholder="t('qqbot.redemption.search')" :aria-label="t('qqbot.redemption.search')" maxlength="254" />
        <select v-model="status" class="input" :aria-label="t('qqbot.redemption.status')"><option value="">{{ t('qqbot.redemption.all') }}</option><option v-for="value in statuses" :key="value" :value="value">{{ t(`qqbot.redemption.statuses.${value}`) }}</option></select>
        <button class="btn btn-secondary" type="submit" :disabled="loading">{{ t('qqbot.redemption.searchButton') }}</button>
      </form>
      <p v-if="listError" role="alert" class="text-sm text-red-600">{{ listError }}</p>
      <p v-if="loading" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-left text-sm">
          <thead><tr class="border-b dark:border-dark-700"><th class="p-2">{{ t('qqbot.redemption.code') }}</th><th class="p-2">{{ t('qqbot.redemption.reward') }}</th><th class="p-2">{{ t('qqbot.redemption.status') }}</th><th class="p-2">{{ t('qqbot.redemption.recipient') }}</th><th class="p-2">{{ t('qqbot.redemption.actions') }}</th></tr></thead>
          <tbody><tr v-for="row in items" :key="row.id" class="border-b align-top dark:border-dark-700">
            <td class="space-y-1 p-2"><div>#{{ row.id }} · {{ row.source_kind && row.source_kind !== 'code' ? t('qqbot.growth.sources.' + row.source_kind) : '…' + row.code_hint }}</div><div>{{ row.batch_name }}</div><div class="text-xs text-gray-500">AppID: {{ row.app_id }}</div><div class="text-xs text-gray-500">{{ t('qqbot.redemption.expiry') }}: {{ date(row.expires_at) }}</div></td>
            <td class="space-y-1 p-2"><div>{{ t(`qqbot.redemption.types.${row.reward_type}`) }}</div><div>{{ row.reward_type === 'balance' ? `USD ${balanceAmount(row.amount)}` : `${row.prize_name || t(row.reward_type === 'coupon' ? 'qqbot.redemption.types.coupon' : 'qqbot.redemption.types.chances')} × ${row.quantity}` }}</div><div v-if="row.claim_instructions" class="max-w-xs whitespace-pre-wrap text-xs text-gray-500">{{ row.claim_instructions }}</div></td>
            <td class="space-y-1 p-2"><div>{{ t(`qqbot.redemption.statuses.${row.status}`) }}</div><div v-if="row.redeemed_at" class="text-xs text-gray-500">{{ t('qqbot.redemption.redeemed') }}: {{ date(row.redeemed_at) }}</div><div v-if="row.fulfilled_at" class="text-xs text-gray-500">{{ t('qqbot.redemption.fulfilled') }}: {{ date(row.fulfilled_at) }}</div><div v-if="row.fulfillment_note" class="max-w-xs whitespace-pre-wrap text-xs text-gray-500">{{ row.fulfillment_note }}</div></td>
            <td class="space-y-1 p-2"><div>{{ row.email || (row.user_id ? `#${row.user_id}` : '—') }}</div><div v-if="row.member_id" class="max-w-[12rem] break-all text-xs text-gray-500">{{ row.member_id }}</div><div v-if="row.target_user_id" class="text-xs text-gray-500">{{ t('qqbot.redemption.target') }}: #{{ row.target_user_id }}</div></td>
            <td class="p-2"><button v-if="row.status === 'unused' || row.status === 'expired' || row.status === 'pending'" class="btn btn-secondary whitespace-nowrap" :disabled="busy" type="button" @click="selected = row; note = ''">{{ t(row.status === 'pending' ? 'qqbot.redemption.fulfill' : 'qqbot.redemption.disable') }}</button></td>
          </tr><tr v-if="!items.length"><td colspan="5" class="p-6 text-center text-gray-500">{{ t('qqbot.redemption.empty') }}</td></tr></tbody>
        </table>
      </div>
      <div class="flex flex-wrap items-center justify-between gap-2"><span class="text-sm text-gray-500">{{ t('qqbot.recordsPage', { total, page, pages: Math.max(1, Math.ceil(total / 20)) }) }}</span><div class="flex gap-2"><button class="btn btn-secondary" :disabled="loading || page <= 1" @click="page--; load()">{{ t('qqbot.redemption.previousPage') }}</button><button class="btn btn-secondary" :disabled="loading || page * 20 >= total" @click="page++; load()">{{ t('qqbot.redemption.nextPage') }}</button></div></div>
      <form v-if="selected" class="space-y-3 rounded-lg border border-amber-300 p-4" @submit.prevent="update">
        <p>{{ t(selected.status === 'pending' ? 'qqbot.redemption.fulfillConfirm' : 'qqbot.redemption.disableConfirm', { id: selected.id }) }}</p>
        <label v-if="selected.status === 'pending'" class="block space-y-1"><span class="text-sm">{{ t('qqbot.redemption.note') }}</span><textarea v-model="note" class="input w-full" maxlength="500" /></label>
        <div class="flex gap-2"><button class="btn btn-primary" :disabled="busy" type="submit">{{ t('common.confirm') }}</button><button class="btn btn-secondary" :disabled="busy" type="button" @click="selected = null">{{ t('common.cancel') }}</button></div>
      </form>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQRewardCode } from '@/api/qqbot'

const { t } = useI18n()
const statuses = ['unused', 'pending', 'fulfilled', 'disabled', 'expired'] as const
const error = ref(''), listError = ref(''), notice = ref(''), note = ref('')
const busy = ref(false), loading = ref(false)
const items = ref<QQRewardCode[]>([]), selected = ref<QQRewardCode | null>(null)
const total = ref(0), page = ref(1), search = ref(''), status = ref('')
let loadVersion = 0
const date = (value: string | null) => value ? new Date(value).toLocaleString() : '—'
const balanceAmount = (value: number) => value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 8, useGrouping: false })
const message = (e: unknown) => (e as Error)?.message || t('common.error')

async function load() {
  const version = ++loadVersion
  loading.value = true; listError.value = ''
  try {
    const data = await qqbotAPI.rewardCodes(search.value.trim(), status.value, page.value)
    if (version !== loadVersion) return
    items.value = data.items; total.value = data.total
  } catch (e) { if (version === loadVersion) { items.value = []; listError.value = message(e) } }
  finally { if (version === loadVersion) loading.value = false }
}
async function update() {
  if (!selected.value || busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try { await qqbotAPI.updateRewardCode(selected.value.id, selected.value.status === 'pending' ? 'fulfill' : 'disable', note.value); selected.value = null; notice.value = t('common.saved'); await load() }
  catch (e) { error.value = message(e) }
  finally { busy.value = false }
}
onMounted(load)
onBeforeUnmount(() => { loadVersion++ })
</script>
