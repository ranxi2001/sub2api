<template>
  <section class="card space-y-3 p-4" data-testid="upstream-channel-manager">
    <div class="flex flex-wrap items-center justify-between gap-2"><h2 class="font-semibold">{{ t('upstreamEntities.title') }}</h2><button type="button" class="btn btn-secondary" data-testid="open-channel-manager" @click="open">{{ t('upstreamEntities.manage') }}</button></div>
    <p class="text-sm text-gray-500">{{ t('upstreamEntities.history') }}</p>
    <template v-if="opened">
      <slot />
      <div class="flex flex-wrap gap-2"><input v-model="search" class="input" :placeholder="t('upstreamEntities.search')" /><button type="button" class="btn btn-secondary" @click="load(1)">{{ t('upstreamEntities.load') }}</button><button type="button" class="btn btn-primary" data-testid="new-upstream-channel" :disabled="busy" @click="begin()">{{ t('upstreamEntities.create') }}</button></div>
      <div class="overflow-x-auto"><table class="w-full min-w-[650px] text-left text-sm"><thead><tr><th>ID / {{ t('upstreamEntities.code') }}</th><th>{{ t('upstreamEntities.name') }}</th><th>{{ t('upstreamEntities.remark') }}</th><th>{{ t('upstreamEntities.members') }}</th><th>{{ t('upstreamEntities.actions') }}</th></tr></thead><tbody><tr v-for="c in items" :key="c.id" class="border-t"><td class="p-2">#{{ c.id }} · {{ c.code }}</td><td>{{ c.name }}</td><td class="max-w-80 whitespace-pre-wrap break-words">{{ c.remark || t('upstreamEntities.noRemark') }}</td><td>{{ c.members }}</td><td><button type="button" class="btn btn-secondary" data-testid="edit-upstream-channel" :disabled="busy" @click="begin(c.id)">{{ t('upstreamEntities.edit') }}</button><button type="button" class="btn btn-secondary" :disabled="busy" @click="showMembers(c)">{{ t('upstreamEntities.members') }}</button></td></tr></tbody></table></div>
      <div class="flex gap-3"><button type="button" :disabled="busy || page <= 1" @click="load(page - 1)">{{ t('profitOps.previous') }}</button><span>{{ page }} · {{ total }}</span><button type="button" :disabled="busy || page * 20 >= total" @click="load(page + 1)">{{ t('profitOps.next') }}</button></div>
      <div v-if="draft" class="space-y-3 rounded-lg border p-3" data-testid="channel-entity-draft">
        <label class="block">{{ t('upstreamEntities.code') }}<input v-model="draft.code" class="input w-full" :disabled="draft.id > 0 || busy" maxlength="64" data-testid="channel-code" /></label>
        <label class="block">{{ t('upstreamEntities.name') }}<input v-model="draft.name" class="input w-full" :disabled="busy" data-testid="channel-name" /></label>
        <label class="block">{{ t('upstreamEntities.remark') }}<textarea v-model="draft.remark" class="input w-full" :disabled="busy" data-testid="channel-remark" /></label>
        <p v-if="latestSaved" class="whitespace-pre-wrap text-sm">{{ t('upstreamEntities.currentRemark') }}：{{ latestSaved }}</p>
        <p>{{ t('upstreamEntities.impact') }} {{ draft.members }} {{ t('upstreamEntities.accountsUnit') }}</p>
        <label class="flex gap-2"><input v-model="confirmed" type="checkbox" data-testid="confirm-channel-edit" />{{ t(draft.id ? 'upstreamEntities.confirmEdit' : 'upstreamEntities.confirmNewChannel') }}</label>
        <p v-if="invalid" class="text-red-600">{{ t('upstreamEntities.validation') }}</p>
        <div class="flex flex-wrap gap-2"><button type="button" class="btn btn-primary" data-testid="save-upstream-channel" :disabled="busy || invalid || needsRead || !confirmed" @click="save">{{ t('upstreamEntities.save') }}</button><button type="button" class="btn btn-secondary" :disabled="busy" @click="draft = null; error = ''">{{ t('upstreamEntities.cancel') }}</button><button v-if="draft.id" type="button" class="btn btn-secondary" :disabled="busy" @click="readLatest">{{ t('upstreamEntities.readLatest') }}</button><button v-if="draft.id" type="button" class="btn btn-danger" data-testid="delete-upstream-channel" :disabled="busy || !confirmed || draft.members > 0 || needsRead" @click="remove">{{ t('upstreamEntities.delete') }}</button></div>
        <p v-if="draft.id && draft.members" class="text-sm">{{ t('upstreamEntities.deleteBlocked') }}</p>
      </div>
      <section v-if="memberChannel" class="space-y-2 border-t pt-3"><h3>{{ memberChannel.name }} #{{ memberChannel.id }} · {{ t('upstreamEntities.members') }} {{ membersTotal }}</h3><p class="text-xs">{{ t('upstreamEntities.bounded') }}</p><label v-for="m in members" :key="m.account_id" class="block"><input v-model="selectedIds" type="checkbox" :value="m.account_id" /> #{{ m.account_id }} {{ m.account_name }} {{ m.deleted ? t('profitOps.deleted') : '' }}</label><AccountUpstreamChannelBinding :account-ids="selectedIds" @saved="reloadMembers" /><div class="flex gap-3"><button type="button" :disabled="memberPage <= 1 || busy" @click="showMembers(memberChannel, memberPage - 1)">{{ t('profitOps.previous') }}</button><span>{{ memberPage }}</span><button type="button" :disabled="memberPage * 20 >= membersTotal || busy" @click="showMembers(memberChannel, memberPage + 1)">{{ t('profitOps.next') }}</button></div></section>
    </template>
    <p v-if="error" role="alert" class="text-red-600" data-testid="channel-entity-error">{{ error }}</p><p v-if="saved" role="status">{{ t('upstreamEntities.saved') }}</p>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { listUpstreamChannels, getUpstreamChannel, saveUpstreamChannel, deleteUpstreamChannel, getChannelMembers, type UpstreamChannel, type ChannelBinding } from '@/api/admin/upstreamChannels'
import AccountUpstreamChannelBinding from '@/components/account/AccountUpstreamChannelBinding.vue'
const props = defineProps<{ expanded?: boolean }>()
const emit = defineEmits<{ changed: []; selected: [channel: UpstreamChannel] }>()
const { t } = useI18n()
const items = ref<UpstreamChannel[]>([]), total = ref(0), page = ref(1), search = ref(''), opened = ref(false), busy = ref(false), error = ref(''), saved = ref(false), confirmed = ref(false), needsRead = ref(false)
const latestSaved = ref('')
const draft = ref<UpstreamChannel | null>(null), memberChannel = ref<UpstreamChannel | null>(null), members = ref<ChannelBinding[]>([]), membersTotal = ref(0), memberPage = ref(1), selectedIds = ref<number[]>([])
const invalid = computed(() => !draft.value || !/^[a-z0-9][a-z0-9_-]{0,63}$/.test(draft.value.code) || !draft.value.name.trim() || Array.from(draft.value.name.trim()).length > 100 || Array.from(draft.value.remark.trim()).length > 500)
async function load(next = page.value) { busy.value = true; error.value = ''; try { const result = await listUpstreamChannels(search.value, next); items.value = result.items; total.value = result.total; page.value = next } catch { error.value = t('upstreamEntities.failed') } finally { busy.value = false } }
function open() { opened.value = true; void load(1) }
async function begin(id = 0) { latestSaved.value = ''; error.value = ''; saved.value = false; confirmed.value = false; needsRead.value = false; if (!id) { draft.value = { id: 0, code: '', name: '', remark: '', revision: 0, members: 0 }; return } busy.value = true; try { draft.value = await getUpstreamChannel(id) } catch { error.value = t('upstreamEntities.failed') } finally { busy.value = false } }
async function readLatest() { if (!draft.value?.id) return; busy.value = true; try { const fresh = await getUpstreamChannel(draft.value.id); latestSaved.value = `${fresh.name} · ${fresh.remark}`; draft.value.revision = fresh.revision; draft.value.members = fresh.members; confirmed.value = false; needsRead.value = false; error.value = ''; } catch { error.value = t('upstreamEntities.failed') } finally { busy.value = false } }
async function save() {
 if (!draft.value || invalid.value || busy.value || needsRead.value || !confirmed.value) return
 busy.value = true; error.value = ''; saved.value = false
 try {
  const written = await saveUpstreamChannel(draft.value.id, { code: draft.value.code, name: draft.value.name.trim(), remark: draft.value.remark.trim(), revision: draft.value.revision, confirm: confirmed.value })
  draft.value.id = written.id; draft.value.code = written.code; draft.value.revision = written.revision; needsRead.value = true
  const fresh = await getUpstreamChannel(written.id)
  if (fresh.name !== draft.value.name.trim() || fresh.remark !== draft.value.remark.trim() || fresh.revision !== written.revision) throw new Error('readback')
  draft.value = null; needsRead.value = false; saved.value = true; await load(); emit('changed')
 } catch (cause) { const e = cause as { status?: number; response?: { status?: number } }; needsRead.value = (draft.value?.id ?? 0) > 0; error.value = t((e.status ?? e.response?.status) === 409 ? 'upstreamEntities.conflict' : 'upstreamEntities.readbackFailed') }
 finally { busy.value = false }
}
async function remove() { if (!draft.value?.id || draft.value.members || !confirmed.value || needsRead.value) return; busy.value = true; error.value = ''; try { await deleteUpstreamChannel(draft.value.id, draft.value.revision); draft.value = null; await load(); emit('changed') } catch { needsRead.value = true; error.value = t('upstreamEntities.deleteBlocked') } finally { busy.value = false } }
async function showMembers(channel: UpstreamChannel, next = 1) { emit('selected', channel); busy.value = true; error.value = ''; try { const result = await getChannelMembers(channel.id, next); memberChannel.value = channel; members.value = result.items; membersTotal.value = result.total; memberPage.value = next; selectedIds.value = [] } catch { error.value = t('upstreamEntities.failed') } finally { busy.value = false } }
function reloadMembers() { if (memberChannel.value) void showMembers(memberChannel.value, memberPage.value); void load(); emit('changed') }
onMounted(() => { if (props.expanded) open() })
</script>
