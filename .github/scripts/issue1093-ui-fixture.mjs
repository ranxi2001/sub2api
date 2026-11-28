// CI-only component preview. These wrappers label fictional cases; all balance
// and usage content and controls are rendered by the archived real components.
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { i18n, initI18n } from '/src/i18n/index.ts'
import { apiClient } from '/src/api/client.ts'
import AccountUsageCell from '/src/components/account/AccountUsageCell.vue'
import '/src/style.css'

await initI18n()
document.documentElement.classList.toggle('dark', new URLSearchParams(location.search).get('theme') === 'dark')
const stats = { requests: 1200, tokens: 75100000, cost: 67.83, standard_cost: 67.83, user_cost: 15.63 }
const rows = ref([
  ['official', 'Official HTTPS · saved balance', {}, { deepseek_balance: 12.34, deepseek_balance_currency: 'CNY' }],
  ['zero', 'Official HTTPS · zero / unavailable', {}, { deepseek_balance: 0, deepseek_balance_currency: 'CNY', deepseek_balance_available: false, deepseek_balance_low: false }],
  ['low', 'Official HTTPS · reactive low marker', {}, { deepseek_balance: 0.4, deepseek_balance_currency: 'CNY', deepseek_balance_low: true }],
  ['missing', 'Official HTTPS · no snapshot yet', {}, {}],
  ['custom', 'Custom URL · historical supported snapshot', { base_url: 'https://supported.example.invalid' }, { deepseek_balances: [{ currency: 'CNY', balance: 4 }, { currency: 'USD', balance: 0 }], deepseek_balance_updated_at: '2025-01-01T00:00:00Z' }],
  ['unsupported', 'Custom URL · no balance capability', { base_url: 'https://unsupported.example.invalid' }, {}],
  ['adaptive', 'Adaptive · official Chat Completions route', { api_protocol: 'adaptive', base_url: 'https://relay.example.invalid', api_base_urls: { chat_completions: 'https://api.deepseek.com' } }, { deepseek_balance: 8, deepseek_balance_currency: 'CNY' }],
  ['openai', 'OpenAI Key · existing local usage', {}, {}]
].map(([key, label, credentials, extra], index) => ({
  key, label, account: { id: 10930 + index, name: 'Fictional account ' + index,
    platform: key === 'openai' ? 'openai' : 'deepseek', type: 'apikey',
    credentials: { account_mode: 'payg', ...credentials }, extra }
})))
const state = window.__issue1093 = { ready: false, requests: [], unexpected: [], queryFailure: false }
window.__issue1093ReplaceOfficial = () => {
  rows.value[0].account = { ...rows.value[0].account,
    credentials: { account_mode: 'payg', base_url: 'https://replacement.example.invalid' },
    extra: { deepseek_balance: 4, deepseek_balance_currency: 'CNY' } }
}
apiClient.defaults.adapter = async config => {
  const path = config.url, method = config.method || 'get'
  state.requests.push({ path, method })
  if (method !== 'get' || path !== '/admin/cn-providers/accounts/10930/balance') {
    state.unexpected.push({ path, method })
    throw new Error('Unexpected fixture endpoint')
  }
  const data = state.queryFailure
    ? { provider: 'deepseek', success: false, error: 'Fictional HTTP 401' }
    : { provider: 'deepseek', success: true, balance: 0, currency: 'CNY', available: false, fetched_at: 1795867200, persisted: true }
  return { config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, data } }
}
createApp({ setup: () => () => h('main', { class: 'p-4 space-y-3' }, [
  h('p', { class: 'text-sm text-gray-500 dark:text-gray-400' }, 'Issue1093 · fictional data · real AccountUsageCell / CNProviderBalanceCell'),
  ...rows.value.map(row => h('section', { class: 'card p-3 space-y-2', 'data-case': row.key, key: row.key }, [
    h('p', { class: 'text-xs font-medium text-gray-700 dark:text-gray-300' }, row.label),
    h(AccountUsageCell, { account: row.account, todayStats: stats })
  ]))
]) }).use(createPinia()).use(i18n).mount('#app')
state.ready = true
