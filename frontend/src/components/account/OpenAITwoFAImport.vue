<template>
  <div class="space-y-4">
    <CredentialEncryptionSetup @ready="encryptionReady = $event" />
    <p class="input-hint">{{ t('tokenGuard.twoFA.hint') }}</p>
    <a href="/admin/token-guard" target="_blank" rel="noopener noreferrer" class="text-primary-600">
      {{ t('tokenGuard.twoFA.settings') }}
    </a>
    <template v-if="!rows.length">
      <label for="two-fa-credentials" class="input-label">{{ t('tokenGuard.twoFA.credentials') }}</label>
      <textarea id="two-fa-credentials" v-model="raw" class="input font-mono" rows="6"
        autocomplete="off" autocapitalize="off" :spellcheck="false" :disabled="busy"
        :placeholder="t('tokenGuard.twoFA.placeholder')" />
    </template>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="!busy && hasOwnedJobs" role="status" class="input-hint" data-testid="two-fa-owned-jobs">
      {{ t('tokenGuard.twoFA.ownedJobs') }}
    </p>
    <ul v-if="rows.length" class="space-y-2" aria-live="polite">
      <li v-for="(row, index) in rows" :key="row.entry.email" class="flex justify-between gap-4 text-sm">
        <span>{{ index + 1 }}. {{ row.entry.email }}</span>
        <span>{{ t(`tokenGuard.twoFA.states.${row.status}`) }}</span>
      </li>
    </ul>
    <div class="flex gap-3">
      <button v-if="!busy && (!rows.length || hasPending)" type="button" class="btn btn-primary" :disabled="!encryptionReady" data-testid="two-fa-start" @click="run">
        {{ t(rows.length ? 'tokenGuard.twoFA.retry' : 'tokenGuard.twoFA.start') }}
      </button>
      <button v-if="busy || hasOwnedJobs" type="button" class="btn btn-secondary" :disabled="busy && stopRequested" data-testid="two-fa-stop" @click="stop">
        {{ t('tokenGuard.twoFA.stop') }}
      </button>
      <button v-if="!busy && rows.length" type="button" class="btn btn-secondary" :disabled="hasOwnedJobs" @click="reset">
        {{ t('tokenGuard.twoFA.reset') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import CredentialEncryptionSetup from './CredentialEncryptionSetup.vue'
import { useI18n } from 'vue-i18n'
import { deleteTwoFALogin, getTwoFALogin, parseTwoFALoginText, startTwoFALogin } from '@/api/admin/accountTokenGuard'
import type { TokenGuardReloginAccount } from '@/api/admin/accountTokenGuard'

const props = defineProps<{
  importCredential: (credential: Record<string, unknown>, email: string, login: TokenGuardReloginAccount, signal?: AbortSignal) => Promise<'created' | 'skipped'>
}>()
const emit = defineEmits<{ busy: [value: boolean] }>()
const { t } = useI18n()
type Row = {
  entry: TokenGuardReloginAccount
  status: 'pending' | 'login' | 'importing' | 'created' | 'skipped' | 'failed' | 'importFailed'
  credential?: Record<string, unknown>
  jobId?: string
  cleanup?: Promise<void>
  retiring?: boolean
}
const raw = ref('')
const encryptionReady = ref(false)
const error = ref('')
const rows = ref<Row[]>([])
const busy = ref(false)
const stopRequested = ref(false)
const hasPending = computed(() => rows.value.some(row => row.status !== 'created' && row.status !== 'skipped'))
const hasOwnedJobs = computed(() => rows.value.some(row => row.jobId || row.cleanup))
let disposed = false
let controller: AbortController | undefined
const pipelineLimit = 3

// Parent Close must preserve ownership even when this attempt has paused.
// The local busy flag stays separate so explicit Retry/cleanup remains usable.
watch(() => busy.value || hasOwnedJobs.value, value => {
  if (!disposed) emit('busy', value)
}, { flush: 'sync' })

async function stop() {
  stopRequested.value = true
  controller?.abort()
  const ownsBusy = !busy.value
  if (ownsBusy) busy.value = true
  try {
    await Promise.all(rows.value.filter(row => row.jobId || row.cleanup)
      .map(row => discardJob(row).catch(() => {})))
  } finally {
    if (ownsBusy) busy.value = false
  }
}

function pollDelay(signal: AbortSignal) {
  return new Promise<void>(resolve => {
    const finish = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', finish)
      resolve()
    }
    const timer = setTimeout(finish, 1500)
    signal.addEventListener('abort', finish, { once: true })
    if (signal.aborted) finish()
  })
}

function discardJob(row: Row): Promise<void> {
  if (row.cleanup) return row.cleanup
  if (!row.jobId) return Promise.resolve()
  const id = row.jobId
  row.retiring = true
  // Keep both ownership and the shared cleanup promise until DELETE confirms
  // retirement. A failed DELETE fences this slot and identity across Retry.
  row.cleanup = deleteTwoFALogin(id).then(() => {
    row.jobId = undefined
    row.retiring = false
  }).finally(() => { row.cleanup = undefined })
  return row.cleanup
}

async function run() {
  if (busy.value || disposed || !encryptionReady.value) return
  error.value = ''
  if (!rows.value.length) {
    try {
      rows.value = parseTwoFALoginText(raw.value).map(entry => ({ entry, status: 'pending' }))
      raw.value = ''
    } catch {
      error.value = t('tokenGuard.twoFA.invalid')
      return
    }
  }
  busy.value = true
  stopRequested.value = false
  controller = new AbortController()
  const { signal } = controller
  const batchRows = rows.value
  try {
    // Retained jobs already consume capacity. Resume/retire them before
    // admitting fresh rows, including when an earlier attempt lost polling.
    const pending = batchRows.filter(row => row.status !== 'created' && row.status !== 'skipped')
      .sort((a, b) => Number(Boolean(b.jobId)) - Number(Boolean(a.jobId)))
    let next = 0
    const worker = async () => {
      while (!signal.aborted && !disposed) {
        const row = pending[next++]
        if (!row) return
        // A slot covers login, import and credential-operations enrollment.
        // Each worker claims one stable input row before yielding.
        if (!await runRow(row, signal)) return
      }
    }
    await Promise.all(Array.from({ length: Math.min(pipelineLimit, pending.length) }, worker))
  } finally {
    // Stop may also retire jobs whose polling worker already returned. Busy
    // must cover those DELETEs, so Retry cannot overlap their ownership.
    const cleanup = batchRows.flatMap(row => row.cleanup ? [row.cleanup.catch(() => {})] : [])
    await Promise.all(cleanup)
    controller = undefined
    busy.value = false
  }
}

async function runRow(row: Row, signal: AbortSignal) {
  try {
    if (row.retiring) await discardJob(row)
    signal.throwIfAborted()
    if (!row.credential) {
      row.status = 'login'
      // Keep the job ID on polling failures; explicit retry resumes that job.
      // Do not abort this short POST: Axios could discard an accepted job ID.
      // Stop fences the worker until its response arrives and is retired.
      if (!row.jobId) row.jobId = (await startTwoFALogin(row.entry)).id
      signal.throwIfAborted()
      let job = await getTwoFALogin(row.jobId, { signal })
      signal.throwIfAborted()
      while (job.status === 'running') {
        await pollDelay(signal)
        signal.throwIfAborted()
        job = await getTwoFALogin(row.jobId!, { signal })
        signal.throwIfAborted()
      }
      if (job.status !== 'succeeded' || !job.credential) {
        await discardJob(row)
        throw new Error('login_failed')
      }
      row.credential = job.credential
      await discardJob(row)
    }
    signal.throwIfAborted()
    row.status = 'importing'
    // Retry is explicit and reuses the credential; completed rows stay skipped.
    row.status = await props.importCredential(row.credential, row.entry.email, { ...row.entry }, signal)
    row.credential = undefined
    row.entry.password = ''
    row.entry.mfa_secret = ''
  } catch (cause: unknown) {
    if (signal.aborted) {
      try {
        if (!row.retiring || row.cleanup) await discardJob(row)
      } catch {
        // Retain the ID/retiring flag on failed cleanup; explicit Retry can
        // retry DELETE, but may not submit a replacement for this identity.
      }
      row.status = row.credential ? 'importFailed' : row.jobId ? 'failed' : 'pending'
      return false
    }
    const status = (cause as { status?: number; response?: { status?: number } })?.status ??
      (cause as { response?: { status?: number } })?.response?.status
    if (status === 404 && !row.retiring) row.jobId = undefined
    row.status = row.credential ? 'importFailed' : 'failed'
  }
  // A polling/cleanup failure has not terminated the server job. Its worker
  // stays reserved rather than dequeuing a fourth live account pipeline.
  return !row.jobId
}

function reset() {
  if (!disposed && (busy.value || hasOwnedJobs.value)) return
  for (const row of rows.value) void discardJob(row).catch(() => {})
  rows.value = []
  raw.value = ''
  error.value = ''
}

onBeforeUnmount(() => {
  disposed = true
  stop()
  reset()
})
</script>
