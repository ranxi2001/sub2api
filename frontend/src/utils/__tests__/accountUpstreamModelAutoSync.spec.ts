import { describe, expect, it, vi } from 'vitest'
import type { Account } from '@/types'
import type { SyncUpstreamModelsResult } from '@/api/admin/accounts'
import {
  createAccountUpstreamModelAutoSync,
  supportsAutomaticUpstreamModelSync,
  UPSTREAM_MODEL_SYNC_INTERVAL_MS
} from '../accountUpstreamModelAutoSync'

const account = (id: number, patch: Partial<Account> = {}): Account => ({
  id, platform: 'openai', type: 'apikey', status: 'active', schedulable: true,
  credentials: {}, ...patch
} as Account)

const changed = { models: ['new-model'], applied: true, changed: true }

describe('automatic upstream model discovery', () => {
  it('only queues account types that can discover and safely apply an upstream catalog', () => {
    expect(supportsAutomaticUpstreamModelSync(account(1))).toBe(true)
    for (const patch of [
      { status: 'inactive' }, { schedulable: false }, { parent_account_id: 9 },
      { platform: 'unknown' }, { type: 'bedrock' },
      { type: 'oauth', credentials: { model_mapping_mode: 'aliases' } },
      { platform: 'gemini', type: 'oauth', credentials: { project_id: 'project' } },
      { platform: 'gemini', type: 'service_account', credentials: { auth_mode: 'apikey' } },
      { platform: 'anthropic', type: 'service_account' }
    ] as Partial<Account>[]) {
      expect(supportsAutomaticUpstreamModelSync(account(1, patch))).toBe(false)
    }
    for (const patch of [
      { platform: 'antigravity', type: 'oauth' },
      { platform: 'anthropic', type: 'setup-token' },
      { platform: 'gemini', type: 'service_account' }
    ] as Partial<Account>[]) {
      expect(supportsAutomaticUpstreamModelSync(account(1, patch))).toBe(true)
    }
  })

  it('limits concurrency to two and stops the old page before starting queued accounts', async () => {
    const resolvers: Array<(result: SyncUpstreamModelsResult) => void> = []
    const sync = vi.fn((id: number) => id === 9 ? Promise.resolve(changed) :
      new Promise<SyncUpstreamModelsResult>(resolve => resolvers.push(resolve)))
    const queue = createAccountUpstreamModelAutoSync({ sync, now: () => 1000 })
    let currentPage = true
    const pending = queue.run([account(1), account(2), account(3)], () => currentPage)
    expect(sync).toHaveBeenCalledTimes(2)
    expect(await queue.run([account(9)], () => true)).toBeNull()
    currentPage = false
    resolvers.forEach(resolve => resolve(changed))
    expect(await pending).toBeNull()
    expect(sync).toHaveBeenCalledTimes(2)
    expect(await queue.run([account(9)], () => true)).toBeTruthy()
  })

  it('backs off failed attempts for five minutes instead of retrying on each table refresh', async () => {
    let now = 1000
    const sync = vi.fn().mockRejectedValueOnce(new Error('unavailable')).mockResolvedValue(changed)
    const queue = createAccountUpstreamModelAutoSync({ sync, now: () => now })
    expect(await queue.run([account(1)], () => true)).toEqual({ attempted: 1, changed: 0, failed: 1, skipped: 0 })
    for (let step = 0; step < 6; step++) {
      now += 5000
      expect(await queue.run([account(1)], () => true)).toBeNull()
    }
    expect(sync).toHaveBeenCalledTimes(1)
    now = 1000 + UPSTREAM_MODEL_SYNC_INTERVAL_MS
    expect(await queue.run([account(1)], () => true)).toMatchObject({ changed: 1, failed: 0 })
    expect(sync).toHaveBeenCalledTimes(2)
  })

  it('honors a later server cooldown shared with another browser', async () => {
    let now = Date.parse('2026-11-06T00:00:00Z')
    const sync = vi.fn().mockResolvedValue({ models: [], skipped: 'throttled', next_sync_at: '2026-11-06T00:08:00Z' })
    const queue = createAccountUpstreamModelAutoSync({ sync, now: () => now })
    expect(await queue.run([account(1)], () => true)).toMatchObject({ attempted: 1, changed: 0, skipped: 1 })
    now += UPSTREAM_MODEL_SYNC_INTERVAL_MS
    expect(await queue.run([account(1)], () => true)).toBeNull()
    expect(sync).toHaveBeenCalledTimes(1)
  })

  it('aborts in-flight discovery without accepting late results or retrying immediately', async () => {
    const resolvers: Array<(value: SyncUpstreamModelsResult) => void> = []
    const sync = vi.fn((_id: number, _signal: AbortSignal) => new Promise<SyncUpstreamModelsResult>(resolve => { resolvers.push(resolve) }))
    const queue = createAccountUpstreamModelAutoSync({ sync, now: () => 1000 })
    const pending = queue.run([account(1), account(2), account(3)], () => true)
    const signals = sync.mock.calls.map(([, signal]) => signal)
    queue.cancel()
    expect(signals.every(signal => signal.aborted)).toBe(true)
    // Even if a transport ignores AbortSignal, cancellation prevents state writes.
    resolvers.forEach(resolve => resolve(changed))
    expect(await pending).toBeNull()
    expect(sync).toHaveBeenCalledTimes(2)
    expect(await queue.run([account(1), account(2)], () => true)).toBeNull()
  })
})
