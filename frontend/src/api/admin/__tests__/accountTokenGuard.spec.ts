import { describe, expect, it, vi } from 'vitest'
import { apiClient } from '../../client'
import {
  formatTokenGuardReloginText,
  parseTwoFALoginText,
  parseTokenGuardReloginText,
  startTwoFALogin, getTwoFALogin
} from '../accountTokenGuard'

vi.mock('../../client', () => ({ apiClient: { post: vi.fn(), get: vi.fn() } }))

describe('token guard re-login credential format', () => {
  it('parses the account----password----2FA format', () => {
    expect(parseTokenGuardReloginText(
      'user@example.com----p,a,s,s----JBSWY3DPEHPK3PXP\nsecond@example.com----secret----'
    )).toEqual([
      { email: 'user@example.com', password: 'p,a,s,s', mfa_secret: 'JBSWY3DPEHPK3PXP' },
      { email: 'second@example.com', password: 'secret', mfa_secret: '' }
    ])
  })

  it('keeps reading legacy comma-separated entries and formats them with the new separator', () => {
    const accounts = parseTokenGuardReloginText('user@example.com,password,JBSWY3DPEHPK3PXP')
    expect(formatTokenGuardReloginText(accounts)).toBe('user@example.com----password----JBSWY3DPEHPK3PXP')
  })
})


describe('initial 2FA login input', () => {
  it('defaults new UI logins to credential operations', async () => {
    vi.mocked(apiClient.post).mockResolvedValue({ data: { id: 'job', status: 'running' } })
    const entry = { email: 'user@example.com', password: 'test-password', mfa_secret: 'test-secret' }
    await startTwoFALogin(entry)
    expect(apiClient.post).toHaveBeenCalledWith('/admin/account-ops/token-guard/two-fa-login', {
      ...entry, credential_target: 'operations',
    }, undefined)
  })
  it('forwards the batch cancellation signal to login start and polling', async () => {
    const controller = new AbortController()
    const options = { signal: controller.signal }
    const entry = { email: 'fixture@example.test', password: 'synthetic', mfa_secret: 'synthetic' }
    vi.mocked(apiClient.post).mockResolvedValue({ data: { id: 'fixture', status: 'running' } })
    vi.mocked(apiClient.get).mockResolvedValue({ data: { id: 'fixture', status: 'running' } })
    await startTwoFALogin(entry, options)
    await getTwoFALogin('fixture', options)
    expect(apiClient.post).toHaveBeenLastCalledWith('/admin/account-ops/token-guard/two-fa-login', { ...entry, credential_target: 'operations' }, options)
    expect(apiClient.get).toHaveBeenLastCalledWith('/admin/account-ops/token-guard/two-fa-login/fixture', options)
    controller.abort()
    expect(options.signal.aborted).toBe(true)
  })
  it('supports both separators and passwords containing commas', () => {
    expect(parseTwoFALoginText('USER@example.com----p,a!ss----SECRET\nsecond@example.com,password,SECRET')).toEqual([
      { email: 'user@example.com', password: 'p,a!ss', mfa_secret: 'SECRET' },
      { email: 'second@example.com', password: 'password', mfa_secret: 'SECRET' }
    ])
  })
  it.each(['', 'bad', 'u@example.com----p----', 'u@example.com----p----s----extra', 'u@example.com----p----s\nU@example.com----p----s'])('rejects invalid or duplicate entries', input => {
    expect(() => parseTwoFALoginText(input)).toThrow('invalid_batch')
  })
})
