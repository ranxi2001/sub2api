import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

const render = (current: number | null | undefined, concurrency = 3) => mount(AccountCapacityCell, {
  props: { account: { id: 41, platform: 'openai', type: 'oauth', concurrency, current_concurrency: current, extra: {} } as Account },
  global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: {
    en: { admin: { accounts: { capacity: { concurrencyUnavailable: 'Current concurrency is unavailable. Refresh to retry.' } } } }
  } })] }
})

describe('AccountCapacityCell concurrency observation', () => {
  it.each([null, undefined, NaN, -1, Infinity])('renders %s as unknown rather than idle', current => {
    const view = render(current)
    const badge = view.get('[data-testid="account-concurrency"]')
    expect(badge.text().replace(/\s/g, '')).toBe('—/3')
    expect(badge.attributes('title')).toContain('unavailable')
    expect(badge.classes()).not.toContain('bg-red-100')
    view.unmount()
  })

  it('reacts to occupied → unknown → real zero without changing configured capacity', async () => {
    const view = render(2, 7)
    const badge = () => view.get('[data-testid="account-concurrency"]')
    expect(badge().text().replace(/\s/g, '')).toBe('2/7')
    await view.setProps({ account: { ...view.props('account'), current_concurrency: null } })
    expect(badge().text().replace(/\s/g, '')).toBe('—/7')
    await view.setProps({ account: { ...view.props('account'), current_concurrency: 0 } })
    expect(badge().text().replace(/\s/g, '')).toBe('0/7')
    expect(badge().attributes('title')).toBeUndefined()
    view.unmount()
  })

  it.each([0, -1])('keeps capacity %s and does not mark zero occupancy as full', capacity => {
    const view = render(0, capacity)
    const badge = view.get('[data-testid="account-concurrency"]')
    expect(badge.text().replace(/\s/g, '')).toBe(`0/${capacity}`)
    expect(badge.classes()).not.toContain('bg-red-100')
    view.unmount()
  })
})
