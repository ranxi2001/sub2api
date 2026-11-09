<template>
 <section class="min-w-0 space-y-4 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900" data-testid="degradation-action">
  <header class="flex flex-wrap items-center justify-between gap-3">
   <div class="flex items-center gap-2"><h2 class="text-lg font-semibold">{{ t('autoConfig.degradation.title') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthDegradationActions" :user-id="userId" /></div>
   <label class="flex items-center gap-2 text-sm"><input v-model="config.enabled" data-testid="degradation-enabled" type="checkbox" role="switch" class="h-4 w-4 rounded text-primary-600" />{{ t('autoConfig.enable') }}</label>
  </header>
  <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.degradation.hint') }}</p>
  <div class="grid gap-4 sm:grid-cols-2">
   <label class="text-sm font-medium">{{ t('autoConfig.degradation.threshold') }}<input :value="config.threshold_percent ?? ''" data-testid="degradation-threshold" type="number" min="0" max="100" step="any" :required="config.enabled" class="input mt-2 w-full" @input="setThreshold" /></label>
   <label class="text-sm font-medium">{{ t('autoConfig.degradation.concurrency') }}<input v-model.number="config.target_concurrency" data-testid="degradation-concurrency" type="number" min="1" max="10000" step="1" required class="input mt-2 w-full" /></label>
  </div>
  <p v-if="config.threshold_percent == null" class="text-xs leading-5 text-amber-700 dark:text-amber-300">{{ t('autoConfig.degradation.thresholdRequired') }}</p>
  <p class="rounded-xl bg-amber-50 p-3 text-xs leading-5 text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ t('autoConfig.degradation.defaultsPending') }}</p>
  <fieldset class="space-y-2"><legend class="mb-2 text-sm font-medium">{{ t('autoConfig.degradation.models') }}</legend>
   <div class="grid max-h-48 gap-x-4 gap-y-2 overflow-y-auto rounded-xl border border-gray-200 p-3 sm:grid-cols-2 lg:grid-cols-3 dark:border-dark-700">
    <label v-for="model in options" :key="model" class="flex min-w-0 items-center gap-2 break-all text-sm"><input v-model="config.models" type="checkbox" :value="model" :data-degradation-model="model" class="h-4 w-4 shrink-0 rounded text-primary-600" /><span>{{ model }}</span></label>
   </div>
  </fieldset>
  <div class="flex flex-wrap items-end gap-2"><label class="min-w-0 flex-1 text-sm">{{ t('autoConfig.selection.custom') }}<input v-model="custom" class="input mt-2 w-full" maxlength="256" @keydown.enter.prevent="addModel" /></label><button type="button" class="btn btn-secondary" :disabled="!validCustom" @click="addModel">{{ t('autoConfig.selection.add') }}</button></div>
  <p v-if="!config.models.length" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t('autoConfig.degradation.empty') }}</p>
  <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.degradation.scope') }}</p>
 </section>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { getModelsByPlatform } from '@/composables/useModelWhitelist'
import type { OAuthDegradationConfig } from '@/utils/oauthDegradation'
const props = defineProps<{ modelValue: OAuthDegradationConfig; userId?: number }>()
const { t } = useI18n()
const config = computed(() => props.modelValue)
const custom = ref('')
const options = computed(() => [...new Set([...getModelsByPlatform('openai'), ...getModelsByPlatform('anthropic'), ...getModelsByPlatform('gemini'), ...getModelsByPlatform('antigravity'), ...getModelsByPlatform('grok'), ...config.value.models])])
const validCustom = computed(() => !!custom.value.trim() && !/[\s*\x00-\x1f\x7f-\x9f]/.test(custom.value.trim()) && new TextEncoder().encode(custom.value.trim()).length <= 256)
function addModel() { if (validCustom.value) { config.value.models = [...new Set([...config.value.models, custom.value.trim()])]; custom.value = '' } }
function setThreshold(event: Event) { const text = (event.target as HTMLInputElement).value; config.value.threshold_percent = text === '' ? null : Number(text) }
</script>
