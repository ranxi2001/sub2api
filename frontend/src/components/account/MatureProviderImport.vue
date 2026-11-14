<template>
  <section class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
    <div class="flex flex-wrap items-center gap-2"><h3 class="font-medium">{{ t('matureProviders.importTitle') }}</h3><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.matureProviderImport" :user-id="auth.user?.id" /></div>
    <p class="text-xs text-gray-500">{{ t('matureProviders.importNote') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <label class="block text-sm">{{ t('matureProviders.title') }}<div class="mt-1 flex gap-2"><select data-testid="provider-choice" v-model.number="providerID" class="input min-w-0 flex-1" :disabled="busy" @change="selectProvider"><option :value="0">{{ t('matureProviders.chooseProvider') }}</option><option v-for="p in providers" :key="p.id" :value="p.id" :disabled="!p.has_credential">{{ p.name }} · {{ p.site_type }}</option></select><button type="button" class="btn btn-secondary" :disabled="busy" @click="loadProviders">{{ t('matureProviders.refreshList') }}</button></div></label>
    <label v-if="providerID" class="block text-sm">{{ t('matureProviders.chooseKey') }}<div class="mt-1 flex gap-2"><select data-testid="provider-key-choice" v-model="keyID" class="input min-w-0 flex-1" :disabled="busy" @change="selected"><option value="">{{ t('matureProviders.chooseKey') }}</option><option v-for="key in keys" :key="key.id" :value="key.id" :disabled="!key.available">{{ key.name }} · {{ key.group_name }} {{ key.group_platform || '' }} · {{ key.group_multiplier ?? t('matureProviders.unknown') }}</option></select><button type="button" class="btn btn-secondary" :disabled="busy" @click="loadKeys">{{ t('matureProviders.refreshKeys') }}</button></div></label>
    <div v-if="providerID" class="flex flex-wrap gap-2"><button type="button" class="btn btn-secondary" :disabled="busy" @click="refreshSite">{{ t('matureProviders.refresh') }}</button><button type="button" class="btn btn-secondary" :disabled="busy" data-testid="provider-key-new" @click="openCreate">{{ t('matureProviders.createKey') }}</button></div>
    <div v-if="creating" class="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4">
      <section role="dialog" aria-modal="true" :aria-label="t('matureProviders.createKey')" class="w-full max-w-lg space-y-4 rounded-xl bg-white p-5 dark:bg-dark-900">
        <h3 class="font-semibold">{{ t('matureProviders.createKey') }}</h3>
        <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
        <label class="block text-sm">{{ t('matureProviders.chooseGroup') }}<select data-testid="provider-group-choice" v-model="groupID" class="input mt-1 w-full" :disabled="busy" @change="resetOperation"><option value="">{{ t('matureProviders.chooseGroup') }}</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }} · {{ group.platform || '' }} · {{ group.multiplier ?? t('matureProviders.unknown') }}</option></select></label>
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="loadGroups">{{ t('matureProviders.refreshGroups') }}</button>
        <p class="text-sm">{{ t('matureProviders.keyName') }}: {{ selectedGroup?.key_name || t('matureProviders.chooseGroupFirst') }}</p>
        <label class="flex items-start gap-2 text-sm"><input v-model="confirmed" type="checkbox" class="mt-1" :disabled="busy || uncertain" />{{ t('matureProviders.confirmKey') }}</label>
        <div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="busy" @click="creating = false">{{ t('matureProviders.cancel') }}</button><button type="button" class="btn btn-primary" :disabled="busy || !groupID || !confirmed || uncertain" @click="createKey">{{ t('matureProviders.create') }}</button></div>
      </section>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import api from '@/api/client'
import { listProviders, refreshProvider, type MatureProvider } from '@/api/admin/matureProviders'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
export interface ProviderSelection { provider_id: number; provider_key_id: string }
interface Key { id: string; name: string; group_name: string; group_platform?: string; group_multiplier: number | null; available: boolean }
interface Group { id: string; name: string; platform?: string; multiplier: number | null; key_name: string }
const props = defineProps<{ platform: string }>()
const emit = defineEmits<{ select: [selection: ProviderSelection | null] }>()
const { t } = useI18n(), auth = useAuthStore()
const providers = ref<MatureProvider[]>([]), keys = ref<Key[]>([]), groups = ref<Group[]>([])
const providerID = ref(0), keyID = ref(''), groupID = ref(''), busy = ref(false), error = ref(''), creating = ref(false), confirmed = ref(false), uncertain = ref(false)
const provider = computed(() => providers.value.find(p => p.id === providerID.value)), selectedGroup = computed(() => groups.value.find(g => g.id === groupID.value))
let operationKey = ''
function selected() { emit('select', providerID.value && keyID.value ? { provider_id: providerID.value, provider_key_id: keyID.value } : null) }
function resetOperation() { confirmed.value = false; uncertain.value = false; operationKey = '' }
async function execute(work: () => Promise<void>) { if (busy.value) return; busy.value = true; error.value = ''; try { await work() } catch (e) { error.value = t((e as { status?: number })?.status === 409 ? 'matureProviders.conflict' : 'matureProviders.failed') } finally { busy.value = false } }
async function loadProviders() { await execute(async () => { const all: MatureProvider[] = []; for (let page = 1; ; page++) { const data = await listProviders({ search: '', status: '', page, page_size: 100 }); all.push(...data.items); if (all.length >= data.total || !data.items.length) break } providers.value = all; if (!provider.value) { providerID.value = 0; keyID.value = ''; selected() } }) }
async function fetchKeys() { const data = await api.get<{ keys: Key[] }>(`/admin/providers/${providerID.value}/keys`); keys.value = data.data.keys; if (!keys.value.some(k => k.id === keyID.value && k.available)) keyID.value = ''; selected() }
async function loadKeys() { await execute(fetchKeys) }
function selectProvider() { keyID.value = ''; keys.value = []; groups.value = []; resetOperation(); selected(); if (providerID.value) void loadKeys() }
async function refreshSite() { await execute(async () => { if (!provider.value) return; const updated = await refreshProvider(provider.value); providers.value = providers.value.map(p => p.id === updated.id ? updated : p); await fetchKeys() }) }
async function loadGroups() { await execute(async () => { const response = await api.get<{ groups: Group[] }>(`/admin/providers/${providerID.value}/groups`); groups.value = response.data.groups; if (!groups.value.some(g => g.id === groupID.value)) groupID.value = '' }) }
async function openCreate() { creating.value = true; resetOperation(); await loadGroups() }
async function createKey() {
  if (busy.value || !confirmed.value || !groupID.value || !provider.value || uncertain.value) return
  busy.value = true; error.value = ''; operationKey ||= crypto.randomUUID()
  try {
    const result = await api.post<{ key_id: string }>(`/admin/providers/${providerID.value}/keys`, { group_id: groupID.value, revision: provider.value.revision, confirm: true }, { headers: { 'Idempotency-Key': operationKey } })
    keyID.value = result.data.key_id || ''; creating.value = false; await fetchKeys()
  } catch (e) {
    const reason = (e as { reason?: string })?.reason
    if (reason === 'PROVIDER_KEY_CREATE_FAILED' || reason === 'PROVIDER_VALIDATION' || reason === 'PROVIDER_REVISION') {
      confirmed.value = false; operationKey = ''; error.value = t('matureProviders.keyRejected')
    } else {
      // A pending result or transport failure cannot prove the write failed.
      // Keep the operation blocked until the operator inspects upstream keys.
      uncertain.value = true; error.value = t('matureProviders.keyUncertain')
    }
  } finally { busy.value = false }
}
watch(() => props.platform, () => { providerID.value = 0; keyID.value = ''; selected() })
onMounted(loadProviders)
</script>
