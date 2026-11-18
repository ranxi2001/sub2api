import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { adminAPI } from '@/api/admin'

type State = 'pending' | 'submitted' | 'saved' | 'unchanged' | 'failed' | 'stopped'
export interface WhitelistRow {
  id: number
  state: State
  detail?: 'empty' | 'account_policy' | 'account_changed' | 'unverified_catalog' | 'unknown'
}

export function useBatchUpstreamWhitelist(scope: () => number | null, selection: () => number[], onSaved: () => void) {
  const ids = ref<number[]>([])
  const rows = ref<WhitelistRow[]>([])
  const busy = ref(false)
  let session = 0
  let stopped = false

  function stop() {
    stopped = true
    for (const row of rows.value) {
      if (row.state === 'pending') row.state = 'stopped'
    }
  }

  function leave() {
    stop()
    session++
    rows.value = []
    ids.value = []
  }

  // Watch only the dialog/identity. A table reload must not replace this selection.
  watch(scope, user => {
    leave()
    if (user != null) ids.value = [...selection()]
  }, { immediate: true })
  onBeforeUnmount(leave)

  const valid = computed(() => ids.value.length > 0 &&
    ids.value.every(id => Number.isSafeInteger(id) && id > 0) && new Set(ids.value).size === ids.value.length)

  async function run(retry: boolean) {
    if (scope() == null || !valid.value || busy.value) return
    if (!retry && rows.value.length) return
    if (!retry) rows.value = ids.value.map(id => ({ id, state: 'pending' }))
    const queue = rows.value.filter(row => row.state === (retry ? 'failed' : 'pending'))
    if (!queue.length) return
    for (const row of queue) {
      row.state = 'pending'
      row.detail = undefined
    }
    const current = session
    const adminId = scope()
    let next = 0
    let changed = false
    stopped = false
    busy.value = true
    async function worker() {
      while (current === session && scope() === adminId && !stopped && next < queue.length) {
        const row = queue[next++]
        row.state = 'submitted'
        try {
          const result = await adminAPI.accounts.syncUpstreamModels(row.id, { apply: true, whitelist_only: true }, { timeout: 70000 })
          if (current !== session) continue
          if (result.applied && !result.skipped) {
            row.state = result.changed ? 'saved' : 'unchanged'
            changed ||= !!result.changed
          } else {
            row.state = 'failed'
            const reason = result.skipped
            row.detail = reason === 'empty' || reason === 'account_policy' || reason === 'account_changed' || reason === 'unverified_catalog' ? reason : 'unknown'
          }
        } catch {
          if (current === session) {
            row.state = 'failed'
            row.detail = 'unknown'
          }
        }
      }
    }
    try {
      await Promise.all([worker(), worker()])
    } finally {
      // Closing retires the rows but keeps the lock until submitted writes settle.
      busy.value = false
      if (current === session && changed) onSaved()
    }
  }
  return { rows, busy, valid, stop, run, leave }
}
