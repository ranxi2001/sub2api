import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CNProviderBalanceCell from '../CNProviderBalanceCell.vue'
import type { Account } from '@/types'

const { queryBalance } = vi.hoisted(() => ({
  queryBalance: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    cnProviders: { queryBalance }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const account = {
  id: 7,
  platform: 'kimi',
  type: 'apikey',
  credentials: { account_mode: 'payg' },
  extra: {
    kimi_balance: 12.5,
    kimi_balance_currency: 'CNY'
  }
} as Account

describe('CNProviderBalanceCell', () => {
  beforeEach(() => {
    queryBalance.mockReset()
  })

  it('renders the persisted balance as static text with an explicit query action', async () => {
    const wrapper = mount(CNProviderBalanceCell, { props: { account } })
    await flushPromises()

    // Snapshot value renders without any probe.
    expect(queryBalance).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="cn-provider-balance-value"]').text()).toContain('CNY 12.50')

    // The control reads as an action; the i18n mock returns the key itself.
    const probeButton = wrapper.get('[data-test="cn-provider-balance-probe"]')
    expect(probeButton.text()).toBe('admin.accounts.cnProviders.probe')

    await probeButton.trigger('click')
    await flushPromises()
    expect(queryBalance).toHaveBeenCalledWith(account.id)
  })

  it('shows the low-balance badge from the snapshot marker', () => {
    const lowAccount = {
      ...account,
      extra: { kimi_balance: 0.4, kimi_balance_low: true }
    } as Account

    const wrapper = mount(CNProviderBalanceCell, { props: { account: lowAccount } })

    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')
  })

  it('keeps the snapshot balance visible when a query fails', async () => {
    queryBalance.mockResolvedValue({ success: false, error: 'HTTP 401' })
    const wrapper = mount(CNProviderBalanceCell, { props: { account } })

    await wrapper.get('[data-test="cn-provider-balance-probe"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('CNY 12.50')
    expect(wrapper.text()).toContain('HTTP 401')
  })

  it('renders probedBalance from sibling quota probe and reflects unavailable status as low balance', () => {
    const mistralAccount = {
      id: 8,
      platform: 'mistral',
      type: 'apikey',
      credentials: { account_mode: 'payg' },
      extra: {}
    } as Account

    const wrapper = mount(CNProviderBalanceCell, {
      props: {
        account: mistralAccount,
        probedBalance: {
          provider: 'mistral',
          success: true,
          balance: 0,
          currency: 'USD',
          balances: [{ currency: 'USD', balance: 0 }],
          available: false,
          fetched_at: 1700000000,
          persisted: true
        }
      }
    })

    expect(wrapper.get('[data-test="cn-provider-balance-value"]').text()).toContain('USD 0.00')
    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')
  })

  it('updates displayed balance when a newer sibling quota probe arrives after a manual balance query', async () => {
    const mistralAccount = {
      id: 9,
      platform: 'mistral',
      type: 'apikey',
      credentials: { account_mode: 'payg' },
      extra: {}
    } as Account

    queryBalance.mockResolvedValueOnce({
      provider: 'mistral',
      success: true,
      balance: 0,
      currency: 'USD',
      balances: [{ currency: 'USD', balance: 0 }],
      available: false,
      fetched_at: 1700000000,
      persisted: true
    })

    const wrapper = mount(CNProviderBalanceCell, {
      props: { account: mistralAccount, probedBalance: null }
    })

    await wrapper.get('[data-test="cn-provider-balance-probe"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="cn-provider-balance-value"]').text()).toContain('USD 0.00')
    expect(wrapper.text()).toContain('admin.accounts.cnProviders.balanceLow')

    await wrapper.setProps({
      probedBalance: {
        provider: 'mistral',
        success: true,
        balance: 25.5,
        currency: 'USD',
        balances: [{ currency: 'USD', balance: 25.5 }],
        available: true,
        fetched_at: 1700000010,
        persisted: true
      }
    })
    await flushPromises()

    expect(wrapper.get('[data-test="cn-provider-balance-value"]').text()).toContain('USD 25.50')
    expect(wrapper.text()).not.toContain('admin.accounts.cnProviders.balanceLow')
  })
})
