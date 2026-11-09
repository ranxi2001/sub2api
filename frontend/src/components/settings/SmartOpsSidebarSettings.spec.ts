import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { defineComponent, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Panel from './SmartOpsSidebarSettings.vue'
import Badge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS, SMART_OPS_MENU_ITEMS } from '@/constants/smartOpsMenu'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
function setup(locale = 'zh') {
  const pinia = createPinia()
  return { pinia, global: { plugins: [pinia, createI18n({ legacy: false, locale, messages: { zh, en } })] } }
}
beforeEach(() => localStorage.clear())
describe('smart operations sidebar settings', () => {
  it('edits the six-entry default draft and restores entries with bulk actions', async () => {
    const Harness = defineComponent({
      components: { Panel },
      setup: () => ({ hidden: ref<string[]>([...DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS, '/admin/future-entry']) }),
      template: '<form><Panel v-model="hidden"/></form>',
    })
    const wrapper = mount(Harness, { global: setup().global })
    expect(wrapper.findAll('[role="switch"]')).toHaveLength(14)
    expect(wrapper.findAll('[role="switch"][aria-checked="true"]')).toHaveLength(6)
    expect(wrapper.text()).toContain('已选择 6 / 14 个入口')
    expect(wrapper.findAll('button').every(button => button.attributes('type') === 'button')).toBe(true)
    await wrapper.get('#smart-ops-sidebar-token-guard').trigger('click')
    expect(wrapper.vm.hidden).toEqual([...DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS.filter(path => path !== '/admin/token-guard'), '/admin/future-entry'])
    expect(wrapper.get('#smart-ops-sidebar-token-guard').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('#smart-ops-sidebar-token-guard-v2').attributes('aria-checked')).toBe('true')
    await wrapper.findAll('button').find(button => button.text() === '全部隐藏')!.trigger('click')
    expect(SMART_OPS_MENU_ITEMS.every(item => wrapper.vm.hidden.includes(item.path))).toBe(true)
    await wrapper.findAll('button').find(button => button.text() === '全部显示')!.trigger('click')
    expect(wrapper.vm.hidden).toEqual(['/admin/future-entry'])
    expect(wrapper.findAll('[role="switch"]').every(toggle => toggle.attributes('aria-checked') === 'true')).toBe(true)
  })
  it.each(['zh', 'en'])('renders translated labels and scope explanation in %s', locale => {
    const wrapper = mount(Panel, { props: { modelValue: [] }, global: setup(locale).global })
    expect(wrapper.text()).not.toMatch(/admin\.settings\.|\.title\b/)
    expect(wrapper.text()).toContain('14')
  })
  it('shares its new notice ID and keeps dismissals across refresh without hiding future notices', async () => {
    const { pinia, global } = setup()
    const A = NEW_FEATURE_NOTICES.protocolFeatureSwitches
    const B = NEW_FEATURE_NOTICES.smartOpsSidebar
    const C = 'fixture-future-sidebar-announcement'
    useNewFeatureNoticeStore(pinia).dismiss(41, A)
    const wrapper = mount(Panel, { props: { modelValue: [] }, global })
    const other = mount(Badge, { props: { noticeId: B, userId: 41 }, global })
    expect(wrapper.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    await wrapper.get('[data-testid="new-feature-badge"]').trigger('click')
    await nextTick()
    expect(other.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    const refreshed = useNewFeatureNoticeStore(createPinia())
    expect(refreshed.isDismissed(41, A)).toBe(true)
    expect(refreshed.isDismissed(41, B)).toBe(true)
    expect(refreshed.isDismissed(41, C)).toBe(false)
    expect(refreshed.isDismissed(42, B)).toBe(false)
  })
})
