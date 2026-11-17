import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import UpstreamChannelOperationsView from '../UpstreamChannelOperationsView.vue'

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ default: api }))
vi.mock('@/components/admin/operations/UpstreamAnalytics.vue', () => ({ default: { template: '<div>existing analytics<slot /></div>' } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 1 }, isAdmin: true }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const provider = { id: 7, revision: 3, name: 'Synthetic provider', site_type: 'sub2api', base_url: 'https://provider.example.invalid', notes: '', cost_multiplier: 0.25, status: 'unknown', metrics: { balance: null, today_spend: 0, total_spend: 3, total_recharge: null, currency: 'USD' }, has_credential: false }
describe('mature provider operations', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.get.mockResolvedValue({ data: { items: [provider], total: 1, page: 1, page_size: 20 } })
    api.post.mockResolvedValue({ data: provider })
    api.put.mockResolvedValue({ data: { ...provider, revision: 4 } })
  })
  it('manages persisted providers without inventing unknown financial values', async () => {
    const w = mount(UpstreamChannelOperationsView, { global: { stubs: { NewFeatureBadge: true } } })
    await flushPromises()
    expect(w.find('[data-testid="provider-add"]').exists(), 'channel operations must expose provider CRUD').toBe(true)
    expect(w.text()).toContain('Synthetic provider')
    expect(w.get('[data-testid="provider-7-balance"]').text()).toContain('matureProviders.unknown')
    expect(w.get('[data-testid="provider-7-total_recharge"]').text()).toContain('matureProviders.unknown')
    expect(w.get('[data-testid="provider-7-today_spend"]').text()).toContain('USD')
    await w.get('[data-testid="provider-add"]').trigger('click')
    await w.get('[data-testid="provider-name"]').setValue('New provider')
    await w.get('[data-testid="provider-url"]').setValue('https://new.example.invalid')
    await w.get('[data-testid="provider-save"]').trigger('submit')
    await flushPromises()
    expect(api.post).toHaveBeenCalledWith('/admin/providers', expect.objectContaining({ name: 'New provider', base_url: 'https://new.example.invalid', site_type: 'sub2api' }))
    expect(w.text()).toContain('existing analytics')
    w.unmount()
  })
  it('keeps the edit draft on a revision conflict and does not retry writes', async () => {
    api.put.mockRejectedValue({ status: 409, message: 'conflict' })
    const w = mount(UpstreamChannelOperationsView, { global: { stubs: { NewFeatureBadge: true } } })
    await flushPromises()
    await w.get('[data-testid="provider-7-edit"]').trigger('click')
    await w.get('[data-testid="provider-name"]').setValue('My unsaved change')
    await w.get('[data-testid="provider-save"]').trigger('submit')
    await flushPromises()
    expect(api.put).toHaveBeenCalledTimes(1)
    expect(api.put).toHaveBeenCalledWith('/admin/providers/7', expect.objectContaining({ revision: 3 }))
    expect(w.get('[role="alert"]').text()).toContain('matureProviders.conflict')
    expect((w.get('[data-testid="provider-name"]').element as HTMLInputElement).value).toBe('My unsaved change')
    w.unmount()
  })
  it('refreshes saved provider metrics once when channel operations opens', async () => {
    api.get.mockResolvedValue({ data: { items: [{ ...provider, has_credential: true }], total: 1, page: 1, page_size: 20 } })
    api.post.mockResolvedValue({ data: { ...provider, has_credential: true, revision: 4 } })
    const w = mount(UpstreamChannelOperationsView, { global: { stubs: { NewFeatureBadge: true } } })
    await flushPromises(); await flushPromises()
    expect(api.post).toHaveBeenCalledTimes(1)
    expect(api.post).toHaveBeenCalledWith('/admin/providers/7/refresh', { revision: 3 })
    w.unmount()
  })
})
