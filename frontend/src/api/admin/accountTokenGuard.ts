import { apiClient } from '../client'

export interface TokenGuardReloginAccount {
  email: string
  password: string
  mfa_secret: string
}

// Unlike the settings parser, imports must reject malformed lines rather than
// silently dropping an account from a batch.
export function parseTwoFALoginText(raw: string): TokenGuardReloginAccount[] {
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

const twoFALoginPath = '/admin/account-ops/token-guard/two-fa-login'

export async function startTwoFALogin(entry: TokenGuardReloginAccount, options?: { signal?: AbortSignal }): Promise<TwoFALoginJob> {
  return (await apiClient.post(twoFALoginPath, { ...entry, credential_target: 'operations' }, options)).data
}

export async function getTwoFALogin(id: string, options?: { signal?: AbortSignal }): Promise<TwoFALoginJob> {
  return (await apiClient.get(`${twoFALoginPath}/${id}`, options)).data
}

export async function deleteTwoFALogin(id: string): Promise<void> {
  await apiClient.delete(`${twoFALoginPath}/${id}`)
}

export function parseTokenGuardReloginText(raw: string): TokenGuardReloginAccount[] {
  return raw
    .split(/\r?\n/)
    .map(line => line.trim())
    .filter(Boolean)
    .map(line => {
      const [email = '', password = '', mfa = ''] = line.includes('----')
        ? line.split('----', 3)
        : line.split(',')
      return { email: email.trim(), password: password.trim(), mfa_secret: mfa.trim() }
    })
    .filter(item => item.email && item.password)
}

export function formatTokenGuardReloginText(accounts: TokenGuardReloginAccount[] | undefined): string {
  return (accounts ?? [])
    .map(item => `${item.email}----${item.password}----${item.mfa_secret}`)
    .join('\n')
}

export interface TokenGuardConfig {
  enabled: boolean
  group_ids: number[]
  interval_seconds: number
  probe_endpoint: string
  probe_model: string
  probe_headers: Record<string, string>
  probe_timeout_seconds: number
  probe_concurrency: number
  max_probe_per_cycle: number
  auto_relogin: boolean
  relogin_endpoint: string
  relogin_headers: Record<string, string>
  relogin_accounts: TokenGuardReloginAccount[]
  restore_schedulable: boolean
  fail_streak_threshold: number
  bark_key: string
  notify_on_fix: boolean
  notify_on_fail: boolean
}

export interface TokenGuardAccountState {
  account_id: number
  account_name: string
  account_status: string
  schedulable: boolean
  probe_state: 'ok' | 'auth' | 'transient' | string
  probe_detail: string
  latency_ms: number
  fail_streak: number
  last_probe_at: string | null
  last_fix_at: string | null
  last_fix_action: string
  last_fix_result: string
  needs_relogin: boolean
  updated_at: string
}

export interface TokenGuardEvent {
  id: number
  account_id: number
  account_name: string
  kind: string
  detail: string
  latency_ms: number
  created_at: string
}

export interface TokenGuardStats {
  probed: number
  healthy: number
  auth_failed: number
  transient: number
  repaired: number
  state_fixed: number
  failed: number
  duration_ms: number
  started_at: number
}

export interface TokenGuardJob {
  id: string
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled' | string
  manual: boolean
  started_at: string | null
  finished_at: string | null
  total: number
  completed: number
  stats: TokenGuardStats
  error?: string
  cancel_requested?: boolean
}

export interface TokenGuardStatus {
  config: TokenGuardConfig
  accounts: TokenGuardAccountState[]
  events: TokenGuardEvent[]
  runtime: {
    running: boolean
    last_run: string | null
    last_message: string
    stats: TokenGuardStats
    job?: TokenGuardJob | null
  }
}

export async function getTokenGuardStatus(): Promise<TokenGuardStatus> {
  return (await apiClient.get('/admin/account-ops/token-guard/status')).data
}

export async function saveTokenGuardConfig(config: TokenGuardConfig): Promise<TokenGuardConfig> {
  return (await apiClient.put('/admin/account-ops/token-guard/config', config)).data
}

export async function runTokenGuard(): Promise<TokenGuardStats> {
  return (await apiClient.post('/admin/account-ops/token-guard/run')).data
}

export async function startTokenGuardRun(): Promise<TokenGuardJob> {
  return (await apiClient.post('/admin/account-ops/token-guard/run/start')).data
}

export async function getTokenGuardJob(jobId: string): Promise<TokenGuardJob> {
  return (await apiClient.get(`/admin/account-ops/token-guard/jobs/${jobId}`)).data
}

export async function cancelTokenGuardJob(jobId: string): Promise<TokenGuardJob> {
  return (await apiClient.post(`/admin/account-ops/token-guard/jobs/${jobId}/cancel`)).data
}

export async function reloginTokenGuardAccount(accountId: number): Promise<{ account_id: number; action: string }> {
  return (await apiClient.post(`/admin/account-ops/token-guard/accounts/${accountId}/relogin`)).data
}
