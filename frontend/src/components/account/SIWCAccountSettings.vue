<template>
  <section class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="siwc-settings">
    <label class="input-label" :for="`siwc-protocol-${accountId}`">{{ t('siwc.protocol') }}</label>
    <select :id="`siwc-protocol-${accountId}`" class="input" :value="enabled ? 'siwc' : 'codex'"
      :disabled="busy" data-testid="siwc-protocol"
      @change="emit('update:enabled', ($event.target as HTMLSelectElement).value === 'siwc')">
      <option value="codex">{{ t('siwc.codex') }}</option>
      <option value="siwc" :disabled="!loaded || !status?.bound">{{ t('siwc.siwc') }}</option>
    </select>
    <p class="input-hint">{{ t('siwc.protocolHint') }}</p>
    <p class="input-hint">{{ t('siwc.proxyHint') }}</p>
    <p role="status" class="text-sm" data-testid="siwc-status">{{ t(status?.bound ? 'siwc.bound' : 'siwc.unbound') }}</p>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || activeTask" data-testid="siwc-auto" @click="automatic">{{ t('siwc.automatic') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || activeTask" data-testid="siwc-browser" @click="browser">{{ t('siwc.browser') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="refresh">{{ t('siwc.refresh') }}</button>
    </div>
    <p class="input-hint">{{ t('siwc.loginHint') }}</p>
    <p v-if="activeTask" class="text-sm" data-testid="siwc-task">{{ t('siwc.running') }}</p>
    <p v-if="status?.task?.error && !authorization" class="text-sm text-amber-700 dark:text-amber-300">{{ status.task.error }}</p>
    <div v-if="authorization" class="space-y-2" data-testid="siwc-browser-flow">
      <div class="flex flex-wrap gap-2">
        <a class="btn btn-primary btn-sm" :href="authorization.auth_url" target="_blank" rel="noopener noreferrer" data-testid="siwc-open">{{ t('siwc.continue') }}</a>
        <button type="button" class="btn btn-secondary btn-sm" data-testid="siwc-copy" @click="copyAuthorization">{{ t('siwc.copyLink') }}</button>
      </div>
      <label :for="`siwc-auth-url-${accountId}`" class="input-label">{{ t('siwc.authLink') }}</label>
      <textarea :id="`siwc-auth-url-${accountId}`" class="input w-full break-all font-mono text-xs" :value="authorization.auth_url"
        readonly rows="4" spellcheck="false" data-testid="siwc-auth-url" @focus="($event.target as HTMLTextAreaElement).select()" />
      <p class="input-hint">{{ t('siwc.authLinkHint') }}</p>
      <p class="input-hint">{{ t('siwc.callbackHint') }}</p>
      <label :for="`siwc-callback-${accountId}`" class="input-label">{{ t('siwc.callback') }}</label>
      <input :id="`siwc-callback-${accountId}`" v-model="callback" class="input" autocomplete="off" spellcheck="false" data-testid="siwc-callback" />
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || !callback.trim()" data-testid="siwc-complete" @click="complete">{{ t('siwc.complete') }}</button>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useClipboard } from '@/composables/useClipboard'
import { authorizeSIWC, completeSIWC, getSIWCStatus, loginSIWCWithSavedCredentials, type SIWCAuthorization, type SIWCStatus } from '@/api/admin/siwc'

const props = defineProps<{ accountId: number; enabled: boolean; active: boolean }>()
const emit = defineEmits<{ 'update:enabled': [boolean] }>()
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const status = ref<SIWCStatus | null>(null)
const authorization = ref<SIWCAuthorization | null>(null)
const callback = ref('')
const busy = ref(false)
const loaded = ref(false)
const error = ref('')
const activeTask = computed(() => ['queued', 'running', 'callback_processing'].includes(status.value?.task?.status ?? ''))
let timer: ReturnType<typeof setTimeout> | undefined
let generation = 0
function clearTimer() { if (timer) clearTimeout(timer); timer = undefined }
function message(cause: unknown): string {
  const failure = cause as { message?: string; response?: { data?: { message?: string } } }
  return failure?.response?.data?.message || failure?.message || t('siwc.failed')
}
async function refresh() {
  clearTimer()
  const current = generation
  const id = props.accountId
  try {
    const next = await getSIWCStatus(id)
    if (current !== generation || !props.active) return
    status.value = next
    loaded.value = true
    error.value = ''
    if (activeTask.value) timer = setTimeout(() => { void refresh() }, 3000)
  } catch (cause) { if (current === generation) error.value = message(cause) }
}
async function run(action: (id: number, current: number) => Promise<void>) {
  if (busy.value) return
  const current = generation
  busy.value = true
  error.value = ''
  try { await action(props.accountId, current) } catch (cause) { if (current === generation) error.value = message(cause) }
  finally { if (current === generation) busy.value = false }
}
function automatic() {
  return run(async (id, current) => {
    await loginSIWCWithSavedCredentials(id)
    if (current === generation) await refresh()
  })
}
function browser() {
  return run(async (id, current) => {
    authorization.value = null
    callback.value = ''
    const result = await authorizeSIWC(id)
    if (current !== generation) return
    let url: URL
    try { url = new URL(result.auth_url) } catch { throw new Error(t('siwc.invalidAuthorization')) }
    if (url.origin !== 'https://auth.openai.com' || url.pathname !== '/api/accounts/authorize' ||
        url.username || url.password || url.hash || !result.session_id?.trim()) {
      throw new Error(t('siwc.invalidAuthorization'))
    }
    authorization.value = result
  })
}
async function copyAuthorization() {
  if (!authorization.value) return
  const current = generation
  const copied = await copyToClipboard(authorization.value.auth_url)
  if (current === generation) error.value = copied ? '' : t('siwc.copyFailed')
}
function complete() {
  if (!authorization.value) return
  const session = authorization.value.session_id
  const callbackURL = callback.value.trim()
  return run(async (id, current) => {
    await completeSIWC(id, session, callbackURL)
    if (current !== generation) return
    authorization.value = null
    callback.value = ''
    await refresh()
  })
}
watch(() => [props.accountId, props.active] as const, () => {
  generation++
  clearTimer()
  authorization.value = null
  callback.value = ''
  status.value = null
  loaded.value = false
  busy.value = false
  error.value = ''
  if (props.active) void refresh()
}, { immediate: true })
onBeforeUnmount(() => { generation++; clearTimer() })
</script>
