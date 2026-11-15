<template>
  <div class="min-w-0 space-y-2" data-testid="qq-member-picker">
    <label :for="`${id}-search`" class="block text-sm">{{ t('qqbot.memberPicker.search') }}</label>
    <div class="flex gap-2">
      <input :id="`${id}-search`" v-model="query" type="search" class="input min-w-0 flex-1" maxlength="100" :disabled="disabled || missingApp" :placeholder="t('qqbot.memberPicker.searchPlaceholder')" :aria-describedby="`${id}-help`" data-testid="qq-member-search" @keydown.enter.prevent="refresh" />
      <button type="button" class="btn btn-secondary shrink-0" :disabled="disabled || missingApp || loading" @click="refresh">{{ t('common.refresh') }}</button>
    </div>
    <label :for="`${id}-select`" class="block text-sm">{{ t('qqbot.memberPicker.member') }}</label>
    <select :id="`${id}-select`" :value="selectedKey" :required="required" :disabled="disabled || missingApp || (!required && (loading || !!error))" :aria-busy="loading" class="input w-full" data-testid="qq-member-select" :aria-describedby="`${id}-help`" @change="choose">
      <option value="">{{ t(required ? 'qqbot.memberPicker.choose' : 'qqbot.memberPicker.optional') }}</option>
      <option v-for="option in options" :key="key(option)" :value="key(option)" :disabled="key(option) !== selectedKey && (loading || !!error || !identifiable(option))">{{ optionLabel(option) }}</option>
    </select>
    <p v-if="loading" role="status" class="text-xs text-gray-500">{{ t('qqbot.memberPicker.loading') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="missingApp" class="text-xs text-amber-700">{{ t('qqbot.groupAdmins.appRequired') }}</p>
    <div v-if="modelValue" class="space-y-1 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800" data-testid="qq-member-selected">
      <p class="break-words font-medium">{{ selected?.nickname || selected?.email || selectedLabel || t('qqbot.memberPicker.saved') }}</p>
      <p v-if="selected?.email" class="break-all">{{ t('qqbot.memberPicker.email') }}: {{ selected.email }}</p>
      <p v-if="selected?.group_label" class="break-words">{{ t('qqbot.memberPicker.group') }}: {{ selected.group_label }}</p>
      <p v-if="selected?.message_preview" class="break-words">{{ selected.message_preview }}</p>
      <p v-if="selected?.last_seen_at" class="text-xs text-gray-500">{{ t('qqbot.groupAdmins.lastSpoke', { time: formatTime(selected.last_seen_at) }) }}</p>
      <p v-if="!selected || !identifiable(selected)" class="text-xs text-amber-700">{{ t('qqbot.memberPicker.unresolved') }}</p>
      <details class="text-xs text-gray-500">
        <summary class="cursor-pointer">{{ t('qqbot.memberPicker.identityDetails') }}</summary>
        <p class="mt-1 break-all">{{ t('qqbot.memberPicker.internalMember') }}: {{ modelValue }}</p>
        <p class="break-all">{{ t('qqbot.groupAdmins.group') }}: {{ selected?.group_id || groupId || '—' }}</p>
      </details>
      <button v-if="!required" type="button" class="text-xs underline" :disabled="disabled" @click="clear">{{ t('qqbot.memberPicker.clear') }}</button>
    </div>
    <p v-if="!loading && !error && !items.length" class="text-xs text-gray-500">{{ t('qqbot.memberPicker.empty') }}</p>
    <div v-if="total > pageSize" class="flex items-center justify-between gap-2 text-xs">
      <button type="button" class="btn btn-secondary" :disabled="disabled || loading || page <= 1" @click="loadPage(page - 1)">{{ t('pagination.previous') }}</button>
      <span>{{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }}</span>
      <button type="button" class="btn btn-secondary" :disabled="disabled || loading || page * pageSize >= total" @click="loadPage(page + 1)">{{ t('pagination.next') }}</button>
    </div>
    <p :id="`${id}-help`" class="text-xs text-gray-500">{{ t('qqbot.memberPicker.help') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQMemberOption, type QQMemberPage } from '@/api/qqbot'

const props = defineProps<{ modelValue: string; groupId?: string; appId?: string; selectedLabel?: string; disabled?: boolean; required?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string]; select: [member: QQMemberOption] }>()
const { t, locale } = useI18n()
const id = useId()
const query = ref(''), error = ref(''), loading = ref(false), discoveredApp = ref('')
const items = ref<QQMemberOption[]>([]), selected = ref<QQMemberOption | null>(null)
const page = ref(1), total = ref(0), pageSize = 20
const missingApp = computed(() => props.appId !== undefined && !props.appId.trim())
let listRequest = 0, selectionRequest = 0
let timer: ReturnType<typeof setTimeout> | undefined
let justSelected: QQMemberOption | null = null

function key(member: Pick<QQMemberOption, 'group_id' | 'member_id'>) { return JSON.stringify([member.group_id, member.member_id]) }
function identifiable(member: QQMemberOption) { return !!(member.nickname || member.email || member.message_preview) }
const selectedKey = computed(() => props.modelValue ? key(selected.value?.member_id === props.modelValue ? selected.value : { group_id: props.groupId || '', member_id: props.modelValue }) : '')
const options = computed(() => {
  const list = [...items.value]
  if (props.modelValue && !list.some(member => key(member) === selectedKey.value)) list.unshift(selected.value ?? {
    group_id: props.groupId || '', member_id: props.modelValue, nickname: props.selectedLabel || '',
    email: '', message_preview: '', last_seen_at: '', group_label: ''
  })
  return list
})
function formatTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}
function readableLabel(member: QQMemberOption) {
  const label = [member.nickname || member.email || t('qqbot.memberPicker.unknown')]
  if (member.email && member.nickname) label.push(member.email)
  if (!props.groupId) label.push(member.group_label || t('qqbot.memberPicker.unnamedGroup'))
  if (member.message_preview) label.push(member.message_preview)
  if (member.last_seen_at) label.push(formatTime(member.last_seen_at))
  if (!identifiable(member)) label.push(t('qqbot.memberPicker.unidentified'))
  return label.join(' · ')
}
function optionLabel(member: QQMemberOption) {
  const label = readableLabel(member)
  if (options.value.filter(option => readableLabel(option) === label).length < 2) return label
  // Names can collide. Keep the genuine identifier as a secondary distinction,
  // never use a display name or a user's search text as an identity.
  const suffix = `${member.group_id.slice(-6)}/${member.member_id.slice(-8)}`
  const suffixClashes = options.value.filter(option => readableLabel(option) === label && `${option.group_id.slice(-6)}/${option.member_id.slice(-8)}` === suffix).length > 1
  return `${label} · ${suffixClashes ? `${member.group_id}/${member.member_id}` : suffix}`
}
function checked(result: QQMemberPage) {
  const expected = props.appId || discoveredApp.value
  if (expected && result.app_id !== expected) throw new Error(t('qqbot.memberPicker.appChanged'))
  discoveredApp.value = result.app_id
  return result.items.filter(member => !!member.member_id && (!props.groupId || member.group_id === props.groupId))
}
async function loadPage(target: number) {
  clearTimeout(timer)
  const seq = ++listRequest
  if (missingApp.value) { loading.value = false; items.value = []; return }
  loading.value = true; error.value = ''
  try {
    const result = await qqbotAPI.members({ app_id: props.appId || discoveredApp.value || undefined, group_id: props.groupId || undefined, search: query.value.trim(), page: target, page_size: pageSize })
    if (seq !== listRequest) return
    items.value = checked(result); total.value = result.total; page.value = result.page
    const match = items.value.find(member => member.member_id === props.modelValue && (!props.groupId || member.group_id === props.groupId))
    if (match && (!selected.value || key(match) === key(selected.value))) selected.value = match
  } catch (e) {
    if (seq === listRequest) { items.value = []; total.value = 0; error.value = (e as Error).message || t('common.error') }
  } finally { if (seq === listRequest) loading.value = false }
}
async function resolveSelection() {
  const seq = ++selectionRequest
  if (!props.modelValue || missingApp.value) { selected.value = null; return }
  if (selected.value?.member_id !== props.modelValue || (props.groupId && selected.value?.group_id !== props.groupId)) selected.value = null
  try {
    const result = await qqbotAPI.members({ app_id: props.appId || discoveredApp.value || undefined, group_id: props.groupId || undefined, member_id: props.modelValue, page: 1, page_size: pageSize })
    if (seq !== selectionRequest) return
    const matches = checked(result).filter(member => member.member_id === props.modelValue)
    const match = matches.find(member => props.groupId && member.group_id === props.groupId) || (matches.length === 1 ? matches[0] : undefined)
    if (match) selected.value = match
  } catch { /* Keep the saved identity intact when its display hints cannot be resolved. */ }
}
function choose(event: Event) {
  if (props.disabled || missingApp.value || loading.value || error.value) return
  const value = (event.target as HTMLSelectElement).value
  if (!value) { clear(); return }
  const member = options.value.find(option => key(option) === value)
  if (!member || !identifiable(member)) return
  selected.value = member; justSelected = member
  emit('update:modelValue', member.member_id)
  emit('select', member)
}
function clear() { selected.value = null; justSelected = null; emit('update:modelValue', '') }
function refresh() { void loadPage(1); void resolveSelection() }
watch([() => props.appId, () => props.groupId], ([app, group], [oldApp, oldGroup]) => {
  ++listRequest; ++selectionRequest; clearTimeout(timer); items.value = []; total.value = 0
  const chosenGroup = app === oldApp && justSelected && justSelected.group_id === group && justSelected.member_id === props.modelValue
  if (app !== oldApp) discoveredApp.value = ''
  if (app !== oldApp || (group !== oldGroup && !chosenGroup)) clear()
  justSelected = null
  void loadPage(1)
})
watch(query, () => {
  ++listRequest; loading.value = true; clearTimeout(timer)
  timer = setTimeout(() => { void loadPage(1) }, 250)
})
watch([() => props.appId, () => props.groupId, () => props.modelValue], resolveSelection, { immediate: true })
void loadPage(1)
onBeforeUnmount(() => { clearTimeout(timer); ++listRequest; ++selectionRequest })
</script>
