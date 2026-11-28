import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountUsageCell from '../AccountUsageCell.vue'
import CNProviderBalanceCell from '../CNProviderBalanceCell.vue'
import type { Account } from '@/types'
import type { CNProviderBalanceResult } from '@/api/admin/cnProviders'

const { queryBalance, getUsage } = vi.hoisted(() => ({ queryBalance: vi.fn(), getUsage: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { cnProviders: { queryBalance }, accounts: { getUsage } } }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const value = '[data-test="cn-provider-balance-value"]'
const probe = '[data-test="cn-provider-balance-probe"]'
const stats = { requests: 1200, tokens: 75_100_000, cost: 67.83, standard_cost: 67.83, user_cost: 15.63 }
const account = (credentials: Record<string, unknown> = {}, extra: Record<string, unknown> = {}) => ({
  id: 1093, name: 'Fictional DeepSeek', platform: 'deepseek', type: 'apikey',
  credentials: { account_mode: 'payg', ...credentials }, extra
}) as Account
const result = (balance: number, available = true): CNProviderBalanceResult => ({
  provider: 'deepseek', success: true, balance, currency: 'CNY', available,
  fetched_at: 1795867200, persisted: true
})

describe('DeepSeek balance in the real usage cell', () => {
  beforeEach(() => { queryBalance.mockReset(); getUsage.mockReset() })

  it('ISSUE1093 restores official balance alongside generic local usage', async () => {
    const wrapper = mount(AccountUsageCell, { props: {
      account: account({ base_url: 'https://api.deepseek.com' }, { deepseek_balance: 12.34, deepseek_balance_currency: 'CNY' }),
      todayStats: stats
    } })
    await flushPromises()
    expect(wrapper.find(value).exists(), 'ISSUE1093_OFFICIAL_BALANCE_MISSING').toBe(true)
    expect(wrapper.get(value).text()).toBe('CNY 12.34')
    expect(wrapper.findAllComponents(CNProviderBalanceCell)).toHaveLength(1)
    for (const text of ['1.2K req', '75.1M', 'A $67.83', 'U $15.63']) expect(wrapper.text()).toContain(text)
    expect(wrapper.findAll(probe)).toHaveLength(1)
    expect(queryBalance).not.toHaveBeenCalled()
    expect(getUsage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    {}, { base_url: '' }, { base_url: ' https://api.deepseek.com/ ' },
    { base_url: 'https://api.deepseek.com/v1', api_protocol: 'responses' },
    { base_url: 'https://api.deepseek.com/anthropic', api_protocol: 'anthropic' },
    { base_url: 'https://relay.example.invalid', api_protocol: 'adaptive', api_base_urls: { chat_completions: 'https://api.deepseek.com' } }
  ])('offers explicit Query for official/default routes %j without automatic reads', async credentials => {
    const wrapper = mount(AccountUsageCell, { props: { account: account(credentials), todayStatsLoading: true } })
    await flushPromises()
    expect(wrapper.get(value).text()).toBe('admin.accounts.cnProviders.balance')
    expect(wrapper.findAll(probe)).toHaveLength(1)
    expect(wrapper.find('.animate-pulse').exists()).toBe(true)
    expect(queryBalance).not.toHaveBeenCalled()
    expect(getUsage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    { base_url: 'https://relay.example.invalid' },
    { base_url: 'https://api.deepseek.com.attacker.invalid' },
    { base_url: 'http://api.deepseek.com' }, { base_url: 'invalid URL' },
    { base_url: 'https://relay.example.invalid/anthropic', api_protocol: 'anthropic' },
    { base_url: 'https://api.deepseek.com', api_protocol: 'adaptive', api_base_urls: { chat_completions: 'https://relay.example.invalid' } },
    { account_mode: 'coding' }
  ])('keeps unsupported routes generic-only %j', async credentials => {
    const wrapper = mount(AccountUsageCell, { props: { account: account(credentials, { deepseek_balance_low: true, deepseek_balance_updated_at: '2026-11-28T12:00:00Z' }), todayStats: stats } })
    await flushPromises()
    expect(wrapper.find(value).exists()).toBe(false)
    expect(wrapper.find(probe).exists()).toBe(false)
    expect(wrapper.text()).toContain('1.2K req')
    expect(queryBalance).not.toHaveBeenCalled()
    expect(getUsage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    [{ deepseek_balance: 0, deepseek_balance_currency: 'CNY' }, 'CNY 0.00'],
    [{ deepseek_balance: 0.4, deepseek_balance_low: true }, '¥ 0.40'],
    [{ deepseek_balances: [{ currency: 'CNY', balance: 12.34 }, { currency: 'USD', balance: 0 }] }, 'CNY 12.34 · USD 0.00']
  ] as const)('retains proven custom snapshots read-only even if old %j', async (extra, label) => {
    const wrapper = mount(AccountUsageCell, { props: { account: account({ base_url: 'https://relay.example.invalid' }, { ...extra, deepseek_balance_updated_at: '2025-01-01T00:00:00Z' }), todayStats: stats } })
    await flushPromises()
    expect(wrapper.get(value).text()).toBe(label)
    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceSnapshot')
    expect(wrapper.find(probe).exists()).toBe(false)
    expect(queryBalance).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    {}, { deepseek_balance: '0' }, { deepseek_balance: Number.NaN }, { deepseek_balance: Infinity },
    { deepseek_balances: [{ currency: 'CNY', balance: '0' }, { currency: 'USD', balance: Infinity }] }
  ])('does not treat malformed or missing custom data as capability %j', extra => {
    const wrapper = mount(AccountUsageCell, { props: { account: account({ base_url: 'https://relay.example.invalid' }, extra) } })
    expect(wrapper.find(value).exists()).toBe(false)
    expect(wrapper.find(probe).exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows zero/unavailable and reactive low markers, then updates from periodic snapshots', async () => {
    const wrapper = mount(AccountUsageCell, { props: { account: account({}, { deepseek_balance: 0, deepseek_balance_available: false, deepseek_balance_low: false }) } })
    expect(wrapper.get(value).text()).toBe('¥ 0.00')
    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')
    expect(wrapper.text()).not.toContain('-')
    await wrapper.setProps({ account: account({}, { deepseek_balance: 8, deepseek_balance_available: true, deepseek_balance_low: true }) })
    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')
    await wrapper.setProps({ account: account({}, { deepseek_balance: 18, deepseek_balance_available: true, deepseek_balance_low: false }) })
    expect(wrapper.get(value).text()).toBe('¥ 18.00')
    expect(wrapper.text()).not.toContain('admin.accounts.cnProviders.balanceLow')
    expect(queryBalance).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('queries only on click and retains the last successful value on failure', async () => {
    queryBalance.mockResolvedValueOnce(result(0, false)).mockResolvedValueOnce({ success: false, error: 'Fictional HTTP 401' })
    const wrapper = mount(AccountUsageCell, { props: { account: account({}, { deepseek_balance: 12.34 }) } })
    await wrapper.get(probe).trigger('click'); await flushPromises()
    expect(queryBalance).toHaveBeenCalledTimes(1)
    expect(queryBalance).toHaveBeenCalledWith(1093)
    expect(wrapper.get(value).text()).toBe('CNY 0.00')
    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')
    await wrapper.get(probe).trigger('click'); await flushPromises()
    expect(wrapper.get(value).text()).toBe('CNY 0.00')
    expect(wrapper.text()).toContain('Fictional HTTP 401')
    wrapper.unmount()
  })

  it('drops late success and error responses after credentials or row identity changes', async () => {
    let resolve!: (value: CNProviderBalanceResult) => void
    queryBalance.mockImplementationOnce(() => new Promise<CNProviderBalanceResult>(done => { resolve = done }))
    const original = account({}, { deepseek_balance: 12.34 })
    const wrapper = mount(AccountUsageCell, { props: { account: original } })
    await wrapper.get(probe).trigger('click')
    await wrapper.setProps({ account: account({ base_url: 'https://relay.example.invalid' }, { deepseek_balance: 4 }) })
    resolve(result(999)); await flushPromises()
    expect(wrapper.get(value).text()).toBe('¥ 4.00')
    expect(wrapper.find(probe).exists()).toBe(false)
    await wrapper.setProps({ account: original })
    let reject!: (error: Error) => void
    queryBalance.mockImplementationOnce(() => new Promise((_done, fail) => { reject = fail }))
    await wrapper.get(probe).trigger('click')
    await wrapper.setProps({ account: { ...original, id: 1094, credentials: { api_key: 'fictional-replacement' } } })
    reject(new Error('old credential failure')); await flushPromises()
    expect(wrapper.text()).not.toContain('old credential failure')
    expect(wrapper.get(probe).attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('clears completed query data and errors when base URL changes', async () => {
    queryBalance.mockResolvedValueOnce(result(99)).mockResolvedValueOnce({ success: false, error: 'old endpoint error' })
    const wrapper = mount(AccountUsageCell, { props: { account: account({}, { deepseek_balance: 12.34 }) } })
    await wrapper.get(probe).trigger('click'); await flushPromises()
    await wrapper.get(probe).trigger('click'); await flushPromises()
    await wrapper.setProps({ account: account({ base_url: 'https://relay.example.invalid' }, { deepseek_balance: 4 }) })
    expect(wrapper.get(value).text()).toBe('¥ 4.00')
    expect(wrapper.text()).not.toContain('old endpoint error')
    wrapper.unmount()
  })

  it('prefers a newer periodic snapshot after a manual query', async () => {
    queryBalance.mockResolvedValueOnce(result(99))
    const wrapper = mount(AccountUsageCell, { props: { account: account({}, { deepseek_balance: 12.34 }) } })
    await wrapper.get(probe).trigger('click'); await flushPromises()
    expect(wrapper.get(value).text()).toBe('CNY 99.00')
    await wrapper.setProps({ account: account({}, { deepseek_balance: 8, deepseek_balance_currency: 'CNY', deepseek_balance_updated_at: '2099-01-01T00:00:00Z' }) })
    expect(wrapper.get(value).text()).toBe('CNY 8.00')
    expect(queryBalance).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it.each(['ollama_cloud_usage', 'opencode_go_usage'] as const)('keeps %s as the sole provider usage owner', async key => {
    const usage = key === 'ollama_cloud_usage'
      ? { eligible: true, configured: false, snapshot: { status: 'ok', data: { five_hour: { used_percent: 12 } } } }
      : { eligible: true, snapshot: { status: 'ok', data: { rolling: { percent: 12 } } } }
    const wrapper = mount(AccountUsageCell, { props: {
      account: { ...account({}, { deepseek_balance: 12.34 }), [key]: usage } as Account,
      todayStats: stats
    } })
    await flushPromises()
    expect(wrapper.find(value).exists()).toBe(false)
    expect(wrapper.findAllComponents(CNProviderBalanceCell)).toHaveLength(0)
    expect(wrapper.text()).toContain('1.2K req')
    expect(queryBalance).not.toHaveBeenCalled()
    expect(getUsage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('retains Kimi behavior and leaves ordinary OpenAI keys generic-only', async () => {
    const wrapper = mount(AccountUsageCell, { props: { account: { ...account(), platform: 'kimi', extra: { kimi_balance: 7 } } } })
    expect(wrapper.get(value).text()).toBe('¥ 7.00')
    expect(wrapper.findAll(probe)).toHaveLength(1)
    await wrapper.setProps({ account: { ...account(), platform: 'openai' }, todayStats: stats })
    expect(wrapper.find(value).exists()).toBe(false)
    expect(wrapper.text()).toContain('1.2K req')
    wrapper.unmount()
  })
})
