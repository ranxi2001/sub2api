// Copied unchanged into each complete archived frontend. No component replacements.
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { i18n, loadLocaleMessages } from '/src/i18n/index.ts'
import { apiClient } from '/src/api/client.ts'
import { useAuthStore } from '/src/stores/auth.ts'
import { useAppStore } from '/src/stores/app.ts'
import CreateAccountModal from '/src/components/account/CreateAccountModal.vue'
import EditAccountModal from '/src/components/account/EditAccountModal.vue'
import HeaderOverrideEditor from '/src/components/account/HeaderOverrideEditor.vue'
import '/src/style.css'

const params = new URLSearchParams(location.search)
const locale = params.get('locale') === 'en' ? 'en' : 'zh'
const after = params.get('mode') === 'after'
await loadLocaleMessages(locale)
i18n.global.locale.value = locale
document.documentElement.lang = locale
localStorage.setItem('sub2api_locale', locale)
const t = i18n.global.t
const fixture = window.__pr1050 = { requests: [], previews: [], unexpected: [], ready: false,
  labels: { create: t('admin.accounts.createAccount'), edit: t('admin.accounts.editAccount'),
    baseURL: t('admin.accounts.baseUrl'), header: t('admin.accounts.headerOverride.title'),
    // The baseline does not have these translations or controls.
    sync: after ? t('admin.accounts.upstreamBilling.syncGroup') : null },
}
const account = {
  id: 1050, name: 'Fixture local group', notes: 'Fixture local notes', platform: 'openai', type: 'apikey',
  status: 'active', schedulable: true, concurrency: 2, priority: 1, rate_multiplier: 1,
  group_ids: [], groups: [], proxy_id: null,
  credentials: { base_url: 'https://relay.example.invalid/v1', api_key: 'synthetic-fixture-key',
    header_override_enabled: true, header_overrides: { 'x-demo-tag': 'fixture-' } },
  extra: { upstream_billing_probe_enabled: false, upstream_billing_group_sync_enabled: false },
  created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
}
apiClient.defaults.adapter = async request => {
  const path = request.url, method = request.method?.toLowerCase()
  fixture.requests.push({ path, method })
  let data
  if (method === 'get' && path === '/admin/accounts/management-capabilities') {
    data = { concurrency_upgrade_enabled: false }
  } else if (method === 'get' && path === '/admin/tls-fingerprint-profiles') {
    data = []
  } else if (method === 'get' && path === '/admin/accounts/openai-request-timezones') {
    data = { default: 'Asia/Singapore', timezones: ['Asia/Singapore', 'UTC'] }
  } else if (method === 'get' && path === '/admin/accounts/antigravity/default-model-mapping') {
    data = { 'fixture-antigravity-source': 'fixture-antigravity-target' }
  } else if (method === 'post' && path === '/admin/accounts/upstream-billing-probe/group-preview') {
    const body = typeof request.data === 'string' ? JSON.parse(request.data) : request.data
    const byID = Object.keys(body).length === 1 && body.account_id === 1050
    const byFields = Object.keys(body).sort().join(',') === 'api_key,base_url,platform,proxy_id' &&
      body.platform === 'openai' && body.base_url === account.credentials.base_url &&
      body.api_key === account.credentials.api_key && body.proxy_id === null
    if (!byID && !byFields) {
      fixture.unexpected.push({ path, reason: 'Unexpected preview source' })
      throw Error('Unexpected synthetic group preview source')
    }
    fixture.previews.push(structuredClone(body))
    data = { group_name: 'Fixture upstream group', group_description: 'Fixture upstream notes',
      sync_guard: { source: 'synthetic-fixture-source', name: account.name, notes: account.notes } }
  } else {
    fixture.unexpected.push({ path, method })
    throw Error(`Unexpected fixture endpoint: ${method} ${path}`)
  }
  return { data: { code: 0, message: 'success', data }, status: 200, statusText: 'OK', headers: {}, config: request }
}
const pinia = createPinia()
useAuthStore(pinia).user = { id: 1050, role: 'admin', status: 'active', username: 'Fixture administrator', email: 'fixture-admin@example.invalid' }
useAppStore(pinia).cachedPublicSettings = { excel_bps_enabled: false, prism_browser_enabled: false }
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
await router.push('/admin/accounts')
await router.isReady()
createApp({
  setup() {
    const edit = ref(false), create = ref(false), rows = ref([{ name: 'x-demo-tag', value: 'fixture-' }])
    return () => h('main', { class: 'mx-auto max-w-4xl space-y-5 p-4 sm:p-6' }, [
      h('h1', { class: 'text-xl font-semibold' }, 'PR1050 · ' + (locale === 'zh' ? '虚构管理员组件验收' : 'Synthetic administrator component acceptance')),
      h('div', { class: 'flex flex-wrap gap-3' }, [
        h('button', { class: 'btn btn-primary', 'data-testid': 'fixture-open-edit', onClick: () => { edit.value = true } }, t('admin.accounts.editAccount')),
        h('button', { class: 'btn btn-secondary', 'data-testid': 'fixture-open-create', onClick: () => { create.value = true } }, t('admin.accounts.createAccount')),
      ]),
      h('section', { class: 'space-y-3 rounded-lg border bg-white p-4', 'data-testid': 'fixture-header' }, [
        h('h2', { class: 'font-semibold' }, fixture.labels.header),
        h(HeaderOverrideEditor, { rows: rows.value, ...(after ? { userId: 1050, allowVariables: true } : {}), 'onUpdate:rows': value => { rows.value = value } }),
      ]),
      h(EditAccountModal, { show: edit.value, account, proxies: [], groups: [], onClose: () => { edit.value = false } }),
      h(CreateAccountModal, { show: create.value, proxies: [], groups: [], onClose: () => { create.value = false } }),
    ])
  },
}).use(pinia).use(i18n).use(router).mount('#app')
fixture.ready = true
