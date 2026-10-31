import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it } from 'vitest'
import NewFeatureBadge from '../NewFeatureBadge.vue'
import zhCommon from '@/i18n/locales/zh/common'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'

describe('NewFeatureBadge', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  function mountBadge(userId = 41) {
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: zhCommon } })
    return mount(NewFeatureBadge, {
      props: { noticeId: 'protocol-switches', userId },
      global: { plugins: [i18n, createPinia()] },
    })
  }

  it('hides a shared notice for the current admin when clicked', async () => {
    const wrapper = mountBadge()
    expect(wrapper.get('[data-testid="new-feature-badge"]').text()).toBe('新功能')

    await wrapper.get('[data-testid="new-feature-badge"]').trigger('click')

    expect(wrapper.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(useNewFeatureNoticeStore().isDismissed(41, 'protocol-switches')).toBe(true)
    expect(mountBadge().find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(mountBadge(42).find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  })
})
