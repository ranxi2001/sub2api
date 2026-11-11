import { createApp, h, ref, onMounted } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { i18n, loadLocaleMessages } from '/src/i18n/index.ts'
import { apiClient } from '/src/api/client.ts'
import { useAuthStore } from '/src/stores/auth.ts'
import SettingsView from '/src/views/admin/SettingsView.vue'
import KeysView from '/src/views/user/KeysView.vue'
import AccountsView from '/src/views/admin/AccountsView.vue'
import AccountUsageCell from '/src/components/account/AccountUsageCell.vue'
import PricingEntryCard from '/src/components/admin/channel/PricingEntryCard.vue'
import PaymentProviderDialog from '/src/components/payment/PaymentProviderDialog.vue'
import '/src/style.css'

await loadLocaleMessages('zh')
i18n.global.locale.value = 'zh'
const scenario = new URLSearchParams(location.search).get('scenario') || 'pricing'
const state = window.__v0221 = { requests: [], saved: [], pending: [], pendingSettings: [], pendingAccounts: [], accountLists: 0, unexpected: [] }
if (scenario === 'accounts') {
  localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: false, interval_seconds: 60 }))
  localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats', 'proxy', 'notes', 'scheduler_score', 'rate_multiplier', 'upstream_billing_rate', 'performance']))
}
const usage = { antigravity_quota: { 'gemini-3-flash': { utilization: 71, reset_time: '2026-12-01T12:00:00Z' } } }
const account = { id: 9101, name: 'Synthetic quota account', platform: 'antigravity', type: 'oauth', status: 'active', schedulable: true, concurrency: 2, priority: 1, rate_multiplier: 1, credentials: {}, extra: {}, group_ids: [] }
const key = { id: 9102, name: 'Synthetic key', key: 'sk-fixture-not-a-secret', status: 'active', user_id: 41, group_id: 1, quota: 100, quota_used: 12, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z' }
apiClient.defaults.adapter = async request => {
  const path = request.url.split('?')[0]
  state.requests.push({ path, method: request.method, params: request.params })
  let data = {}
  if (request.method === 'put' || request.method === 'post') {
    let payload = request.data
    try { payload = JSON.parse(payload) } catch { /* Form data remains a synthetic string. */ }
    state.saved.push({ path, payload })
  }
  if (path === '/admin/settings' && request.method === 'put') await new Promise(resolve => state.pendingSettings.push(resolve))
  if (path === '/admin/accounts') {
    state.accountLists++
    if (state.accountLists > 1) await new Promise(resolve => state.pendingAccounts.push(resolve))
    data = { items: [{ ...account, name: state.accountLists > 1 ? 'Synthetic refreshed account' : 'Synthetic quota account' }], total: 1, page: 1, page_size: 20, pages: 1 }
  }
  else if (path === '/admin/accounts/management-capabilities') data = { concurrency_upgrade_enabled: false }
  else if (path === '/admin/accounts/upstream-billing-probe/settings') data = { enabled: false }
  else if (path === '/admin/accounts/usage/batch') data = { usage: { 9101: usage }, errors: {} }
  else if (path === '/admin/accounts/today-stats/batch') data = { stats: {} }
  else if (path === '/admin/accounts/upstream-billing-rates') data = { items: [], total: 1, page: 1, page_size: 20, pages: 1 }
  else if (path === '/admin/accounts/performance') data = {}
  else if (path === '/admin/ops/settings/metric-thresholds') data = { upstream_error_rate_percent_max: null }
  else if (path === '/admin/tls-fingerprint-profiles') data = []
  else if (/\/schedulable-timers$/.test(path)) data = { tasks: [], history: [], server_now: new Date().toISOString(), poll_seconds: 30, open_grace_seconds: 900, max_delay_minutes: 100 }
  else if (path === '/admin/settings') data = { site_name: 'Synthetic Sub2API', contact_email: 'operator@example.invalid', payment_balance_recharge_multiplier: 0.140845, balance_recharge_multiplier: 0.140845 }
  else if (path.endsWith('/beta-policy')) data = { rules: [] }
  else if (path.endsWith('/web-search-emulation')) data = { enabled: false, providers: [] }
  else if (path.endsWith('/admin-api-key')) data = { exists: false, masked_key: '' }
  else if (path.endsWith('/proxies')) data = { items: [] }
  else if (path.endsWith('/groups/all') || path.endsWith('/proxies/all') || path.endsWith('/payment/providers') || path.endsWith('/groups/available')) data = []
  else if (/\/admin\/accounts\/\d+\/usage$/.test(path)) data = request.params?.force ? { antigravity_quota: { 'gemini-3-flash': { utilization: 9, reset_time: '2026-12-02T12:00:00Z' } } } : usage
  else if (path === '/keys') data = { items: [key], total: 1, page: 1, page_size: 20, pages: 1 }
  else if (path === '/keys/concurrency') data = { queue_policy: { max_waiting: 10, timeout_seconds: 30 }, items: [{ id: 9102, current_concurrency: 0, current_waiting: 0 }] }
  else if (path.endsWith('/user-group-rates')) data = {}
  else if (path.includes('api-keys-usage')) {
    await new Promise(resolve => state.pending.push(resolve))
    data = { stats: { 9102: { api_key_id: 9102, today_actual_cost: 0.25, total_actual_cost: 3.75 } } }
  }
  return { data: { code: 0, message: 'success', data }, status: 200, statusText: 'OK', headers: {}, config: request }
}
const pinia = createPinia()
useAuthStore(pinia).user = { id: 41, role: 'admin', status: 'active', email: 'admin@example.invalid' }
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
await router.push(scenario === 'settings' ? '/admin/settings?tab=general' : scenario === 'keys' ? '/keys' : scenario === 'accounts' ? '/admin/accounts' : '/admin/channels')
await router.isReady()
const pricing = ref({ models: ['gpt-6-synthetic'], billing_mode: 'token', input_price: 2, output_price: 8, cache_write_price: 2.5, cache_write_1h_price: null, cache_write_12h_price: 5, cache_read_price: 0.2, fast_multiplier: 2, flex_multiplier: null, reasoning_effort_multipliers: null, image_input_price: null, image_output_price: null, per_request_price: null, intervals: [], time_pricing: { timezone: 'UTC', weekdays_only: false, periods: [] } })
const provider = { id: 9103, provider_key: 'easypay', name: 'Synthetic EasyPay', enabled: true, refund_enabled: true, allow_user_refund: false, payment_mode: 'qrcode', supported_types: ['alipay'], config: { apiBase: 'https://pay.example.invalid', pid: 'synthetic-merchant', pkey: 'synthetic-placeholder', refundSuccessCode: '1' }, limits: '' }
const options = [{ value: 'easypay', label: 'EasyPay' }, { value: 'alipay', label: 'Alipay' }]
const App = {
  setup() {
    const dialog = ref(null)
    onMounted(() => { if (scenario === 'payment') dialog.value.loadProvider(provider) })
    return () => scenario === 'settings' ? h(SettingsView) : scenario === 'keys' ? h(KeysView) : scenario === 'accounts' ? h(AccountsView) : h('main', { class: 'p-6 min-h-screen bg-gray-50 dark:bg-dark-900', 'data-fixture': scenario }, [
      h('h1', { class: 'mb-6 text-xl font-semibold text-gray-900 dark:text-white' }, 'Sub2API'),
      scenario === 'pricing' ? h(PricingEntryCard, { entry: pricing.value, platform: 'openai', noticeUserId: 41, onUpdate: value => { pricing.value = value; state.saved.push({ pricing: value }) } }) : null,
      scenario === 'usage' ? h(AccountUsageCell, { account, noticeUserId: 41 }) : null,
      scenario === 'payment' ? h(PaymentProviderDialog, { ref: dialog, show: true, saving: false, editing: provider, userId: 41, allKeyOptions: options, enabledKeyOptions: options, allPaymentTypes: [{ value: 'alipay', label: 'Alipay' }], redirectLabel: 'Redirect', onSave: value => state.saved.push({ provider: value }) }) : null,
    ])
  },
}
createApp(App).use(pinia).use(i18n).use(router).mount('#app')
window.__v0221Ready = true
