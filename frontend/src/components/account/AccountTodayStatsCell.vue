<template>
  <div>
    <!-- Loading state -->
    <div v-if="props.loading && !props.stats" class="space-y-0.5">
      <div class="h-3 w-12 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-16 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-10 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
    </div>

    <!-- Error state -->
    <div v-else-if="props.error && !props.stats" class="text-xs text-red-500">
      {{ props.error }}
    </div>

    <!-- Stats data -->
    <div v-else-if="props.stats" class="grid grid-cols-2 gap-x-3 gap-y-1 text-xs sm:block sm:space-y-0.5">
      <div class="flex min-w-0 items-center gap-1">
        <span class="min-w-0 truncate text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stats.todayTokens') }}:</span>
        <span class="whitespace-nowrap font-medium text-emerald-600 dark:text-emerald-400">{{
          formatTokens(props.stats.tokens)
        }}</span>
      </div>
      <div class="flex min-w-0 items-center gap-1">
        <span class="min-w-0 truncate text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stats.lifetimeTokens') }}:</span>
        <span class="whitespace-nowrap font-medium text-gray-900 dark:text-gray-100">{{
          formatTokens(props.stats.lifetime_tokens ?? 0)
        }}</span>
      </div>
      <div class="flex min-w-0 items-center gap-1">
        <span class="min-w-0 truncate text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stats.todayCost') }}:</span>
        <span class="whitespace-nowrap font-medium text-emerald-600 dark:text-emerald-400">{{
          formatCurrency(props.stats.cost)
        }}</span>
      </div>
      <div class="flex min-w-0 items-center gap-1">
        <span class="min-w-0 truncate text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stats.lifetimeCost') }}:</span>
        <span class="whitespace-nowrap font-medium text-gray-900 dark:text-gray-100">{{
          formatCurrency(props.stats.lifetime_cost ?? 0)
        }}</span>
      </div>
    </div>

    <!-- No data -->
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCurrency } from '@/utils/format'

const props = withDefaults(
  defineProps<{
    stats?: WindowStats | null
    loading?: boolean
    error?: string | null
  }>(),
  {
    stats: null,
    loading: false,
    error: null
  }
)

const { t } = useI18n()

// Format large token numbers (e.g., 1234567 -> 1.23M)
const formatTokens = (tokens: number): string => {
  if (tokens >= 1000000) {
    return `${(tokens / 1000000).toFixed(2)}M`
  } else if (tokens >= 1000) {
    return `${(tokens / 1000).toFixed(1)}K`
  }
  return tokens.toString()
}
</script>
