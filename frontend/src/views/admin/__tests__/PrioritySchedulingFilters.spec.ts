import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import PrioritySchedulingView from '../PrioritySchedulingView.vue'

const fixture = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => fixture.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/priorityScheduling', () => ({ getPriorityConfig: vi.fn(), savePriorityConfig: vi.fn(), getPrioritySnapshot: vi.fn(), getPriorityLatency: vi.fn(), getPriorityAnalysis: vi.fn(), recomputePriorityScheduling: vi.fn(), getPriorityStickyBindings: vi.fn(), clearPriorityStickyBindings: vi.fn() }))
vi.mock('@/api/admin/groups', () => ({ getAll: vi.fn().mockResolvedValue([{ id: 11, name: 'Fixture group' }]), getModelAllowlistCandidates: vi.fn().mockResolvedValue(['fixture-model', 'not-in-latest-pool']) }))
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => { fixture.auth = reactive({ user: { id: 1, role: 'admin' } }) })
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('priority explicit scope filters', () => {
  it('offers explicit model and group filters independently of the latest pool', async () => {
    wrapper = mount(PrioritySchedulingView)
    await flushPromises()
    expect(wrapper.find('[data-testid="priority-model-filter"]').exists(), 'administrators must be able to select the analysis model').toBe(true)
    expect(wrapper.find('[data-testid="priority-group-filter"]').exists(), 'administrators must be able to select the analysis group').toBe(true)
  })
})
