<template>
  <div class="space-y-1 whitespace-nowrap text-xs tabular-nums" :title="t('admin.users.liveUsageHint')">
    <div class="flex items-center gap-2">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.users.liveConcurrency') }}</span>
      <UserConcurrencyCell v-if="user.concurrency_available !== false && user.current_concurrency != null" :current="user.current_concurrency" :max="user.concurrency" :show-unlimited="true" />
      <span v-else :title="t('admin.users.liveUsageUnavailable')">—</span>
    </div>
    <div class="flex items-center gap-2">
      <span class="text-gray-500 dark:text-gray-400">RPM</span>
      <span v-if="user.current_rpm == null" :title="t('admin.users.liveUsageUnavailable')">— / {{ user.rpm_limit || '∞' }}</span>
      <span v-else :class="user.rpm_limit && user.current_rpm >= user.rpm_limit ? 'text-red-600 dark:text-red-400' : 'text-gray-700 dark:text-gray-200'">
        {{ user.current_rpm }} / {{ user.rpm_limit || '∞' }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AdminUser } from '@/types'
import UserConcurrencyCell from './UserConcurrencyCell.vue'
defineProps<{ user: AdminUser }>()
const { t } = useI18n()
</script>
