import type { AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'

export type ChannelRuleView = 'wallets' | 'official' | 'other'

export function channelRuleGroups(
  groups: AccountOpsGroup[],
  accounts: AccountOpsThresholdAccount[],
  view: ChannelRuleView,
  scoped: boolean,
): AccountOpsGroup[] {
  const byId = new Map(accounts.map(account => [account.account_id, account]))
  const assigned = new Set<number>()
  const result: AccountOpsGroup[] = []

  for (const group of groups) {
    const wallet = !!group.provider_id
    const members = group.account_ids.filter(id => {
      const account = byId.get(id)
      if (!account) return false
      assigned.add(id)
      if (account.type === 'oauth') return view === 'official'
      return wallet ? view === 'wallets' : view === 'other'
    })
    // Empty groups remain editable in the directory, including old saved groups.
    if (members.length || (!scoped && !group.account_ids.length && !wallet && view === 'other')) {
      result.push({ ...group, account_ids: members })
    }
  }
  // Accounts outside a manual monitoring group are still visible, including
  // OAuth accounts that have never had a quota rule.
  for (const account of accounts) {
    if (assigned.has(account.account_id)) continue
    if (view !== (account.type === 'oauth' ? 'official' : 'other')) continue
    result.push({
      id: `account:${account.account_id}`, name: account.account_name, default_name: account.account_name,
      site: '', provider: account.platform, mode: 'account', account_ids: [account.account_id],
    })
  }
  return result
}
