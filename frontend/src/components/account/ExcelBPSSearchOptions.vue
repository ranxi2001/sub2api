<template>
  <section class="space-y-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700" data-testid="bps-search-options">
    <div class="flex items-center gap-2">
      <label class="flex items-center gap-2 text-sm font-medium">
        <input v-model="enabled" type="checkbox" data-testid="excel-bps-web-search" class="h-4 w-4 rounded text-primary-600" />
        {{ t('admin.accounts.openai.excelBPSWebSearch') }}
      </label>
      <NewFeatureBadge v-if="auth.user?.id != null" :notice-id="NEW_FEATURE_NOTICES.bpsAccountSearch" :user-id="auth.user?.id" />
    </div>
    <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.excelBPSWebSearchDesc') }}</p>
    <label class="flex items-center gap-2 text-sm">
      <input v-model="automatic" :disabled="!enabled" type="checkbox" data-testid="excel-bps-web-search-auto" class="h-4 w-4 rounded text-primary-600 disabled:opacity-50" />
      {{ t('admin.accounts.openai.excelBPSWebSearchAuto') }}
    </label>
    <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.excelBPSWebSearchAutoDesc') }}</p>
  </section>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
const enabled = defineModel<boolean>('enabled', { default: false })
const automatic = defineModel<boolean>('automatic', { default: false })
const { t } = useI18n()
const auth = useAuthStore()
watch(enabled, value => { if (!value) automatic.value = false })
</script>
