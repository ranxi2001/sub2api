import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, expect, it, vi } from 'vitest'
import ProfitOperationsView from '../ProfitOperationsView.vue'
import { readProfitOperations, type OperationsSnapshot } from '@/api/admin/profitOperations'
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: null }) }))
vi.mock('@/components/common/NewFeatureBadge.vue', () => ({ default: { template: '<span />' } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/profitOperations', () => ({ readProfitOperations: vi.fn() }))
vi.mock('@/api/admin/upstreamChannels', () => ({ listUpstreamChannels: vi.fn().mockResolvedValue({ items: [], total: 0 }) }))
beforeEach(() => vi.mocked(readProfitOperations).mockResolvedValue({ usage: { catalog: [], rows: [{ key: 'channel:7', name: '渠道A', assignment: 'explicit', channel_id: 7, channel_remark: '统一采购' }], accounts: [], summary: {} }, scope: {}, quality: { status: 'ready', data: { summary: {}, rows: [] } }, errors: { status: 'ready', data: { rows: [] } } } as unknown as OperationsSnapshot))
it('offers actual entity management and marks explicit ownership in the exact profit page', async () => {
 const w = mount(ProfitOperationsView); await flushPromises()
 expect(w.find('[data-testid="upstream-channel-manager"]').exists()).toBe(true)
 expect(w.text()).toContain('upstreamEntities.explicit')
 expect(w.text()).toContain('统一采购')
 w.unmount()
})

it('drills into an explicit ID and shows current members without requiring a URL filter', async () => {
 const next=await readProfitOperations({} as never)
 vi.mocked(readProfitOperations).mockClear()
 next.usage.members=[{id:41,name:'unknown endpoint',key:'channel:7',attribution:'current_explicit_channel',updated_at:'',deleted:false,multiplier:.25,cost_basis:'current_saved_multiplier'}]
 vi.mocked(readProfitOperations).mockResolvedValue(next)
 const w=mount(ProfitOperationsView);await flushPromises();await w.get('[data-testid="drill-row"]').trigger('click');await flushPromises()
 expect(readProfitOperations).toHaveBeenLastCalledWith(expect.objectContaining({channel_id:7,upstream:'',dimension:'account'}),expect.anything())
 expect(w.get('[data-testid="member-drill"]').text()).toContain('unknown endpoint');expect(w.get('[data-testid="profit-current-channel"]').text()).toContain('#7');w.unmount()
})
