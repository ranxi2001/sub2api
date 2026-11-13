<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

defineProps<{ userId?: number }>()
const enabled = defineModel<boolean>({ required: true })
const { t } = useI18n()
</script>

<template>
  <div class="flex items-start justify-between gap-4 border-b border-gray-100 pb-5 dark:border-dark-700 md:col-span-2">
    <div>
      <div class="flex flex-wrap items-center gap-2">
        <label for="openai-400-omit-encrypted-history" class="text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.settings.gatewayForwarding.omitEncryptedHistory400') }}
        </label>
        <span class="rounded bg-amber-100 px-2 py-0.5 text-xs text-amber-800 dark:bg-amber-900/30 dark:text-amber-300">Beta</span>
        <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.openai400OmitEncryptedHistory" :user-id="userId" />
      </div>
      <p id="openai-400-omit-encrypted-history-hint" class="mt-1 max-w-2xl text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.gatewayForwarding.omitEncryptedHistory400Hint') }}
      </p>
    </div>
    <Toggle
      id="openai-400-omit-encrypted-history"
      v-model="enabled"
      :aria-label="t('admin.settings.gatewayForwarding.omitEncryptedHistory400')"
      aria-describedby="openai-400-omit-encrypted-history-hint"
      data-testid="openai-400-omit-encrypted-history-toggle"
    />
  </div>
</template>
