<template>
  <section class="card space-y-4 p-5">
    <h2 class="text-lg font-semibold">{{ t('qqbot.moderation.title') }}</h2>
    <form class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" @submit.prevent="search">
      <label class="text-sm">{{ t('qqbot.group') }}<input v-model.trim="filters.group_id" class="input" maxlength="128" data-testid="qq-moderation-group" /></label>
      <QQMemberPicker v-model="filters.member_id" :group-id="filters.group_id" :app-id="appId" :disabled="loading" @select="selectMember" />
      <label class="text-sm">{{ t('common.status') }}<select v-model="filters.status" class="input"><option value="">{{ t('qqbot.workspace.all') }}</option><option v-for="status in statuses" :key="status" :value="status">{{ label('statuses', status) }}</option></select></label>
      <label class="text-sm">{{ t('qqbot.workspace.from') }}<input v-model="filters.from" class="input" type="datetime-local" /></label>
      <label class="text-sm">{{ t('qqbot.workspace.to') }}<input v-model="filters.to" class="input" type="datetime-local" /></label>
      <button class="btn btn-primary self-end" :disabled="loading">{{ t('common.search') }}</button>
    </form>
    <p v-if="error" role="alert" class="text-red-600">{{ error }}</p>
    <article v-for="item in data.items" :key="item.id" class="space-y-2 rounded-lg border p-3 text-sm dark:border-dark-700">
      <p>{{ new Date(item.created_at).toLocaleString() }} · {{ label('reasons', item.reason) }}</p>
      <p class="break-all text-gray-500">{{ item.group_id }} · {{ item.member_id }}</p>
      <p>{{ t('qqbot.moderation.muteResult', { minutes: item.mute_seconds / 60, status: label('statuses', item.mute_status) }) }}</p>
      <p>{{ t('qqbot.moderation.recallResult', { succeeded: item.recall_succeeded, total: item.recall_total, pending: item.pending }) }}</p>
      <p v-if="item.error" class="break-all text-red-600">{{ item.error }}</p>
    </article>
    <p v-if="!data.items.length && !loading">{{ t('common.noData') }}</p>
    <div class="flex flex-wrap items-center justify-between gap-3 text-sm">
      <span>{{ t('qqbot.recordsPage', { total: data.total, page: data.page, pages }) }}</span>
      <div class="flex gap-2"><button class="btn btn-secondary" :disabled="loading || data.page <= 1" @click="load(data.page - 1)">{{ t('qqbot.previousPage') }}</button><button class="btn btn-secondary" :disabled="loading || data.page >= pages" @click="load(data.page + 1)">{{ t('qqbot.nextPage') }}</button></div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQModerationRecord, type QQMemberOption } from '@/api/qqbot'
import QQMemberPicker from './QQMemberPicker.vue'
const props = defineProps<{ appId?: string }>()
const { t, te } = useI18n()
const filters = reactive({ group_id: '', member_id: '', status: '', from: '', to: '' })
watch(() => filters.group_id, () => { filters.member_id = '' }, { flush: 'sync' })
function selectMember(member: QQMemberOption) { filters.group_id = member.group_id; filters.member_id = member.member_id }
let applied = { ...filters }
const statuses = ['queued', 'started', 'succeeded', 'failed', 'unknown', 'skipped']
const data = ref({ items: [] as QQModerationRecord[], total: 0, page: 1, page_size: 20 })
const error = ref(''), loading = ref(false)
const pages = computed(() => Math.max(1, Math.ceil(data.value.total / data.value.page_size)))
function label(kind: string, code: string) { const key = `qqbot.moderation.${kind}.${code}`; return te(key) ? t(key) : code }
function search() { applied = { ...filters }; void load(1) }
let request = 0
async function load(page: number) {
  const seq = ++request
  error.value = ''
  if (props.appId === '') { loading.value = false; return }
  loading.value = true
  try {
    const result = await qqbotAPI.moderationRecords({ ...applied, from: applied.from ? new Date(applied.from).toISOString() : '', to: applied.to ? new Date(applied.to).toISOString() : '', page, page_size: 20 })
    if (seq === request) data.value = result
  } catch (e) { if (seq === request) error.value = (e as Error).message }
  finally { if (seq === request) loading.value = false }
}
watch(() => props.appId, () => {
  ++request
  Object.assign(filters, { group_id: '', member_id: '', status: '', from: '', to: '' })
  data.value = { items: [], total: 0, page: 1, page_size: 20 }
  search()
}, { immediate: true })
onBeforeUnmount(() => { ++request })
</script>
