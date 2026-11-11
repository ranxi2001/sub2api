<template>
 <section class="min-w-0 space-y-4 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900" data-testid="degradation-action">
  <header class="flex flex-wrap items-center justify-between gap-3">
   <div class="flex items-center gap-2"><h2 class="text-lg font-semibold">{{ t('autoConfig.degradation.title') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthDegradationActions" :user-id="userId" class="!border-gray-300 !text-gray-600 hover:!bg-gray-50 focus-visible:!ring-gray-400 dark:!border-dark-600 dark:!text-gray-300 dark:hover:!bg-dark-800" /></div>
   <label class="flex items-center gap-2 text-sm"><input v-model="config.enabled" data-testid="degradation-enabled" type="checkbox" role="switch" class="h-4 w-4 rounded text-primary-600" />{{ t('autoConfig.enable') }}</label>
  </header>
  <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.degradation.hint') }}</p>
  <fieldset class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
   <legend class="px-1 text-sm font-medium">{{ t('autoConfig.degradation.conditions') }}</legend>
   <label class="flex items-center gap-2 text-sm"><input :checked="config.probe_enabled !== false" data-testid="degradation-probe-enabled" type="checkbox" class="h-4 w-4 rounded text-primary-600" @change="config.probe_enabled = ($event.target as HTMLInputElement).checked" />{{ t('autoConfig.degradation.threshold') }}</label>
   <label v-if="config.probe_enabled !== false" class="block text-sm font-medium sm:max-w-xs"><span class="sr-only">{{ t('autoConfig.degradation.threshold') }}</span><input :value="config.threshold_percent ?? ''" data-testid="degradation-threshold" type="number" min="0" max="100" step="any" :required="config.enabled" class="input w-full" @input="setThreshold" /></label>
   <p v-if="config.probe_enabled !== false && config.threshold_percent == null" class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('autoConfig.degradation.thresholdRequired') }}</p>
   <div class="flex flex-wrap items-center gap-2"><label class="flex items-center gap-2 text-sm"><input :checked="config.upstream_429_enabled !== false" data-testid="degradation-429-enabled" type="checkbox" class="h-4 w-4 rounded text-primary-600" @change="config.upstream_429_enabled = ($event.target as HTMLInputElement).checked" />{{ t('autoConfig.degradation.upstream429') }}</label><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthDegradation429" :user-id="userId" /></div>
   <div v-if="config.upstream_429_enabled !== false" class="grid gap-3 sm:grid-cols-2">
    <label class="block text-sm">{{ t('autoConfig.degradation.upstreamErrorThreshold') }}<input v-model.number="config.upstream_error_threshold_percent" data-testid="degradation-error-threshold" type="number" min="0.01" max="100" step="any" required class="input mt-2 w-full" /></label>
    <label class="block text-sm">{{ t('autoConfig.degradation.upstream429ShareThreshold') }}<input v-model.number="config.upstream_429_share_threshold_percent" data-testid="degradation-429-threshold" type="number" min="0.01" max="100" step="any" required class="input mt-2 w-full" /></label>
   </div>
   <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('autoConfig.degradation.upstream429Hint') }}</p>
   <p v-if="config.probe_enabled === false && config.upstream_429_enabled === false" role="status" class="text-xs text-gray-500 dark:text-gray-400">{{ t('autoConfig.degradation.conditionsRequired') }}</p>
  </fieldset>
  <fieldset class="space-y-2">
   <legend class="mb-2 text-sm font-medium">{{ t('autoConfig.degradation.actions') }}</legend>
   <div class="flex flex-wrap gap-x-6 gap-y-3">
    <label class="flex items-center gap-2 text-sm"><input :checked="config.limit_concurrency !== false" data-testid="degradation-limit-concurrency" type="checkbox" class="h-4 w-4 rounded text-primary-600" @change="config.limit_concurrency = ($event.target as HTMLInputElement).checked" />{{ t('autoConfig.degradation.limitConcurrency') }}</label>
    <label class="flex items-center gap-2 text-sm"><input :checked="config.limit_models !== false" data-testid="degradation-limit-models" type="checkbox" class="h-4 w-4 rounded text-primary-600" @change="config.limit_models = ($event.target as HTMLInputElement).checked" />{{ t('autoConfig.degradation.limitModels') }}</label>
    <label class="flex items-center gap-2 text-sm"><input v-model="config.limit_groups" data-testid="degradation-limit-groups" type="checkbox" class="h-4 w-4 rounded text-primary-600" />{{ t('autoConfig.degradation.limitGroups') }}</label>
   </div>
   <p v-if="config.limit_concurrency === false && config.limit_models === false && !config.limit_groups" role="status" class="text-xs text-gray-500 dark:text-gray-400">{{ t('autoConfig.degradation.actionsRequired') }}</p>
  </fieldset>
  <label v-if="config.limit_concurrency !== false" class="block text-sm font-medium sm:max-w-xs">{{ t('autoConfig.degradation.concurrency') }}<input v-model.number="config.target_concurrency" data-testid="degradation-concurrency" type="number" min="1" max="10000" step="1" required class="input mt-2 w-full" /></label>
  <fieldset v-if="config.limit_models !== false" class="space-y-2"><legend class="mb-2 text-sm font-medium">{{ t('autoConfig.degradation.models') }}</legend>
   <div data-testid="degradation-models"><ModelWhitelistSelector v-model="config.models" :platforms="['openai', 'anthropic', 'gemini', 'antigravity', 'grok']" :show-sync-button="false" /></div>
   <p v-if="!config.models.length" role="status" class="text-sm text-gray-500 dark:text-gray-400">{{ t('autoConfig.degradation.empty') }}</p>
  </fieldset>
  <fieldset v-if="config.limit_groups" class="space-y-2" data-testid="degradation-groups"><legend class="mb-2 text-sm font-medium">{{ t('autoConfig.degradation.groups') }}</legend>
   <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.degradation.groupsHint') }}</p>
   <div class="grid max-h-48 gap-2 overflow-y-auto rounded-xl border border-gray-200 p-3 sm:grid-cols-2 dark:border-dark-700">
    <label v-for="group in groups" :key="group.id" class="flex min-w-0 items-center gap-2 text-sm"><input v-model="config.group_ids" :value="group.id" :data-testid="'degradation-group-' + group.id" type="checkbox" class="h-4 w-4 shrink-0 rounded text-primary-600" /><span class="break-words">{{ group.name }} <small class="text-gray-400">{{ group.platform }} · #{{ group.id }}</small></span></label>
    <label v-for="id in unavailableGroups" :key="id" class="flex items-center gap-2 text-sm text-gray-500 dark:text-gray-400"><input v-model="config.group_ids" :value="id" :data-testid="'degradation-group-' + id" type="checkbox" class="h-4 w-4 shrink-0 rounded text-primary-600" /><span>#{{ id }} · {{ t('autoConfig.degradation.unavailableGroup') }}</span></label>
    <p v-if="!groups.length && !unavailableGroups.length" class="text-sm text-gray-500 sm:col-span-2">{{ t('autoConfig.noGroups') }}</p>
   </div>
   <p v-if="!config.group_ids?.length" role="status" class="text-xs text-gray-500 dark:text-gray-400">{{ t('autoConfig.degradation.groupsRequired') }}</p>
  </fieldset>
  <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.degradation.scope') }}</p>
 </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import type { Group } from '@/types'
import type { OAuthDegradationConfig } from '@/utils/oauthDegradation'
const props = withDefaults(defineProps<{ modelValue: OAuthDegradationConfig; groups?: Group[]; userId?: number }>(), { groups: () => [] })
const { t } = useI18n()
const config = computed(() => props.modelValue)
const unavailableGroups = computed(() => (config.value.group_ids ?? []).filter(id => !props.groups.some(group => group.id === id)))
function setThreshold(event: Event) { const text = (event.target as HTMLInputElement).value; config.value.threshold_percent = text === '' ? null : Number(text) }
</script>
