<template>
  <details class="card" @toggle="onToggle">
    <summary class="cursor-pointer px-6 py-4 font-medium text-gray-900 dark:text-white">
      {{ t(`${prefix}.collectionTitle`) }}
    </summary>
    <div class="space-y-6 border-t border-gray-100 p-6 dark:border-dark-700">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t(`${prefix}.collectionHint`) }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="busy" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
      <template v-if="loaded">
        <section class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="font-medium">{{ t(`${prefix}.wallets`) }}</h3>
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="resetWallet">{{ t(`${prefix}.addWallet`) }}</button>
          </div>
          <div v-for="wallet in wallets" :key="wallet.id" class="flex flex-wrap items-center gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <div class="min-w-0 flex-1">
              <p class="font-medium">{{ wallet.name || networkName(wallet.trade_type) }} · {{ networkName(wallet.trade_type) }} · {{ t(wallet.status === 1 ? 'common.enabled' : 'common.disabled') }}</p>
              <p class="break-all font-mono text-xs text-gray-500">{{ wallet.address }}</p>
            </div>
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="editWallet(wallet)">{{ t('common.edit') }}</button>
            <button type="button" class="btn btn-secondary text-red-600" :disabled="busy" @click="deleteTarget = wallet.id">{{ t('common.delete') }}</button>
            <div v-if="deleteTarget === wallet.id" class="flex basis-full flex-wrap items-center gap-2">
              <span class="text-sm">{{ t(`${prefix}.deleteWalletConfirm`) }}</span>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="run(() => bepusdtAPI.deleteWallet(wallet.id))">{{ t('common.confirm') }}</button>
              <button type="button" class="btn btn-secondary" @click="deleteTarget = null">{{ t('common.cancel') }}</button>
            </div>
          </div>
          <p v-if="wallets.length === 0" class="text-sm text-gray-500">{{ t(`${prefix}.noWallets`) }}</p>
          <div class="grid gap-3 rounded-lg bg-gray-50 p-4 dark:bg-dark-800 md:grid-cols-2">
            <div><label for="bepusdt-wallet-name" class="input-label">{{ t(`${prefix}.walletName`) }}</label><input id="bepusdt-wallet-name" v-model="walletForm.name" class="input" :disabled="busy" /></div>
            <div><label for="bepusdt-wallet-network" class="input-label">{{ t(`${prefix}.walletNetwork`) }}</label><select id="bepusdt-wallet-network" v-model="walletForm.trade_type" class="input" :disabled="busy"><option v-for="network in BEPUSDT_TRADE_TYPES" :key="network.value" :value="network.value">{{ network.label }}</option></select></div>
            <div class="md:col-span-2"><label for="bepusdt-wallet-address" class="input-label">{{ t(`${prefix}.walletAddress`) }}</label><input id="bepusdt-wallet-address" v-model="walletForm.address" class="input font-mono" :disabled="busy" autocomplete="off" /></div>
            <div><label for="bepusdt-wallet-remark" class="input-label">{{ t(`${prefix}.walletRemark`) }}</label><input id="bepusdt-wallet-remark" v-model="walletForm.remark" class="input" :disabled="busy" /></div>
            <label class="flex items-center gap-2"><input v-model="walletEnabled" type="checkbox" :disabled="busy" />{{ t('common.enabled') }}</label>
            <div class="flex gap-2 md:col-span-2">
              <button type="button" class="btn btn-primary" :disabled="busy || !walletForm.address.trim()" @click="saveWallet">{{ t('common.save') }}</button>
              <button v-if="walletForm.id" type="button" class="btn btn-secondary" @click="resetWallet">{{ t('common.cancel') }}</button>
            </div>
          </div>
        </section>
        <section class="space-y-3 border-t border-gray-200 pt-5 dark:border-dark-600">
          <h3 class="font-medium">{{ t(`${prefix}.nodes`) }}</h3>
          <p class="text-xs text-gray-500">{{ t(`${prefix}.nodesHint`) }}</p>
          <div class="grid gap-3 md:grid-cols-2">
            <div v-for="node in nodes" :key="node.key">
              <label class="input-label">{{ node.label }}</label><input v-model="settings[node.key]" class="input font-mono text-xs" :disabled="busy" />
            </div>
            <div v-for="secret in secrets" :key="secret.key">
              <label class="input-label">{{ secret.label }}</label><input v-model="settings[secret.key]" type="password" class="input" :disabled="busy" autocomplete="new-password" :placeholder="secretConfigured[secret.key] ? t('admin.accounts.leaveEmptyToKeep') : ''" />
            </div>
            <div><label class="input-label">{{ t(`${prefix}.rateSource`) }}</label><input v-model="settings.rate_sync_coingecko_api_url" class="input" :disabled="busy" /></div>
            <div><label class="input-label">{{ t(`${prefix}.rateInterval`) }}</label><input v-model="settings.rate_sync_interval" type="number" min="60" max="86400" class="input" :disabled="busy" /></div>
          </div>
          <button type="button" class="btn btn-primary" :disabled="busy" @click="run(() => bepusdtAPI.saveSettings(settings))">{{ t('common.save') }}</button>
        </section>
        <section class="space-y-3 border-t border-gray-200 pt-5 dark:border-dark-600">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="font-medium">{{ t(`${prefix}.scanStatus`) }}</h3>
            <button type="button" class="btn btn-secondary" :disabled="busy" data-testid="check-payment-nodes" @click="checkNodes">{{ t(`${prefix}.checkNodes`) }}</button>
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="run()">{{ t('common.refresh') }}</button>
          </div>
          <p class="text-xs text-gray-500">{{ t(`${prefix}.scanHint`) }}</p>
          <p v-if="Object.keys(stats).length === 0" class="text-sm text-gray-500">{{ t(`${prefix}.scanIdle`) }}</p>
          <div v-for="(stat, network) in stats" :key="network" class="flex flex-wrap gap-x-5 gap-y-1 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
            <span class="font-medium">{{ network }}</span>
            <span>{{ t(`${prefix}.scanBlock`) }}: {{ stat.block || '—' }}</span>
            <span v-if="stat.head">{{ t(`${prefix}.scanHead`) }}: {{ stat.head }}</span>
            <span>{{ t(`${prefix}.scanSuccess`) }}: {{ stat.succ }}</span>
            <span>{{ stat.time ? new Date(stat.time * 1000).toLocaleString() : t(`${prefix}.scanNeverSucceeded`) }}</span>
            <span v-if="stat.endpoint">{{ stat.endpoint }} <span v-if="stat.failover" class="text-amber-600">{{ t(`${prefix}.usingFallback`) }}</span></span>
            <p v-if="stat.status === 'error'" role="alert" class="basis-full text-red-600">{{ t(`${prefix}.scanRetrying`) }}: {{ stat.error }}</p>
          </div>
          <div v-for="(check, network) in nodeChecks" :key="network" class="rounded-lg border p-3 text-sm" :class="check.status === 'healthy' ? 'border-green-200 text-green-700 dark:border-green-800 dark:text-green-400' : 'border-red-200 text-red-600 dark:border-red-800'">
            {{ network }} · {{ t(`${prefix}.${check.status === 'healthy' ? 'nodeHealthy' : 'nodeUnavailable'}`) }} · {{ new Date(check.checked_at * 1000).toLocaleString() }}
            <p v-if="check.error" class="mt-1 break-words">{{ check.error }}</p>
          </div>
        </section>
        <section class="space-y-3 border-t border-gray-200 pt-5 dark:border-dark-600">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="font-medium">{{ t(`${prefix}.rates`) }}</h3><button type="button" class="btn btn-secondary" :disabled="busy" @click="run(bepusdtAPI.syncRates)">{{ t(`${prefix}.syncRates`) }}</button>
          </div>
          <p class="text-xs text-gray-500">{{ t(`${prefix}.ratesHint`) }}</p>
          <div class="grid gap-2 sm:grid-cols-3"><p v-for="rate in latestRates" :key="`${rate.crypto}:${rate.fiat}`" class="text-sm">1 {{ rate.crypto }} = {{ rate.rate }} {{ rate.fiat }}</p></div>
          <div class="flex flex-wrap items-end gap-3">
            <div><label class="input-label">{{ t(`${prefix}.coin`) }}</label><select v-model="rateForm.crypto" class="input"><option v-for="coin in ['USDT', 'USDC', 'TRX', 'ETH', 'BNB', 'GRAM']" :key="coin">{{ coin }}</option></select></div>
            <div><label class="input-label">{{ t(`${prefix}.fiat`) }}</label><select v-model="rateForm.fiat" class="input"><option v-for="fiat in ['CNY', 'USD', 'EUR', 'GBP', 'JPY']" :key="fiat">{{ fiat }}</option></select></div>
            <div><label class="input-label">{{ t(`${prefix}.manualRate`) }}</label><input v-model="rateForm.rate" type="number" min="0" step="any" class="input" /></div>
            <button type="button" class="btn btn-secondary" :disabled="busy || Number(rateForm.rate) <= 0" @click="run(() => bepusdtAPI.saveRate(rateForm.fiat, rateForm.crypto, rateForm.rate))">{{ t('common.save') }}</button>
          </div>
        </section>
      </template>
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { bepusdtAPI, type CollectionConfig, type CryptoWallet, type CryptoRate } from '@/api/admin/bepusdt'
import { BEPUSDT_TRADE_TYPES } from './providerConfig'
const { t } = useI18n()
const prefix = 'admin.settings.payment'
const loaded = ref(false)
const busy = ref(false)
const error = ref('')
const settings = reactive<Record<string, string>>({})
const secretConfigured = ref<Record<string, boolean>>({})
const wallets = ref<CryptoWallet[]>([])
const rates = ref<CryptoRate[]>([])
const stats = ref<CollectionConfig['stats']>({})
const nodeChecks = ref<NonNullable<CollectionConfig['node_checks']>>({})
const deleteTarget = ref<number | null>(null)
const emptyWallet = (): CryptoWallet => ({ id: 0, name: '', address: '', trade_type: 'usdt.trc20', status: 1, remark: '' })
const walletForm = reactive(emptyWallet())
const walletEnabled = computed({ get: () => walletForm.status === 1, set: (value: boolean) => { walletForm.status = value ? 1 : 0 } })
const rateForm = reactive({ fiat: 'CNY', crypto: 'USDT', rate: '' })
const nodes = [
  { key: 'rpc_endpoint_tron', label: 'Tron gRPC' },
  ...[['ethereum', 'Ethereum'], ['bsc', 'BSC'], ['polygon', 'Polygon'], ['solana', 'Solana'], ['aptos', 'Aptos'], ['arbitrum', 'Arbitrum'], ['xlayer', 'X-Layer'], ['base', 'Base'], ['plasma', 'Plasma']].map(([key, label]) => ({ key: `rpc_endpoint_${key}`, label: `${label} RPC` })),
  { key: 'rpc_global_config_url_ton', label: 'TON Global Config' },
]
const secrets = [{ key: 'rpc_endpoint_tron_grid_api_key', label: 'TronGrid API Key' }, { key: 'rate_sync_coingecko_api_key', label: 'CoinGecko API Key' }]
const latestRates = computed(() => {
  const seen = new Set<string>()
  return rates.value.filter(rate => { const key = `${rate.crypto}:${rate.fiat}`; if (seen.has(key)) return false; seen.add(key); return true })
})
function networkName(value: string) { return BEPUSDT_TRADE_TYPES.find(network => network.value === value)?.label || value }
function resetWallet() { Object.assign(walletForm, emptyWallet()) }
function editWallet(wallet: CryptoWallet) { Object.assign(walletForm, wallet) }
async function load() {
  const { data } = await bepusdtAPI.get()
  Object.keys(settings).forEach(key => delete settings[key])
  Object.assign(settings, data.settings)
  secretConfigured.value = data.secret_configured
  wallets.value = data.wallets || []
  rates.value = data.rates || []
  stats.value = data.stats || {}
  nodeChecks.value = data.node_checks || {}
  loaded.value = true
}
async function checkNodes() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try { const { data } = await bepusdtAPI.checkNodes(); nodeChecks.value = data.node_checks }
  catch (cause) { error.value = (cause as { message?: string }).message || t(`${prefix}.collectionError`) }
  finally { busy.value = false }
}
async function run(action?: () => Promise<unknown>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try { if (action) await action(); await load(); deleteTarget.value = null }
  catch (cause) { error.value = (cause as { message?: string }).message || t(`${prefix}.collectionError`) }
  finally { busy.value = false }
}
async function saveWallet() { await run(() => bepusdtAPI.saveWallet({ ...walletForm })); if (!error.value) resetWallet() }
function onToggle(event: Event) { if ((event.target as HTMLDetailsElement).open && !loaded.value) void run() }
</script>
