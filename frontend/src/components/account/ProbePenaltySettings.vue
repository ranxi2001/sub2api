<template>
      <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="probe-penalty-settings">
        <div class="flex items-center gap-2">
          <label class="flex items-center gap-2 font-medium">
            <input v-model="enabled" type="checkbox" data-testid="probe-penalty-enabled" />
            {{ t('admin.accounts.probePenalty.title') }}
          </label>
          <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.accountProbePenalty" :user-id="authStore.user?.id" />
        </div>
        <p class="input-hint">{{ t('admin.accounts.probePenalty.hint') }}</p>
        <label v-if="enabled" class="mt-3 block">
          <span class="input-label">{{ t('admin.accounts.probePenalty.minimum') }}</span>
          <input v-model.number="minimum" type="number" min="0.00000001" max="1000" step="0.00000001" required class="input" data-testid="probe-penalty-minimum" />
        </label>
        <p v-if="enabled" class="input-hint">{{ t('admin.accounts.probePenalty.downstream') }}</p>
      </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
const enabled = defineModel<boolean>('enabled', { required: true })
const minimum = defineModel<number>('minimum', { required: true })
const { t } = useI18n()
const authStore = useAuthStore()
</script>
