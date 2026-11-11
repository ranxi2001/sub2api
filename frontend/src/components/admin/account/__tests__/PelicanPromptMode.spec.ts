import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent } from 'vue'
import PelicanPromptMode from '../PelicanPromptMode.vue'
import PelicanQuestionSnapshot from '../PelicanQuestionSnapshot.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 41 } }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
beforeEach(() => { localStorage.clear(); setActivePinia(createPinia()) })
describe('Pelican random question controls', () => {
 it('keeps old notice dismissed, shares the new notice across editors, and persists without hiding later notices', async () => {
  useNewFeatureNoticeStore().dismiss(41, NEW_FEATURE_NOTICES.vipAutoCredit)
  const Host = defineComponent({ components: { PelicanPromptMode, NewFeatureBadge }, template: '<div><NewFeatureBadge notice-id="vip-auto-credit-initial-announcement" :user-id="41" /><PelicanPromptMode /><PelicanPromptMode /><NewFeatureBadge notice-id="later-feature-fixture" :user-id="41" /></div>' })
  const first = mount(Host)
  expect(first.findAll('[data-testid=new-feature-badge]')).toHaveLength(3)
  await first.find('[data-testid=pelican-prompt-mode] [data-testid=new-feature-badge]').trigger('click')
  expect(first.findAll('[data-testid=new-feature-badge]')).toHaveLength(1)
  first.unmount()
  setActivePinia(createPinia())
  const reloaded = mount(Host)
  expect(reloaded.findAll('[data-testid=new-feature-badge]')).toHaveLength(1)
  expect(reloaded.findAll('[data-testid=pelican-prompt-mode] [data-testid=new-feature-badge]')).toHaveLength(0)
 })
 it('explicitly selects custom or random and shows immutable historical metadata', async () => {
  const wrapper = mount(PelicanPromptMode)
  expect(wrapper.get('select').element.value).toBe('custom')
  await wrapper.get('select').setValue('random')
  expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['random'])
  const snapshot = mount(PelicanQuestionSnapshot, { props: { question: { id: 'old-question', version: 'old-v1', round_id: 'manual:fixture', source: 'builtin:svg-motion', prompt: 'Original prompt', criteria: ['Original criterion'] }, actualPrompt: 'Exact submitted prompt' } })
  expect(snapshot.text()).toContain('old-v1')
  expect(snapshot.text()).toContain('Exact submitted prompt')
  expect(snapshot.text()).toContain('Original criterion')
  await snapshot.setProps({ question: undefined })
  expect(snapshot.text()).toContain('pelicanTests.questionBank.legacy')
 })
})
