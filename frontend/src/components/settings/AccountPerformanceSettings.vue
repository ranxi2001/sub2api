<template>
  <div class="space-y-4 p-6">
    <p v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-green-600">{{ notice }}</p>
    <template v-if="loaded">
      <div>
        <label for="account-performance-window" class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('accountPerformance.settings.windowMinutes') }}
        </label>
        <input id="account-performance-window" v-model.number="windowMinutes" type="number" min="1" max="1440" step="1" class="input w-32" :disabled="saving || loading" />
        <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">{{ t('accountPerformance.settings.windowHint') }}</p>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('accountPerformance.settings.effectHint') }}</p>
      <div class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700">
        <button type="button" class="btn btn-primary btn-sm" data-testid="performance-window-save" :disabled="saving || loading" @click="save">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
    <button v-else-if="!loading" type="button" class="btn btn-secondary btn-sm" @click="load">{{ t('common.refresh') }}</button>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { DEFAULT_PERFORMANCE_WINDOW_MINUTES } from '@/types/accountPerformance'

const { t } = useI18n()
const windowMinutes = ref<number | string>(DEFAULT_PERFORMANCE_WINDOW_MINUTES)
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const settings = await adminAPI.settings.getAccountPerformanceSettings()
    windowMinutes.value = settings.window_minutes
    loaded.value = true
  } catch {
    error.value = t('accountPerformance.settings.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!loaded.value || saving.value || loading.value) return
  const minutes = windowMinutes.value
  notice.value = ''
  if (typeof minutes !== 'number' || !Number.isInteger(minutes) || minutes < 1 || minutes > 1440) {
    error.value = t('accountPerformance.settings.invalid')
    return
  }
  saving.value = true
  error.value = ''
  try {
    const settings = await adminAPI.settings.updateAccountPerformanceSettings({ window_minutes: minutes })
    windowMinutes.value = settings.window_minutes
    notice.value = t('accountPerformance.settings.saved')
  } catch {
    error.value = t('accountPerformance.settings.saveFailed')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
