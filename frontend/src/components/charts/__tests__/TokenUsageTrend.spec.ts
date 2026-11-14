import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { Line } from 'vue-chartjs'

import TokenUsageTrend from '../TokenUsageTrend.vue'

const messages: Record<string, string> = {
  'admin.dashboard.tokenUsageTrend': 'Token Usage Trend',
  'admin.dashboard.noDataAvailable': 'No data available',
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

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    template: '<div class="chart-data">{{ JSON.stringify(data) }}</div>',
  },
}))

describe('TokenUsageTrend', () => {
  it('calculates cache hit rate against all prompt tokens', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 500,
            output_tokens: 100,
            cache_creation_tokens: 0,
            cache_read_tokens: 1500,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    // Hit rate = 1500 / (500 + 1500 + 0) * 100 = 75%
    expect(hitRateDataset.data[0]).toBe(75)
  })

  it('returns 0 hit rate when all prompt tokens are zero', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 0,
            input_tokens: 0,
            output_tokens: 0,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0,
            actual_cost: 0,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    expect(hitRateDataset.data[0]).toBe(0)
  })

  it('includes cache_creation_tokens in denominator for Anthropic models', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 200,
            output_tokens: 50,
            cache_creation_tokens: 300,
            cache_read_tokens: 500,
            cost: 0.02,
            actual_cost: 0.01,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    // Hit rate = 500 / (200 + 500 + 300) * 100 = 50%
    expect(hitRateDataset.data[0]).toBe(50)
  })
})
describe('TokenUsageTrend RPM/TPM tooltip', () => {
  const point = {
    date: '2026-11-13 17:00',
    requests: 46,
    input_tokens: 1_000_000,
    output_tokens: 300_000,
    cache_creation_tokens: 0,
    cache_read_tokens: 1_000_000,
    total_tokens: 2_300_000,
    cost: 12,
    actual_cost: 6,
  }
  // The first 23 minutes of the 17:00 hour in Shanghai.
  const rateContext = {
    window: { start: '2026-11-13T09:37:00Z', end: '2026-11-14T09:37:00Z', now: Date.parse('2026-11-14T09:37:00Z') },
    granularity: 'hour',
    serverTimezone: 'Asia/Shanghai',
    serverUTCOffset: '+08:00',
  }
  const mountTrend = (props: Record<string, unknown>) => mount(TokenUsageTrend, {
    props: { trendData: [point], ...props },
    global: { stubs: { LoadingSpinner: true, NewFeatureBadge: true } },
  })
  const footer = (wrapper: ReturnType<typeof mountTrend>) =>
    wrapper.findComponent(Line as any).props('options').plugins.tooltip.callbacks.footer([{ dataIndex: 0 }])

  it('keeps the cost-only footer and no badge unless enabled', () => {
    const wrapper = mountTrend({ rateContext, noticeUserId: 7 })
    expect(footer(wrapper)).toBe('Actual: $6.00 | Standard: $12.00')
    expect(wrapper.findComponent({ name: 'NewFeatureBadge' }).exists()).toBe(false)
  })

  it('adds the bucket RPM and TPM over the minutes it covers', () => {
    const wrapper = mountTrend({ showRate: true, rateContext })
    expect(footer(wrapper)).toEqual(['Actual: $6.00 | Standard: $12.00', 'RPM: 2.00 | TPM: 100.00K'])
  })

  it('shows dashes until the trend has a rate context', () => {
    const wrapper = mountTrend({ showRate: true })
    expect(footer(wrapper)).toEqual(['Actual: $6.00 | Standard: $12.00', 'RPM: — | TPM: —'])
  })

  it('shows the new-feature badge beside the title only for an admin', () => {
    const withAdmin = mountTrend({ showRate: true, rateContext, noticeUserId: 7 })
    const badge = withAdmin.findComponent({ name: 'NewFeatureBadge' })
    expect(badge.exists()).toBe(true)
    expect(badge.props()).toMatchObject({
      noticeId: 'usage-trend-tooltip-rpm-tpm-initial-announcement',
      userId: 7,
    })
    expect(withAdmin.find('h3').element.contains(badge.element)).toBe(true)

    const withoutAdmin = mountTrend({ showRate: true, rateContext })
    expect(withoutAdmin.findComponent({ name: 'NewFeatureBadge' }).exists()).toBe(false)
  })
})
