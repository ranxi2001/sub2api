<template>
  <section class="mb-4 space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="btn btn-secondary" data-testid="batch-upstream-start"
        :disabled="disabled || busy || !valid || rows.length > 0" @click="emit('run', false)">
        {{ t('admin.accounts.batchUpstream.start') }}
      </button>
      <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.batchUpstreamWhitelist" :user-id="userId" />
      <button v-if="busy && rows.some(row => row.state === 'pending')" type="button" class="btn btn-secondary"
        data-testid="batch-upstream-stop" @click="emit('stop')">{{ t('admin.accounts.batchUpstream.stop') }}</button>
      <button v-if="!busy && rows.some(row => row.state === 'failed')" type="button" class="btn btn-secondary"
        :disabled="disabled" data-testid="batch-upstream-retry" @click="emit('run', true)">{{ t('admin.accounts.batchUpstream.retry') }}</button>
    </div>
    <p class="text-xs text-gray-600 dark:text-gray-400">{{ t('admin.accounts.batchUpstream.hint') }}</p>
    <p v-if="busy && !rows.length" class="text-sm" role="status">{{ t('admin.accounts.batchUpstream.draining') }}</p>
    <ul v-if="rows.length" class="max-h-40 overflow-auto text-sm" aria-live="polite">
      <li v-for="row in rows" :key="row.id" :data-sync-account="row.id">
        #{{ row.id }} · {{ t('admin.accounts.batchUpstream.' + row.state) }}
        <span v-if="row.detail"> · {{ t('admin.accounts.batchUpstream.' + row.detail) }}</span>
      </li>
    </ul>
  </section>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import type { WhitelistRow } from '@/composables/useBatchUpstreamWhitelist'

defineProps<{ rows: WhitelistRow[]; valid: boolean; busy: boolean; disabled: boolean; userId: number }>()
const emit = defineEmits<{ run: [retry: boolean]; stop: [] }>()
const { t } = useI18n()
</script>
