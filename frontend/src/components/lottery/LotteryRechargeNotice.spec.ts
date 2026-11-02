import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { reactive } from 'vue'
import { createI18n } from 'vue-i18n'
import type { LotteryPool } from '@/api/lottery'
import en from '@/i18n/locales/en/lottery'
import zh from '@/i18n/locales/zh/lottery'
import LotteryRechargeNotice from './LotteryRechargeNotice.vue'

let lottery: { enabled: boolean; pools: LotteryPool[]; refresh: ReturnType<typeof vi.fn> }
vi.mock('@/stores/lottery', () => ({ useLotteryStore: () => lottery }))
enableAutoUnmount(afterEach)

const pool = (overrides: Partial<LotteryPool> = {}): LotteryPool => ({
  id: 1, name: 'Balance rewards', mode: 'single', threshold: 100.5,
  grant_enabled: true, draw_enabled: true, rule_locked: true,
  sort_order: 0, available: 0, debt: 0, progress: 0, prizes: [],
  ...overrides,
})

function render(source: 'recharge' | 'redeem' = 'recharge', locale = 'en') {
  return mount(LotteryRechargeNotice, {
    props: { source },
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: { en: { lottery: en }, zh: { lottery: zh } } })],
      stubs: { RouterLink: RouterLinkStub, Icon: true },
    },
  })
}

beforeEach(() => {
  lottery = reactive({ enabled: true, pools: [pool()], refresh: vi.fn().mockResolvedValue(undefined) })
})

describe('recharge lottery notice', () => {
  it('hides immediately when the site disables lottery, even with cached pools', async () => {
    const wrapper = render()
    expect(wrapper.find('section').exists()).toBe(true)
    lottery.enabled = false
    await flushPromises()
    expect(wrapper.find('section').exists()).toBe(false)
  })

  it.each(['empty', 'stopped', 'invite_visit', 'invite_signup', 'invite_recharge'] as const)(
    'does not promote ineligible %s pools', (scenario) => {
      lottery.pools = scenario === 'empty' ? [] : [pool(scenario === 'stopped'
        ? { grant_enabled: false, available: 10 }
        : { mode: scenario })]
      expect(render().find('section').exists()).toBe(false)
    },
  )

  it('explains independent single and cumulative grants using credited USD and includes bonuses', () => {
    lottery.pools.push(pool({ id: 2, name: 'Cumulative rewards', mode: 'cumulative', threshold: 50, progress: 12.3456789 }))
    const wrapper = render()
    expect(wrapper.findAll('li')).toHaveLength(2)
    expect(wrapper.text()).toContain('One draw per recharge of at least 100.5 USD')
    expect(wrapper.text()).toContain('50 USD')
    expect(wrapper.text()).toContain('12.3456789 / 50 USD')
    expect(wrapper.text()).toContain(en.rechargeNotice.rechargeHint)
    expect(wrapper.text()).toContain(en.rechargeNotice.multiplePools)
    expect(wrapper.getComponent(RouterLinkStub).props('to')).toBe('/lottery')
    expect(lottery.refresh).toHaveBeenCalledOnce()
  })

  it('shows balance-only redemption rules and updated account progress after redemption', async () => {
    lottery.pools = [pool({ mode: 'cumulative', progress: 20 })]
    const wrapper = render('redeem')
    expect(wrapper.text()).toContain(en.rechargeNotice.redeemTitle)
    expect(wrapper.text()).toContain(en.rechargeNotice.redeemHint)
    expect(wrapper.text()).not.toContain(en.rechargeNotice.rechargeHint)
    lottery.pools = [pool({ mode: 'cumulative', progress: 9.5, available: 1 })]
    await flushPromises()
    expect(wrapper.text()).toContain('9.5 / 100.5 USD')
    expect(wrapper.text()).toContain(en.pendingPool.replace('{count}', '1'))
  })

  it('discloses paused drawing, refund debt and unavailable prizes without hiding active grants', () => {
    lottery.pools = [pool({ draw_enabled: false, debt: 2 }), pool({ id: 2, name: 'Awaiting restock' })]
    const wrapper = render()
    expect(wrapper.findAll('li')).toHaveLength(2)
    expect(wrapper.text()).toContain(en.rechargeNotice.drawPaused)
    expect(wrapper.text()).toContain(en.rechargeNotice.debtHint.replace('{count}', '2'))
    expect(wrapper.text()).toContain(en.noPrizes)
  })

  it('renders Chinese copy and preserves small fractional thresholds', () => {
    lottery.pools = [pool({ threshold: 0.00000001 })]
    const wrapper = render('redeem', 'zh')
    expect(wrapper.text()).toContain(zh.rechargeNotice.redeemTitle)
    expect(wrapper.text()).toContain('每笔到账满 0.00000001 USD，赠送 1 次')
    expect(wrapper.text()).not.toContain(zh.rechargeNotice.multiplePools)
  })
})
