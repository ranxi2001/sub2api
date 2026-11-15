import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import MatureProviderImport from '../MatureProviderImport.vue'

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ default: api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 1 } }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/matureProviders', () => ({
  listProviders: async () => ({ items: [{ id: 7, revision: 3, name: 'Example', site_type: 'sub2api', has_credential: true }], total: 1 }),
  refreshProvider: vi.fn(),
}))
beforeEach(() => {
  vi.clearAllMocks()
  api.get.mockImplementation(async (url: string) => ({ data: url.endsWith('/groups') ? { groups: [{ id: '4', name: 'Example group', multiplier: 0.25, key_name: 'Example key' }] } : { keys: [] } }))
})
describe('provider key creation failure classification', () => {
  it.each([
    ['PROVIDER_KEY_CREATE_FAILED', false, 'matureProviders.keyRejected'],
    ['PROVIDER_KEY_CREATE_UNCERTAIN', true, 'matureProviders.keyUncertain'],
    ['PROVIDER_KEY_CREATE_PENDING', true, 'matureProviders.keyUncertain'],
  ])('handles %s without repeating a remote write', async (reason, uncertain, message) => {
    api.post.mockRejectedValue({ status: 502, reason })
    const w = mount(MatureProviderImport, { props: { platform: 'openai' }, global: { stubs: { NewFeatureBadge: true } } })
    await flushPromises()
    await w.get('[data-testid="provider-choice"]').setValue('7'); await flushPromises()
    await w.get('[data-testid="provider-key-new"]').trigger('click'); await flushPromises()
    await w.get('[data-testid="provider-group-choice"]').setValue('4')
    await w.get('input[type="checkbox"]').setValue(true)
    const create = w.findAll('button').find(b => b.text() === 'matureProviders.create')!
    await create.trigger('click'); await flushPromises()
    expect(api.post).toHaveBeenCalledTimes(1)
    expect(w.findAll('[role="alert"]')[0]!.text(), 'definite refusals must remain distinct from uncertain writes').toBe(message)
    expect(w.get('input[type="checkbox"]').attributes('disabled') !== undefined).toBe(uncertain)
    expect(create.attributes('disabled')).toBeDefined()
    w.unmount()
  })
})
