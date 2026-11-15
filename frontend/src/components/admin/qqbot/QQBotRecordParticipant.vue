<template>
  <div class="min-w-0 space-y-1 text-sm">
    <p v-if="person.nickname" class="break-all font-medium">{{ person.nickname }}</p>
    <code class="block break-all text-xs text-gray-500">{{ person.member_id }}</code>
    <span class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
      :class="person.bound ? 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'">
      {{ person.bound ? t('qqbot.recordBound') : t('qqbot.recordUnbound') }}
    </span>
    <p v-if="person.user_id" class="break-all text-xs">{{ person.email || t('qqbot.websiteAccount') }} · ID: {{ person.user_id }}</p>
    <p v-if="person.user_deleted || (person.user_id && person.user_status !== 'active')" class="text-xs text-red-600 dark:text-red-400">
      {{ person.user_deleted ? t('qqbot.accountDeleted') : t('qqbot.recordAccountUnavailable') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { QQActivityParticipant } from '@/api/qqbot'
defineProps<{ person: QQActivityParticipant }>()
const { t } = useI18n()
</script>
