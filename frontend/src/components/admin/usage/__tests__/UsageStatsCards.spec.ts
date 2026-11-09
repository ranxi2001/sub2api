import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.inSelectedRange': 'in selected range',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheTotal': 'Cache',
  'usage.cacheBreakdown': 'Cache Token Breakdown',
  'usage.cacheCreationTokensLabel': 'Cache Creation',
  'usage.cacheReadTokensLabel': 'Cache Read',
  'usage.totalCost': 'Total Cost',
  'usage.accountCost': 'Cost',
  'usage.standardCost': 'Standard',
  'usage.avgDuration': 'Avg Duration',
  'usage.rateCardHint': 'Per-minute averages over the selected range',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const stats = {
  total_requests: 1,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 34,
  total_cache_creation_tokens: 12,
  total_cache_read_tokens: 22,
  total_tokens: 184,
  total_cost: 0.001,
  total_actual_cost: 0.001,
  total_account_cost: 0.001,
  average_duration_ms: 250,
}

describe('UsageStatsCards', () => {
  it('keeps total consumption, standard cost and account cost distinct', () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: { ...stats, total_actual_cost: 3, total_cost: 12, total_account_cost: 5 } },
      global: { stubs: { Icon: true } },
    })
    const costCard = wrapper.findAll('.card')[2]
    expect(costCard.get('.text-xl').text()).toBe('$3.0000')
    expect(costCard.text()).toContain('Standard $12.0000')
    expect(costCard.text()).toContain('Cost $5.0000')
    wrapper.unmount()
  })

  it('shows cache token breakdown values', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Cache: 34')
    expect(text).toContain('Cache Token Breakdown')
    expect(text).toContain('Cache Creation')
    expect(text).toContain('12')
    expect(text).toContain('Cache Read')
    expect(text).toContain('22')
  })

  it('keeps the cache tooltip out of the layout while it is hidden', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const tooltip = wrapper.findAll('span').find((el) => el.classes().includes('group-hover:block'))

    expect(tooltip).toBeDefined()
    // `opacity-0` hides the tooltip visually but keeps it in the layout, so its
    // fixed width still widens the document and causes horizontal scrolling on
    // narrow screens. `hidden` (display: none) takes it out of the flow.
    expect(tooltip?.classes()).toContain('hidden')
    expect(tooltip?.classes()).not.toContain('opacity-0')
  })

  describe('RPM/TPM card', () => {
    const dayWindow = {
      start: '2026-11-09T03:00:00.000Z',
      end: '2026-11-10T03:00:00.000Z',
      now: Date.parse('2026-11-10T03:00:00.000Z'),
    }
    const global = { stubs: { Icon: true, NewFeatureBadge: true } }

    it('is hidden unless the page asks for it, keeping the four-card layout', () => {
      const wrapper = mount(UsageStatsCards, { props: { stats }, global })
      expect(wrapper.find('[data-testid="usage-rate-card"]').exists()).toBe(false)
      expect(wrapper.classes()).toContain('lg:grid-cols-4')
    })

    it('sits between the token and cost cards with per-minute averages of the stats window', () => {
      const wrapper = mount(UsageStatsCards, {
        props: { stats: { ...stats, total_requests: 2880, total_tokens: 14_400_000 }, showRate: true, rateWindow: dayWindow },
        global,
      })
      const cards = wrapper.findAll('.card')
      expect(cards).toHaveLength(5)
      expect(cards[1].text()).toContain('Total Tokens')
      expect(cards[2].attributes('data-testid')).toBe('usage-rate-card')
      expect(cards[3].text()).toContain('Total Cost')
      expect(wrapper.get('[data-testid="usage-rpm"]').text()).toBe('2.00')
      expect(wrapper.get('[data-testid="usage-tpm"]').text()).toBe('10.00K')
      expect(cards[2].attributes('title')).toBe('Per-minute averages over the selected range')
      expect(wrapper.classes()).not.toContain('lg:grid-cols-4')
    })

    it('shows placeholders until stats for a window arrive', () => {
      const wrapper = mount(UsageStatsCards, { props: { stats: null, showRate: true, rateWindow: null }, global })
      expect(wrapper.get('[data-testid="usage-rpm"]').text()).toBe('—')
      expect(wrapper.get('[data-testid="usage-tpm"]').text()).toBe('—')
    })

    it('shows the new-feature badge only for the admin who can dismiss it', () => {
      const withAdmin = mount(UsageStatsCards, {
        props: { stats, showRate: true, rateWindow: dayWindow, noticeUserId: 7 },
        global,
      })
      const badge = withAdmin.findComponent({ name: 'NewFeatureBadge' })
      expect(badge.exists()).toBe(true)
      expect(badge.props('noticeId')).toBe(NEW_FEATURE_NOTICES.usageRateCard)
      expect(badge.props('userId')).toBe(7)
      expect(badge.props('compact')).toBe(true)

      const withoutAdmin = mount(UsageStatsCards, { props: { stats, showRate: true, rateWindow: dayWindow }, global })
      expect(withoutAdmin.findComponent({ name: 'NewFeatureBadge' }).exists()).toBe(false)
    })
  })
})
