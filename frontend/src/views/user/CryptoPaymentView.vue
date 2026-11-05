<template>
  <div class="min-h-screen bg-gray-50 px-4 py-10 dark:bg-dark-900">
    <main class="mx-auto max-w-xl space-y-5">
      <div class="flex items-center justify-between gap-4">
        <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('payment.cryptoCheckout.title') }}</h1>
        <RouterLink to="/purchase" class="text-sm text-primary-600">{{ t('payment.cryptoCheckout.back') }}</RouterLink>
      </div>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20">{{ error }}</p>
      <div v-if="!checkout && !error" class="card p-8 text-center">{{ t('common.loading') }}</div>
      <section v-if="checkout" class="card space-y-5 p-6">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <span class="rounded-full bg-gray-100 px-3 py-1 text-sm font-medium dark:bg-dark-700">{{ networkName }}</span>
          <span class="text-sm" role="status">{{ statusText }}</span>
        </div>
        <p v-if="networkDelayed" role="alert" class="rounded-lg bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300" data-testid="payment-network-delay">{{ t('payment.cryptoCheckout.networkDelayHint') }}</p>
        <template v-if="payable && !checkout.confirming && !networkDelayed">
          <p class="text-sm text-amber-700 dark:text-amber-400">{{ t('payment.cryptoCheckout.instruction', { network: networkName }) }}</p>
          <div class="text-center">
            <p class="text-sm text-gray-500">{{ t('payment.cryptoCheckout.exactAmount') }}</p>
            <p class="mt-1 break-all text-3xl font-semibold tabular-nums">{{ checkout.crypto_amount }} {{ coinName }}</p>
            <p class="mt-1 text-xs text-gray-500">{{ checkout.fiat_amount }} {{ checkout.currency }}</p>
            <button type="button" class="mt-2 text-sm text-primary-600" @click="copy(checkout.crypto_amount)">{{ t('payment.cryptoCheckout.copyAmount') }}</button>
          </div>
          <img v-if="qr" :src="qr" :alt="t('payment.cryptoCheckout.addressQR')" class="mx-auto h-52 w-52 rounded-lg bg-white p-2" />
          <div class="rounded-lg bg-gray-50 p-4 dark:bg-dark-800">
            <p class="mb-2 text-xs text-gray-500">{{ t('payment.cryptoCheckout.address') }}</p>
            <p class="break-all font-mono text-sm" data-testid="receiving-address">{{ checkout.address }}</p>
            <button type="button" class="mt-3 text-sm text-primary-600" @click="copy(checkout.address)">{{ t('payment.cryptoCheckout.copyAddress') }}</button>
          </div>
          <p class="text-center text-sm text-gray-500">{{ t('payment.cryptoCheckout.remaining', { time: remaining }) }}</p>
          <p v-if="copied" role="status" class="text-center text-sm text-green-600">{{ t('payment.cryptoCheckout.copied') }}</p>
        </template>
        <p v-else class="py-6 text-center text-lg font-medium">{{ statusText }}</p>
        <div class="flex flex-wrap justify-center gap-3">
          <button type="button" class="btn btn-primary" :disabled="checking" @click="refresh(true)">{{ t('payment.cryptoCheckout.check') }}</button>
          <button v-if="payable && !checkout.confirming && !networkDelayed" type="button" class="btn btn-secondary" :disabled="checking" @click="confirmCancel = true">{{ t('common.cancel') }}</button>
          <RouterLink v-if="completed" :to="{ path: '/payment/result', query: { order_id: checkout.order_id, out_trade_no: checkout.out_trade_no } }" class="btn btn-secondary">{{ t('payment.cryptoCheckout.viewResult') }}</RouterLink>
        </div>
        <div v-if="confirmCancel" class="flex flex-wrap items-center justify-center gap-3 border-t pt-4">
          <span class="text-sm">{{ t('payment.cryptoCheckout.cancelConfirm') }}</span>
          <button type="button" class="btn btn-secondary" :disabled="checking" @click="cancel">{{ t('common.confirm') }}</button>
          <button type="button" class="btn btn-secondary" @click="confirmCancel = false">{{ t('payment.cryptoCheckout.keepWaiting') }}</button>
        </div>
      </section>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { paymentAPI, type CryptoCheckout } from '@/api/payment'
import { useClipboard } from '@/composables/useClipboard'
import { BEPUSDT_TRADE_TYPES } from '@/components/payment/providerConfig'
const { t } = useI18n()
const route = useRoute()
const checkout = ref<CryptoCheckout | null>(null)
const error = ref('')
const qr = ref('')
const checking = ref(false)
const { copied, copyToClipboard } = useClipboard()
const confirmCancel = ref(false)
const now = ref(Date.now())
let alive = true
let timer: ReturnType<typeof setInterval> | undefined
let revision = 0
const outTradeNo = computed(() => String(route.params.outTradeNo || ''))
const networkName = computed(() => BEPUSDT_TRADE_TYPES.find(method => method.value === checkout.value?.network)?.label || checkout.value?.network)
const coinName = computed(() => checkout.value?.crypto === 'GRAM' ? 'TON' : checkout.value?.crypto)
const completed = computed(() => checkout.value?.status === 'COMPLETED')
const networkDelayed = computed(() => checkout.value?.network_status === 'delayed' && ['PENDING', 'EXPIRED'].includes(checkout.value?.status || ''))
const seconds = computed(() => Math.max(0, Math.ceil((new Date(checkout.value?.expires_at || 0).getTime() - now.value) / 1000)))
const payable = computed(() => checkout.value?.status === 'PENDING' && (seconds.value > 0 || checkout.value.confirming))
const remaining = computed(() => `${Math.floor(seconds.value / 60)}:${String(seconds.value % 60).padStart(2, '0')}`)
const statusText = computed(() => {
  const status = checkout.value?.status
  if (completed.value) return t('payment.cryptoCheckout.paid')
  if (['PAID', 'RECHARGING'].includes(status || '')) return t('payment.cryptoCheckout.fulfillment')
  if (checkout.value?.confirming) return t('payment.cryptoCheckout.confirming')
  if (status === 'CANCELLED') return t('payment.cryptoCheckout.cancelled')
  if (status === 'FAILED') return t('payment.cryptoCheckout.failed')
  if (status === 'EXPIRED' || seconds.value === 0) return t('payment.cryptoCheckout.expired')
  if (networkDelayed.value) return t('payment.cryptoCheckout.networkDelayed')
  return t('payment.cryptoCheckout.waiting')
})
async function refresh(verify = false) {
  if (checking.value || !outTradeNo.value) return
  checking.value = true
  const currentRevision = ++revision
  const currentOrder = outTradeNo.value
  try {
    if (verify) await paymentAPI.verifyOrder(currentOrder)
    const { data } = await paymentAPI.getCryptoCheckout(currentOrder)
    if (!alive || currentRevision !== revision) return
    const changedAddress = checkout.value?.address !== data.address
    checkout.value = data
    error.value = ''
    if (changedAddress) {
      const nextQR = await QRCode.toDataURL(data.address, { width: 240, margin: 2 })
      if (alive && currentRevision === revision) qr.value = nextQR
    }
  } catch (cause) { if (alive && currentRevision === revision) error.value = (cause as { message?: string }).message || t('payment.cryptoCheckout.loadError') }
  finally { if (alive && currentRevision === revision) checking.value = false }
}
async function copy(value: string) {
  if (!await copyToClipboard(value, t('payment.cryptoCheckout.copied'))) error.value = t('payment.cryptoCheckout.copyError')
}
async function cancel() {
  if (!checkout.value || checking.value) return
  checking.value = true
  try { await paymentAPI.cancelOrder(checkout.value.order_id); confirmCancel.value = false }
  catch (cause) { error.value = (cause as { message?: string }).message || t('payment.cryptoCheckout.loadError') }
  finally { checking.value = false }
  await refresh()
}
onMounted(() => {
  void refresh()
  let ticks = 0
  timer = setInterval(() => {
    now.value = Date.now()
    ticks++
    const status = checkout.value?.status || ''
    if (ticks % 5 === 0 && ['PENDING', 'PAID', 'RECHARGING'].includes(status)) void refresh(true)
    // A transfer sent before expiry can be recognized during outage recovery.
    if (ticks % 15 === 0 && status === 'EXPIRED' && now.value < new Date(checkout.value?.expires_at || 0).getTime() + 3 * 60 * 60 * 1000) void refresh(true)
  }, 1000)
})
watch(outTradeNo, () => { revision++; checking.value = false; checkout.value = null; qr.value = ''; error.value = ''; void refresh() })
onUnmounted(() => { alive = false; if (timer) clearInterval(timer) })
</script>
