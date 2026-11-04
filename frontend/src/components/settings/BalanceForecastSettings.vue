<template>
  <section class="card" data-testid="balance-forecast-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h3 class="flex items-center gap-2 text-base font-medium text-gray-900 dark:text-white">
        {{ t('admin.settings.balanceForecast.title') }}
        <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.balanceForecast" :user-id="userId" />
      </h3>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.balanceForecast.description') }}</p>
    </div>
    <div class="space-y-4 px-6 py-6">
      <label class="flex items-center justify-between gap-4 text-sm font-medium">
        {{ t('admin.settings.balanceForecast.enabled') }}
        <input data-testid="forecast-enabled" type="checkbox" :checked="enabled" @change="emit('update:enabled', ($event.target as HTMLInputElement).checked)" />
      </label>
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="block text-sm">
          {{ t('admin.settings.balanceForecast.threshold') }}
          <input data-testid="forecast-hours" class="input mt-2" type="number" min="0.1" max="168" step="0.1" :value="thresholdHours" @input="emit('update:thresholdHours', Number(($event.target as HTMLInputElement).value))" />
        </label>
        <label class="block text-sm">
          {{ t('admin.settings.balanceForecast.cap') }}
          <input data-testid="forecast-cap" class="input mt-2" type="number" min="1" max="10" step="1" :value="maxNotifications" @input="emit('update:maxNotifications', Number(($event.target as HTMLInputElement).value))" />
        </label>
      </div>
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.balanceForecast.hint') }}</p>
    </div>
  </section>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
defineProps<{ enabled: boolean; thresholdHours: number; maxNotifications: number; userId?: number | null }>()
const emit = defineEmits<{ 'update:enabled': [value: boolean]; 'update:thresholdHours': [value: number]; 'update:maxNotifications': [value: number] }>()
const { t } = useI18n()
</script>
