<template>
  <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="group-anti-distillation-settings">
    <div class="mb-2 flex flex-wrap items-center gap-2">
      <h4 class="text-sm font-medium text-gray-900 dark:text-gray-100">
        {{ t('admin.groups.antiDistillation.title') }}
      </h4>
      <NewFeatureBadge
        v-if="userId != null"
        :notice-id="NEW_FEATURE_NOTICES.groupAntiDistillation"
        :user-id="userId"
      />
    </div>
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.antiDistillation.rule') }}</p>
    <p class="mt-2 text-xs text-amber-700 dark:text-amber-300">{{ t('admin.groups.antiDistillation.signalHint') }}</p>

    <p v-if="loading" class="mt-3 text-sm text-gray-500" role="status">{{ t('common.loading') }}</p>
    <div v-if="loadFailed" class="mt-3">
      <p class="text-sm text-red-600 dark:text-red-400" role="alert">{{ t('admin.groups.antiDistillation.loadFailed') }}</p>
      <button type="button" class="btn btn-secondary mt-2" data-testid="anti-distillation-retry" @click="load">
        {{ t('admin.groups.antiDistillation.retry') }}
      </button>
    </div>

    <fieldset :disabled="loading || saving || !loaded" class="mt-4 space-y-3">
      <div class="flex items-start justify-between gap-4">
        <label :for="`${fieldId}-enabled`" class="text-sm text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.antiDistillation.enable') }}
        </label>
        <Toggle
          :id="`${fieldId}-enabled`"
          v-model="enabled"
          :disabled="loading || saving || !loaded"
          :aria-label="t('admin.groups.antiDistillation.enable')"
          data-testid="anti-distillation-toggle"
        />
      </div>
      <div v-if="enabled">
        <label :for="`${fieldId}-minutes`" class="input-label">{{ t('admin.groups.antiDistillation.pauseMinutes') }}</label>
        <input
          :id="`${fieldId}-minutes`"
          v-model="pauseMinutes"
          type="text"
          inputmode="numeric"
          :disabled="loading || saving || !loaded"
          class="input"
          :aria-invalid="!validMinutes"
          :aria-describedby="`${fieldId}-minutes-hint`"
          data-testid="anti-distillation-minutes"
          @keydown.enter.prevent="save"
        />
        <p :id="`${fieldId}-minutes-hint`" class="input-hint">{{ t('admin.groups.antiDistillation.minutesHint') }}</p>
        <p v-if="!validMinutes" class="mt-1 text-xs text-red-600 dark:text-red-400" role="alert">
          {{ t('admin.groups.antiDistillation.invalidMinutes') }}
        </p>
      </div>
    </fieldset>

    <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.antiDistillation.scopeHint') }}</p>
    <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.antiDistillation.saveHint') }}</p>
    <p v-if="saveFailed" class="mt-2 text-sm text-red-600 dark:text-red-400" role="alert">{{ t('admin.groups.antiDistillation.saveFailed') }}</p>
    <button
      type="button"
      class="btn btn-secondary mt-3"
      :disabled="loading || saving || !loaded || (enabled && !validMinutes)"
      data-testid="anti-distillation-save"
      @click="save"
    >
      {{ saving ? t('common.saving') : t('admin.groups.antiDistillation.save') }}
    </button>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Toggle from '@/components/common/Toggle.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAppStore } from '@/stores/app'

const props = withDefaults(defineProps<{
  groupId: number
  userId?: number | null
  active?: boolean
}>(), { active: true, userId: null })

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const loadFailed = ref(false)
const saveFailed = ref(false)
const enabled = ref(false)
const pauseMinutes = ref('5')
const fieldId = computed(() => `group-${props.groupId}-anti-distillation`)
const validMinutes = computed(() => /^\d+$/.test(pauseMinutes.value)
  && Number(pauseMinutes.value) >= 1 && Number(pauseMinutes.value) <= 1440)
let generation = 0

async function load(): Promise<void> {
  const current = ++generation
  loaded.value = false
  loading.value = false
  saving.value = false
  loadFailed.value = false
  saveFailed.value = false
  enabled.value = false
  pauseMinutes.value = '5'
  if (!props.active || props.groupId <= 0) return
  loading.value = true
  try {
    const config = await adminAPI.groups.getAntiDistillation(props.groupId)
    if (generation !== current) return
    enabled.value = config.enabled
    pauseMinutes.value = String(config.pause_minutes)
    loaded.value = true
  } catch {
    if (generation === current) loadFailed.value = true
  } finally {
    if (generation === current) loading.value = false
  }
}

async function save(): Promise<void> {
  if (!props.active || !loaded.value || loading.value || saving.value || (enabled.value && !validMinutes.value)) return
  const current = generation
  const groupId = props.groupId
  saving.value = true
  saveFailed.value = false
  try {
    const config = await adminAPI.groups.saveAntiDistillation(groupId, {
      enabled: enabled.value,
      pause_minutes: validMinutes.value ? Number(pauseMinutes.value) : 5,
    })
    if (generation !== current) return
    enabled.value = config.enabled
    pauseMinutes.value = String(config.pause_minutes)
    appStore.showSuccess(t('admin.groups.antiDistillation.saved'))
  } catch {
    if (generation === current) saveFailed.value = true
  } finally {
    if (generation === current) saving.value = false
  }
}

watch(() => [props.groupId, props.active], load, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>
