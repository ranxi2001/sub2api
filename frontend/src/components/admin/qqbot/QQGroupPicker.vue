<template>
  <fieldset class="space-y-2">
    <legend class="text-sm font-medium">{{ label }}</legend>
    <label v-if="allowAll" class="flex items-center gap-2 text-sm"><input :checked="!model.length" type="checkbox" @change="selectAllMode" />{{ t('qqbot.workspace.allGroups') }}</label>
    <input v-model="search" class="input" :placeholder="t('qqbot.workspace.searchGroup')" :aria-label="t('qqbot.workspace.searchGroup')" />
    <div class="max-h-48 space-y-2 overflow-auto rounded-lg border p-3 dark:border-dark-700">
      <label v-for="group in filtered" :key="group" class="flex items-start gap-2 text-sm">
        <input type="checkbox" :checked="model.includes(group)" @change="toggle(group, $event)" />
        <span class="min-w-0 break-all">{{ groupName(group) || group }}<code v-if="groupName(group)" class="block text-xs text-gray-500">{{ group }}</code></span>
      </label>
      <p v-if="!filtered.length" class="text-sm text-gray-500">{{ t('common.noData') }}</p>
    </div>
    <p class="text-xs text-gray-500">{{ allowAll && !model.length ? t('qqbot.workspace.allGroupsHelp') : t('qqbot.workspace.selectedGroups', { count: model.length }) }}</p>
  </fieldset>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QQGroupPolicy } from '@/api/qqbot'
const model = defineModel<string[]>({ required: true })
const props = defineProps<{ groups: string[]; policies?: QQGroupPolicy[]; names?: Record<string, string>; label: string; allowAll?: boolean }>()
const { t } = useI18n()
const search = ref('')
function groupName(group: string) { return props.policies?.find(p => p.group_id === group)?.label || props.names?.[group] }
const filtered = computed(() => [...new Set([...props.groups, ...model.value])].filter(g => `${g} ${groupName(g) || ''} ${props.names?.[g] || ''}`.toLowerCase().includes(search.value.toLowerCase())))
function toggle(group: string, event: Event) { if (props.allowAll && model.value.length === 1 && model.value[0] === group) { (event.target as HTMLInputElement).checked = true; return } model.value = model.value.includes(group) ? model.value.filter(g => g !== group) : [...model.value, group] }
function selectAllMode(event: Event) { model.value = (event.target as HTMLInputElement).checked ? [] : [...props.groups] }
</script>
