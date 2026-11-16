import { mount } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import type { AdminUser } from '@/types'
import UserLiveUsageCell from '../UserLiveUsageCell.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const render = (rpm: number | null | undefined, limit: number) => mount(UserLiveUsageCell, {
  props: { user: { current_concurrency: 2, concurrency: 10, current_rpm: rpm, rpm_limit: limit } as AdminUser }
})
describe('UserLiveUsageCell', () => {
  it('shows actual zero and over-limit counts', () => {
    expect(render(0, 60).text()).toContain('0 / 60')
    const over = render(61, 60)
    expect(over.text()).toContain('61 / 60')
    expect(over.find('.text-red-600').exists()).toBe(true)
  })
  it('does not turn uncounted or unavailable RPM into zero', () => {
    expect(render(null, 0).text()).toContain('admin.users.rpmNotCounted')
    expect(render(null, 60).text()).toContain('— / 60')
    expect(render(undefined, 60).text()).toContain('— / 60')
  })
})
