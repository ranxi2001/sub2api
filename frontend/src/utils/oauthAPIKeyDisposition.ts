export interface OAuthAPIKeyDispositionConfig {
  enabled: boolean
  scope: 'accounts' | 'groups'
  account_ids: number[]
  group_ids: number[]
  action: 'pause' | 'disable'
  delay_minutes: number
  pause_minutes: number
  timezone: string
}
export function oauthAPIKeyDispositionPayload(value?: Partial<OAuthAPIKeyDispositionConfig>): OAuthAPIKeyDispositionConfig {
  return { enabled: false, scope: 'accounts', action: 'pause', delay_minutes: 5, pause_minutes: 180, timezone: 'Asia/Shanghai', ...value, account_ids: [...(value?.account_ids ?? [])], group_ids: [...(value?.group_ids ?? [])] }
}
export function oauthAPIKeyDispositionError(p: OAuthAPIKeyDispositionConfig): string | null {
  if (!p.enabled) return null
  const validIDs = (ids: number[]) => ids.length <= 1000 && new Set(ids).size === ids.length && ids.every(id => Number.isSafeInteger(id) && id > 0)
  if (!['accounts', 'groups'].includes(p.scope) || !['pause', 'disable'].includes(p.action) || !validIDs(p.account_ids) || !validIDs(p.group_ids) ||
    !(p.scope === 'accounts' ? p.account_ids : p.group_ids).length || !Number.isInteger(p.delay_minutes) || p.delay_minutes < 0 || p.delay_minutes > 43200 ||
    p.action === 'pause' && (!Number.isInteger(p.pause_minutes) || p.pause_minutes < 1 || p.pause_minutes > 43200)) return 'autoConfig.apikeyDisposition.invalid'
  return null
}
