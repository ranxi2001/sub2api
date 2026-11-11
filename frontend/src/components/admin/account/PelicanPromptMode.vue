<template>
  <div class="space-y-2" data-testid="pelican-prompt-mode">
    <div class="flex items-center gap-2">
      <label class="text-sm">{{ t('pelicanTests.questionBank.mode') }}
        <select :value="modelValue || 'custom'" :disabled="disabled" class="input mt-1" data-testid="pelican-mode-select" @change="$emit('update:modelValue', ($event.target as HTMLSelectElement).value as 'custom' | 'random')">
          <option value="custom">{{ t('pelicanTests.questionBank.custom') }}</option>
          <option value="random">{{ t('pelicanTests.questionBank.random') }}</option>
        </select>
      </label>
      <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.pelicanRandomQuestionBank" :user-id="auth.user?.id" />
    </div>
    <p v-if="modelValue === 'random'" class="text-xs text-gray-500">{{ t('pelicanTests.questionBank.hint') }}</p>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
defineProps<{ modelValue?: 'custom' | 'random'; disabled?: boolean }>()
defineEmits<{ 'update:modelValue': [value: 'custom' | 'random'] }>()
const { t } = useI18n()
const auth = useAuthStore()
</script>
