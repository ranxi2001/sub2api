import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import AccountsView from '../AccountsView.vue'

const { list, getConfig, saveConfig, probe, auth } = vi.hoisted(() => ({ list: vi.fn(), getConfig: vi.fn(), saveConfig: vi.fn(), probe: vi.fn(), auth: { user: { id: 42 }, token: 'test-token', isAdmin: true, isSimpleMode: false } }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  accounts: {
    getManagementCapabilities: vi.fn().mockResolvedValue({ concurrency_upgrade_enabled: false }), list,
    getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
    getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
    probeUpstreamBilling: probe,
    getUpstreamBillingRatesWithEtag: vi.fn().mockResolvedValue({ notModified: true, data: null })
  }, proxies: { getAll: vi.fn().mockResolvedValue([]) }, groups: { getAll: vi.fn().mockResolvedValue([]) }
} }))
vi.mock('@/api/admin/accounts', async () => ({ ...await vi.importActual('@/api/admin/accounts'), getNewAPIUpstreamConfig: getConfig, saveNewAPIUpstreamConfig: saveConfig }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
const row = { id: 7, name: 'current', platform: 'openai', type: 'apikey', status: 'active', concurrency: 1, priority: 1, group_ids: [], credentials: { base_url: 'https://upstream.example' }, extra: { upstream_billing_probe: { status: 'unsupported', last_attempt_at: '', next_probe_at: '' } } }
let wrapper: VueWrapper | undefined
const open = async () => {
  wrapper = mount(AccountsView, { global: { plugins: [createPinia()], stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="table" /></div>' },
    DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-upstream_billing_rate" :row="row" /></div></div>' },
    AccountTableActions: true, AccountTableFilters: true, AccountBulkActionsBar: true, Pagination: true,
    ConfirmDialog: true, AccountActionMenu: true, ImportDataModal: true, ReAuthAccountModal: true,
    AccountTestModal: true, AccountStatsModal: true, IQTestModal: true, ScheduledTestsPanel: true,
    SyncFromCrsModal: true, TempUnschedStatusModal: true, ErrorPassthroughRulesModal: true,
    TLSFingerprintProfilesModal: true, CreateAccountModal: true, EditAccountModal: true,
    BulkEditAccountModal: true, TotpStepUpDialog: true, Teleport: true
  } } })
  await flushPromises()
  return wrapper
}
beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  auth.isAdmin = true
  list.mockResolvedValue({ items: [row], total: 1, page: 1, page_size: 20, pages: 1 })
  getConfig.mockResolvedValue({ account_id: 7, site_url: 'https://upstream.example', configured: true, user_id: 42, encryption_key_configured: true, accounts: [{ account_id: 7, name: 'current', configured_user_id: 42 }] })
  saveConfig.mockResolvedValue({ site_url: 'https://upstream.example', user_id: 42, wallet: { amount: 12.34, unit: 'USD' }, accounts: [{ account_id: 7, name: 'current', matched: true, token_id: 101, group: 'default', rate: 1, token_options: [] }] })
  probe.mockResolvedValue({ account_id: 7 })
})
afterEach(() => wrapper?.unmount())

describe('account page New API configuration', () => {
  it('opens the dialog for the clicked account and refreshes list bindings after saving', async () => {
    const wrapper = await open()
    await wrapper.get('[data-testid="upstream-billing-configure"]').trigger('click')
    await flushPromises()
    expect(getConfig).toHaveBeenCalledWith(7)
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-user-id"]').element.value).toBe('42')
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    await flushPromises()
    expect(saveConfig).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7] })
    expect(list).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="new-api-user-id"]').exists()).toBe(false)
  })

  it('leaves observers able to refresh without offering shared credentials', async () => {
    auth.isAdmin = false
    const wrapper = await open()
    expect(wrapper.find('[data-testid="upstream-billing-configure"]').exists()).toBe(false)
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    await flushPromises()
    expect(probe).toHaveBeenCalledWith(7)
    expect(getConfig).not.toHaveBeenCalled()
  })
})
