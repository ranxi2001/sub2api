import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dialog from '../AccountPerformanceDialog.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import { emptyPerformanceConfig, type AccountPerformance } from '@/types/accountPerformance'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'

const api=vi.hoisted(()=>({getPerformance:vi.fn(),getPerformanceEvents:vi.fn(),savePerformance:vi.fn(),releasePerformance:vi.fn()}))
vi.mock('@/api/admin',()=>({adminAPI:{accounts:api}}))
let wrappers:VueWrapper[]=[]
const metric:AccountPerformance={account_id:7,avg_tps:null,degradation_percent:null,avg_ttft_ms:null,tps_samples:0,ttft_samples:0,quality_samples:0,config:emptyPerformanceConfig(),revision:4,paused:true,pause_generation:2,paused_at:'2026-11-02T00:00:00Z',reason:'tps',window_start:'2026-11-01T23:45:00Z',window_end:'2026-11-02T00:00:00Z'}
async function open(locale='en',noticeUserId?:number,pinia=createPinia()){
 const w=mount(Dialog,{props:{accountId:7,accountName:'Synthetic account',noticeUserId},global:{plugins:[pinia,createI18n({legacy:false,locale,messages:{en,zh}})],stubs:{BaseDialog:{template:'<section><slot/><slot name="footer"/></section>'}}}})
 wrappers.push(w);await flushPromises();return w
}
beforeEach(()=>{localStorage.clear();vi.resetAllMocks();api.getPerformance.mockResolvedValue({7:structuredClone(metric)});api.getPerformanceEvents.mockResolvedValue([]);api.savePerformance.mockResolvedValue(undefined);api.releasePerformance.mockResolvedValue(undefined)})
afterEach(()=>{wrappers.forEach(w=>w.unmount());wrappers=[]})
describe('Account performance dialog',()=>{
 it('sends all directional thresholds and exact revision, with blank bounds as null',async()=>{
  const w=await open()
  await w.get('[data-testid="monitor-enabled"]').setValue(true)
  await w.get('[data-testid="tps-min"]').setValue('10')
  await w.get('[data-testid="degradation-max"]').setValue('25')
  await w.get('[data-testid="ttft-max"]').setValue('5000')
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(api.savePerformance).toHaveBeenCalledWith(7,{enabled:true,tps:{min:10,max:null},degradation:{min:null,max:25},ttft:{min:null,max:5000},upstream_error:{min:null,max:null}},4)
  expect(w.emitted('updated')).toHaveLength(1)
 })
 it('rejects inverted or invalid percentage bounds',async()=>{
  const w=await open()
  await w.get('[data-testid="tps-min"]').setValue('20');await w.get('[data-testid="tps-max"]').setValue('10')
  await w.get('[data-testid="performance-save"]').trigger('click');expect(api.savePerformance).not.toHaveBeenCalled()
  await w.get('[data-testid="tps-max"]').setValue('30');await w.get('[data-testid="degradation-max"]').setValue('101')
  await w.get('[data-testid="performance-save"]').trigger('click');expect(api.savePerformance).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('0–100')
 })
 it('loads and saves upstream HTTP bounds alongside existing thresholds with exact revision',async()=>{
  const configured=structuredClone(metric)
  configured.config={enabled:true,tps:{min:2,max:40},degradation:{min:null,max:25},ttft:{min:null,max:5000},upstream_error:{min:0,max:10}}
  configured.upstream_error={percent:50,total:2,failures:1,source:'process_local_http_responses',observed_since:'2026-11-02T00:00:00Z',window_complete:false}
  api.getPerformance.mockResolvedValue({7:configured})
  const w=await open()
  expect(w.get<HTMLInputElement>('[data-testid="upstream_error-min"]').element.value).toBe('0')
  expect(w.get<HTMLInputElement>('[data-testid="upstream_error-max"]').element.value).toBe('10')
  expect(w.get('[data-testid="upstream_error-max"]').attributes('max')).toBe('100')
  expect(w.get('[data-testid="upstream-threshold-samples"]').text()).toContain('1 / observed responses 2')
  await w.get('[data-testid="upstream_error-max"]').setValue('12.5')
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(api.savePerformance).toHaveBeenCalledWith(7,{...configured.config,upstream_error:{min:0,max:12.5}},4)
 })
 it('defaults older responses without upstream bounds to off and preserves their other bounds',async()=>{
  const config={enabled:true,tps:{min:5,max:50},degradation:{min:0,max:20},ttft:{min:null,max:3000}}
  api.getPerformance.mockResolvedValue({7:{...structuredClone(metric),config}})
  const w=await open()
  expect(w.get<HTMLInputElement>('[data-testid="upstream_error-min"]').element.value).toBe('')
  expect(w.get<HTMLInputElement>('[data-testid="upstream_error-max"]').element.value).toBe('')
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(api.savePerformance).toHaveBeenCalledWith(7,{...config,upstream_error:{min:null,max:null}},4)
 })
 it.each([[-0.1,''],['',100.1],[50,40]])('rejects invalid upstream percentage bounds %s / %s',async(min,max)=>{
  const w=await open()
  await w.get('[data-testid="upstream_error-min"]').setValue(min)
  await w.get('[data-testid="upstream_error-max"]').setValue(max)
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(api.savePerformance).not.toHaveBeenCalled()
  expect(w.get('[role="alert"]').text()).toContain('0–100')
 })
 it.each([0,100])('accepts equal upstream percentage bounds at %s without treating zero as blank',async(value)=>{
  const w=await open()
  await w.get('[data-testid="upstream_error-min"]').setValue(value)
  await w.get('[data-testid="upstream_error-max"]').setValue(value)
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(api.savePerformance).toHaveBeenCalledWith(7,expect.objectContaining({upstream_error:{min:value,max:value}}),4)
 })
 it.each(['en','zh'])('explains process-local evidence and shared pause boundaries in %s',async(locale)=>{
  const w=await open(locale)
  const note=w.get('[data-testid="upstream-threshold-note"]').text()
  for(const text of locale==='zh'
   ? ['4xx/5xx','2xx–5xx','历史不完整时仍按已观测样本评估','缺样本不触发','真实0%','仅运行监控任务的实例','网关实例样本不会自动汇入','全站','WebSocket','HTTP 200']
   : ['4xx/5xx','2xx–5xx','Partial history is still evaluated','Missing samples do not trigger','measured 0%','Only instances running monitoring jobs','Gateway samples are not automatically merged','across the site','WebSocket','HTTP 200'])expect(note).toContain(text)
  expect(w.get('[data-testid="upstream-threshold-samples"]').text()).toContain('—')
 })
 it('announces thresholds separately from the dismissed display feature and persists dismissal across dialog instances',async()=>{
  const A=NEW_FEATURE_NOTICES.accountUpstreamHTTPErrorRate
  const B=NEW_FEATURE_NOTICES.accountUpstreamHTTPErrorThreshold
  const C='future-performance-feature-c'
  expect(B).not.toBe(A)
  expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
  const pinia=createPinia()
  const notices=useNewFeatureNoticeStore(pinia)
  notices.dismiss(41,A)
  const first=await open('en',41,pinia),second=await open('en',41,pinia)
  expect(first.get('[data-metric="upstream"]').find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  expect(second.get('[data-threshold="upstream_error"]').find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  await first.get('[data-threshold="upstream_error"] [data-testid="new-feature-badge"]').trigger('click')
  expect(second.get('[data-threshold="upstream_error"]').find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  first.unmount();second.unmount();wrappers=[]
  const reloadedPinia=createPinia()
  const reopened=await open('en',41,reloadedPinia)
  expect(reopened.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  await reopened.setProps({noticeUserId:42})
  expect(reopened.get('[data-threshold="upstream_error"]').find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  await reopened.setProps({noticeUserId:41})
  expect(reopened.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  const future=mount(NewFeatureBadge,{props:{noticeId:C,userId:41},global:{plugins:[reloadedPinia,createI18n({legacy:false,locale:'en',messages:{en}})]}})
  wrappers.push(future)
  expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  const reloaded=useNewFeatureNoticeStore(reloadedPinia)
  expect(reloaded.isDismissed(41,A)).toBe(true)
  expect(reloaded.isDismissed(41,B)).toBe(true)
 })
 it('release uses ownership generation and never calls the manual scheduling API',async()=>{
  const w=await open();await w.get('[data-testid="performance-release"]').trigger('click');await flushPromises()
  expect(api.releasePerformance).toHaveBeenCalledWith(7,4,2)
 })
 it('retains edits after a conflict and requires reload before retry',async()=>{
  api.savePerformance.mockRejectedValue({response:{status:409}})
  const w=await open();await w.get('[data-testid="tps-min"]').setValue('10')
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(w.get('[role="alert"]').text()).toContain('Reload')
  expect(w.get<HTMLInputElement>('[data-testid="tps-min"]').element.value).toBe('10')
  expect(w.emitted('updated')).toBeUndefined()
 })
 it('uses Chinese probe-round wording and never presents absent samples as zero',async()=>{
  const w=await open('zh');expect(w.text()).toContain('探针轮次降智率');expect(w.text()).toContain('并非全部业务请求')
  expect(w.findAll('[data-metric]').every(r=>r.text().includes('未采集'))).toBe(true)
 })
 it.each(['en','zh'])('shows the actual response window in metric labels, bounds and semantics in %s',async(locale)=>{
  api.getPerformance.mockResolvedValue({7:{...structuredClone(metric),window_start:'2026-11-01T22:00:00Z'}})
  const w=await open(locale)
  expect(w.text()).toContain('120m')
  expect(w.text()).toContain(locale==='zh'?'最近120分钟':'Rolling 120 minutes')
  expect(w.get('[data-testid="degradation-max"]').attributes('aria-label')).toContain('120m')
  expect(w.text()).not.toContain('15m')
 })
})
