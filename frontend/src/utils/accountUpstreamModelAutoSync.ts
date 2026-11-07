import type { SyncUpstreamModelsResult } from '@/api/admin/accounts'
import { supportsUpstreamModelSync } from '@/constants/platformCatalog'
import type { Account } from '@/types'

export const UPSTREAM_MODEL_SYNC_INTERVAL_MS = 5 * 60 * 1000

type SyncCandidate = Pick<Account, 'id' | 'platform' | 'type' | 'status' | 'schedulable' | 'parent_account_id' | 'credentials'>

/** Keep automatic discovery aligned with upstream_models.go's supported types. */
export function supportsAutomaticUpstreamModelSync(account: SyncCandidate): boolean {
  if (account.status !== 'active' || !account.schedulable || account.parent_account_id != null) return false
  if (!supportsUpstreamModelSync(account.platform)) return false
  if (account.platform === 'openai' && account.type === 'oauth' && account.credentials?.model_mapping_mode === 'aliases') return false
  if (account.type === 'service_account') {
    return account.platform === 'gemini' && account.credentials?.auth_mode !== 'apikey'
  }
  if (account.type === 'apikey') return true
  if (account.type === 'setup-token') return account.platform === 'anthropic'
  if (account.type !== 'oauth') return false
  if (account.platform === 'gemini') return !account.credentials?.project_id
  return ['openai', 'anthropic', 'antigravity', 'grok'].includes(account.platform)
}

export interface UpstreamModelSyncSummary {
  attempted: number
  changed: number
  failed: number
  skipped: number
}

/**
 * One bounded batch for a page snapshot. A failed or cancelled request still
 * consumes its local cooldown: rapid toggles/page changes must not retry it.
 * The server independently enforces a shared cooldown across browser tabs.
 */
export function createAccountUpstreamModelAutoSync(options: {
  sync: (id: number, signal: AbortSignal) => Promise<SyncUpstreamModelsResult>
  now?: () => number
}) {
  const now = options.now ?? Date.now
  const nextAttemptAt = new Map<number, number>()
  let activeController: AbortController | null = null

  const cancel = () => activeController?.abort()

  const run = async (
    accounts: readonly SyncCandidate[],
    canContinue: () => boolean
  ): Promise<UpstreamModelSyncSummary | null> => {
    if (activeController || !canContinue()) return null
    const pending = accounts.filter(account => supportsAutomaticUpstreamModelSync(account) &&
      (nextAttemptAt.get(account.id) ?? 0) <= now())
    if (!pending.length) return null

    const controller = new AbortController()
    activeController = controller
    const summary: UpstreamModelSyncSummary = { attempted: 0, changed: 0, failed: 0, skipped: 0 }
    let index = 0
    const current = () => !controller.signal.aborted && canContinue()
    const worker = async () => {
      while (current()) {
        const account = pending[index++]
        if (!account) return
        nextAttemptAt.set(account.id, now() + UPSTREAM_MODEL_SYNC_INTERVAL_MS)
        summary.attempted++
        try {
          const result = await options.sync(account.id, controller.signal)
          const serverNext = Date.parse(result.next_sync_at ?? '')
          if (Number.isFinite(serverNext)) {
            nextAttemptAt.set(account.id, Math.max(nextAttemptAt.get(account.id) ?? 0, serverNext))
          }
          if (result.changed) summary.changed++
          if (result.skipped) summary.skipped++
        } catch {
          if (!controller.signal.aborted) summary.failed++
        }
      }
    }
    try {
      await Promise.all([worker(), worker()])
      return current() ? summary : null
    } finally {
      if (activeController === controller) activeController = null
    }
  }

  return { run, cancel }
}
