<template>
  <div class="space-y-4 text-sm" data-testid="quality-bps-settings">
    <p class="text-gray-500">{{ t('qualityOps.bpsHint') }}</p>
    <div class="grid gap-3 sm:grid-cols-2">
      <label class="space-y-1"><span>{{ t('qualityOps.bpsThreshold') }}</span><input v-model.number="bps.failure_threshold" type="number" min="0" max="100" step="1" class="input" data-testid="quality-bps-threshold" /><span class="block text-xs text-gray-500">{{ t('qualityOps.bpsThresholdHint') }}</span></label>
      <label class="space-y-1"><span>{{ t('qualityOps.bpsUsage') }}</span><input v-model.number="bps.usage_percent" type="number" min="0" max="100" step="1" class="input" data-testid="quality-bps-usage" /><span class="block text-xs text-gray-500">{{ t('qualityOps.bpsUsageHint') }}</span></label>
    </div>
    <div v-if="bps.failure_threshold > 0 && bps.usage_percent > 0" class="flex flex-wrap items-center gap-4" role="radiogroup" :aria-label="t('qualityOps.bpsTriggerMode')">
      <span class="text-gray-500">{{ t('qualityOps.bpsTriggerMode') }}</span>
      <label class="flex items-center gap-2"><input v-model="bps.require_all" type="radio" :value="false" data-testid="quality-bps-require-any" />{{ t('qualityOps.bpsRequireAny') }}</label>
      <label class="flex items-center gap-2"><input v-model="bps.require_all" type="radio" :value="true" data-testid="quality-bps-require-all" />{{ t('qualityOps.bpsRequireAll') }}</label>
    </div>
    <QualityBPSRestoreOptions v-if="showAutoRestore" v-model:bps="bps" v-model:auto-restore="autoRestore" />
    <div class="space-y-2">
      <p class="font-medium">{{ t('qualityOps.bpsModelsTitle') }}</p>
      <label class="flex items-center gap-2"><input v-model="bps.all_models" type="checkbox" data-testid="quality-bps-all-models" />{{ t('admin.accounts.openai.excelBPSAllModels') }}</label>
      <div v-if="!bps.all_models" class="space-y-2" data-testid="quality-bps-models">
        <ModelWhitelistSelector v-model="bps.models" platform="openai" />
        <button type="button" class="btn btn-secondary" data-testid="quality-bps-astra-only" @click="bps.models = ['gpt-6-astra']">{{ t('admin.accounts.openai.excelBPSAstraOnly') }}</button>
        <p class="text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPSModelsHint') }}</p>
      </div>
    </div>
    <div class="space-y-3">
      <div><p class="font-medium">{{ t('qualityOps.bpsOptions') }}</p><p class="text-xs text-gray-500">{{ t('qualityOps.bpsOptionsHint') }}</p></div>
      <ExcelBPSSearchOptions v-model:enabled="bps.web_search" v-model:automatic="bps.web_search_auto" />
      <div v-for="[key, label] in bpsToggles" :key="key">
        <label class="flex items-center gap-2"><input v-model="bps[key]" type="checkbox" :disabled="key === 'auto_recover_on_403' && !bps.auto_disable_on_403" :data-testid="`quality-bps-${key}`" />{{ t(`admin.accounts.openai.excelBPS${label}`) }}</label>
        <p class="mt-1 pl-6 text-xs text-gray-500">{{ t(`admin.accounts.openai.excelBPS${label}Desc`) }}</p>
        <div v-if="key === 'auto_recover_on_403' && bps.auto_disable_on_403 && bps.auto_recover_on_403" class="mt-2 pl-6">
          <label class="block space-y-1"><span>{{ t('admin.accounts.openai.excelBPS403RecoveryInterval') }}</span>
            <input v-model.number="bps.recovery_interval_minutes" type="number" min="1" :max="MAX_BPS_RECOVERY_INTERVAL_MINUTES" step="1" required class="input w-40" data-testid="quality-bps-recovery-interval" />
          </label>
          <p class="mt-1 text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPS403RecoveryIntervalHint') }}</p>
        </div>
      </div>
      <div>
        <label class="flex items-center gap-2"><input v-model="bps.auto_move_on_403" type="checkbox" data-testid="quality-bps-auto_move_on_403" />{{ t('admin.accounts.openai.excelBPSAutoMoveOn403') }}</label>
        <p class="mt-1 pl-6 text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPSAutoMoveOn403Desc') }}</p>
        <label v-if="bps.auto_move_on_403" class="mt-2 block space-y-1 pl-6"><span>{{ t('admin.accounts.openai.excelBPS403TargetGroup') }}</span>
          <select v-model.number="bps.target_group_id" class="input" data-testid="quality-bps-target-group">
            <option :value="-1" disabled>{{ t('admin.accounts.openai.excelBPS403SelectTarget') }}</option>
            <option :value="0">{{ t('admin.accounts.openai.excelBPS403LeaveAllGroups') }}</option>
            <option v-for="group in targetGroups" :key="group.id" :value="group.id">{{ group.name }} #{{ group.id }}</option>
          </select>
        </label>
      </div>
      <div>
        <label class="flex items-center gap-2"><input v-model="bps.session_proxy" type="checkbox" data-testid="quality-bps-session_proxy" />{{ t('admin.accounts.openai.excelBPSMihomo') }}</label>
        <p class="mt-1 pl-6 text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPSMihomoDesc') }}</p>
        <div v-if="bps.session_proxy" class="mt-2 flex flex-wrap items-center gap-4 pl-6" role="radiogroup" :aria-label="t('admin.accounts.openai.excelBPSProxySource')">
          <span class="text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPSProxySource') }}</span>
          <label class="flex items-center gap-2"><input v-model="bps.proxy_source" type="radio" value="mihomo" />{{ t('admin.accounts.openai.excelBPSProxySourceMihomo') }}</label>
          <label class="flex items-center gap-2"><input v-model="bps.proxy_source" type="radio" value="ip_pool" />{{ t('admin.accounts.openai.excelBPSProxySourceIPPool') }}</label>
        </div>
        <p v-if="bps.session_proxy && bps.proxy_source === 'ip_pool'" class="mt-1 pl-6 text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPSProxySourceIPPoolDesc') }}</p>
      </div>
      <div>
        <label class="flex items-center gap-2"><input v-model="bps.cache_creation_as_input" type="checkbox" data-testid="quality-bps-cache_creation_as_input" />{{ t('admin.accounts.openai.excelBPSCacheCreationAsInput') }}</label>
        <p class="mt-1 pl-6 text-xs text-gray-500">{{ t('admin.accounts.openai.excelBPSCacheCreationAsInputDesc') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// 「降智后开启 BPS」的触发条件、满血后关闭和开启时默认勾选的模型/选项。
// 质量运维的规则表单和添加/编辑账号弹窗共用这一份，改一处三处一致。
import { useI18n } from 'vue-i18n'
import { MAX_BPS_RECOVERY_INTERVAL_MINUTES } from '@/utils/excelBPSRecovery'
import ExcelBPSSearchOptions from '@/components/account/ExcelBPSSearchOptions.vue'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import QualityBPSRestoreOptions from './QualityBPSRestoreOptions.vue'
import type { QualityBPSPolicy } from '@/types'

withDefaults(defineProps<{ targetGroups: { id: number; name: string }[]; showAutoRestore?: boolean }>(), { showAutoRestore: true })
const bps = defineModel<QualityBPSPolicy>('bps', { required: true })
const autoRestore = defineModel<boolean>('autoRestore', { default: false })
const { t } = useI18n()
const bpsToggles = [['omit_unsupported_tools', 'OmitUnsupportedTools'], ['ignore_encrypted_content', 'IgnoreEncryptedContent'], ['auto_disable_on_403', 'AutoDisableOn403'], ['auto_recover_on_403', 'AutoRecoverOn403']] as const
</script>
