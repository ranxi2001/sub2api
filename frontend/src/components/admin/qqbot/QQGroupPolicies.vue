<template>
  <div class="space-y-4">
    <p class="text-sm text-gray-500">{{ t('qqbot.workspace.groupPolicyHelp') }}</p>
    <article v-for="group in groups" :key="group" class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
      <p v-if="policy(group).label || names?.[group]" class="break-all font-medium">{{ policy(group).label || names?.[group] }}</p>
      <code class="block break-all text-xs">{{ group }}</code>
      <label class="block text-sm">{{ t('qqbot.workspace.groupLabel') }}<input class="input" :value="policy(group).label" :placeholder="names?.[group]" maxlength="60" @input="update(group, { label: ($event.target as HTMLInputElement).value })" /></label>
      <details :open="hasOverrides(group)"><summary class="cursor-pointer text-sm">{{ t(hasOverrides(group) ? 'qqbot.simple.groupCustom' : 'qqbot.simple.groupInherited') }}</summary>
      <button type="button" class="btn btn-secondary my-3" @click="restore(group)">{{ t('qqbot.simple.restoreInheritance') }}</button>
      <div class="flex flex-wrap gap-4">
        <label v-for="key in switches" :key="key" class="flex items-center gap-2 text-sm"><input type="checkbox" :checked="!policy(group)[key]" @change="update(group, { [key]: !($event.target as HTMLInputElement).checked })" />{{ t(key === 'disable_autonomous' || key === 'disable_moderation' ? `qqbot.simple.${key}` : `qqbot.workspace.${key}`) }}</label>
      </div>
      <div class="grid gap-3 sm:grid-cols-2">
        <label v-for="key in quotas" :key="key" class="text-sm">{{ t(`qqbot.workspace.${key}`) }}<input class="input" type="number" min="0" max="100000" :value="policy(group)[key]" :placeholder="t('qqbot.workspace.inherit')" @input="update(group, { [key]: ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value) })" /></label>
      </div>
      </details>
    </article>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { QQGroupPolicy } from '@/api/qqbot'
const model = defineModel<QQGroupPolicy[]>({ required: true })
defineProps<{ groups: string[]; names?: Record<string, string> }>()
const { t } = useI18n()
const switches = ['disable_chat', 'disable_images', 'disable_activities', 'disable_notifications', 'disable_autonomous', 'disable_moderation'] as const
const quotas = ['chat_daily_limit', 'image_daily_limit'] as const
function policy(group: string): QQGroupPolicy { return model.value.find(p => p.group_id === group) || { group_id: group, label: '', disable_chat: false, disable_images: false, disable_activities: false, disable_notifications: false, chat_daily_limit: null, image_daily_limit: null } }
function update(group: string, patch: Partial<QQGroupPolicy>) { const next = { ...policy(group), ...patch }; model.value = [...model.value.filter(p => p.group_id !== group), next] }
function hasOverrides(group: string) { const p = policy(group); return switches.some(key => p[key]) || quotas.some(key => p[key] != null) }
function restore(group: string) { update(group, { disable_chat: false, disable_images: false, disable_activities: false, disable_notifications: false, disable_autonomous: false, disable_moderation: false, chat_daily_limit: null, image_daily_limit: null }) }
</script>
