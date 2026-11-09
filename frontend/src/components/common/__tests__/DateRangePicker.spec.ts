import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { ref } from 'vue'

import DateRangePicker from '../DateRangePicker.vue'

enableAutoUnmount(afterEach)
afterEach(() => vi.useRealTimers())

const messages: Record<string, string> = {
  'dates.today': 'Today',
  'dates.yesterday': 'Yesterday',
  'dates.last24Hours': 'Last 24 Hours',
  'dates.last7Days': 'Last 7 Days',
  'dates.last14Days': 'Last 14 Days',
  'dates.last30Days': 'Last 30 Days',
  'dates.thisMonth': 'This Month',
  'dates.lastMonth': 'Last Month',
  'dates.startDate': 'Start Date',
  'dates.endDate': 'End Date',
  'dates.apply': 'Apply',
  'dates.selectDateRange': 'Select date range'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
    locale: ref('en')
  })
}))

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

describe('DateRangePicker', () => {
  it('uses last 24 hours as the default recognized preset', () => {
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: formatLocalDate(yesterday),
        endDate: formatLocalDate(now)
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('Last 24 Hours')
  })

  it('emits range updates with last24Hours preset when applied', async () => {
    const now = new Date()
    const today = formatLocalDate(now)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: today,
        endDate: today
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Last 24 Hours')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    const nowAfterClick = new Date()
    const yesterdayAfterClick = new Date(nowAfterClick.getTime() - 24 * 60 * 60 * 1000)
    const expectedStart = formatLocalDate(yesterdayAfterClick)
    const expectedEnd = formatLocalDate(nowAfterClick)

    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([expectedStart])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([expectedEnd])
    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: expectedStart,
        endDate: expectedEnd,
        preset: 'last24Hours'
      }
    ])
  })

  it('keeps yesterday through today as calendar dates when the 24-hour shortcut is hidden', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 10, 9, 15))
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '2026-11-08', endDate: '2026-11-09', showLast24Hours: false },
      global: { stubs: { Icon: true } }
    })

    expect(wrapper.get('.date-picker-trigger').text()).not.toContain('Last 24 Hours')
    await wrapper.get('.date-picker-trigger').trigger('click')
    expect(wrapper.findAll('.date-picker-preset').map(button => button.text())).not.toContain('Last 24 Hours')
    expect(wrapper.findAll('.date-picker-preset').map(button => button.text())).toContain('Last 7 Days')

    const inputs = wrapper.findAll<HTMLInputElement>('input[type="date"]')
    await inputs[0].setValue('2026-11-06')
    await inputs[0].setValue('2026-11-08')
    expect(wrapper.get('.date-picker-trigger').text()).not.toContain('Last 24 Hours')
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')?.[0]).toEqual([{
      startDate: '2026-11-08', endDate: '2026-11-09', preset: null
    }])
  })

  it.each([false, true])('preserves an unapplied range when hiding its 24-hour shortcut (rolling: %s)', async (rolling24Hours) => {
    vi.useFakeTimers({ toFake: ['Date'] })
    const now = new Date(2026, 10, 9, 15)
    vi.setSystemTime(now)
    const start = new Date(now.getTime() - 86_400_000)
    const startDate = rolling24Hours ? start.toISOString() : formatLocalDate(start)
    const endDate = rolling24Hours ? now.toISOString() : formatLocalDate(now)
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '2026-10-01', endDate: '2026-10-07', rolling24Hours },
      global: { stubs: { Icon: true } }
    })

    await wrapper.get('.date-picker-trigger').trigger('click')
    await wrapper.findAll('.date-picker-preset').find(button => button.text() === 'Last 24 Hours')!.trigger('click')
    await wrapper.setProps({ showLast24Hours: false })
    expect(wrapper.get('.date-picker-trigger').text()).not.toContain('Last 24 Hours')
    expect(wrapper.findAll('.date-picker-preset').map(button => button.text())).not.toContain('Last 24 Hours')
    expect(wrapper.findAll<HTMLInputElement>('input[type="date"]').map(input => input.element.value)).toEqual([
      formatLocalDate(start), formatLocalDate(now)
    ])

    await wrapper.setProps({ showLast24Hours: true })
    expect(wrapper.get('.date-picker-trigger').text()).toContain('Last 24 Hours')
    await wrapper.setProps({ showLast24Hours: false })
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')?.[0]).toEqual([{ startDate, endDate, preset: null }])
  })
})
