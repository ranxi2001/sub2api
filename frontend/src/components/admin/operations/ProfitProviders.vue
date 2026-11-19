<template>
  <section class="space-y-2 border-y py-3" data-testid="provider-links">
    <div class="flex flex-wrap items-center gap-2"><h3 class="font-semibold">{{ t('profitOps.profitChannel') }}</h3><button type="button" class="btn btn-secondary" :disabled="busy" @click="load(page)">{{ t('matureProviders.refreshList') }}</button></div>
    <p v-if="loading" role="status">{{ t('matureProviders.loading') }}</p>
    <p v-else-if="loadError" role="alert">{{ t('profitOps.providerLoadFailed') }}</p>
    <template v-else-if="result">
      <p v-if="!result.items.length">{{ t('profitOps.providersEmpty') }}</p>
      <div v-for="p in result.items" :key="p.provider_id" class="space-y-2" :data-testid="`provider-link-${p.provider_id}`">
        <div class="flex flex-wrap items-center gap-2"><span>{{ p.provider_name }} #{{ p.provider_id }} → {{ channelName(p) }}</span><button type="button" class="btn btn-secondary btn-sm" :aria-expanded="editing === p.provider_id" :disabled="busy" @click="begin(p)">{{ t('profitOps.linkChannel') }}</button></div>
        <label class="flex flex-wrap items-center gap-2 text-sm">{{ t('channelConfig.costMode') }}
          <select :value="p.cost_mode || 'wallet'" :disabled="busy" class="input" @change="setCostMode(p, $event)"><option value="estimate">{{ t('channelConfig.estimate') }}</option><option value="wallet">{{ t('channelConfig.wallet') }}</option></select>
        </label>
        <p class="text-xs text-gray-500">{{ t('channelConfig.costHelp') }}</p>
        <ProfitChannelLink v-if="editing === p.provider_id" :provider="p" :current-channel="channelName(p)" @cancel="editing = null" @busy="writing = $event" @reread="linkResult" />
      </div>
      <div v-if="result.total > result.page_size || page > 1" class="flex flex-wrap items-center gap-3 text-sm">
        <button type="button" class="btn btn-secondary" :disabled="busy || page <= 1" @click="load(page - 1)">{{ t('profitOps.previous') }}</button>
        <span>{{ t('matureProviders.page') }} {{ page }} · {{ t('matureProviders.total') }}: {{ result.total }}</span>
        <button type="button" class="btn btn-secondary" :disabled="busy || page * result.page_size >= result.total" @click="load(page + 1)">{{ t('profitOps.next') }}</button>
      </div>
    </template>
    <p v-if="message" role="status" class="text-sm">{{ message }}</p>
    <p v-if="actionError" role="alert" class="text-sm text-red-600">{{ actionError }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { linkProfitChannel, listProfitProviders, type ProfitProvider, type ProfitProviderPage } from '@/api/admin/profitOperations'
import ProfitChannelLink from './ProfitChannelLink.vue'

const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n()
const result = ref<ProfitProviderPage | null>(null), page = ref(1)
const loading = ref(false), loadError = ref(false), writing = ref(false), busy = computed(() => loading.value || writing.value)
const editing = ref<number | null>(null), message = ref(''), actionError = ref('')
const lifetime = new AbortController()
function channelName(p: ProfitProvider) {
  return p.channel_id ? `${p.channel_name || t('profitOps.unknown')} #${p.channel_id}` : t('profitOps.noLink')
}
async function load(next: number) {
  if (busy.value || lifetime.signal.aborted) return
  editing.value = null; message.value = ''; actionError.value = ''; loading.value = true; loadError.value = false
  try {
    result.value = await listProfitProviders(next, lifetime.signal); page.value = result.value.page
  } catch { result.value = null; loadError.value = !lifetime.signal.aborted }
  finally { loading.value = false }
}
function begin(p: ProfitProvider) {
  message.value = ''; actionError.value = ''; editing.value = editing.value === p.provider_id ? null : p.provider_id
}
async function linkResult(providerId: number, channelId: number, state: 'written' | 'conflict' | 'failed', mode?: 'estimate'|'wallet') {
  await load(page.value)
  if (lifetime.signal.aborted) return
  if (state === 'written' && result.value?.items.some(p => p.provider_id === providerId && p.channel_id === channelId && (!mode || p.cost_mode===mode))) message.value = t('upstreamEntities.saved')
  else actionError.value = t(state === 'conflict' ? 'profitOps.linkConflict' : 'profitOps.linkUnconfirmed')
  emit('changed')
}
async function setCostMode(p: ProfitProvider, event: Event) {
 const select=event.target as HTMLSelectElement, mode=select.value as 'estimate'|'wallet'
 select.value=p.cost_mode || 'wallet'
 if(!p.channel_id || busy.value) return
 writing.value=true
 let state: 'written'|'conflict'|'failed'='written'
 try { await linkProfitChannel(p.provider_id,p.channel_id,p.revision,mode) }
 catch(cause){state=(cause as {status?:number})?.status===409?'conflict':'failed'}
 finally{writing.value=false}
 await linkResult(p.provider_id,p.channel_id,state,mode)
}
onMounted(() => load(1))
onBeforeUnmount(() => lifetime.abort())
</script>
