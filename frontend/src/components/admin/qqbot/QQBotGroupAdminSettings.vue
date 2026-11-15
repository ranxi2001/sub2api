<template>
  <section class="card space-y-4 p-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="text-lg font-semibold">{{ t('qqbot.groupAdmins.title') }}</h2>
      <button type="button" class="btn btn-secondary" data-testid="add-group-admin" :disabled="disabled || !appId || modelValue.length >= 100" @click="add">{{ t('qqbot.groupAdmins.add') }}</button>
    </div>
    <p class="text-sm text-gray-500">{{ t('qqbot.groupAdmins.help') }}</p>
    <p class="text-sm text-gray-500">{{ t('qqbot.groupAdmins.recentHelp') }}</p>
    <p v-if="!appId" class="text-sm text-amber-700">{{ t('qqbot.groupAdmins.appRequired') }}</p>
    <p v-if="!modelValue.length" class="text-sm text-gray-500">{{ t('qqbot.groupAdmins.empty') }}</p>
    <fieldset v-for="(admin, index) in modelValue" :key="index" :disabled="disabled" class="space-y-3 rounded-lg border p-4 dark:border-dark-700" data-testid="group-admin-entry">
      <legend class="px-1 text-sm font-medium">{{ admin.nickname || t('qqbot.groupAdmins.entry', { index: index + 1 }) }}</legend>
      <div class="flex flex-wrap items-center justify-between gap-2">
        <span class="break-all text-xs text-gray-500">AppID: {{ admin.app_id }}</span>
        <button type="button" class="btn btn-secondary text-sm" :aria-label="t('qqbot.groupAdmins.remove', { index: index + 1 })" @click="remove(index)">{{ t('common.delete') }}</button>
      </div>
      <p v-if="admin.app_id !== appId" role="alert" class="text-sm text-amber-700">{{ t('qqbot.groupAdmins.appChanged') }}</p>
      <fieldset :disabled="admin.app_id !== appId" class="space-y-3">
        <div class="grid gap-3 md:grid-cols-2">
          <label class="space-y-1">{{ t('qqbot.groupAdmins.group') }}<select :value="admin.group_id" data-testid="group-admin-group" class="input" required @change="changeGroup(index, $event)"><option value="">{{ t('qqbot.workspace.chooseGroup') }}</option><option v-for="group in [...new Set([...groups, admin.group_id].filter(Boolean))]" :key="group" :value="group">{{ groupLabels?.[group] || group }}</option></select></label>
          <div class="min-w-0" data-testid="group-admin-member"><QQMemberPicker :key="JSON.stringify([admin.app_id, admin.group_id])" :model-value="admin.member_id" :group-id="admin.group_id" :app-id="admin.app_id" :selected-label="admin.nickname" :disabled="disabled || admin.app_id !== appId || !admin.group_id" required @update:model-value="updateMember(index, $event)" @select="selectMember(index, $event)" /></div>
        </div>
        <label class="block space-y-1">{{ t('qqbot.groupAdmins.nickname') }}<input :value="admin.nickname" data-testid="group-admin-nickname" class="input" maxlength="100" @input="updateNickname(index, $event)" /></label>
      </fieldset>
    </fieldset>
    <p class="text-xs text-gray-500">{{ t('qqbot.groupAdmins.saveHelp') }}</p>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { QQBotRuntime, QQGroupAdmin, QQMemberOption } from '@/api/qqbot'
import QQMemberPicker from './QQMemberPicker.vue'

const props = defineProps<{ modelValue: QQGroupAdmin[]; appId: string; groups: string[]; groupLabels?: Record<string, string>; runtime: QQBotRuntime | null; disabled: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: QQGroupAdmin[]] }>()
const { t } = useI18n()
function add() {
  if (props.disabled || !props.appId || props.modelValue.length >= 100) return
  emit('update:modelValue', [...props.modelValue, { app_id: props.appId, group_id: props.groups[0] ?? '', member_id: '', nickname: '' }])
}
function remove(index: number) { emit('update:modelValue', props.modelValue.filter((_, i) => i !== index)) }
function patch(index: number, update: Partial<QQGroupAdmin>) {
  const current = props.modelValue[index]
  if (props.disabled || !current || current.app_id !== props.appId) return
  emit('update:modelValue', props.modelValue.map((admin, i) => i === index ? { ...admin, ...update } : admin))
}
function changeGroup(index: number, event: Event) {
  patch(index, { group_id: (event.target as HTMLSelectElement).value, member_id: '', nickname: '' })
}
function updateMember(index: number, member: string) {
  patch(index, { member_id: member, ...(member ? {} : { nickname: '' }) })
}
function updateNickname(index: number, event: Event) { patch(index, { nickname: (event.target as HTMLInputElement).value }) }
function selectMember(index: number, member: QQMemberOption) {
  const current = props.modelValue[index]
  if (!current || current.group_id !== member.group_id) return
  patch(index, { member_id: member.member_id, nickname: Array.from(member.nickname || member.email).slice(0, 100).join('') })
}
</script>
