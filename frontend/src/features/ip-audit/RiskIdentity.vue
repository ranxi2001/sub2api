<template>
  <span class="inline-flex max-w-full items-baseline gap-1.5">
    <span class="min-w-0 truncate text-primary-600 dark:text-primary-400" :title="name?.trim() || t('ipAudit.keyRisk.unnamed')">{{ name?.trim() || t('ipAudit.keyRisk.unnamed') }}</span>
    <span class="shrink-0 text-gray-500 dark:text-gray-400">#{{ id }}</span>
  </span>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ id: number; name?: string }>()
const { t } = useI18n()
</script>
