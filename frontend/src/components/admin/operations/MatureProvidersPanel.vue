<template>
  <section class="space-y-4" :aria-label="t('matureProviders.title')">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <div class="flex items-center gap-2"><h2 class="text-xl font-semibold">{{ t('matureProviders.title') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.matureProviders" :user-id="auth.user?.id" /></div>
        <p class="mt-1 text-sm text-gray-500">{{ t('matureProviders.description') }}</p>
      </div>
      <div class="flex items-center gap-2"><a href="/admin/account-ops" class="btn btn-secondary">{{ t('accountOps.accountRules') }}</a><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.accountOpsProviderBalance" :user-id="auth.user?.id" /></div>
      <button class="btn btn-primary" data-testid="provider-add" @click="edit()">{{ t('matureProviders.add') }}</button>
    </header>
    <form class="flex flex-wrap gap-2" @submit.prevent="search">
      <input v-model="query" :aria-label="t('matureProviders.search')" :placeholder="t('matureProviders.search')" class="input min-w-0 flex-1" maxlength="120" />
      <select v-model="status" :aria-label="t('matureProviders.status')" class="input"><option value="">{{ t('matureProviders.allStatuses') }}</option><option v-for="state in states" :key="state" :value="state">{{ t(`matureProviders.${state}`) }}</option></select>
      <button class="btn btn-secondary" :disabled="loading">{{ t('matureProviders.search') }}</button>
    </form>
    <p v-if="error && !formOpen" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="loading" role="status" class="text-sm text-gray-500">{{ t('matureProviders.loading') }}</p>
    <p v-else-if="!error && !providers.length" class="card p-6 text-center text-gray-500">{{ t('matureProviders.empty') }}</p>
    <div class="grid gap-4 xl:grid-cols-2">
      <article v-for="p in providers" :key="p.id" class="card min-w-0 space-y-4 p-5">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0"><h3 class="break-words font-semibold">{{ p.name }}</h3><a :href="p.base_url" target="_blank" rel="noopener noreferrer" class="mt-1 block break-all text-sm text-primary-600 underline">{{ p.base_url }}</a></div>
          <span class="shrink-0 rounded-full px-2.5 py-1 text-xs" :class="p.status === 'connected' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950' : 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-gray-300'">{{ t(`matureProviders.${p.status}`) }}</span>
        </div>
        <dl class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <div v-for="metric in metrics" :key="metric" class="min-w-0"><dt class="text-xs text-gray-500">{{ t(`matureProviders.${metric}`) }}</dt><dd :data-testid="`provider-${p.id}-${metric}`" class="mt-1 break-words text-sm font-medium">{{ amount(p, metric) }}</dd></div>
        </dl>
        <p v-if="lowBalance(p)" class="rounded-lg bg-amber-50 p-2 text-sm text-amber-800 dark:bg-amber-950">{{ t('matureProviders.lowBalance') }}</p>
        <p v-if="p.last_error" class="text-sm text-red-600">{{ p.last_error }}</p>
        <p v-if="p.notes" class="break-words text-sm text-gray-500">{{ p.notes }}</p>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-500"><span>{{ p.site_type }}</span><span>{{ t('matureProviders.cost') }}: {{ p.cost_multiplier ?? t('matureProviders.unknown') }}</span><span>{{ t('matureProviders.observed') }}: {{ p.observed_at ? new Date(p.observed_at).toLocaleString() : t('matureProviders.never') }}</span></div>
        <div class="flex flex-wrap gap-2 border-t border-gray-100 pt-3 dark:border-dark-700">
          <button class="btn btn-secondary btn-sm" :disabled="busy !== null || !p.has_credential" @click="refresh(p)">{{ t('matureProviders.refresh') }}</button>
          <button class="btn btn-secondary btn-sm" :data-testid="`provider-${p.id}-edit`" @click="edit(p)">{{ t('matureProviders.edit') }}</button>
          <a class="btn btn-secondary btn-sm" :href="p.base_url" target="_blank" rel="noopener noreferrer">{{ t('matureProviders.login') }}</a>
          <button class="btn btn-secondary btn-sm text-red-600" @click="deleting = p">{{ t('matureProviders.remove') }}</button>
        </div>
      </article>
    </div>
    <div class="flex items-center justify-between gap-3 text-sm"><span>{{ t('matureProviders.total') }}: {{ total }} · {{ t('matureProviders.page') }} {{ page }}</span><div class="flex gap-2"><button class="btn btn-secondary" :disabled="loading || page <= 1" @click="changePage(-1)">{{ t('matureProviders.previous') }}</button><button class="btn btn-secondary" :disabled="loading || page * pageSize >= total" @click="changePage(1)">{{ t('matureProviders.next') }}</button></div></div>
    <div v-if="formOpen" class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" @keydown.esc="close">
      <section role="dialog" aria-modal="true" :aria-label="t(editID == null ? 'matureProviders.add' : 'matureProviders.edit')" class="max-h-[90vh] w-full max-w-2xl overflow-y-auto rounded-2xl bg-white p-5 shadow-xl dark:bg-dark-900">
        <div class="mb-4 flex items-center justify-between"><h3 class="text-lg font-semibold">{{ t(editID == null ? 'matureProviders.add' : 'matureProviders.edit') }}</h3><button type="button" :disabled="saving" :aria-label="t('matureProviders.close')" class="btn btn-secondary" @click="close">×</button></div>
        <form data-testid="provider-save" class="space-y-4" @submit.prevent="save">
          <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="text-sm">{{ t('matureProviders.name') }}<input v-model="draft.name" data-testid="provider-name" required maxlength="120" class="input mt-1 w-full" /></label>
            <label class="text-sm">{{ t('matureProviders.siteType') }}<select v-model="draft.site_type" class="input mt-1 w-full"><option value="sub2api">Sub2API</option><option value="newapi">New API</option></select></label>
          </div>
          <label class="block text-sm">{{ t('matureProviders.url') }}<input v-model="draft.base_url" data-testid="provider-url" type="url" required placeholder="https://provider.example.com" class="input mt-1 w-full" /></label>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="text-sm">{{ t('matureProviders.authMode') }}<select v-model="draft.auth_mode" class="input mt-1 w-full"><option value="token">{{ t('matureProviders.token') }}</option><option value="password">{{ t('matureProviders.passwordMode') }}</option></select></label>
            <label v-if="draft.auth_mode === 'password'" class="text-sm">{{ t('matureProviders.account') }}<input v-model="draft.account" autocomplete="off" maxlength="320" class="input mt-1 w-full" /></label>
            <label v-if="draft.site_type === 'newapi' && draft.auth_mode === 'token'" class="text-sm">{{ t('matureProviders.userID') }}<input v-model="draft.user_id" maxlength="128" class="input mt-1 w-full" /></label>
          </div>
          <label class="block text-sm">{{ t(draft.auth_mode === 'password' ? 'matureProviders.password' : 'matureProviders.token') }}<input v-model="credential" type="password" autocomplete="new-password" :placeholder="t(editID == null ? 'matureProviders.optionalCredential' : 'matureProviders.keepCredential')" class="input mt-1 w-full" /></label>
          <label v-if="draft.auth_mode === 'token' && draft.site_type === 'sub2api'" class="block text-sm">{{ t('matureProviders.refreshToken') }}<input v-model="refreshToken" type="password" autocomplete="new-password" class="input mt-1 w-full" /></label>
          <p class="text-xs text-gray-500">{{ t('matureProviders.credentialNote') }}</p>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="text-sm">{{ t('matureProviders.cost') }}<input v-model="cost" type="number" min="0" max="1000000000" step="any" class="input mt-1 w-full" /></label>
            <label class="text-sm">{{ t('matureProviders.threshold') }}<input v-model="threshold" type="number" min="0" max="1000000000" step="any" class="input mt-1 w-full" /></label>
          </div>
          <label class="block text-sm">{{ t('matureProviders.refreshInterval') }}<input v-model.number="draft.refresh_interval_seconds" type="number" min="0" max="86400" step="1" class="input mt-1 w-full" /></label>
          <p class="text-xs text-gray-500">{{ t('matureProviders.refreshIntervalNote') }}</p>
          <p class="text-xs text-gray-500">{{ t('matureProviders.costNote') }}</p>
          <label class="flex items-center gap-2 text-sm"><input v-model="draft.suppress_low_balance_alert" type="checkbox" />{{ t('matureProviders.suppress') }}</label>
          <label class="block text-sm">{{ t('matureProviders.notes') }}<textarea v-model="draft.notes" maxlength="1000" rows="2" class="input mt-1 w-full" /></label>
          <div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('matureProviders.cancel') }}</button><button class="btn btn-primary" :disabled="saving">{{ t('matureProviders.save') }}</button></div>
        </form>
      </section>
    </div>
    <div v-if="deleting" class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <section role="dialog" aria-modal="true" :aria-label="t('matureProviders.remove')" class="w-full max-w-md space-y-4 rounded-2xl bg-white p-5 dark:bg-dark-900"><h3 class="font-semibold">{{ t('matureProviders.remove') }} · {{ deleting.name }}</h3><p class="text-sm">{{ t('matureProviders.deleteNote') }}</p><div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="busy !== null" @click="deleting = null">{{ t('matureProviders.cancel') }}</button><button class="btn btn-primary" :disabled="busy !== null" @click="remove">{{ t('matureProviders.confirmDelete') }}</button></div></section>
    </div>
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { listProviders, saveProvider, refreshProvider, deleteProvider, type MatureProvider, type ProviderInput } from '@/api/admin/matureProviders'
const props = defineProps<{ passive?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n(), auth = useAuthStore()
const providers = ref<MatureProvider[]>([]), total = ref(0), page = ref(1), pageSize = 20
const query = ref(''), status = ref(''), loading = ref(false), error = ref(''), busy = ref<number | null>(null)
const states = ['unknown', 'connected', 'disconnected'] as const
const metrics = ['balance', 'today_spend', 'total_spend', 'total_recharge'] as const
const formOpen = ref(false), editID = ref<number | null>(null), saving = ref(false), deleting = ref<MatureProvider | null>(null)
const defaults = (): ProviderInput => ({ refresh_interval_seconds: 0, name: '', site_type: 'sub2api', base_url: '', notes: '', cost_multiplier: null, low_balance_threshold: null, suppress_low_balance_alert: false, auth_mode: 'token', account: '', user_id: '' })
const draft = ref<ProviderInput>(defaults()), credential = ref(''), refreshToken = ref(''), cost = ref<string | number>(''), threshold = ref<string | number>('')
let controller: AbortController | undefined, generation = 0, alive = true
const refreshed = new Set<number>()
function amount(p: MatureProvider, field: typeof metrics[number]) {
  const value = p.metrics[field]
  return value == null || !Number.isFinite(value) ? t('matureProviders.unknown') : `${p.metrics.currency || t('matureProviders.currencyUnknown')} ${value.toLocaleString(undefined, { maximumFractionDigits: 6 })}`
}
function lowBalance(p: MatureProvider) { return p.status === 'connected' && !p.suppress_low_balance_alert && p.low_balance_threshold != null && p.metrics.balance != null && p.metrics.balance < p.low_balance_threshold }
function message(e: unknown) { return t((e as { status?: number })?.status === 409 ? 'matureProviders.conflict' : 'matureProviders.failed') }
async function refreshVisible(items: MatureProvider[], current: number) {
  if (props.passive || formOpen.value || current !== generation) return
  const pending = items.filter(p => p.has_credential && !refreshed.has(p.id))
  if (!pending.length) return
  for (const provider of pending) {
    refreshed.add(provider.id)
    if (!alive || formOpen.value || current !== generation) return
    try { await refreshProvider(provider) } catch { /* keep the last saved card */ }
  }
  if (alive && !formOpen.value && current === generation) await load()
}
async function load() {
  controller?.abort(); controller = new AbortController(); const current = ++generation; loading.value = true
  try {
    const result = await listProviders({ search: query.value, status: status.value, page: page.value, page_size: pageSize }, controller.signal)
    if (alive && current === generation) { providers.value = result.items; total.value = result.total; await refreshVisible(result.items, current) }
  }
  catch (e) { if (alive && current === generation) error.value = message(e) }
  finally { if (alive && current === generation) loading.value = false }
}
function search() { page.value = 1; error.value = ''; void load() }
function changePage(delta: number) { page.value += delta; error.value = ''; void load() }
function edit(p?: MatureProvider) {
  error.value = ''; editID.value = p?.id ?? null
  draft.value = p ? { refresh_interval_seconds: p.refresh_interval_seconds || 0, name: p.name, site_type: p.site_type, base_url: p.base_url, notes: p.notes, cost_multiplier: p.cost_multiplier, low_balance_threshold: p.low_balance_threshold, suppress_low_balance_alert: p.suppress_low_balance_alert ?? false, auth_mode: p.auth_mode || 'token', account: p.account || '', user_id: p.user_id || '', revision: p.revision } : defaults()
  credential.value = ''; refreshToken.value = ''; cost.value = p?.cost_multiplier ?? ''; threshold.value = p?.low_balance_threshold ?? ''; formOpen.value = true
}
function close() { if (saving.value) return; formOpen.value = false; credential.value = ''; refreshToken.value = ''; error.value = '' }
async function save() {
  if (saving.value) return
  saving.value = true; error.value = ''
  const input = { ...draft.value, cost_multiplier: cost.value === '' ? null : Number(cost.value), low_balance_threshold: threshold.value === '' ? null : Number(threshold.value) }
  if (credential.value) { if (input.auth_mode === 'password') input.password = credential.value; else input.access_token = credential.value }
  if (refreshToken.value && input.auth_mode === 'token' && input.site_type === 'sub2api') input.refresh_token = refreshToken.value
  try { await saveProvider(editID.value, input); formOpen.value = false; credential.value = ''; refreshToken.value = ''; await load(); emit('changed') }
  catch (e) { error.value = message(e) }
  finally { saving.value = false }
}
async function refresh(p: MatureProvider) {
  if (busy.value !== null) return
  busy.value = p.id; error.value = ''
  try { await refreshProvider(p); await load() } catch (e) { error.value = message(e) } finally { busy.value = null }
}
async function remove() {
  if (!deleting.value || busy.value !== null) return
  busy.value = deleting.value.id; error.value = ''
  try { await deleteProvider(deleting.value); deleting.value = null; await load(); emit('changed') } catch (e) { error.value = message(e); deleting.value = null } finally { busy.value = null }
}
onMounted(load)
onBeforeUnmount(() => { alive = false; generation++; controller?.abort(); credential.value = ''; refreshToken.value = '' })
</script>
