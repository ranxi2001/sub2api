import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import type { LotteryPrize } from '@/api/lottery'
import LotteryReel from './LotteryReel.vue'

const winner = { id: 9, name: 'Awarded snapshot', kind: 'balance', amount: 50 } as LotteryPrize
function render(count = 1) {
  return mount(LotteryReel, { props: { prizes: [], count }, global: {
    plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { lottery: { prizes: 'Prizes', drawing: 'Drawing', noPrizes: 'Empty', reelWinner: 'Winner: {name}' } } } })],
    stubs: { Icon: true, PrizeIcon: true },
  } })
}
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

describe('LotteryReel', () => {
  it('reveals unavailable snapshots immediately with reduced motion', async () => {
    const wrapper = render()
    await wrapper.vm.spinTo([winner])
    await flushPromises()
    expect(wrapper.get('.is-winner').attributes('data-prize-id')).toBe('9')
    expect(wrapper.get('[role="status"]').text()).toBe('Winner: Awarded snapshot')
    await wrapper.setProps({ prizes: [] })
    expect(wrapper.find('.is-winner').exists()).toBe(true)
    wrapper.unmount()
  })
  it('settles active animations when unmounted', async () => {
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
    const cancel = vi.fn()
    const animate = vi.fn(() => ({ finished: new Promise(() => {}), cancel }))
    Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, value: animate })
    const wrapper = render(3)
    const completion = wrapper.vm.spinTo([winner, winner, winner])
    await flushPromises()
    expect(animate).toHaveBeenCalledTimes(3)
    expect(animate.mock.calls.map(call => (call as unknown[])[1])).toEqual([
      expect.objectContaining({ delay: 0 }), expect.objectContaining({ delay: 150 }), expect.objectContaining({ delay: 300 }),
    ])
    wrapper.unmount()
    await completion
    expect(cancel).toHaveBeenCalledTimes(3)
    delete (HTMLElement.prototype as Partial<HTMLElement>).animate
  })
  it('reveals the complete winner name on keyboard focus with only one tab stop per row', async () => {
    const wrapper = render(5)
    await wrapper.vm.spinTo([winner, winner, winner, winner, winner])
    await flushPromises()
    expect(wrapper.findAll('[tabindex="0"]')).toHaveLength(5)
    await wrapper.findAll('.reel-window')[0].trigger('focus')
    expect(wrapper.get('[role="tooltip"]').text()).toContain(winner.name)
    await wrapper.findAll('.reel-window')[0].trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
