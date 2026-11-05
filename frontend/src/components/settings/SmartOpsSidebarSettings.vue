<template>
  <section class="card" data-testid="smart-ops-sidebar-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 id="settings-section-smart-ops-sidebar" tabindex="-1" class="flex items-center gap-2 text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settings.features.smartOpsSidebar.title') }}
        <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.smartOpsSidebar" :user-id="authStore.user?.id" />
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.features.smartOpsSidebar.description') }}</p>
    </div>
    <div class="space-y-5 p-6">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <p class="text-sm text-gray-500 dark:text-gray-400" aria-live="polite">
          {{ t('admin.settings.features.smartOpsSidebar.count', { visible: visibleCount, total: SMART_OPS_MENU_ITEMS.length }) }}
        </p>
        <div class="flex gap-2">
          <button type="button" class="btn btn-secondary" @click="showAll">{{ t('admin.settings.features.smartOpsSidebar.showAll') }}</button>
          <button type="button" class="btn btn-secondary" @click="hideAll">{{ t('admin.settings.features.smartOpsSidebar.hideAll') }}</button>
        </div>
      </div>
      <div class="grid gap-x-8 gap-y-5 md:grid-cols-2">
        <div v-for="item in SMART_OPS_MENU_ITEMS" :key="item.path" class="flex items-center justify-between gap-4">
          <label :for="toggleId(item.path)" class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t(item.label) }}</label>
          <Toggle :id="toggleId(item.path)" :aria-label="t(item.label)" :model-value="!modelValue.includes(item.path)" @update:model-value="setVisible(item.path, $event)" />
        </div>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.features.smartOpsSidebar.hint') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { SMART_OPS_MENU_ITEMS } from '@/constants/smartOpsMenu'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ modelValue: string[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>()
const { t } = useI18n()
const authStore = useAuthStore()
const visibleCount = computed(() => SMART_OPS_MENU_ITEMS.filter(item => !props.modelValue.includes(item.path)).length)
const toggleId = (path: string) => `smart-ops-sidebar-${path.split('/').pop()}`
function setVisible(path: string, visible: boolean) {
  emit('update:modelValue', visible ? props.modelValue.filter(item => item !== path) : [...new Set([...props.modelValue, path])])
}
function showAll() {
  emit('update:modelValue', props.modelValue.filter(path => !SMART_OPS_MENU_ITEMS.some(item => item.path === path)))
}
function hideAll() {
  emit('update:modelValue', [...new Set([...props.modelValue, ...SMART_OPS_MENU_ITEMS.map(item => item.path)])])
}
</script>
