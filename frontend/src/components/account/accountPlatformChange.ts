import type { Account, Group } from '@/types'

/** Start target settings from portable credentials and account-wide quota/costs. */
export function platformAccountDraft(source: Account, platform: string, availableGroups: Group[] = []): Account {
  const credentialKeys = source.type === 'service_account'
    ? ['auth_mode', 'api_key', 'service_account_json', 'service_account', 'private_key', 'project_id', 'client_email', 'location', 'vertex_location', 'tier_id']
    : ['api_key', 'base_url']
  const credentials = Object.fromEntries(Object.entries(source.credentials ?? {}).filter(([key]) => credentialKeys.includes(key)))
  const extra = Object.fromEntries(Object.entries(source.extra ?? {}).filter(([key]) => key.startsWith('quota_') || key === 'cost_multiplier' || key === 'cost_multiplier_auto_sync' || key === 'token_procurement_basis' || key === 'image_purchase_prices'))
  const groups = [...new Map([...availableGroups, ...(source.groups ?? [])].map(group => [group.id, group])).values()]
    .filter(group => group.platform === platform || group.platform === 'composite')
  const groupIds = new Set(groups.map(group => group.id))
  return {
    ...source, platform, credentials, extra, groups,
    ollama_cloud_usage: undefined,
    opencode_go_usage: undefined,
    grok_model_diagnostics: undefined,
    group_ids: (source.group_ids ?? []).filter(id => groupIds.has(id)),
    account_groups: (source.account_groups ?? []).filter(binding => groupIds.has(binding.group_id))
  }
}
