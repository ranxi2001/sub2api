import { mount } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import type { AdminUser } from '@/types'
import UserLiveUsageCell from '../UserLiveUsageCell.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const render = (rpm: number | null | undefined, limit: number) => mount(UserLiveUsageCell, {
  props: { user: { current_concurrency: 2, concurrency: 10, current_rpm: rpm, rpm_limit: limit } as AdminUser }
})
describe('UserLiveUsageCell', () => {
  it('keeps failed concurrency reads unknown while preserving legacy numeric responses', async () => {
    const wrapper = render(0, 0)
    await wrapper.setProps({ user: { ...wrapper.props('user'), current_concurrency: 0, concurrency_available: false } })
    expect(wrapper.findComponent({ name: 'UserConcurrencyCell' }).exists()).toBe(false)
    expect(wrapper.text()).toContain('—')
    await wrapper.setProps({ user: { ...wrapper.props('user'), concurrency_available: true } })
    expect(wrapper.findComponent({ name: 'UserConcurrencyCell' }).exists()).toBe(true)
    expect(render(0, 0).findComponent({ name: 'UserConcurrencyCell' }).exists()).toBe(true)
  })
  it('shows actual zero and over-limit counts', () => {
    expect(render(0, 60).text()).toContain('0 / 60')
    const over = render(61, 60)
    expect(over.text()).toContain('61 / 60')
    expect(over.find('.text-red-600').exists()).toBe(true)
  })
  it('shows both zero limits as unlimited while retaining counts', async () => {
    const wrapper = render(18, 0)
    await wrapper.setProps({ user: { ...wrapper.props('user'), concurrency: 0, current_concurrency: 3 } })
    expect(wrapper.text()).toMatch(/3\s*\/\s*∞/)
    expect(wrapper.text()).toContain('18 / ∞')
  })
  it('shows unlimited usage and keeps unavailable RPM distinct from zero', () => {
    expect(render(12, 0).text()).toContain('12 / ∞')
    expect(render(0, 0).text()).toContain('0 / ∞')
    expect(render(null, 0).text()).toContain('— / ∞')
    expect(render(null, 60).text()).toContain('— / 60')
    expect(render(undefined, 60).text()).toContain('— / 60')
  })
})
