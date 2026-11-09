import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { mount, flushPromises } from '@vue/test-utils'
import { reactive } from 'vue'
import AutoConfigView from '../AutoConfigView.vue'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { getAutoConfig, saveAutoConfig } from '@/api/admin/autoConfig'
import { getAll } from '@/api/admin/groups'
import { defaultOAuthDegradationConfig, oauthDegradationError, oauthDegradationPayload } from '@/utils/oauthDegradation'
const auth = vi.hoisted(() => ({ user: { id: 41, role: 'admin' } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(auth) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('@/components/admin/operations/AutoConfigHistory.vue', () => ({ default: { template: '<div />' } }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn() } }))
vi.mock('@/api/admin/autoConfig', () => ({ getAutoConfig: vi.fn(), saveAutoConfig: vi.fn(), getModelBillingGroups: vi.fn(async () => []) }))
vi.mock('@/api/admin/groups', () => ({ getAll: vi.fn(async () => []) }))
vi.mock('@/api/admin/accountQuality', () => ({ listQualityPlans: vi.fn(async () => []) }))
const baseline = { enabled:false,platform:'openai',priority:50,load_factor:1,concurrency:3,group_ids:[],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:'fixture' }
beforeEach(() => {
 localStorage.clear(); setActivePinia(createPinia()); vi.clearAllMocks()
 vi.mocked(getAutoConfig).mockResolvedValue(structuredClone(baseline))
 vi.mocked(saveAutoConfig).mockImplementation(async c => c)
 vi.mocked(getAll).mockResolvedValue([{id:5,name:'OpenAI fixture',platform:'openai',status:'active'},{id:6,name:'Anthropic fixture',platform:'anthropic',status:'active'}] as any)
})
it('OAuth degradation renders a separate safe disabled card and requires a threshold before save', async () => {
 const w = mount(AutoConfigView); await flushPromises()
 expect(w.find('[data-testid="degradation-action"]').exists(), 'missing degradation action business controls').toBe(true)
 expect(w.get<HTMLInputElement>('[data-testid="degradation-enabled"]').element.checked).toBe(false)
 expect(w.get<HTMLInputElement>('[data-testid="degradation-threshold"]').element.value).toBe('')
 expect(w.get<HTMLInputElement>('[data-testid="degradation-concurrency"]').element.value).toBe('10')
 expect(w.get<HTMLInputElement>('[data-testid="degradation-limit-concurrency"]').element.checked).toBe(true)
 expect(w.get<HTMLInputElement>('[data-testid="degradation-limit-models"]').element.checked).toBe(true)
 expect(w.get<HTMLInputElement>('[data-testid="degradation-limit-groups"]').element.checked).toBe(false)
 const picker=w.get('[data-testid="degradation-models"]').findComponent(ModelWhitelistSelector)
 expect(picker.props('showSyncButton')).toBe(false)
 await picker.get('.cursor-pointer').trigger('click')
 const option=picker.findAll('[data-testid="model-option"]').find(row=>row.text().includes('gpt-6-luna'))!
 await option.get('[data-testid="select-model"]').trigger('click')
 expect(picker.props('modelValue')).not.toContain('gpt-6-luna')
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

it('reads a saved null model scope as deny-all and keeps it editable', async () => {
 vi.mocked(getAutoConfig).mockResolvedValueOnce({ ...baseline, degradation_action: { enabled: true, threshold_percent: 50, target_concurrency: 10, models: null as any } })
 const w=mount(AutoConfigView); await flushPromises()
 expect(w.get('[data-testid="degradation-action"]').text()).toContain('autoConfig.degradation.empty')
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).toHaveBeenCalledWith(expect.objectContaining({degradation_action:expect.objectContaining({models:[]})}))
 w.unmount()
})

it.each([
 [true, false, false],
 [false, true, false],
 [false, false, true],
 [true, true, false],
 [true, false, true],
 [false, true, true],
 [true, true, true],
])('saves and reloads independent actions: concurrency=%s models=%s groups=%s', async (limitConcurrency, limitModels, limitGroups) => {
 const w = mount(AutoConfigView); await flushPromises()
 await w.get('[data-testid="degradation-threshold"]').setValue('40')
 await w.get('[data-testid="degradation-limit-concurrency"]').setValue(limitConcurrency)
 await w.get('[data-testid="degradation-limit-models"]').setValue(limitModels)
 await w.get('[data-testid="degradation-limit-groups"]').setValue(limitGroups)
 expect(w.find('[data-testid="degradation-concurrency"]').exists()).toBe(limitConcurrency)
 expect(w.find('[data-testid="degradation-models"]').exists()).toBe(limitModels)
 expect(w.find('[data-testid="degradation-groups"]').exists()).toBe(limitGroups)
 if (limitGroups) {
  expect(w.get('[data-testid="degradation-groups"]').text()).toContain('OpenAI fixture')
  expect(w.get('[data-testid="degradation-groups"]').text()).toContain('Anthropic fixture')
  await w.get('[data-testid="degradation-group-5"]').setValue(true)
 }
 await w.get('[data-testid="degradation-enabled"]').setValue(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).toHaveBeenCalledWith(expect.objectContaining({degradation_action:expect.objectContaining({enabled:true,threshold_percent:40,limit_concurrency:limitConcurrency,limit_models:limitModels,limit_groups:limitGroups,group_ids:limitGroups?[5]:[]})}))
 const payload = vi.mocked(saveAutoConfig).mock.calls[0][0]
 w.unmount(); vi.mocked(getAutoConfig).mockResolvedValueOnce(payload)
 const reloaded = mount(AutoConfigView); await flushPromises()
 expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-limit-concurrency"]').element.checked).toBe(limitConcurrency)
 expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-limit-models"]').element.checked).toBe(limitModels)
 expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-limit-groups"]').element.checked).toBe(limitGroups)
 if (limitGroups) expect(reloaded.get<HTMLInputElement>('[data-testid="degradation-group-5"]').element.checked).toBe(true)
 reloaded.unmount()
})

it('rejects enabling with no actions or with no groups selected, then saves groups alone', async () => {
 const w = mount(AutoConfigView); await flushPromises()
 await w.get('[data-testid="degradation-threshold"]').setValue('50')
 await w.get('[data-testid="degradation-limit-concurrency"]').setValue(false)
 await w.get('[data-testid="degradation-limit-models"]').setValue(false)
 await w.get('[data-testid="degradation-enabled"]').setValue(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).not.toHaveBeenCalled()
 await w.get('[data-testid="degradation-limit-groups"]').setValue(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).not.toHaveBeenCalled()
 await w.get('[data-testid="degradation-group-6"]').setValue(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(saveAutoConfig).toHaveBeenCalledWith(expect.objectContaining({degradation_action:expect.objectContaining({limit_concurrency:false,limit_models:false,limit_groups:true,group_ids:[6]})}))
 w.unmount()
})

it('keeps old action defaults and ignores inactive action parameters during validation', () => {
 const old = oauthDegradationPayload({enabled:true,threshold_percent:20,target_concurrency:10,models:[]})
 expect(old).toMatchObject({limit_concurrency:true,limit_models:true,limit_groups:false,group_ids:[]})
 const groupsOnly = {...defaultOAuthDegradationConfig(),enabled:true,threshold_percent:20,limit_concurrency:false,limit_models:false,limit_groups:true,group_ids:[5],target_concurrency:0,models:['invalid *']}
 expect(oauthDegradationError(groupsOnly)).toBe(false)
 expect(oauthDegradationError({...groupsOnly,limit_concurrency:true})).toBe(true)
 expect(oauthDegradationError({...groupsOnly,limit_models:true})).toBe(true)
 expect(oauthDegradationError({...groupsOnly,group_ids:[5,5]})).toBe(true)
 expect(oauthDegradationError({...groupsOnly,group_ids:[-1]})).toBe(true)
 expect(oauthDegradationError({...groupsOnly,group_ids:Array.from({length:100}, (_,i)=>i+1)})).toBe(false)
 expect(oauthDegradationError({...groupsOnly,group_ids:Array.from({length:101}, (_,i)=>i+1)})).toBe(true)
})

it('retains unavailable saved group IDs until explicitly unchecked', async () => {
 vi.mocked(getAutoConfig).mockResolvedValueOnce({...baseline,degradation_action:{...defaultOAuthDegradationConfig(),enabled:true,threshold_percent:50,limit_concurrency:false,limit_models:false,limit_groups:true,group_ids:[5,999]}})
 const w = mount(AutoConfigView); await flushPromises()
 expect(w.get<HTMLInputElement>('[data-testid="degradation-group-999"]').element.checked).toBe(true)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(vi.mocked(saveAutoConfig).mock.lastCall?.[0].degradation_action?.group_ids).toEqual([5,999])
 await w.get('[data-testid="degradation-group-999"]').setValue(false)
 await w.get('form').trigger('submit'); await flushPromises()
 expect(vi.mocked(saveAutoConfig).mock.lastCall?.[0].degradation_action?.group_ids).toEqual([5])
 w.unmount()
})
