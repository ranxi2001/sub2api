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
 it.each(['', '?panel=accounts', '?panel=channels', '?panel=profit'])('shows profit then combined channel management at %s', async query => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{path:'/admin/channel-config',component:ChannelConfig}] })
  await router.push('/admin/channel-config' + query)
  const w = mount(ChannelConfig, {global:{plugins:[router]}})
  await vi.waitFor(() => expect(w.find('[data-testid="accounts"]').exists()).toBe(true))
  const panels = w.get('[data-testid="channel-config-panels"]').element.children
  expect([...panels].map(p => [p.tagName, p.getAttribute('data-testid')])).toEqual([
   ['SECTION','config-panel-profit'], ['SECTION','config-panel-channels']
  ])
  expect(w.get('[data-testid="profit"]').isVisible()).toBe(true)
  expect(w.get('[data-testid="accounts"]').isVisible()).toBe(true)
  const directory = w.get('[data-testid="channel-directory"]')
  ;(directory.element as HTMLDetailsElement).open = true
  await directory.trigger('toggle'); await flushPromises()
  await vi.waitFor(() => expect(w.find('[data-testid="manager"]').exists()).toBe(true))
  w.unmount()
 })
})
