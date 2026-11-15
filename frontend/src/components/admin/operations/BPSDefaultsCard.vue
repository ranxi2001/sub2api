<template>
  <section class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900" data-testid="bps-defaults-card">
    <header class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
      <div class="flex items-center gap-3">
        <span class="rounded-xl bg-primary-50 px-3 py-2 text-xs font-bold tracking-wide text-primary-700 dark:bg-primary-950 dark:text-primary-300">BPS</span>
        <div><h2 class="text-lg font-semibold">{{ t('autoConfig.bps.title') }}</h2><p class="mt-1 text-xs text-gray-500">{{ t('autoConfig.bps.subtitle') }}</p></div>
      </div>
      <span class="rounded-full bg-gray-100 px-3 py-1 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300">ChatGPT OAuth</span>
    </header>
    <div class="space-y-5 p-5">
      <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('autoConfig.bps.scopeHint') }}</p>
      <div class="grid gap-6 lg:grid-cols-2">
        <div class="min-w-0 space-y-3">
          <div class="flex items-center justify-between gap-3"><h3 class="text-sm font-semibold">{{ t('autoConfig.bps.models') }}</h3><button type="button" class="text-xs font-medium text-primary-600 hover:underline dark:text-primary-400" data-testid="bps-reset-options" @click="resetOptions">{{ t('autoConfig.bps.reset') }}</button></div>
          <label class="flex items-center gap-2 text-sm"><input v-model="draft.all_models" type="checkbox" data-testid="bps-all-models" />{{ t('admin.accounts.openai.excelBPSAllModels') }}</label>
          <ModelWhitelistSelector v-if="!draft.all_models" v-model="draft.models" platform="openai" />
          <p class="text-xs leading-5 text-gray-500">{{ t('admin.accounts.openai.excelBPSModelsHint') }}</p>
        </div>
        <div class="space-y-3">
          <h3 class="text-sm font-semibold">{{ t('autoConfig.bps.options') }}</h3>
          <label v-for="item in recommended" :key="item.key" class="flex cursor-pointer items-start gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700">
            <input v-model="draft[item.key]" type="checkbox" class="mt-0.5" :data-testid="'bps-' + item.key" />
            <span><span class="block text-sm font-medium">{{ t('admin.accounts.openai.excelBPS' + item.label) }}</span><span class="mt-1 block text-xs leading-5 text-gray-500">{{ t('autoConfig.bps.' + item.key + 'Hint') }}</span></span>
          </label>
        </div>
      </div>
      <ExcelBPSSearchOptions v-model:enabled="draft.web_search" v-model:automatic="draft.web_search_auto" />
      <details class="group rounded-xl border border-gray-200 dark:border-dark-700" data-testid="bps-advanced">
        <summary class="cursor-pointer px-4 py-3 text-sm font-medium">{{ t('autoConfig.bps.advanced') }}<span class="ml-2 text-xs font-normal text-gray-500">{{ t('autoConfig.bps.advancedHint') }}</span></summary>
        <div class="grid gap-5 border-t border-gray-100 p-4 md:grid-cols-2 dark:border-dark-700">
          <div v-for="item in advanced" :key="item.key">
            <label class="flex items-center gap-2 text-sm"><input v-model="draft[item.key]" type="checkbox" :disabled="item.key === 'auto_recover_on_403' && !draft.auto_disable_on_403" :data-testid="'bps-' + item.key" />{{ t('admin.accounts.openai.excelBPS' + item.label) }}</label>
            <p class="mt-1 pl-6 text-xs leading-5 text-gray-500">{{ t('autoConfig.bps.' + item.key + 'Hint') }}</p>
            <label v-if="item.key === 'auto_recover_on_403' && draft.auto_recover_on_403 && draft.auto_disable_on_403" class="mt-3 block pl-6 text-xs">{{ t('admin.accounts.openai.excelBPS403RecoveryInterval') }}<input v-model.number="draft.recovery_interval_minutes" class="input mt-1 w-full" type="number" min="1" :max="MAX_BPS_RECOVERY_INTERVAL_MINUTES" step="1" required data-testid="bps-recovery-interval" /></label>
            <label v-if="item.key === 'auto_move_on_403' && draft.auto_move_on_403" class="mt-3 block pl-6 text-xs">{{ t('admin.accounts.openai.excelBPS403TargetGroup') }}<select v-model.number="draft.target_group_id" class="input mt-1 w-full" data-testid="bps-target-group"><option :value="-1" disabled>{{ t('admin.accounts.openai.excelBPS403SelectTarget') }}</option><option :value="0">{{ t('admin.accounts.openai.excelBPS403LeaveAllGroups') }}</option><option v-for="g in targetGroups" :key="g.id" :value="g.id">{{ g.name }} #{{ g.id }}</option></select></label>
            <div v-if="item.key === 'session_proxy' && draft.session_proxy" class="mt-3 flex flex-wrap gap-4 pl-6 text-xs" role="radiogroup" :aria-label="t('admin.accounts.openai.excelBPSProxySource')"><label class="flex items-center gap-2"><input v-model="draft.proxy_source" type="radio" value="mihomo" />{{ t('admin.accounts.openai.excelBPSProxySourceMihomo') }}</label><label class="flex items-center gap-2"><input v-model="draft.proxy_source" type="radio" value="ip_pool" />{{ t('admin.accounts.openai.excelBPSProxySourceIPPool') }}</label></div>
          </div>
        </div>
      </details>
      <div class="grid gap-3 md:grid-cols-2">
        <label v-for="item in companions" :key="item.key" class="flex cursor-pointer items-start gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700">
          <input v-model="draft[item.key]" type="checkbox" class="mt-0.5" :data-testid="'bps-' + item.key" />
          <span><span class="block text-sm font-medium">{{ t('admin.accounts.openai.' + item.label) }}</span><span class="mt-1 block text-xs leading-5 text-gray-500">{{ t('autoConfig.bps.' + item.key + 'Hint') }}</span></span>
        </label>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ExcelBPSSearchOptions from '@/components/account/ExcelBPSSearchOptions.vue'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import { defaultExcelBPSDefaults, type ExcelBPSDefaults } from '@/utils/excelBPSDefaults'
import { MAX_BPS_RECOVERY_INTERVAL_MINUTES } from '@/utils/excelBPSRecovery'
import type { Group } from '@/types'
const props = defineProps<{ groups: Group[] }>()
const draft = defineModel<ExcelBPSDefaults>({ required: true })
const { t } = useI18n()
const companions = [{ key: 'ws_sse_acceleration', label: 'wsSseAcceleration' }, { key: 'auto_enable_on_degradation', label: 'autoBPS' }] as const
const recommended = [{ key: 'ignore_encrypted_content', label: 'IgnoreEncryptedContent' }, { key: 'auto_disable_on_403', label: 'AutoDisableOn403' }, { key: 'cache_creation_as_input', label: 'CacheCreationAsInput' }] as const
const advanced = [{ key: 'omit_unsupported_tools', label: 'OmitUnsupportedTools' }, { key: 'auto_recover_on_403', label: 'AutoRecoverOn403' }, { key: 'auto_move_on_403', label: 'AutoMoveOn403' }, { key: 'session_proxy', label: 'Mihomo' }] as const
const targetGroups = computed(() => props.groups.filter(g => g.platform === 'openai' || g.platform === 'composite'))
watch(() => draft.value.auto_disable_on_403, enabled => { if (!enabled) draft.value.auto_recover_on_403 = false })
function resetOptions() {
  draft.value = defaultExcelBPSDefaults()
}
</script>

<style scoped>
input[type=checkbox], input[type=radio] { @apply h-4 w-4 shrink-0 text-primary-600; }
input[type=checkbox] { @apply rounded; }
</style>
