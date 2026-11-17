import { onBeforeUnmount, shallowReactive } from 'vue'
import { readProfitSync, startProfitSync, type ProfitDates } from '@/api/admin/profitOperations'

interface SyncJob extends ProfitDates {
  providerId: number
  jobId: string
  message: string
  busy: boolean
  error?: boolean
  matched?: number
  unmatched?: number
}

const pollInterval = 3000
const failureReasons = new Set(['upstream', 'timeout', 'conflict', 'simple_mode', 'internal', 'evidence_budget', 'rate_limited'])
// A navigation during the short POST must not discard its acknowledgement or start another POST.
const admissions = new Map<string, Promise<{ job_id: string }>>()

function validDates(from: unknown, until: unknown): boolean {
  if (typeof from !== 'string' || typeof until !== 'string') return false
  const span = Date.parse(until) - Date.parse(from)
  return span > 0 && span <= 32 * 86_400_000
}
function validId(id: unknown): id is string {
  return typeof id === 'string' && id.trim().length > 0 && id !== '.' && id !== '..'
}
function remember(key: string, jobId: string) {
  try { localStorage.setItem(key, jobId) } catch { /* Polling still works without storage. */ }
}
function remembered(key: string) {
  try { return localStorage.getItem(key) } catch { return null }
}
function forget(key: string, jobId: string) {
  try { if (remembered(key) === jobId) localStorage.removeItem(key) } catch { /* Another tab can recheck the status. */ }
}

export function useProfitSync(userId: number | undefined, onSuccess: (dates: ProfitDates) => void) {
  const prefix = `profit-bill-sync:${userId}:`
  const jobs = shallowReactive(new Map<string, SyncJob>())
  const timers = new Set<ReturnType<typeof setTimeout>>()
  const lifetime = new AbortController()
  const keyFor = (id: number, dates: ProfitDates) => prefix + JSON.stringify([id, dates.from, dates.until])
  const state = (id: number | undefined, dates: ProfitDates) => id ? jobs.get(keyFor(id, dates)) : undefined
  onBeforeUnmount(() => {
    lifetime.abort()
    for (const timer of timers) clearTimeout(timer)
    timers.clear()
  })

  async function poll(key: string, job: SyncJob) {
    try {
      const result = await readProfitSync(job.providerId, job.jobId, lifetime.signal)
      if (lifetime.signal.aborted) return
      if (result?.status === 'running') {
        const timer = setTimeout(() => { timers.delete(timer); void poll(key, job) }, pollInterval)
        timers.add(timer)
        return
      }
      if (result?.status === 'failed') {
        const reason = result.reason && failureReasons.has(result.reason) ? result.reason : 'internal'
        jobs.set(key, { ...job, busy: false, error: true, message: `syncFailure.${reason}` })
      } else if (result?.status === 'succeeded') {
        const { matched, unmatched } = result
        const hasCounts = [matched, unmatched].every(n => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0)
        jobs.set(key, { ...job, busy: false, message: hasCounts ? 'syncResult' : 'syncComplete', matched, unmatched })
      } else {
        throw new Error('Unknown sync status')
      }
      forget(key, job.jobId)
      if (result.status === 'succeeded') onSuccess(job)
    } catch (cause) {
      if (lifetime.signal.aborted) return
      const expired = (cause as { status?: number } | null)?.status === 404
      if (expired) forget(key, job.jobId)
      jobs.set(key, { ...job, busy: false, error: true, message: expired ? 'syncExpired' : 'syncDisconnected' })
    }
  }

  function attach(key: string, job: SyncJob) {
    const running = { ...job, busy: true, error: false, message: 'syncing' }
    jobs.set(key, running)
    void poll(key, running)
  }

  async function sync(providerId: number, { from, until }: ProfitDates) {
    const key = keyFor(providerId, { from, until }), previous = jobs.get(key)
    if (!userId || lifetime.signal.aborted || previous?.busy) return
    // A status read can fail while the server job is still running. Retry GET, not POST.
    const saved = remembered(key), jobId = validId(saved) ? saved : previous?.message === 'syncDisconnected' ? previous.jobId : ''
    const job = { providerId, from, until, jobId, busy: true, message: 'syncStarting' }
    if (jobId) { attach(key, job); return }
    jobs.set(key, job)
    let admission = admissions.get(key)
    if (!admission) {
      // An empty handle records the exact dates if a hard reload loses the POST response.
      // Reconnect is explicit in that case; the server reuses a running provider/window job.
      remember(key, '')
      admission = startProfitSync(providerId, { from, until }).then(result => {
        if (!validId(result?.job_id)) throw new Error('Missing sync job ID')
        remember(key, result.job_id)
        return result
      }).finally(() => admissions.delete(key))
      admissions.set(key, admission)
    }
    try {
      const { job_id } = await admission
      if (!lifetime.signal.aborted) attach(key, { ...job, jobId: job_id })
    } catch (cause) {
      const status = (cause as { status?: number } | null)?.status
      if (status && status >= 400 && status < 500) forget(key, '')
      if (!lifetime.signal.aborted) jobs.set(key, { ...job, busy: false, error: true, message: status === 429 ? 'syncBusy' : 'syncUnconfirmed' })
    }
  }

  function restore(): ProfitDates | undefined {
    if (!userId) return
    let dates: ProfitDates | undefined
    try {
      for (const key of Object.keys(localStorage)) {
        if (!key.startsWith(prefix)) continue
        let tuple: unknown
        try { tuple = JSON.parse(key.slice(prefix.length)) } catch { continue }
        if (!Array.isArray(tuple)) continue
        const [providerId, from, until] = tuple
        if (!Number.isSafeInteger(providerId) || providerId <= 0 || !validDates(from, until)) continue
        const jobId = remembered(key)
        if (jobId === null || (jobId !== '' && !validId(jobId))) continue
        const job = { providerId, from, until, jobId, busy: false, message: 'syncUnconfirmed', error: true }
        dates = { from, until }
        if (jobId) attach(key, job)
        else if (admissions.has(key)) void sync(providerId, dates)
        else jobs.set(key, job)
      }
    } catch { /* No stored jobs are required to open a report. */ }
    return dates
  }
  return { state, sync, restore }
}
