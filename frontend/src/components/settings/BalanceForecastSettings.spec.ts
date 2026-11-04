import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { defineComponent, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'
import Panel from './BalanceForecastSettings.vue'
import Badge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const props = { enabled: false, thresholdHours: 12, maxNotifications: 3, userId: 41 }
function setup(locale = 'zh') {
  const pinia = createPinia()
  const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
  return { pinia, global: { plugins: [pinia, i18n] } }
}
beforeEach(() => localStorage.clear())
describe('balance forecast settings', () => {
  it('edits independent controls without submitting the outer settings form', async () => {
    const { global } = setup()
    const wrapper = mount(Panel, { props, global })
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[data-testid="forecast-hours"]').element).toHaveProperty('value', '12')
    await wrapper.get('[data-testid="forecast-enabled"]').setValue(true)
    await wrapper.get('[data-testid="forecast-hours"]').setValue('6.5')
    await wrapper.get('[data-testid="forecast-cap"]').setValue('2')
    expect(wrapper.emitted('update:enabled')).toEqual([[true]])
    expect(wrapper.emitted('update:thresholdHours')).toEqual([[6.5]])
    expect(wrapper.emitted('update:maxNotifications')).toEqual([[2]])
  })
  it.each(['zh', 'en'])('provides translated limits and uncertainty explanation in %s', locale => {
    const { global } = setup(locale)
    const wrapper = mount(Panel, { props, global })
    expect(wrapper.text()).not.toContain('admin.settings.')
    expect(wrapper.text()).toContain('120%')
    expect(wrapper.get('[data-testid="forecast-cap"]').attributes('max')).toBe('10')
  })
  it('shares B across entry badges, persists on refresh/relogin, and keeps A/B dismissed when C appears', async () => {
    const { pinia, global } = setup()
    const A = 'fixture-old-announcement', B = NEW_FEATURE_NOTICES.balanceForecast, C = 'fixture-future-announcement'
    const store = useNewFeatureNoticeStore(pinia)
    store.dismiss(41, A)
    const Harness = defineComponent({
      components: { Panel, Badge }, setup: () => ({ props, B, userId: ref<number | null>(41) }),
      template: '<div><Badge :notice-id="B" :user-id="userId"/><Panel v-bind="props" :user-id="userId"/></div>',
    })
    const wrapper = mount(Harness, { global })
    expect(wrapper.findAll('[data-testid="new-feature-badge"]')).toHaveLength(2)
    await wrapper.findAll('[data-testid="new-feature-badge"]')[1]!.trigger('click')
    expect(wrapper.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    wrapper.vm.userId = null
    await nextTick()
    wrapper.vm.userId = 41
    await nextTick()
    expect(wrapper.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    const refreshed = useNewFeatureNoticeStore(createPinia())
    expect(refreshed.isDismissed(41, A)).toBe(true)
    expect(refreshed.isDismissed(41, B)).toBe(true)
    expect(refreshed.isDismissed(41, C)).toBe(false)
    expect(refreshed.isDismissed(42, B)).toBe(false)
    const future = mount(Badge, { props: { noticeId: C, userId: 41 }, global })
    expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  })
})
