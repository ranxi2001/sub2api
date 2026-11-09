import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { mount, flushPromises } from '@vue/test-utils'
import { reactive } from 'vue'
import AutoConfigView from '../AutoConfigView.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { getAutoConfig, saveAutoConfig } from '@/api/admin/autoConfig'
const auth = vi.hoisted(() => ({ user: { id: 41, role: 'admin' } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(auth) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('@/components/admin/operations/AutoConfigHistory.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/account/ModelWhitelistSelector.vue', () => ({ default: { template: '<div />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn() } }))
vi.mock('@/api/admin/autoConfig', () => ({ getAutoConfig: vi.fn(), saveAutoConfig: vi.fn(), getModelBillingGroups: vi.fn(async () => []) }))
vi.mock('@/api/admin/groups', () => ({ getAll: vi.fn(async () => []) }))
vi.mock('@/api/admin/accountQuality', () => ({ listQualityPlans: vi.fn(async () => []) }))
const baseline = { enabled:false,platform:'openai',priority:50,load_factor:1,concurrency:3,group_ids:[],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:'fixture' }
beforeEach(() => {
 localStorage.clear(); setActivePinia(createPinia()); vi.clearAllMocks()
 vi.mocked(getAutoConfig).mockResolvedValue(structuredClone(baseline))
 vi.mocked(saveAutoConfig).mockImplementation(async c => c)
})
it('OAuth degradation renders a separate safe disabled card and requires a threshold before save', async () => {
 const w = mount(AutoConfigView); await flushPromises()
 expect(w.find('[data-testid="degradation-action"]').exists(), 'missing degradation action business controls').toBe(true)
 expect(w.get<HTMLInputElement>('[data-testid="degradation-enabled"]').element.checked).toBe(false)
 expect(w.get<HTMLInputElement>('[data-testid="degradation-threshold"]').element.value).toBe('')
 expect(w.get<HTMLInputElement>('[data-testid="degradation-concurrency"]').element.value).toBe('10')
 await w.get('[data-testid="degradation-enabled"]').setValue(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).not.toHaveBeenCalled()
 await w.get('[data-testid="degradation-threshold"]').setValue('25')
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).toHaveBeenCalledWith(expect.objectContaining({degradation_action:expect.objectContaining({enabled:true,threshold_percent:25,target_concurrency:10})}))
 const payload = vi.mocked(saveAutoConfig).mock.calls[0][0]
 w.unmount(); vi.mocked(getAutoConfig).mockResolvedValueOnce(payload)
 const reloaded=mount(AutoConfigView); await flushPromises()
 expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-threshold"]').element.value).toBe('25')
 reloaded.unmount()
})
it('OAuth degradation notice follows A to B to C with shared dismissal, refresh and login persistence', async () => {
 const a='prior-fixture-announcement',b='oauth-degradation-actions-initial-announcement',c='future-fixture-announcement'
 useNewFeatureNoticeStore().dismiss(41,a)
 const w=mount(AutoConfigView); await flushPromises()
 expect(w.find('[data-testid="degradation-action"]').exists()).toBe(true)
 const second=mount(NewFeatureBadge,{props:{noticeId:b,userId:41}})
 await w.get('[data-testid="degradation-action"] [data-testid="new-feature-badge"]').trigger('click')
 expect(second.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
 w.unmount(); second.unmount(); setActivePinia(createPinia())
 const login=mount(NewFeatureBadge,{props:{noticeId:b,userId:41}})
 expect(login.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
 const future=mount(NewFeatureBadge,{props:{noticeId:c,userId:41}})
 expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
 expect(useNewFeatureNoticeStore().isDismissed(41,a)).toBe(true)
 login.unmount(); future.unmount()
})
