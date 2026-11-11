import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import OpenAITwoFAImport from '../OpenAITwoFAImport.vue'
import { deleteTwoFALogin, getTwoFALogin, startTwoFALogin } from '@/api/admin/accountTokenGuard'
import type { TokenGuardReloginAccount, TwoFALoginJob } from '@/api/admin/accountTokenGuard'

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
const emails = ['a', 'b', 'c', 'd', 'e'].map(name => `${name}@example.test`)
const input = emails.map((email, i) => `${email}----password-${i}----secret-${i}`).join('\n')
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function success(email: string): TwoFALoginJob {
  return { id: email, status: 'succeeded', credential: { synthetic_account: email } }
}
async function begin(wrapper: ReturnType<typeof mount>, text = input) {
  await flushPromises()
  await wrapper.get('textarea').setValue(text)
  await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
  await flushPromises()
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(startTwoFALogin).mockImplementation(async entry => ({ id: entry.email, status: 'running' }))
  vi.mocked(getTwoFALogin).mockImplementation(async id => success(id))
  vi.mocked(deleteTwoFALogin).mockResolvedValue()
})

describe('mounted 2FA bounded account pipelines', () => {
  it('overlaps exactly three complete pipelines, holds slots through enrollment, and keeps input order', async () => {
    const logins = new Map(emails.map(email => [email, deferred<TwoFALoginJob>()]))
    const imports = new Map(emails.map(email => [email, deferred<void>()]))
    const enrollments = new Map(emails.map(email => [email, deferred<void>()]))
    const events: string[] = []
    let active = 0
    let peak = 0
    vi.mocked(startTwoFALogin).mockImplementation(async entry => {
      active++
      peak = Math.max(peak, active)
      events.push(`login:${entry.email}`)
      return { id: entry.email, status: 'running' }
    })
    vi.mocked(getTwoFALogin).mockImplementation(id => logins.get(id)!.promise)
    const importCredential = vi.fn(async (credential: Record<string, unknown>, email: string, login: TokenGuardReloginAccount) => {
      expect(credential).toEqual({ synthetic_account: email })
      expect(login).toEqual({ email, password: `password-${emails.indexOf(email)}`, mfa_secret: `secret-${emails.indexOf(email)}` })
      events.push(`import:${email}`)
      await imports.get(email)!.promise
      events.push(`enroll:${email}`)
      await enrollments.get(email)!.promise
      active--
      return email === emails[1] ? 'skipped' as const : 'created' as const
    })
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await begin(wrapper)
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    expect(importCredential).not.toHaveBeenCalled()
    expect(wrapper.findAll('li').map(row => row.find('span').text())).toEqual(emails.map((email, i) => `${i + 1}. ${email}`))

    // Complete the second account first. Neither login completion nor import
    // completion alone may release the account's bounded worker slot.
    logins.get(emails[1]!)!.resolve(success(emails[1]!))
    await flushPromises()
    expect(importCredential).toHaveBeenCalledTimes(1)
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    imports.get(emails[1]!)!.resolve()
    await flushPromises()
    expect(events).toContain(`enroll:${emails[1]}`)
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    enrollments.get(emails[1]!)!.resolve()
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(4)
    for (const email of emails) {
      logins.get(email)!.resolve(success(email))
      imports.get(email)!.resolve()
      enrollments.get(email)!.resolve()
    }
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(5)
    expect(importCredential).toHaveBeenCalledTimes(5)
    expect(peak).toBe(3)
    expect(active).toBe(0)
    for (const email of emails) {
      expect(events.indexOf(`login:${email}`)).toBeLessThan(events.indexOf(`import:${email}`))
      expect(events.indexOf(`import:${email}`)).toBeLessThan(events.indexOf(`enroll:${email}`))
    }
    expect(wrapper.findAll('li').map(row => row.find('span').text())).toEqual(emails.map((email, i) => `${i + 1}. ${email}`))
    expect(wrapper.emitted('busy')).toEqual([[true], [false]])
  })

  it('isolates start, provider and import failures without automatically replaying side effects', async () => {
    vi.mocked(startTwoFALogin).mockImplementation(async entry => {
      if (entry.email === emails[0]) throw Error('synthetic start failure')
      return { id: entry.email, status: 'running' }
    })
    vi.mocked(getTwoFALogin).mockImplementation(async id => id === emails[1] ? { id, status: 'failed' } : success(id))
    const importCredential = vi.fn(async (_credential: Record<string, unknown>, email: string) => {
      if (email === emails[2]) throw Error('synthetic enrollment failure')
      return 'created' as const
    })
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await begin(wrapper)
    expect(startTwoFALogin).toHaveBeenCalledTimes(5)
    expect(importCredential.mock.calls.map(call => call[1])).toEqual(expect.arrayContaining(emails.slice(2)))
    expect(importCredential).toHaveBeenCalledTimes(3)
    expect(wrapper.findAll('li').map(row => row.findAll('span')[1]!.text())).toEqual([
      'tokenGuard.twoFA.states.failed', 'tokenGuard.twoFA.states.failed',
      'tokenGuard.twoFA.states.importFailed', 'tokenGuard.twoFA.states.created', 'tokenGuard.twoFA.states.created',
    ])
    expect(wrapper.text()).not.toContain('synthetic enrollment failure')
  })

  it('rejects case-insensitive duplicate accounts atomically before any login', async () => {
    const importCredential = vi.fn().mockResolvedValue('created')
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await begin(wrapper, 'A@example.test----p----s\na@example.test----other----secret')
    expect(wrapper.get('[role="alert"]').text()).toContain('invalid')
    expect(startTwoFALogin).not.toHaveBeenCalled()
    expect(importCredential).not.toHaveBeenCalled()
  })

  it('ignores overlapping start clicks while the first batch owns its workers', async () => {
    const login = deferred<TwoFALoginJob>()
    vi.mocked(getTwoFALogin).mockReturnValue(login.promise)
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential: vi.fn().mockResolvedValue('created') } })
    await flushPromises()
    await wrapper.get('textarea').setValue(input)
    const button = wrapper.get('[data-testid="two-fa-start"]')
    const first = button.trigger('click')
    const second = button.trigger('click')
    await Promise.all([first, second])
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    expect(wrapper.emitted('busy')).toEqual([[true]])
    wrapper.unmount()
    login.resolve({ id: 'unused', status: 'failed' })
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
  })

  it.each(['stop', 'unmount'])('%s recovers late start IDs without aborting their POST or starting queued accounts', async action => {
    const starts = new Map(emails.map(email => [email, deferred<TwoFALoginJob>()]))
    vi.mocked(startTwoFALogin).mockImplementation(entry => starts.get(entry.email)!.promise)
    const importCredential = vi.fn().mockResolvedValue('created')
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await begin(wrapper)
    const requests = vi.mocked(startTwoFALogin).mock.calls as unknown as [unknown, { signal?: AbortSignal }?][]
    expect(requests).toHaveLength(3)
    if (action === 'stop') await wrapper.get('button').trigger('click')
    else wrapper.unmount()
    for (const [, request] of requests) expect(request?.signal).toBeUndefined()
    for (const email of emails) starts.get(email)!.resolve({ id: email, status: 'running' })
    await flushPromises()
    expect(getTwoFALogin).not.toHaveBeenCalled()
    expect(importCredential).not.toHaveBeenCalled()
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    expect(vi.mocked(deleteTwoFALogin).mock.calls.map(call => call[0]).sort()).toEqual(emails.slice(0, 3))
  })

  it('clears all polling delays immediately on stop without polling or dequeuing again', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(getTwoFALogin).mockImplementation(async id => ({ id, status: 'running' }))
      const importCredential = vi.fn().mockResolvedValue('created')
      const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
      await begin(wrapper)
      expect(vi.getTimerCount()).toBe(3)
      await wrapper.get('button').trigger('click')
      await flushPromises()
      expect(vi.getTimerCount()).toBe(0)
      expect(wrapper.emitted('busy')).toEqual([[true], [false]])
      await vi.advanceTimersByTimeAsync(3000)
      expect(getTwoFALogin).toHaveBeenCalledTimes(3)
      expect(startTwoFALogin).toHaveBeenCalledTimes(3)
      expect(importCredential).not.toHaveBeenCalled()
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it.each(['stop', 'unmount'])('%s propagates cancellation through active imports and keeps queued rows untouched', async action => {
    const imports = deferred<'created'>()
    const importCredential = vi.fn().mockReturnValue(imports.promise)
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await begin(wrapper)
    expect(importCredential).toHaveBeenCalledTimes(3)
    if (action === 'stop') await wrapper.get('button').trigger('click')
    else wrapper.unmount()
    for (const call of importCredential.mock.calls) expect((call[3] as AbortSignal).aborted).toBe(true)
    imports.resolve('created')
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    expect(importCredential).toHaveBeenCalledTimes(3)
  })
})
