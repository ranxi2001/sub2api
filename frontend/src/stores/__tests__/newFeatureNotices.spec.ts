import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useNewFeatureNoticeStore } from '../newFeatureNotices'

describe('new feature notice preferences', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('persists dismissed notice IDs per admin account', () => {
    const store = useNewFeatureNoticeStore()
    expect(store.isDismissed(41, 'protocol-switches')).toBe(false)

    store.dismiss(41, 'protocol-switches')

    expect(store.isDismissed(41, 'protocol-switches')).toBe(true)
    expect(store.isDismissed(42, 'protocol-switches')).toBe(false)
    expect(localStorage.getItem('sub2api:new-feature-notices:v1:41')).toBe('["protocol-switches"]')

    setActivePinia(createPinia())
    expect(useNewFeatureNoticeStore().isDismissed(41, 'protocol-switches')).toBe(true)
  })

  it('ignores malformed stored preferences and does not overwrite other notices', () => {
    localStorage.setItem('sub2api:new-feature-notices:v1:41', '{bad json')
    const store = useNewFeatureNoticeStore()

    expect(store.isDismissed(41, 'protocol-switches')).toBe(false)
    store.dismiss(41, 'protocol-switches')
    store.dismiss(41, 'other-feature')

    expect(JSON.parse(localStorage.getItem('sub2api:new-feature-notices:v1:41') ?? '[]')).toEqual([
      'protocol-switches',
      'other-feature',
    ])
  })
})
