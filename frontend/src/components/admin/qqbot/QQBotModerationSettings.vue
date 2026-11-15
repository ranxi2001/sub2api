<template>
  <section class="card space-y-4 p-6">
    <h2 class="text-lg font-semibold">{{ t('qqbot.moderation.title') }}</h2>
    <p class="text-sm text-gray-500">{{ t('qqbot.moderation.help') }}</p>
    <p class="text-sm text-gray-500">{{ t('qqbot.moderation.permission') }}</p>
    <fieldset :disabled="disabled" class="space-y-4">
      <div class="space-y-3 rounded-lg border p-3 dark:border-dark-700">
        <label class="flex items-center gap-2 font-medium"><input v-model="value.enabled" data-testid="moderation-enabled" type="checkbox" />{{ t('qqbot.moderation.enabled') }}</label>
        <p class="text-sm text-gray-500">{{ t('qqbot.moderation.floodHelp') }}</p>
        <fieldset :disabled="!value.enabled" class="grid gap-4 md:grid-cols-2" data-testid="moderation-flood-fields">
          <label v-for="field in fields" :key="field.key" class="space-y-1">
            {{ t('qqbot.moderation.' + field.key) }}
            <input v-model.number="value[field.key]" :data-testid="'moderation-' + field.key" type="number" :min="field.key === 'escalated_mute_minutes' ? value.mute_minutes : field.min" :max="field.max" step="1" required class="input" />
          </label>
        </fieldset>
        <p class="text-sm text-gray-500">{{ t('qqbot.moderation.ruleHelp') }}</p>
      </div>
      <fieldset :disabled="!moderationEnabled" class="space-y-2 rounded-lg border p-3 dark:border-dark-700" data-testid="moderation-shared-groups">
        <legend class="px-1 text-sm font-medium">{{ t('qqbot.moderation.groups') }}</legend>
        <p class="text-xs text-gray-500">{{ t('qqbot.moderation.sharedHelp') }}</p>
        <p v-if="!groups.length" class="text-sm text-gray-500">{{ t('qqbot.moderation.noGroups') }}</p>
        <label class="flex items-center gap-2 text-sm"><input v-model="value.inherit_groups" type="checkbox" />{{ t('qqbot.simple.inheritGroups') }}</label>
        <label v-for="group in value.inherit_groups ? [] : allGroups" :key="group" class="flex items-start gap-2 break-all text-sm">
          <input v-model="value.groups" :value="group" type="checkbox" data-testid="moderation-group" class="mt-1" />
          <span>{{ groupLabels?.[group] || group }}<span v-if="!groups.includes(group)" class="ml-2 text-red-600">{{ t('qqbot.moderation.removedGroup') }}</span></span>
        </label>
      </fieldset>
      <fieldset :disabled="!moderationEnabled" class="space-y-3" data-testid="moderation-shared-exemptions">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 class="font-medium">{{ t('qqbot.moderation.exemptions') }}</h3>
          <button type="button" class="btn btn-secondary" data-testid="moderation-add-exemption" :disabled="!effectiveGroups.length || value.exemptions.length >= 100" @click="addExemption">{{ t('common.add') }}</button>
        </div>
        <p class="text-xs text-gray-500">{{ t('qqbot.moderation.exemptionsHelp') }}</p>
        <div v-for="(exemption, index) in value.exemptions" :key="index" class="grid items-end gap-3 rounded-lg border p-3 md:grid-cols-[1fr_1fr_auto] dark:border-dark-700" data-testid="moderation-exemption-entry">
          <label class="min-w-0 space-y-1 text-sm">{{ t('qqbot.groupAdmins.group') }}<select :value="exemption.group_id" required class="input" data-testid="moderation-exempt-group" @change="changeExemptionGroup(index, $event)"><option v-for="group in [...new Set([...effectiveGroups, exemption.group_id])]" :key="group" :value="group">{{ groupLabels?.[group] || group }}</option></select></label>
          <div class="min-w-0 space-y-3" data-testid="moderation-exempt-member">
            <QQMemberPicker :key="JSON.stringify([appId, exemption.group_id])" :model-value="exemption.member_id" :group-id="exemption.group_id" :app-id="appId" :selected-label="exemption.nickname" :disabled="disabled || !moderationEnabled" required @update:model-value="updateExemptionMember(index, $event)" @select="selectExemption(index, $event)" />
            <label class="block space-y-1 text-sm">{{ t('qqbot.groupAdmins.nickname') }}<input v-model.trim="exemption.nickname" class="input" maxlength="100" data-testid="moderation-exempt-nickname" /></label>
          </div>
          <button type="button" class="btn btn-secondary" :aria-label="t('qqbot.moderation.removeExemption', { index: index + 1 })" @click="value.exemptions.splice(index, 1)">{{ t('common.delete') }}</button>
        </div>
      </fieldset>
    </fieldset>
    <div class="space-y-3 border-t pt-4 dark:border-dark-700">
      <h3 class="font-medium">{{ t('qqbot.moderation.records') }}</h3>
      <p class="text-xs text-gray-500">{{ t('qqbot.moderation.recordsHelp') }}</p>
      <p v-if="!runtime?.moderation_records?.length" class="text-sm text-gray-500">{{ t('qqbot.moderation.empty') }}</p>
      <article v-for="record in runtime?.moderation_records ?? []" :key="record.id" class="space-y-1 rounded-lg border p-3 text-sm dark:border-dark-700" data-testid="moderation-record">
        <p class="break-all">{{ t('qqbot.groupAdmins.group') }}: {{ record.group_id }}</p>
        <p class="break-all">{{ t('qqbot.groupAdmins.member') }}: {{ record.member_id }}</p>
        <p>{{ new Date(record.created_at).toLocaleString() }} · {{ label('reasons', record.reason) }}</p>
        <p>{{ t('qqbot.moderation.muteResult', { minutes: record.mute_seconds / 60, status: label('statuses', record.mute_status) }) }}</p>
        <p>{{ t('qqbot.moderation.recallResult', { succeeded: record.recall_succeeded, total: record.recall_total, pending: record.pending }) }}</p>
        <p v-if="record.error" class="break-words text-red-600 dark:text-red-400">{{ record.error }}</p>
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QQModerationConfig, QQBotRuntime, QQMemberOption } from '@/api/qqbot'
import QQMemberPicker from './QQMemberPicker.vue'
const value = defineModel<QQModerationConfig>({ required: true })
const props = defineProps<{ groups: string[]; groupLabels?: Record<string, string>; appId?: string; runtime: QQBotRuntime | null; disabled: boolean }>()
const { t, te } = useI18n()
const allGroups = computed(() => [...new Set([...props.groups, ...value.value.groups])])
const effectiveGroups = computed(() => value.value.inherit_groups ? props.groups : value.value.groups)
const moderationEnabled = computed(() => value.value.enabled)
function addExemption() {
  const group = effectiveGroups.value[0]
  if (group && value.value.exemptions.length < 100) value.value.exemptions.push({ group_id: group, member_id: '' })
}
function changeExemptionGroup(index: number, event: Event) {
  const exemption = value.value.exemptions[index]
  if (!exemption || props.disabled) return
  exemption.group_id = (event.target as HTMLSelectElement).value
  exemption.member_id = ''; exemption.nickname = ''
}
function selectExemption(index: number, member: QQMemberOption) {
  const exemption = value.value.exemptions[index]
  if (!exemption || props.disabled || exemption.group_id !== member.group_id) return
  exemption.member_id = member.member_id
  exemption.nickname = Array.from(member.nickname || member.email).slice(0, 100).join('')
}
function updateExemptionMember(index: number, member: string) {
  const exemption = value.value.exemptions[index]
  if (!exemption || props.disabled) return
  exemption.member_id = member
  if (!member) exemption.nickname = ''
}
watch(() => props.appId, (current, previous) => {
  if (current === previous) return
  for (const exemption of value.value.exemptions) { exemption.member_id = ''; exemption.nickname = '' }
})
function label(kind: string, code: string) { const key = `qqbot.moderation.${kind}.${code}`; return te(key) ? t(key) : code }
const fields = [
  { key: 'repeat_window_seconds', min: 1, max: 60 }, { key: 'repeat_count', min: 2, max: 100 },
  { key: 'burst_window_seconds', min: 1, max: 60 }, { key: 'burst_count', min: 2, max: 100 },
  { key: 'mute_minutes', min: 1, max: 43200 }, { key: 'repeat_offense_minutes', min: 1, max: 1440 },
  { key: 'escalated_mute_minutes', min: 1, max: 43200 }
] as const
</script>
