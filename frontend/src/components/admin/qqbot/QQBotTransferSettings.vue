<template>
  <div class="space-y-6">
    <p class="text-sm text-gray-500">{{ t('qqbot.transferHelp') }}</p>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
    <p v-if="dirty" class="text-sm text-amber-700">{{ t('qqbot.workspace.unsaved') }}</p>
    <form v-if="form" class="card space-y-5 p-6" @submit.prevent="save">
      <label class="flex items-center gap-2">
        <input v-model="form.transfer_enabled" type="checkbox" />
        {{ t('qqbot.enableTransfer') }}
      </label>
      <div class="grid gap-4 sm:grid-cols-3">
        <label class="space-y-1">
          {{ t('qqbot.transferFeePercent') }}
          <input
            v-model.number="form.transfer_fee_percent"
            data-testid="transfer-fee-percent"
            type="number"
            min="0"
            max="100"
            step="0.01"
            required
            class="input"
          />
        </label>
        <label class="space-y-1">
          {{ t('qqbot.transferMinAmount') }}
          <input
            v-model.number="form.transfer_min_amount"
            data-testid="transfer-min-amount"
            type="number"
            min="0"
            step="0.0001"
            required
            class="input"
          />
        </label>
        <label class="space-y-1">
          {{ t('qqbot.transferMaxAmount') }}
          <input
            v-model.number="form.transfer_max_amount"
            data-testid="transfer-max-amount"
            type="number"
            min="0"
            step="0.0001"
            required
            class="input"
          />
        </label>
      </div>
      <p class="rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800">{{ t('qqbot.transferRangeHelp') }}</p>
      <p class="text-sm text-gray-500">{{ t('qqbot.transferCommand') }}</p>
      <button type="submit" class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button>
    </form>
    <p v-else-if="!error">{{ t('common.loading') }}</p>
  </div>
</template>
<script setup lang="ts">
import { useQQDraft } from './useQQDraft'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQTransferConfig } from '@/api/qqbot'

const { t } = useI18n()
const form = ref<QQTransferConfig | null>(null)
const { dirty, markSaved } = useQQDraft(form)
const busy = ref(false)
const error = ref('')
const notice = ref('')

async function save() {
  if (!form.value) return
  error.value = ''; notice.value = ''
  const { transfer_fee_percent, transfer_min_amount, transfer_max_amount } = form.value
  if (!Number.isFinite(transfer_fee_percent) || transfer_fee_percent < 0 || transfer_fee_percent > 100) {
    error.value = t('qqbot.invalidTransferFee'); return
  }
  if (!Number.isFinite(transfer_min_amount) || transfer_min_amount < 0) {
    error.value = t('qqbot.invalidTransferMin'); return
  }
  if (!Number.isFinite(transfer_max_amount) || transfer_max_amount < 0) {
    error.value = t('qqbot.invalidTransferMax'); return
  }
  if (transfer_max_amount > 0 && transfer_max_amount < transfer_min_amount) {
    error.value = t('qqbot.invalidTransferRange'); return
  }
  busy.value = true
  try {
    form.value = await qqbotAPI.saveTransfer({ ...form.value })
    markSaved(); notice.value = t('common.saved')
  } catch (e) {
    error.value = (e as Error).message || t('common.error')
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  try {
    form.value = await qqbotAPI.transferConfig()
  } catch (e) {
    error.value = (e as Error).message || t('common.error')
  }
})
</script>
