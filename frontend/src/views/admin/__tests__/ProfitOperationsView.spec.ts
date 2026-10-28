import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import ProfitOperationsView from '../ProfitOperationsView.vue'
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('independent profit operations', () => {
 it('shows the persisted request accounting formula and current configuration estimate boundary', async () => {
  const w = mount(ProfitOperationsView); await flushPromises()
  expect(w.text()).toContain('profitOps.title')
  expect(w.text()).toContain('profitOps.formula')
  expect(w.text()).toContain('profitOps.attribution')
  expect(w.find('[data-testid="profit-filters"]').exists()).toBe(true)
  w.unmount()
 })
})
