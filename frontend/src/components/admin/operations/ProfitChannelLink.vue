<template>
  <fieldset class="space-y-3 rounded-lg border p-3 dark:border-dark-600" :disabled="busy">
    <legend class="px-1 font-medium">{{ t('profitOps.linkTitle', { provider: `${provider.provider_name} #${provider.provider_id}` }) }}</legend>
    <p>{{ t('profitOps.currentLink', { channel: currentChannel }) }}</p>
    <p class="text-sm text-gray-500">{{ t('profitOps.linkImpact') }}</p>
    <form class="flex flex-wrap items-end gap-2" @submit.prevent="verify">
      <label>{{ t('profitOps.existingId') }}<input v-model="id" type="number" min="1" step="1" required class="input block" @input="clearTarget" /></label>
      <button class="btn btn-secondary" :disabled="!validId || busy">{{ t('profitOps.checkChannel') }}</button>
    </form>
    <template v-if="target">
      <p>{{ t('profitOps.proposedLink', { name: target.name, code: target.code, id: target.id }) }}</p>
      <label class="flex items-center gap-2"><input v-model="confirmed" type="checkbox" />{{ t('profitOps.confirmLink', { provider: provider.provider_name, channel: target.name }) }}</label>
    </template>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-primary" :disabled="!target || !confirmed || busy" @click="save">{{ t('profitOps.saveLink') }}</button>
      <button type="button" class="btn btn-secondary" @click="emit('cancel')">{{ t('upstreamEntities.cancel') }}</button>
    </div>
    <p v-if="error" role="alert" class="text-red-600">{{ t('profitOps.channelNotVerified') }}</p>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUpstreamChannel, type UpstreamChannel } from '@/api/admin/upstreamChannels'
import { linkProfitChannel, type ProfitProvider } from '@/api/admin/profitOperations'
const props = defineProps<{ provider: ProfitProvider; currentChannel: string }>()
const emit = defineEmits<{ cancel: []; busy: [value: boolean]; reread: [providerId: number, channelId: number, state: 'written' | 'conflict' | 'failed'] }>()
const { t } = useI18n()
const id = ref<number | ''>(''), confirmed = ref(false), busy = ref(false), error = ref(false)
const target = ref<UpstreamChannel | null>(null)
const validId = computed(() => typeof id.value === 'number' && Number.isSafeInteger(id.value) && id.value > 0 && id.value !== props.provider.channel_id)
function clearTarget() { target.value = null; confirmed.value = false; error.value = false }
function setBusy(value: boolean) { busy.value = value; emit('busy', value) }
async function verify() {
  if (busy.value || !validId.value) return
  clearTarget(); setBusy(true)
  try {
    const channel = await getUpstreamChannel(Number(id.value))
    if (channel.id !== id.value || !channel.code || !channel.name) throw new Error('Unverified channel')
    target.value = channel
  } catch { error.value = true }
  finally { setBusy(false) }
}
async function save() {
  if (busy.value || !confirmed.value || !validId.value || target.value?.id !== id.value || !target.value) return
  const { provider_id, revision } = props.provider, channelId = target.value.id
  setBusy(true)
  let state: 'written' | 'conflict' | 'failed' = 'written'
  try { await linkProfitChannel(provider_id, channelId, revision) }
  catch (cause) { state = (cause as { status?: number })?.status === 409 ? 'conflict' : 'failed' }
  finally { clearTarget(); setBusy(false) }
  emit('reread', provider_id, channelId, state)
}
</script>
