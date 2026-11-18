import { apiClient } from '../client'

export interface OpenAIReloginConfig {
  relogin_endpoint: string
  relogin_headers: Record<string, string> | null
}

export async function getReloginConfig(): Promise<OpenAIReloginConfig> {
  return (await apiClient.get('/admin/account-ops/relogin/config')).data
}

export async function saveReloginConfig(config: OpenAIReloginConfig): Promise<OpenAIReloginConfig> {
  return (await apiClient.put('/admin/account-ops/relogin/config', config)).data
}

export interface OpenAIReloginCredentials {
  email: string
  password: string
  mfa_secret: string
}

// Unlike the settings parser, imports must reject malformed lines rather than
// silently dropping an account from a batch.
export function parseTwoFALoginText(raw: string): OpenAIReloginCredentials[] {
  const lines = raw.split(/\r?\n/).map(line => line.trim()).filter(Boolean)
  if (!lines.length || lines.length > 100) throw new Error('invalid_batch')
  const seen = new Set<string>()
  return lines.map(line => {
    const parts = line.includes('----') ? line.split('----') : line.split(',')
    const [email = '', password = '', mfa = ''] = parts.map(part => part.trim())
    const normalizedEmail = email.toLowerCase()
    if (parts.length !== 3 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) ||
        !password || !mfa || seen.has(normalizedEmail)) throw new Error('invalid_batch')
    seen.add(normalizedEmail)
    return { email: normalizedEmail, password, mfa_secret: mfa }
  })
}

export interface TwoFALoginJob {
  id: string
  status: 'running' | 'succeeded' | 'failed'
  credential?: Record<string, unknown>
}

const twoFALoginPath = '/admin/account-ops/relogin/two-fa-login'

export async function startTwoFALogin(entry: OpenAIReloginCredentials, options?: { signal?: AbortSignal }): Promise<TwoFALoginJob> {
  return (await apiClient.post(twoFALoginPath, { ...entry, credential_target: 'operations' }, options)).data
}

export async function getTwoFALogin(id: string, options?: { signal?: AbortSignal }): Promise<TwoFALoginJob> {
  return (await apiClient.get(`${twoFALoginPath}/${id}`, options)).data
}

export async function deleteTwoFALogin(id: string): Promise<void> {
  await apiClient.delete(`${twoFALoginPath}/${id}`)
}
