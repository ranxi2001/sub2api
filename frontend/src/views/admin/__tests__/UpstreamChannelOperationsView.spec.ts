import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import { readOperations, type OperationsSnapshot } from '@/api/admin/upstreamOperations'
import UpstreamChannelOperationsView from '../UpstreamChannelOperationsView.vue'
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/upstreamOperations', () => ({ readOperations: vi.fn() }))
describe('upstream channel definition', () => {
 it('discovers URLs without creating or fetching model-pricing channels, including idle accounts', async () => {
  vi.mocked(readOperations).mockResolvedValue({ scope: {}, usage: { summary: {}, rows: [], accounts: [], total: 0, catalog_total: 1, catalog: [{key:'https://idle.example:8443/tenant/v1',name:'https://idle.example:8443/tenant/v1',accounts:2}] }, quality: {status:'unsupported_scope'}, errors:{status:'query_failed'} } as unknown as OperationsSnapshot)
  const w=mount(UpstreamChannelOperationsView); await flushPromises()
  expect(w.text()).toContain('profitOps.channelTitle'); expect(w.text()).toContain('https://idle.example:8443/tenant/v1')
  expect(w.text()).toContain('profitOps.latencyNote'); expect(w.text()).toContain('profitOps.qualityNote'); expect(w.text()).toContain('profitOps.errorNote')
  expect(readOperations).toHaveBeenCalledWith('upstream',expect.objectContaining({dimension:'upstream'}),expect.anything())
  expect(w.find('[data-testid="new-channel"]').exists()).toBe(false)
  const idle=w.findAll('button').find(b=>b.text().includes('idle.example'))!;await idle.trigger('click');await flushPromises()
  expect(readOperations).toHaveBeenLastCalledWith('upstream',expect.objectContaining({upstream:'https://idle.example:8443/tenant/v1',account_id:0,dimension:'account'}),expect.anything())
  w.unmount()
 })
})
