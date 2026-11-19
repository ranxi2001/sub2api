import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { describe, it, expect, vi } from 'vitest'
import ChannelConfig from '../ChannelConfigView.vue'
import { profitScopeFromQuery } from '../channelConfigScope'
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}))
vi.mock('@/stores/auth',()=>({useAuthStore:()=>({user:{id:1}})}))
vi.mock('@/components/layout/AppLayout.vue',()=>({default:{template:'<main><slot /></main>'}}))
vi.mock('@/components/admin/operations/SmartOpsNav.vue',()=>({default:{template:'<nav />'}}))
vi.mock('@/components/common/NewFeatureBadge.vue',()=>({default:{template:'<span />'}}))
vi.mock('@/components/admin/operations/UpstreamChannelManager.vue',()=>({default:{template:'<div data-testid="manager" />'}}))
vi.mock('@/components/admin/operations/ProfitAnalytics.vue',()=>({default:{template:'<div data-testid="profit" />'}}))
vi.mock('../AccountOpsView.vue',()=>({default:{template:'<div data-testid="accounts" />'}}))
describe('channel configuration',()=>{
 it('preserves legacy query filters without adopting arbitrary fields',()=>{
  expect(profitScopeFromQuery({channel_id:'7',account_id:'42',page:'2',model:'fixture',from:'2026-11-01T00:00:00Z',panel:'profit',token:'discard'})).toEqual({channel_id:7,account_id:42,page:2,model:'fixture',from:'2026-11-01T00:00:00Z'})
 })
 it('loads configuration first and mounts profit only when expanded, retaining it on collapse',async()=>{
  const router=createRouter({history:createMemoryHistory(),routes:[{path:'/admin/channel-config',component:ChannelConfig}]})
  await router.push('/admin/channel-config');await router.isReady()
  const w=mount(ChannelConfig,{global:{plugins:[router]}});await flushPromises()
  await vi.waitFor(()=>expect(w.find('[data-testid="manager"]').exists()).toBe(true))
  expect(w.find('[data-testid="profit"]').exists()).toBe(false)
  expect(w.find('[data-testid="accounts"]').exists()).toBe(false)
  const panel=w.get('[data-testid="config-panel-profit"]');(panel.element as HTMLDetailsElement).open=true;await panel.trigger('toggle');await flushPromises()
  await vi.waitFor(()=>expect(w.find('[data-testid="profit"]').exists()).toBe(true))
  ;(panel.element as HTMLDetailsElement).open=false;await panel.trigger('toggle');await flushPromises()
  await vi.waitFor(()=>expect(w.find('[data-testid="profit"]').exists()).toBe(true))
  w.unmount()
 })
})
