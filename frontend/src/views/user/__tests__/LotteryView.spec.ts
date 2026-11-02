import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LotteryView from '../LotteryView.vue'
const { route, replace, refreshLottery, acceptPools } = vi.hoisted(() => ({
  route: { query: {} as Record<string, string> }, replace: vi.fn(), refreshLottery: vi.fn(), acceptPools: vi.fn(),
}))
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => ({ replace }) }))
vi.mock('@/stores/lottery', () => ({ useLotteryStore: () => ({ enabled: true, refresh: refreshLottery, acceptPools }) }))

const { pools, records, draw, refreshUser, fetchActiveSubscriptions, spinTo } =
  vi.hoisted(() => ({
    pools: vi.fn(),
    records: vi.fn(),
    draw: vi.fn(),
    refreshUser: vi.fn(),
    fetchActiveSubscriptions: vi.fn(),
    spinTo: vi.fn(),
  }))
vi.mock('@/api/lottery', async () => ({ ...(await vi.importActual<typeof import('@/api/lottery')>('@/api/lottery')), lotteryAPI: { pools, records, draw } }))
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
        LotteryReel: {
          template: '<div />',
          methods: { spinTo },
        },
        Icon: true,
        RouterLink: { template: '<a><slot /></a>' },
      },
    },
  })
}
function drawButton(wrapper: ReturnType<typeof render>) {
  return wrapper.get('[data-testid="lottery-draw"]')
}

describe('LotteryView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    route.query = {}
    pools.mockResolvedValue([fixture()])
    records.mockResolvedValue({ items: [], total: 0 })
    refreshUser.mockResolvedValue({})
    spinTo.mockResolvedValue(undefined)
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
  it('draws three times with separate keys and animates the actual snapshots', async () => {
    pools.mockResolvedValue([fixture(5)])
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[value="3"]').setValue()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(draw).toHaveBeenCalledTimes(3)
    expect(new Set(draw.mock.calls.map(call => call[1])).size).toBe(3)
    expect(spinTo).toHaveBeenCalledWith(Array(3).fill(expect.objectContaining({ name: 'Awarded credit' })))
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBeNull()
    wrapper.unmount()
  })
  it('disables counts exceeding the available chances', async () => {
    pools.mockResolvedValue([fixture(3)])
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('input[value="3"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('input[value="5"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
  it('replays the same complete batch after a partially committed network failure and reload', async () => {
    pools.mockResolvedValue([fixture(3)])
    draw.mockResolvedValueOnce({ id: 20, prize: { id: 10, name: 'First', kind: 'text' } })
      .mockRejectedValueOnce({ message: 'Offline' })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[value="3"]').setValue()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    const stored = JSON.parse(localStorage.getItem('sub2api.lottery.7.1')!)
    expect(draw).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('lottery.batchPartial')
    wrapper.unmount()
    pools.mockResolvedValue([fixture(0)])
    const reloaded = render()
    await flushPromises()
    expect(drawButton(reloaded).attributes('disabled')).toBeUndefined()
    await drawButton(reloaded).trigger('click')
    await flushPromises()
    expect(draw.mock.calls.slice(2).map(call => call[1])).toEqual(stored.keys)
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBeNull()
    reloaded.unmount()
  })
  it('keeps confirmed prizes and stops the rest on exhausted inventory', async () => {
    pools.mockResolvedValue([fixture(5)])
    draw.mockResolvedValueOnce({ id: 20, prize: { id: 10, name: 'Last item', kind: 'text' } })
      .mockRejectedValueOnce({ status: 409, message: 'Empty' })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[value="5"]').setValue()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(draw).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('Last item')
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBeNull()
    wrapper.unmount()
  })
  it('blocks additional draws and pool changes until animation completes', async () => {
    pools.mockResolvedValue([fixture(), { ...fixture(), id: 2, name: 'Another pool' }])
    let finish!: () => void
    spinTo.mockImplementationOnce(() => new Promise<void>(resolve => { finish = resolve }))
    const wrapper = render()
    await flushPromises()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.get('#pool-tab-1').attributes('disabled')).toBeDefined()
    expect(drawButton(wrapper).attributes('disabled')).toBeDefined()
    expect(localStorage.getItem('sub2api.lottery.7.1')).not.toBeNull()
    finish()
    await flushPromises()
    expect(localStorage.getItem('sub2api.lottery.7.1')).toBeNull()
    wrapper.unmount()
  })
  it('does not start more requests after leaving a batch in flight', async () => {
    let resolveDraw!: (value: unknown) => void
    pools.mockResolvedValue([fixture(5)])
    draw.mockImplementationOnce(() => new Promise(resolve => { resolveDraw = resolve }))
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[value="5"]').setValue()
    await drawButton(wrapper).trigger('click')
    wrapper.unmount()
    resolveDraw({ id: 20, prize: { id: 10, kind: 'text' } })
    await flushPromises()
    expect(draw).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('sub2api.lottery.7.1')).not.toBeNull()
  })
  it('does not send a draw if its retry key cannot be saved', async () => {
    const wrapper = render()
    await flushPromises()
    const save = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Storage full') })
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(draw).not.toHaveBeenCalled()
    save.mockRestore()
    wrapper.unmount()
  })
  it('uses a single pool heading without a redundant selector and collapses rules', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.find('[role="tablist"]').exists()).toBe(false)
    expect(wrapper.get('#current-pool-title').text()).toBe('Pool')
    expect(wrapper.get('#lottery-pool-panel').attributes('aria-labelledby')).toBe('current-pool-title')
    expect(wrapper.get('.lottery-rules').attributes('open')).toBeUndefined()
    wrapper.unmount()
  })
  it('totals only confirmed balance rewards and preserves complete text awards', async () => {
    pools.mockResolvedValue([fixture(5)])
    draw.mockResolvedValueOnce({ id: 21, prize: { id: 10, name: 'Small credit', kind: 'balance', amount: 0.1 } })
      .mockResolvedValueOnce({ id: 22, prize: { id: 10, name: 'Another credit', kind: 'balance', amount: 0.2 } })
      .mockResolvedValueOnce({ id: 23, prize: { id: 11, name: 'A gift', kind: 'text', text: 'First line\nFull reward details' } })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('input[value="3"]').setValue()
    await drawButton(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="result-balance-total"]').text()).toBe('+0.3USD')
    expect(wrapper.findAll('.result-item')).toHaveLength(3)
    expect(wrapper.get('.result-text').text()).toBe('First line\nFull reward details')
    wrapper.unmount()
  })
  it('opens the saved prize snapshot from mobile history', async () => {
    records.mockResolvedValue({ items: [{ id: 80, created_at: '2026-11-02T10:00:00Z', prize: { name: 'Historical gift', kind: 'text', pool_name: 'Archived pool', text: 'Original saved content' } }], total: 1 })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('.history-record').trigger('click')
    expect(wrapper.get('.result-text').text()).toBe('Original saved content')
    wrapper.unmount()
  })
})
