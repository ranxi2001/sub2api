<template>
  <p class="mt-2 flex items-start gap-2 text-sm text-gray-500" data-testid="qwen-platform-notice">
    <span>{{ t('common.qwenPlatformHint') }}</span>
    <NewFeatureBadge v-if="userId != null" :notice-id="NEW_FEATURE_NOTICES.qwenPlatform" :user-id="userId" />
  </p>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from './NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
defineProps<{ userId: number | null | undefined }>()
const { t } = useI18n()
</script>
