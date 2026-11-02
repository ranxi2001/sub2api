import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dialog from '../AccountPerformanceDialog.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import { emptyPerformanceConfig, type AccountPerformance } from '@/types/accountPerformance'

const api=vi.hoisted(()=>({getPerformance:vi.fn(),getPerformanceEvents:vi.fn(),savePerformance:vi.fn(),releasePerformance:vi.fn()}))
vi.mock('@/api/admin',()=>({adminAPI:{accounts:api}}))
let wrappers:VueWrapper[]=[]
const metric:AccountPerformance={account_id:7,avg_tps:null,degradation_percent:null,avg_ttft_ms:null,tps_samples:0,ttft_samples:0,quality_samples:0,config:emptyPerformanceConfig(),revision:4,paused:true,pause_generation:2,paused_at:'2026-11-02T00:00:00Z',reason:'tps',window_start:'2026-11-01T23:45:00Z',window_end:'2026-11-02T00:00:00Z'}
async function open(locale='en'){
 const w=mount(Dialog,{props:{accountId:7,accountName:'Synthetic account'},global:{plugins:[createI18n({legacy:false,locale,messages:{en,zh}})],stubs:{BaseDialog:{template:'<section><slot/><slot name="footer"/></section>'}}}})
 wrappers.push(w);await flushPromises();return w
}
beforeEach(()=>{vi.resetAllMocks();api.getPerformance.mockResolvedValue({7:structuredClone(metric)});api.getPerformanceEvents.mockResolvedValue([]);api.savePerformance.mockResolvedValue(undefined);api.releasePerformance.mockResolvedValue(undefined)})
afterEach(()=>{wrappers.forEach(w=>w.unmount());wrappers=[]})
describe('Account performance dialog',()=>{
 it('sends all directional thresholds and exact revision, with blank bounds as null',async()=>{
  const w=await open()
  await w.get('[data-testid="monitor-enabled"]').setValue(true)
  await w.get('[data-testid="tps-min"]').setValue('10')
  await w.get('[data-testid="degradation-max"]').setValue('25')
  await w.get('[data-testid="ttft-max"]').setValue('5000')
  await w.get('[data-testid="performance-save"]').trigger('click');await flushPromises()
  expect(api.savePerformance).toHaveBeenCalledWith(7,{enabled:true,tps:{min:10,max:null},degradation:{min:null,max:25},ttft:{min:null,max:5000}},4)
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
