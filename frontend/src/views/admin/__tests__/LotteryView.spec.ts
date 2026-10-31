import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LotteryView from '../LotteryView.vue'
const { settings, saveSettings, deletePool, deletePrize, refresh } = vi.hoisted(() => ({
  settings: vi.fn(), saveSettings: vi.fn(), deletePool: vi.fn(), deletePrize: vi.fn(), refresh: vi.fn(),
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ fetchPublicSettings: vi.fn(), cachedPublicSettings: {} }) }))
vi.mock('@/stores/lottery', () => ({ useLotteryStore: () => ({ refresh }) }))

const { pools, savePool, savePrize, records, ledger, getPlans } = vi.hoisted(
  () => ({
    pools: vi.fn(),
    savePool: vi.fn(),
    savePrize: vi.fn(),
    records: vi.fn(),
    ledger: vi.fn(),
    getPlans: vi.fn(),
  }),
)
vi.mock('@/api/lottery', () => ({
  adminLotteryAPI: { pools, savePool, savePrize, records, ledger, settings, saveSettings, deletePool, deletePrize },
}))
vi.mock('@/api/admin/payment', () => ({
  default: { getPlans },
  adminPaymentAPI: { getPlans },
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string) => key,
    te: () => false,
    locale: { value: 'en' },
  }),
}))

const prize = {
  id: 10,
  name: 'Credit',
  kind: 'balance',
  amount: 5,
  weight: 1,
  stock: 2,
  enabled: true,
  available: true,
  probability: 1,
  updated_at: '2026-01-01T00:00:00Z',
}
const pool = {
  id: 1,
  name: 'Pool',
  mode: 'cumulative',
  threshold: 100,
  rule_locked: true,
  grant_enabled: true,
  draw_enabled: true,
  prizes: [prize],
}
function render() {
  return mount(LotteryView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        Icon: true,
      },
    },
  })
}

describe('Lottery admin', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    settings.mockResolvedValue({ enabled: false })
    saveSettings.mockResolvedValue({ enabled: true })
    deletePool.mockResolvedValue({})
    deletePrize.mockResolvedValue({})
    pools.mockResolvedValue([pool])
    getPlans.mockResolvedValue({ data: [] })
    savePool.mockResolvedValue(pool)
    savePrize.mockResolvedValue(prize)
    records.mockResolvedValue({ items: [], total: 0 })
    ledger.mockResolvedValue({ items: [], total: 0 })
  })
  it('creates pools with both switches disabled', async () => {
    const w = render()
    await flushPromises()
    await w
      .findAll('button')
      .find((b) => b.text() === 'lottery.addPool')!
      .trigger('click')
    await w.find('#lottery-pool-form input').setValue('New pool')
    await w.find('#lottery-pool-form').trigger('submit')
    await flushPromises()
    expect(savePool).toHaveBeenCalledWith(
      null,
      expect.objectContaining({
        name: 'New pool',
        grant_enabled: false,
        draw_enabled: false,
      }),
      expect.any(String),
    )
    w.unmount()
  })
  it('defaults the site switch off and saves an explicit opt-in', async () => {
    const w = render()
    await flushPromises()
    expect(w.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    await w.get('[role="switch"]').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith(true, expect.any(String))
    expect(w.get('[role="switch"]').attributes('aria-checked')).toBe('true')
    w.unmount()
  })
  it('deletes only after confirmation and preserves a retry key on failure', async () => {
    deletePrize.mockRejectedValueOnce({ message: 'Offline' })
    const w = render()
    await flushPromises()
    await w.get('[aria-label="lottery.deletePrize: Credit"]').trigger('click')
    expect(deletePrize).not.toHaveBeenCalled()
    expect(w.text()).toContain('lottery.deletePrizeNote')
    await w.get('[data-testid="confirm-lottery-delete"]').trigger('click')
    await flushPromises()
    await w.get('[data-testid="confirm-lottery-delete"]').trigger('click')
    await flushPromises()
    expect(deletePrize).toHaveBeenCalledWith(1, 10, expect.any(String))
    expect(deletePrize.mock.calls[1][2]).toBe(deletePrize.mock.calls[0][2])
    expect(w.find('[data-testid="confirm-lottery-delete"]').exists()).toBe(false)
    await w.get('[aria-label="lottery.deletePool: Pool"]').trigger('click')
    expect(w.text()).toContain('lottery.deletePoolNote')
    await w.get('[data-testid="confirm-lottery-delete"]').trigger('click')
    await flushPromises()
    expect(deletePool).toHaveBeenCalledWith(1, expect.any(String))
    w.unmount()
  })
  it('locks the earning rule while allowing grant and draw switches independently', async () => {
    const w = render()
    await flushPromises()
    await w.find('[aria-label="lottery.editPool"]').trigger('click')
    expect(
      w.find('#lottery-pool-form select').attributes('disabled'),
    ).toBeDefined()
    expect(
      w.find('#lottery-pool-form input[type="number"]').attributes('disabled'),
    ).toBeDefined()
    await w
      .findAll('#lottery-pool-form input[type="checkbox"]')[0]
      .setValue(false)
    await w.find('#lottery-pool-form').trigger('submit')
    await flushPromises()
    expect(savePool).toHaveBeenCalledWith(
      1,
      expect.objectContaining({
        threshold: 100,
        grant_enabled: false,
        draw_enabled: true,
      }),
      expect.any(String),
    )
    w.unmount()
  })
  it('preserves stock version and request key on a failed save retry', async () => {
    savePrize.mockRejectedValueOnce({ message: 'Offline' })
    const w = render()
    await flushPromises()
    await w.find('[aria-label="lottery.editPrize: Credit"]').trigger('click')
    await flushPromises()
    await w.find('#lottery-prize-form').trigger('submit')
    await flushPromises()
    await w.find('#lottery-prize-form').trigger('submit')
    await flushPromises()
    expect(savePrize.mock.calls[0][2]).toMatchObject({
      updated_at: prize.updated_at,
      stock: 2,
    })
    expect(savePrize.mock.calls[1][3]).toBe(savePrize.mock.calls[0][3])
    w.unmount()
  })
})
