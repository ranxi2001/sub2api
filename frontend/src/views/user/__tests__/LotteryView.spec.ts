import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LotteryView from '../LotteryView.vue'
const { route, replace, refreshLottery, acceptPools } = vi.hoisted(() => ({
  route: { query: {} as Record<string, string> }, replace: vi.fn(), refreshLottery: vi.fn(), acceptPools: vi.fn(),
}))
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => ({ replace }) }))
vi.mock('@/stores/lottery', () => ({ useLotteryStore: () => ({ enabled: true, refresh: refreshLottery, acceptPools }) }))

const { pools, records, draw, refreshUser, fetchActiveSubscriptions } =
  vi.hoisted(() => ({
    pools: vi.fn(),
    records: vi.fn(),
    draw: vi.fn(),
    refreshUser: vi.fn(),
    fetchActiveSubscriptions: vi.fn(),
  }))
vi.mock('@/api/lottery', () => ({ lotteryAPI: { pools, records, draw } }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 7 }, refreshUser }),
}))
vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({ fetchActiveSubscriptions }),
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string) => key,
    te: () => false,
    locale: { value: 'en' },
  }),
}))

function fixture(available = 1) {
  return {
    id: 1,
    name: 'Pool',
    mode: 'cumulative',
    threshold: 100,
    grant_enabled: true,
    draw_enabled: true,
    available,
    debt: 0,
    progress: 20,
    prizes: [
      {
        id: 10,
        name: 'Credit',
        kind: 'balance',
        amount: 5,
        weight: 1,
        stock: -1,
        available: true,
        enabled: true,
        probability: 1,
      },
    ],
  }
}
function render() {
  return mount(LotteryView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /></div>',
        },
        LotteryWheel: {
          template: '<div />',
          methods: { spinTo: () => Promise.resolve() },
        },
        Icon: true,
        RouterLink: { template: '<a><slot /></a>' },
      },
    },
  })
}
function drawButton(wrapper: ReturnType<typeof render>) {
  return wrapper
    .findAll('button')
    .find((b) => /lottery\.(draw|noTickets|retryDraw|paused)/.test(b.text()))!
}

describe('LotteryView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    route.query = {}
    pools.mockResolvedValue([fixture()])
    records.mockResolvedValue({ items: [], total: 0 })
    refreshUser.mockResolvedValue({})
    draw.mockResolvedValue({
      id: 20,
      pool_id: 1,
      prize: { id: 10, name: 'Awarded credit', kind: 'balance', amount: 5 },
    })
  })
  it('disables drawing without tickets', async () => {
    pools.mockResolvedValue([fixture(0)])
    const wrapper = render()
    await flushPromises()
    expect(drawButton(wrapper).attributes('disabled')).toBeDefined()
    expect(draw).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('keeps the request key after a network error and reuses it on retry', async () => {
    draw.mockRejectedValueOnce({ message: 'Network error' })
    const wrapper = render()
    await flushPromises()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    const key = draw.mock.calls[0][1]
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBe(key)
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(draw.mock.calls[1][1]).toBe(key)
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBeNull()
    expect(wrapper.text()).toContain('Awarded credit')
    wrapper.unmount()
  })
  it('recovers a committed draw after reload even when no tickets remain', async () => {
    localStorage.setItem('sub2api.lottery.7.1', 'pending-request')
    pools.mockResolvedValue([fixture(0)])
    const wrapper = render()
    await flushPromises()
    expect(drawButton(wrapper).attributes('disabled')).toBeUndefined()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(draw).toHaveBeenCalledWith(1, 'pending-request')
    wrapper.unmount()
  })
  it('keeps the successful result when account refresh fails', async () => {
    refreshUser.mockRejectedValueOnce(new Error('Offline'))
    const wrapper = render()
    await flushPromises()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Awarded credit')
    expect(wrapper.text()).toContain('lottery.refreshWarning')
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBeNull()
    wrapper.unmount()
  })
  it('refreshes subscriptions after a subscription award', async () => {
    draw.mockResolvedValueOnce({
      id: 20,
      pool_id: 1,
      prize: {
        id: 10,
        name: 'Three days',
        kind: 'subscription',
        validity_days: 3,
      },
    })
    const wrapper = render()
    await flushPromises()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(fetchActiveSubscriptions).toHaveBeenCalledWith(true)
    expect(refreshLottery).toHaveBeenCalledWith(true)
    wrapper.unmount()
  })
  it('opens the notification pool and shows explicit selectable tabs', async () => {
    pools.mockResolvedValue([fixture(), { ...fixture(3), id: 2, name: 'Second pool' }])
    route.query = { pool: '2' }
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('#pool-tab-2').attributes('aria-selected')).toBe('true')
    expect(wrapper.get('#pool-tab-2').text()).toContain('3')
    await wrapper.get('#pool-tab-1').trigger('click')
    expect(wrapper.get('#pool-tab-1').attributes('aria-selected')).toBe('true')
    expect(replace).toHaveBeenLastCalledWith({ query: { pool: '1' } })
    await wrapper.get('#pool-tab-1').trigger('keydown', { key: 'ArrowRight' })
    expect(wrapper.get('#pool-tab-2').attributes('aria-selected')).toBe('true')
    wrapper.unmount()
  })
  it('falls back from a deleted pool link to a pool with available tickets', async () => {
    pools.mockResolvedValue([fixture(0), { ...fixture(2), id: 2 }])
    route.query = { pool: '99999' }
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('#pool-tab-2').attributes('aria-selected')).toBe('true')
    wrapper.unmount()
  })
})
