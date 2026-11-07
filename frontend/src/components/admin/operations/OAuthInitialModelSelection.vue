<template>
  <fieldset class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700" data-testid="initial-model-selection">
    <legend class="flex items-center gap-2 text-sm font-medium text-gray-600 dark:text-gray-300">{{ t('autoConfig.selection.title') }}<NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthInitialModelSelection" :user-id="userId" /></legend>
    <label class="flex items-center gap-2 text-sm"><input data-testid="model-selection-enabled" type="checkbox" :checked="modelValue != null" @change="toggle" />{{ t('autoConfig.selection.enable') }}</label>
    <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.selection.hint') }}</p>
    <template v-if="modelValue != null">
      <div class="flex flex-wrap gap-3 text-sm">
        <button type="button" data-testid="model-selection-all" class="text-primary-600 underline" @click="emit('update:modelValue', [...options])">{{ t('autoConfig.selection.all') }}</button>
        <button type="button" data-testid="model-selection-none" class="text-primary-600 underline" @click="emit('update:modelValue', [])">{{ t('autoConfig.selection.none') }}</button>
      </div>
      <div class="max-h-60 space-y-2 overflow-y-auto rounded-xl border border-gray-200 p-3 dark:border-dark-700">
        <label v-for="model in options" :key="model" class="flex items-center gap-2 break-all text-sm"><input type="checkbox" :data-model="model" :checked="modelValue.includes(model)" @change="select(model)" /><span>{{ model }}</span></label>
      </div>
      <label class="block text-sm">{{ t('autoConfig.selection.custom') }}<input v-model="custom" data-testid="model-selection-custom" class="input mt-2 w-full" maxlength="256" @keydown.enter.prevent="addCustom" /></label>
      <button type="button" data-testid="model-selection-add" class="btn btn-secondary" :disabled="!validCustom" @click="addCustom">{{ t('autoConfig.selection.add') }}</button>
      <p v-if="!modelValue.length" data-testid="model-selection-empty" class="rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ t('autoConfig.selection.empty') }}</p>
      <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.selection.scope') }}</p>
    </template>
  </fieldset>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getModelsByPlatform } from '@/composables/useModelWhitelist'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
const props = defineProps<{ modelValue?: string[] | null; platform: string; userId?: number; mappings: { from: string; to: string }[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: string[] | null] }>()
const { t } = useI18n(), custom = ref('')
const options = computed(() => [...new Set([...getModelsByPlatform(props.platform), ...(props.modelValue ?? []), ...props.mappings.flatMap(rule => [rule.from.trim(), rule.to.trim()]).filter(name => name && !name.includes('*'))])])
const validCustom = computed(() => Boolean(custom.value.trim()) && !/[\s*]/.test(custom.value.trim()) && [...custom.value.trim()].every(char => char.charCodeAt(0) >= 32 && !(char.charCodeAt(0) >= 127 && char.charCodeAt(0) <= 159)) && new TextEncoder().encode(custom.value.trim()).length <= 256)
function toggle(event: Event) { emit('update:modelValue', (event.target as HTMLInputElement).checked ? [...options.value] : null) }
function select(model: string) { emit('update:modelValue', props.modelValue?.includes(model) ? props.modelValue.filter(value => value !== model) : [...(props.modelValue ?? []), model]) }
function addCustom() { if (validCustom.value) { emit('update:modelValue', [...new Set([...(props.modelValue ?? []), custom.value.trim()])]); custom.value = '' } }
</script>
