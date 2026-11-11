import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AxiosAdapter, InternalAxiosRequestConfig } from 'axios'
import OpenAITwoFAImport from '../OpenAITwoFAImport.vue'
import { apiClient } from '@/api/client'
import { deleteTwoFALogin, getTwoFALogin, startTwoFALogin } from '@/api/admin/accountTokenGuard'
import type { TwoFALoginJob } from '@/api/admin/accountTokenGuard'

vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/credentialEncryption', () => ({
  getCredentialEncryption: vi.fn().mockResolvedValue({ configured: true, source: 'server_config' }),
  initializeCredentialEncryption: vi.fn(),
}))
vi.mock('@/api/admin/accountTokenGuard', async original => ({
  ...await original<typeof import('@/api/admin/accountTokenGuard')>(),
  startTwoFALogin: vi.fn(), getTwoFALogin: vi.fn(), deleteTwoFALogin: vi.fn(),
}))
enableAutoUnmount(afterEach)
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(yes => { resolve = yes })
  return { promise, resolve }
}
const emails = ['a', 'b', 'c', 'd', 'e'].map(name => `${name}@example.test`)
const input = emails.map(email => `${email}----synthetic-password----synthetic-secret`).join('\n')
const success = (id: string): TwoFALoginJob => ({ id, status: 'succeeded', credential: { synthetic_account: id } })
async function begin(wrapper: ReturnType<typeof mount>) {
  await flushPromises()
  await wrapper.get('textarea').setValue(input)
  await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
  await flushPromises()
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(startTwoFALogin).mockImplementation(async entry => ({ id: entry.email, status: 'running' }))
  vi.mocked(getTwoFALogin).mockImplementation(async id => success(id))
  vi.mocked(deleteTwoFALogin).mockResolvedValue()
})

describe('mounted 2FA ownership fences', () => {
  it('retains live polling-failed jobs inside the three-slot budget through explicit Retry', async () => {
    const live = new Set<string>()
    let peak = 0
    vi.mocked(startTwoFALogin).mockImplementation(async entry => {
      expect(live.has(entry.email)).toBe(false)
      live.add(entry.email)
      peak = Math.max(peak, live.size)
      return { id: entry.email, status: 'running' }
    })
    vi.mocked(deleteTwoFALogin).mockImplementation(async id => { live.delete(id) })
    vi.mocked(getTwoFALogin).mockRejectedValue(Error('synthetic polling transport failure'))
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential: vi.fn().mockResolvedValue('created') } })
    await begin(wrapper)
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    expect(live.size).toBe(3)
    expect(wrapper.emitted('busy')).toEqual([[true]])
    // A failed read does not prove the detached backend job has terminated.
    await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    expect(getTwoFALogin).toHaveBeenCalledTimes(6)
    vi.mocked(getTwoFALogin).mockImplementation(async id => success(id))
    await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(5)
    expect(live.size).toBe(0)
    expect(peak).toBe(3)
  })

  it('keeps Stop busy through deferred DELETEs and admits Retry only after retirement', async () => {
    vi.useFakeTimers()
    try {
      const deletions = new Map(emails.map(email => [email, deferred<void>()]))
      const live = new Set<string>()
      let peak = 0
      vi.mocked(startTwoFALogin).mockImplementation(async entry => {
        expect(live.has(entry.email)).toBe(false)
        live.add(entry.email);peak = Math.max(peak, live.size)
        return { id: entry.email, status: 'running' }
      })
      vi.mocked(getTwoFALogin).mockImplementation(async id => ({ id, status: 'running' }))
      vi.mocked(deleteTwoFALogin).mockImplementation(async id => { await deletions.get(id)!.promise;live.delete(id) })
      const wrapper = mount(OpenAITwoFAImport, { props: { importCredential: vi.fn().mockResolvedValue('created') } })
      await begin(wrapper)
      await wrapper.get('button').trigger('click')
      await flushPromises()
      expect(deleteTwoFALogin).toHaveBeenCalledTimes(3)
      expect(wrapper.find('[data-testid="two-fa-start"]').exists()).toBe(false)
      expect(wrapper.emitted('busy')).toEqual([[true]])
      expect(live.size).toBe(3)
      deletions.get(emails[0]!)!.resolve()
      await flushPromises()
      expect(wrapper.find('[data-testid="two-fa-start"]').exists()).toBe(false)
      expect(startTwoFALogin).toHaveBeenCalledTimes(3)
      for (const deletion of deletions.values()) deletion.resolve()
      await flushPromises()
      expect(live.size).toBe(0)
      expect(wrapper.emitted('busy')).toEqual([[true], [false]])
      vi.mocked(getTwoFALogin).mockImplementation(async id => success(id))
      await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
      await flushPromises()
      expect(startTwoFALogin).toHaveBeenCalledTimes(8)
      expect(live.size).toBe(0)
      expect(peak).toBe(3)
      wrapper.unmount()
    } finally { vi.useRealTimers() }
  })

  it.each([503, 404])('fences failed DELETE (%i) across Retry and refuses reset until cleanup is confirmed', async status => {
    vi.useFakeTimers()
    try {
      vi.mocked(getTwoFALogin).mockImplementation(async id => ({ id, status: 'running' }))
      vi.mocked(deleteTwoFALogin).mockRejectedValue({ status })
      const wrapper = mount(OpenAITwoFAImport, { props: { importCredential: vi.fn().mockResolvedValue('created') } })
      await begin(wrapper)
      await wrapper.get('button').trigger('click')
      await flushPromises()
      expect(deleteTwoFALogin).toHaveBeenCalledTimes(3)
      expect(wrapper.findAll('button').at(-1)!.attributes('disabled')).toBeDefined()
      await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
      await flushPromises()
      expect(deleteTwoFALogin).toHaveBeenCalledTimes(6)
      expect(startTwoFALogin).toHaveBeenCalledTimes(3)
      expect(getTwoFALogin).toHaveBeenCalledTimes(3)
      expect(wrapper.findAll('button').at(-1)!.attributes('disabled')).toBeDefined()
      vi.mocked(deleteTwoFALogin).mockResolvedValue()
      vi.mocked(getTwoFALogin).mockImplementation(async id => success(id))
      await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
      await flushPromises()
      expect(startTwoFALogin).toHaveBeenCalledTimes(8)
      expect(wrapper.text()).not.toContain('tokenGuard.twoFA.states.failed')
      wrapper.unmount()
    } finally { vi.useRealTimers() }
  })

  it('recovers accepted start IDs through real Axios after Stop and waits for deferred retirement', async () => {
    const actual = await vi.importActual<typeof import('@/api/admin/accountTokenGuard')>('@/api/admin/accountTokenGuard')
    const adapterBefore = apiClient.defaults.adapter
    const responses: { config: InternalAxiosRequestConfig; gate: ReturnType<typeof deferred<void>>; email: string }[] = []
    const retirement = deferred<void>()
    const live = new Set<string>()
    const adapter: AxiosAdapter = async config => {
      const { email } = JSON.parse(config.data)
      live.add(email)
      const gate = deferred<void>()
      responses.push({ config, gate, email })
      await gate.promise
      // Use the real Axios dispatch/cancellation machinery. On the reviewed
      // implementation an aborted signal discards this accepted start ID.
      return { config, status: 202, statusText: 'Accepted', headers: {}, data: { code: 0, data: { id: email, status: 'running' } } }
    }
    apiClient.defaults.adapter = adapter
    vi.mocked(startTwoFALogin).mockImplementation(actual.startTwoFALogin)
    vi.mocked(deleteTwoFALogin).mockImplementation(async id => { await retirement.promise;live.delete(id) })
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential: vi.fn().mockResolvedValue('created') } })
    try {
      await begin(wrapper)
      expect(responses).toHaveLength(3)
      await wrapper.get('button').trigger('click')
      await flushPromises()
      for (const response of responses) response.gate.resolve()
      await flushPromises()
      expect(deleteTwoFALogin).toHaveBeenCalledTimes(3)
      expect(vi.mocked(deleteTwoFALogin).mock.calls.map(call => call[0]).sort()).toEqual(emails.slice(0, 3))
      expect(live.size).toBe(3)
      expect(wrapper.find('[data-testid="two-fa-start"]').exists()).toBe(false)
      expect(getTwoFALogin).not.toHaveBeenCalled()
      retirement.resolve()
      await flushPromises()
      expect(live.size).toBe(0)
      expect(startTwoFALogin).toHaveBeenCalledTimes(3)
      expect(wrapper.emitted('busy')).toEqual([[true], [false]])
    } finally {
      retirement.resolve()
      for (const response of responses) response.gate.resolve()
      wrapper.unmount()
      await flushPromises()
      apiClient.defaults.adapter = adapterBefore
    }
  })
})
