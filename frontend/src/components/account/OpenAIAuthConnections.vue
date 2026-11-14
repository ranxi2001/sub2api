<template>
  <section v-if="eligible" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="auth-connections">
    <h3 class="font-medium">{{ t('openaiAuth.title') }}</h3>
    <p class="input-hint">{{ t('openaiAuth.hint') }}</p>
    <p class="input-hint">{{ t('openaiAuth.sharedLimits') }}</p>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="refresh">{{ t('openaiAuth.refresh') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="use-legacy" @click="legacy">{{ t('openaiAuth.useLegacy') }}</button>
    </div>
    <ul class="space-y-3">
      <li v-for="connection in connections" :key="connection.id" class="rounded border border-gray-200 p-3 dark:border-dark-600" :data-connection-id="connection.id">
        <div class="flex flex-wrap items-center gap-2">
          <strong>{{ connection.legacy ? t('openaiAuth.legacy') : connection.label }}</strong>
          <span>{{ connection.kind === 'bps' ? 'BPS' : connection.kind === 'siwc' ? 'SIWC' : connection.kind === 'codex' ? 'Codex' : t('openaiAuth.apiKey') }}</span>
          <span>{{ t(`openaiAuth.status.${connection.status}`) }}</span>
          <span v-if="connection.preferred" class="text-primary-600">{{ t('openaiAuth.selected') }}</span>
          <span v-if="connection.legacy">{{ t('openaiAuth.legacy') }}</span>
        </div>
        <p class="text-sm">{{ t('openaiAuth.capabilities') }}: {{ connection.capabilities.join(', ') || t('openaiAuth.identityOnly') }}</p>
        <p v-if="connection.refresh_state && connection.refresh_state !== 'idle'" role="status" class="input-hint">{{ t('openaiAuth.refreshUncertain') }}</p>
        <p v-if="connection.kind === 'bps'" class="input-hint">{{ t('openaiAuth.bpsResponsesOnly') }}</p>
        <p v-if="connection.kind === 'codex' && !connection.legacy" class="input-hint">{{ t('openaiAuth.codexTicketBoundary') }}</p>
        <p v-if="connection.cooldown_until" class="text-sm">{{ t('openaiAuth.cooldown') }}: {{ connection.cooldown_until }}</p>
        <details class="text-sm">
          <summary>{{ t('openaiAuth.registration') }}</summary>
          <dl class="break-all">
            <dt>{{ t('openaiAuth.client') }}</dt><dd>{{ connection.client_id || '—' }}</dd>
            <dt>{{ t('openaiAuth.identity') }}</dt><dd>{{ connection.identity_verified ? t('openaiAuth.verified') : t('openaiAuth.unverified') }}</dd>
            <dt>{{ t('openaiAuth.workspace') }}</dt><dd>{{ connection.workspace || t('openaiAuth.unknown') }}</dd>
            <dt>{{ t('openaiAuth.scopes') }}</dt><dd>{{ connection.scopes.join(' ') || '—' }}</dd>
            <dt>{{ t('openaiAuth.resource') }}</dt><dd>{{ connection.resource || '—' }}</dd>
          </dl>
        </details>
        <p v-if="connection.legacy" class="input-hint">{{ t('openaiAuth.legacyHint') }}</p>
        <div v-else class="mt-2 flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || connection.status !== 'active' || !connection.capabilities.length || (!!connection.refresh_state && connection.refresh_state !== 'idle')" data-testid="select-connection" @click="mutate(connection, 'select')">{{ t('openaiAuth.select') }}</button>
          <button v-if="connection.status === 'active' && connection.refresh_state && connection.refresh_state !== 'idle'" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="mutate(connection, 'recover')">{{ t('openaiAuth.recoverRefresh') }}</button>
          <button v-if="connection.kind !== 'api_key'" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="authorize(connection)">{{ t('openaiAuth.authorize') }}</button>
          <button v-else type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="rotation = connection">{{ t('openaiAuth.rotate') }}</button>
          <button v-if="connection.status === 'active'" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="mutate(connection, 'disable')">{{ t('openaiAuth.disable') }}</button>
          <button v-if="connection.status === 'disabled'" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="mutate(connection, 'enable')">{{ t('openaiAuth.enable') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || connection.status === 'unlinked'" @click="revocation = connection">{{ t('openaiAuth.unlink') }}</button>
        </div>
      </li>
    </ul>
    <div class="space-y-2" data-testid="add-connection-fields">
      <label :for="`connection-kind-${accountId}`" class="input-label">{{ t('openaiAuth.kind') }}</label>
      <select :id="`connection-kind-${accountId}`" v-model="kind" class="input" :disabled="busy">
        <option value="codex">Codex</option><option value="bps">BPS</option><option value="siwc">SIWC</option>
      </select>
      <label :for="`connection-label-${accountId}`" class="input-label">{{ t('openaiAuth.label') }}</label>
      <input :id="`connection-label-${accountId}`" v-model="label" class="input" maxlength="120" autocomplete="off" :disabled="busy" data-testid="connection-label" />
      <p v-if="kind === 'siwc'" class="input-hint">{{ t('openaiAuth.siwcApproval') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" @click="add" :disabled="busy || !label.trim()" data-testid="add-connection">{{ t('openaiAuth.add') }}</button>
    </div>
    <div v-if="authorization" class="space-y-2" data-testid="connection-authorization">
      <p>{{ t('openaiAuth.authorizing', { label: authorization.label }) }}</p>
      <template v-if="!authorization.submitted">
        <a :href="authorization.auth_url" target="_blank" rel="noopener noreferrer" class="btn btn-secondary btn-sm">{{ t('openaiAuth.open') }}</a>
        <label :for="`connection-callback-${accountId}`" class="input-label">{{ t('openaiAuth.callback') }}</label>
        <input :id="`connection-callback-${accountId}`" v-model="callback" class="input" autocomplete="off" spellcheck="false" :disabled="busy" />
        <button type="button" class="btn btn-secondary btn-sm" @click="complete" :disabled="busy || !callback.trim()">{{ t('openaiAuth.complete') }}</button>
      </template>
      <p v-else role="status" data-testid="authorization-result">{{ t(busy ? 'openaiAuth.authorizationChecking' : 'openaiAuth.authorizationUnconfirmed') }}</p>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="checkAuthorization" data-testid="check-authorization">{{ t('openaiAuth.checkAuthorization') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="clearSecrets">{{ t('openaiAuth.cancel') }}</button>
    </div>
    <div v-if="rotation" class="space-y-2">
      <label :for="`connection-rotate-${accountId}`" class="input-label">{{ t('openaiAuth.rotateLabel', { label: rotation.label }) }}</label>
      <input :id="`connection-rotate-${accountId}`" v-model="rotationKey" type="password" class="input" autocomplete="new-password" />
      <button type="button" class="btn btn-secondary btn-sm" @click="rotate" :disabled="busy || !rotationKey.trim()">{{ t('openaiAuth.rotate') }}</button>
    </div>
    <div v-if="revocation" class="space-y-2" role="alert">
      <p>{{ t('openaiAuth.unlinkConfirm', { label: revocation.label }) }}</p>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="unlink">{{ t('openaiAuth.confirm') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="revocation = null">{{ t('openaiAuth.cancel') }}</button>
    </div>
    <p v-if="notice" role="status" class="text-sm">{{ notice }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { authorizeConnection, completeConnection, connectionAuthorizationStatus, createAuthConnection, listAuthConnections, revokeConnection, rotateConnectionAPIKey, updateAuthConnection, useLegacyConnections, type OAuthConnectionKind, type OpenAIAuthConnection } from '@/api/admin/openaiAuthConnections'

const props = defineProps<{ accountId: number; accountType: string; active: boolean }>()
const { t } = useI18n()
const eligible = computed(() => props.active && props.accountType === 'oauth')
const connections = ref<OpenAIAuthConnection[]>([])
const kind = ref<OAuthConnectionKind>('codex')
const label = ref('')
const callback = ref('')
const rotationKey = ref('')
const busy = ref(false)
const error = ref('')
const notice = ref('')
const authorization = ref<{ id: string; label: string; auth_url: string; session_id: string; submitted: boolean } | null>(null)
const rotation = ref<OpenAIAuthConnection | null>(null)
const revocation = ref<OpenAIAuthConnection | null>(null)
let generation = 0
function clearSecrets() { rotationKey.value = ''; callback.value = ''; authorization.value = null; rotation.value = null; revocation.value = null }
async function reload() {
  if (!eligible.value) return
  const current = generation
  const next = await listAuthConnections(props.accountId)
  if (current === generation && eligible.value) connections.value = next
}
async function run(action: (id: number, current: number) => Promise<void>) {
  if (!eligible.value || busy.value) return
  const current = generation
  const id = props.accountId
  busy.value = true; error.value = ''; notice.value = ''
  try { await action(id, current) } catch (cause) {
    if (current === generation) error.value = (cause as { message?: string })?.message || t('openaiAuth.failed')
  } finally { if (current === generation) busy.value = false }
}
async function refresh() { await run(async () => { await reload() }) }
async function add() {
  if (!['codex', 'bps', 'siwc'].includes(kind.value)) return
  const input = { kind: kind.value, label: label.value.trim() }
  await run(async (id, current) => { await createAuthConnection(id, input); if (current === generation) { label.value = ''; await reload() } })
}
async function mutate(c: OpenAIAuthConnection, action: 'select' | 'disable' | 'enable' | 'recover') {
  await run(async (id, current) => { await updateAuthConnection(id, c, action); if (current === generation) await reload() })
}
async function legacy() { await run(async (id, current) => { await useLegacyConnections(id); if (current === generation) await reload() }) }
async function authorize(c: OpenAIAuthConnection) {
  if (busy.value) return
  clearSecrets()
  await run(async (id, current) => { const next = await authorizeConnection(id, c.id); if (current === generation) authorization.value = { ...next, id: c.id, label: c.label, submitted: false } })
}
async function confirmAuthorization(id: number, current: number, selected: NonNullable<typeof authorization.value>) {
  const result = await connectionAuthorizationStatus(id, selected.id, selected.session_id)
  if (current !== generation || authorization.value !== selected) return
  if (result.status === 'saved') {
    clearSecrets()
    notice.value = t('openaiAuth.authorizationSaved')
    await reload()
  } else {
    notice.value = t(result.status === 'waiting' && !selected.submitted ? 'openaiAuth.authorizationWaiting' : 'openaiAuth.authorizationUnconfirmed')
  }
}
async function checkAuthorization() {
  const selected = authorization.value
  if (!selected || busy.value) return
  await run(async (id, current) => { await confirmAuthorization(id, current, selected) })
}
async function complete() {
  const selected = authorization.value; if (!selected || selected.submitted || busy.value || !callback.value.trim()) return
  const url = callback.value.trim(); callback.value = ''
  selected.submitted = true
  selected.auth_url = ''
  await run(async (id, current) => {
    try {
      await completeConnection(id, selected.id, selected.session_id, url)
    } catch {
      // A lost response can follow a committed credential write. Query the
      // receipt once; never resend the callback or infer success from active.
      if (current !== generation || authorization.value !== selected) return
      try { await confirmAuthorization(id, current, selected) } catch {
        if (current === generation) error.value = t('openaiAuth.authorizationUnconfirmed')
      }
      return
    }
    if (current === generation && authorization.value === selected) {
      clearSecrets(); notice.value = t('openaiAuth.authorizationSaved'); await reload()
    }
  })
}
async function rotate() {
  const selected = rotation.value; if (!selected) return
  const key = rotationKey.value.trim(); rotationKey.value = ''
  await run(async (id, current) => { await rotateConnectionAPIKey(id, selected, key); if (current === generation) { clearSecrets(); await reload() } })
}
async function unlink() {
  const selected = revocation.value; if (!selected) return
  await run(async (id, current) => {
    const result = await revokeConnection(id, selected)
    if (current === generation) { clearSecrets(); notice.value = t(result.remote_revocation_confirmed ? 'openaiAuth.revoked' : 'openaiAuth.revocationUnconfirmed'); await reload() }
  })
}
watch(() => [props.accountId, props.active, props.accountType] as const, () => {
  generation++; clearSecrets(); connections.value = []; busy.value = false; error.value = ''; notice.value = ''
  if (eligible.value) void run(async () => { await reload() })
}, { immediate: true })
onBeforeUnmount(() => { generation++; clearSecrets() })
</script>
