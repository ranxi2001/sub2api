<template>
  <nav :aria-label="t('nav.securityAudit')" class="mb-5 flex flex-wrap gap-1 rounded-xl border border-gray-200/70 bg-white/80 p-1 shadow-sm dark:border-dark-700 dark:bg-dark-900">
    <RouterLink
      v-for="item in items"
      :key="item.path"
      :to="item.path"
      class="rounded-lg px-4 py-2 text-sm transition-colors"
      :class="active === item.path ? 'bg-gray-900 font-medium text-white dark:bg-gray-100 dark:text-gray-900' : 'text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-800'"
      :aria-current="active === item.path ? 'page' : undefined"
    >
      {{ t(item.label) }}
    </RouterLink>
  </nav>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'

defineProps<{ active: string }>()
const { t } = useI18n()
// Match the existing sidebar's security group and feature gates.
const items = computed(() => [
  ...(isFeatureFlagEnabled(FeatureFlags.riskControl) ? [
    { path: '/admin/risk-control', label: 'nav.contentModeration' },
    { path: '/admin/prompt-audit', label: 'nav.promptAudit' },
    { path: '/admin/safety-prompt-audit', label: 'admin.safetyPromptAudit.nav' },
  ] : []),
  { path: '/admin/ip-audit', label: 'ipAudit.title' },
])
</script>
