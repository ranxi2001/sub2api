<template>
  <nav :aria-label="t('accountOps.smartTitle')" class="mb-5 flex w-fit flex-wrap gap-1 rounded-xl border border-gray-200/70 bg-white/80 p-1 shadow-sm dark:border-dark-700 dark:bg-dark-900">
    <RouterLink v-for="item in items" :key="item.path" :to="item.path" v-slot="{ isActive }" class="rounded-lg px-4 py-2 text-sm transition-colors" :class="route.path === item.path ? 'bg-gray-900 font-medium text-white dark:bg-gray-100 dark:text-gray-900' : 'text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-800'"><span :aria-current="isActive ? 'page' : undefined">{{ t(item.label) }}</span></RouterLink>
  </nav>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAdminSettingsStore } from '@/stores/adminSettings'
const { t } = useI18n(), route = useRoute(), adminSettingsStore = useAdminSettingsStore()
// Keep the order in sync with the 智能运维 group in AppSidebar.vue.
const items = computed(() => [{ path: '/admin/auto-config', label: 'autoConfig.title' }, { path: '/admin/priority-scheduling', label: 'priorityScheduling.title' }, { path: '/admin/channel-operations', label: 'channelOps.title' }, { path: '/admin/profit-operations', label: 'profitOps.title' }, { path: '/admin/account-quality', label: 'qualityOps.title' }, { path: '/admin/controlled-experiments', label: 'controlledExperiments.title' }, { path: '/admin/account-ops', label: 'accountOps.title' }, { path: '/admin/token-guard', label: 'tokenGuard.title' }, { path: '/admin/token-guard-v2', label: 'tokenGuardV2.title' }, { path: '/admin/pelican-tests', label: 'pelicanTests.title' }, { path: '/admin/group-cache-tests', label: 'admin.groupCacheTest.title' }, ...(adminSettingsStore.requestCaptureEnabled ? [{ path: '/admin/request-captures', label: 'admin.requestCapture.title' }] : []), { path: '/admin/harvest-flow', label: 'nav.harvestFlow' }])
</script>
