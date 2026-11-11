import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { beforeEach, expect, it } from 'vitest'
import Cell from '../AccountPerformanceCell.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import en from '@/i18n/locales/en'

beforeEach(() => localStorage.clear())
it('keeps A dismissed, displays B at every metric entry, synchronizes dismissal and persists across refresh/login; C does not revive A/B', async () => {
 const A = NEW_FEATURE_NOTICES.protocolFeatureSwitches
 const B = NEW_FEATURE_NOTICES.accountUpstreamHTTPErrorRate
 const C = 'future-distinct-feature-c'
 expect(B).not.toBe(A)
 expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
 const pinia=createPinia()
 const i18n=createI18n({legacy:false,locale:'en',messages:{en}})
 const notices=useNewFeatureNoticeStore(pinia)
 notices.dismiss(41,A)
 const entry=()=>mount(Cell,{props:{noticeUserId:41},global:{plugins:[pinia,i18n]}})
 const row=entry(),dialog=entry()
 expect(row.find('[data-metric="upstream"] [data-testid="new-feature-badge"]').exists()).toBe(true)
 expect(dialog.find('[data-metric="upstream"] [data-testid="new-feature-badge"]').exists()).toBe(true)
 await row.get('[data-metric="upstream"] [data-testid="new-feature-badge"]').trigger('click')
 expect(dialog.find('[data-metric="upstream"] [data-testid="new-feature-badge"]').exists()).toBe(false)
 row.unmount();dialog.unmount()
 const refreshedPinia=createPinia()
 const refreshed=useNewFeatureNoticeStore(refreshedPinia)
 expect(refreshed.isDismissed(41,A)).toBe(true)
 expect(refreshed.isDismissed(41,B)).toBe(true)
 expect(refreshed.isDismissed(42,B)).toBe(false)
 const badge=mount(NewFeatureBadge,{props:{noticeId:B,userId:41},global:{plugins:[refreshedPinia,i18n]}})
 expect(badge.find('button').exists()).toBe(false)
 await badge.setProps({userId:null})
 await badge.setProps({userId:41})
 expect(badge.find('button').exists()).toBe(false)
 await badge.setProps({noticeId:C})
 expect(badge.find('button').exists()).toBe(true)
 expect(refreshed.isDismissed(41,A)).toBe(true)
 expect(refreshed.isDismissed(41,B)).toBe(true)
 badge.unmount()
})
