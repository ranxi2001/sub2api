<template>
  <div class="text-xs text-gray-600 dark:text-gray-300">
    {{ label }} <strong>{{ coverage.amount == null ? '—' : formatCurrency(coverage.amount) }}</strong>
    <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.accountProcurement" :user-id="auth.user?.id" />
    <span v-if="coverage.known < coverage.requests" class="block">
      {{ t('profitOps.pricedRequests', { known: coverage.known, total: coverage.requests }) }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProcurementCoverage } from '@/types'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { useAuthStore } from '@/stores/auth'
import { formatCurrency } from '@/utils/format'

defineProps<{ coverage: ProcurementCoverage; label: string }>()
const { t } = useI18n()
const auth = useAuthStore()
</script>
