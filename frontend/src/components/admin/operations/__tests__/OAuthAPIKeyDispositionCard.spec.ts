import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { ref, reactive } from 'vue'
import Card from '../OAuthAPIKeyDispositionCard.vue'
import QualityAccountSelector from '../QualityAccountSelector.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { oauthAPIKeyDispositionPayload, oauthAPIKeyDispositionError } from '@/utils/oauthAPIKeyDisposition'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import type { Group } from '@/types'
const { list } = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ list }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('zh') }) }))
enableAutoUnmount(afterEach)
const groups = [{ id: 5, name: 'Demo group', platform: 'openai' }] as Group[]
function card(pinia = createPinia()) {
  const wrapper = mount(Card, { props: { modelValue: reactive(oauthAPIKeyDispositionPayload({ enabled: true })), groups, platform: 'openai', initialEnabled: true, userId: 41 }, global: { plugins: [pinia] } })
  return wrapper
}
beforeEach(() => {
 localStorage.clear(); list.mockReset()
 list.mockResolvedValue({ page: 1, pages: 1, total: 3, items: [
  { id: 7, name: 'Demo API A', platform: 'openai', type: 'apikey', group_ids: [5], schedulable: true },
  { id: 8, name: 'Demo API B', platform: 'openai', type: 'apikey', group_ids: [6], schedulable: false },
  { id: 9, name: 'Demo OAuth', platform: 'openai', type: 'oauth', group_ids: [5] }
 ] })
})
it('uses the shared picker, filters groups, selects all matches and preserves hidden selections', async () => {
 const wrapper = card(); await flushPromises()
 const picker = wrapper.getComponent(QualityAccountSelector)
 expect(picker.props('accounts').map(a => a.id)).toEqual([7, 8])
 await picker.vm.$emit('update:group', '5'); await flushPromises()
 expect(picker.props('accounts').map(a => a.id)).toEqual([7])
 picker.vm.$emit('selectAll'); await flushPromises()
 expect(wrapper.props('modelValue').account_ids).toEqual([7])
 await picker.vm.$emit('update:group', '6'); await flushPromises()
 picker.vm.$emit('selectAll'); await flushPromises()
 expect(wrapper.props('modelValue').account_ids).toEqual([7, 8])
 await wrapper.get('[data-testid="apikey-disposition-scope"]').setValue('groups')
 await wrapper.get('[data-testid="disposition-group-5"]').setValue(true)
 expect(wrapper.props('modelValue').group_ids).toEqual([5])
 expect(wrapper.props('modelValue').account_ids).toEqual([7, 8])
})
it('retains selections on fetch failure and allows turning the rule off', async () => {
 list.mockRejectedValue(new Error('offline'))
 const wrapper = card(); wrapper.props('modelValue').enabled = true; wrapper.props('modelValue').account_ids = [7]
 await flushPromises()
 expect(wrapper.vm.validationError()).toBe('autoConfig.apikeyDisposition.accountsFailed')
 expect(wrapper.props('modelValue').account_ids).toEqual([7])
 wrapper.props('modelValue').enabled = false
 expect(wrapper.vm.validationError()).toBeNull()
})
it('validates default off, delayed pause and permanent disable', () => {
 const p = oauthAPIKeyDispositionPayload(); expect(p.enabled).toBe(false); expect(p.pause_minutes).toBe(180)
 p.enabled = true; expect(oauthAPIKeyDispositionError(p)).not.toBeNull()
 p.account_ids = [7]; p.delay_minutes = 0; expect(oauthAPIKeyDispositionError(p)).toBeNull()
 p.delay_minutes = 0.5; expect(oauthAPIKeyDispositionError(p)).not.toBeNull()
 p.delay_minutes = 5; p.action = 'disable'; expect(oauthAPIKeyDispositionError(p)).toBeNull()
})
it('keeps A dismissed, shares B dismissal, reloads B and leaves future C visible', async () => {
 const pinia = createPinia(), id = NEW_FEATURE_NOTICES.oauthAPIKeyDisposition, old = NEW_FEATURE_NOTICES.oauthInitialWSSettings
 useNewFeatureNoticeStore(pinia).dismiss(41, old)
 const a = card(pinia), b = card(pinia); await flushPromises()
 expect(a.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
 await a.get('[data-testid="new-feature-badge"]').trigger('click')
 expect(b.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
 a.unmount(); b.unmount()
 const reloaded = createPinia(), c = card(reloaded); await flushPromises()
 expect(c.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
 expect(useNewFeatureNoticeStore(reloaded).isDismissed(41, old)).toBe(true)
 const future = mount(NewFeatureBadge, { props: { noticeId: 'disposition-future-C', userId: 41 }, global: { plugins: [reloaded] } })
 expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
 expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
})

it('normalizes empty account/group arrays from legacy JSON without sharing draft arrays', () => {
 const source = oauthAPIKeyDispositionPayload({ account_ids: [7], group_ids: [5] })
 const copy = oauthAPIKeyDispositionPayload(source)
 copy.account_ids.push(8); expect(source.account_ids).toEqual([7])
 expect(oauthAPIKeyDispositionPayload(JSON.parse('{"account_ids":null,"group_ids":null}')).account_ids).toEqual([])
})
