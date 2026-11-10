<template>
  <AppLayout>
    <div class="space-y-5">
      <header><h1 class="text-2xl font-semibold">{{ t('autoConfig.title') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('autoConfig.description') }}</p></header>
      <SmartOpsNav />
      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error }}</p>
      <p v-if="notice" role="status" class="text-emerald-600">{{ notice }}</p>
      <p v-if="loading">{{ t('common.loading') }}</p>
      <button v-if="!draft && !loading" class="btn btn-secondary" @click="load">{{ t('autoConfig.retry') }}</button>
      <form v-if="draft" class="space-y-5" @submit.prevent="save">
        <p v-if="draft.runtime_blocked" role="alert" class="rounded-xl bg-amber-50 p-4 text-amber-800">{{ t('autoConfig.blocked') }}</p>
        <div class="grid min-w-0 gap-5 lg:grid-cols-2" data-testid="oauth-handling-panels">
          <fieldset :disabled="saving" class="min-w-0"><OAuthDegradationCard v-model="draft.degradation_action" :groups="groups" :user-id="auth.user?.id" /></fieldset>
          <fieldset :disabled="saving" class="min-w-0"><OpenAIAPIKeyFallbackCard ref="fallbackCard" v-model="draft.openai_apikey_fallback" :user-id="auth.user?.id" :status="fallbackStatus" :status-loading="fallbackStatusLoading" :status-failed="fallbackStatusFailed" @refresh-status="refreshFallbackStatus" /></fieldset>
        </div>
        <fieldset :disabled="saving" class="min-w-0"><BPSDefaultsCard v-model="draft.excel_bps" :groups="groups" /></fieldset>
        <fieldset :disabled="saving" class="min-w-0">
          <section class="card space-y-5" data-testid="model-billing">
            <header class="flex flex-wrap items-center justify-between gap-3">
              <h2 class="text-lg font-semibold">{{ t('autoConfig.modelBilling.title') }}</h2>
              <label class="flex items-center gap-2 text-sm"><input v-model="draft.model_billing.enabled" data-testid="model-billing-enabled" type="checkbox" role="switch" />{{ t('autoConfig.enable') }}</label>
            </header>
            <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.modelBilling.hint') }}</p>
            <p v-if="modelBillingGroupsLoading" class="text-sm text-gray-500">{{ t('autoConfig.modelBilling.loadingGroups') }}</p>
            <p v-if="modelBillingGroupsFailed" role="alert" data-testid="model-billing-groups-error" class="text-sm text-red-600">{{ t('autoConfig.modelBilling.groupsFailed') }} <button type="button" class="underline" data-testid="model-billing-groups-retry" @click="loadModelBillingGroups">{{ t('autoConfig.retry') }}</button></p>
            <div v-for="(rule, index) in draft.model_billing.rules" :key="index" class="grid items-end gap-3 sm:grid-cols-[minmax(0,1fr)_10rem_auto]">
              <label class="min-w-0"><span class="field-label">{{ t('autoConfig.modelBilling.model') }}</span><input v-model="rule.model" :data-testid="'model-billing-model-' + index" type="text" maxlength="200" placeholder="gpt-6-luna*" required class="input w-full" /></label>
              <label><span class="field-label">{{ t('autoConfig.modelBilling.multiplier') }}</span><div class="flex items-center gap-2"><input v-model.number="rule.multiplier" :data-testid="'model-billing-multiplier-' + index" type="number" min="1" max="1000" step="any" required class="input min-w-0 w-full" /><span class="text-sm text-gray-500">×</span></div></label>
              <button type="button" :data-testid="'model-billing-remove-' + index" class="btn btn-secondary" @click="draft.model_billing.rules.splice(index, 1)">{{ t('autoConfig.modelBilling.remove') }}</button>
              <fieldset class="min-w-0 space-y-2 sm:col-span-3" :disabled="modelBillingGroupsLoading || modelBillingGroupsFailed">
                <legend class="field-label">{{ t('autoConfig.modelBilling.groups') }}</legend>
                <p :data-testid="'model-billing-scope-' + index" class="text-xs text-gray-500">{{ rule.group_ids?.length ? t('autoConfig.modelBilling.selectedGroups', { count: rule.group_ids.length }) : t('autoConfig.modelBilling.allGroups') }}</p>
                <div class="group-list">
                  <label v-for="g in modelBillingGroups" :key="g.id" class="group-option"><input v-model="rule.group_ids" :value="g.id" type="checkbox" :data-testid="'model-billing-group-' + index + '-' + g.id" /><span>{{ g.name }} <small class="text-gray-400">{{ g.platform }} · #{{ g.id }}</small></span></label>
                  <label v-for="id in unavailableModelBillingGroups(rule)" :key="id" :data-testid="'model-billing-unavailable-' + index + '-' + id" class="group-option text-amber-700 dark:text-amber-300"><input v-model="rule.group_ids" :value="id" type="checkbox" /><span>#{{ id }} · {{ t('autoConfig.modelBilling.unavailableGroup') }}</span></label>
                  <p v-if="!modelBillingGroups.length && !modelBillingGroupsLoading && !modelBillingGroupsFailed" class="text-sm text-gray-500">{{ t('autoConfig.noGroups') }}</p>
                </div>
                <button v-if="rule.group_ids?.length" type="button" :data-testid="'model-billing-clear-groups-' + index" class="text-sm text-primary-600 underline" @click="rule.group_ids = []">{{ t('autoConfig.modelBilling.clearGroups') }}</button>
              </fieldset>
            </div>
            <button type="button" data-testid="model-billing-add" class="btn btn-secondary" :disabled="draft.model_billing.rules.length >= 100" @click="draft.model_billing.rules.push({ model: '', multiplier: 10, group_ids: [] })">{{ t('autoConfig.modelBilling.add') }}</button>
            <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.modelBilling.matchHint') }}</p>
            <p class="rounded-xl bg-amber-50 p-3 text-sm leading-6 text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ t('autoConfig.modelBilling.example') }}</p>
          </section>
        </fieldset>
        <fieldset :disabled="saving" class="min-w-0"><OpenAITwoFAAdmissionCard v-model="draft.openai_two_fa_admission" :groups="groups" /></fieldset>
        <fieldset :disabled="saving" class="grid min-w-0 gap-5 xl:grid-cols-2">
          <section class="card space-y-5">
            <header class="flex items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('autoConfig.initial') }}</h2><label class="flex items-center gap-2 text-sm"><input v-model="draft.enabled" data-testid="initial-enabled" type="checkbox" role="switch" />{{ t('autoConfig.enable') }}</label></header>
            <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.initialHint') }}</p>
            <label class="block"><span class="field-label">{{ t('autoConfig.platform') }}</span><select v-model="draft.platform" data-testid="platform" class="input w-full" @change="draft.group_ids = []; draft.model_mappings = defaultOAuthModelMappings(draft.platform); draft.selected_models = null; draft.quality_rule = null; draft.openai_totp_rotate_on_429 = false; draft.openai_disable_scheduling_on_429 = false; draft.openai_disable_scheduling_on_429_7d_threshold_percent = DEFAULT_DISABLE_SCHEDULING_429_7D_THRESHOLD_PERCENT; draft.openai_ws_mode = OPENAI_WS_MODE_CTX_POOL; draft.openai_ws_sse_acceleration = false; selectedQualityRule = ''"><option v-for="p in platforms" :key="p.value" :value="p.value">{{ p.label }}</option></select></label>
            <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4"><label v-for="field in initialFields" :key="field.key"><span class="field-label">{{ t('autoConfig.' + field.key) }}</span><input v-model.number="draft[field.key]" :data-testid="field.key" type="number" :min="field.min" max="10000" step="1" required class="input w-full" /></label><label><span class="field-label">{{ t('autoConfig.cost_multiplier') }}</span><input v-model.number="draft.cost_multiplier" data-testid="cost_multiplier" type="number" min="0" max="1000000" step="0.001" required class="input w-full" /></label></div>
            <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.loadHint') }}</p>
            <fieldset v-if="draft.platform === 'openai'" class="space-y-2">
              <div class="flex flex-wrap items-center gap-2">
                <label class="flex items-center gap-2 text-sm"><input v-model="draft.openai_totp_rotate_on_429" data-testid="totp-rotate-on-429" type="checkbox" />{{ t('autoConfig.totp429.label') }}</label>
                <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthInitialTOTPRotation429" :user-id="auth.user?.id" />
              </div>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.totp429.hint') }}</p>
            </fieldset>
            <fieldset v-if="draft.platform === 'openai'" class="space-y-2">
              <div class="flex flex-wrap items-center gap-2">
                <label class="flex items-center gap-2 text-sm"><input v-model="draft.openai_disable_scheduling_on_429" data-testid="disable-scheduling-on-429" type="checkbox" />{{ t('autoConfig.disableScheduling429.label') }}</label>
                <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthInitialDisableScheduling429" :user-id="auth.user?.id" />
              </div>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.disableScheduling429.hint') }}</p>
              <label class="block max-w-xs">
                <span class="field-label">{{ t('autoConfig.disableScheduling429.threshold') }}</span>
                <div class="flex items-center gap-2"><input v-model.number="draft.openai_disable_scheduling_on_429_7d_threshold_percent" data-testid="disable-scheduling-on-429-7d-threshold" type="number" min="0" max="100" step="any" required :disabled="!draft.openai_disable_scheduling_on_429" class="input min-w-0 w-full" /><span class="text-sm text-gray-500">%</span></div>
              </label>
            </fieldset>
            <fieldset v-if="draft.platform === 'openai'" class="space-y-2" data-testid="initial-ws-mode">
              <div class="flex flex-wrap items-center gap-2">
                <label for="initial-openai-ws-mode" class="text-sm font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.openai.wsMode') }}</label>
                <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthInitialWSSettings" :user-id="auth.user?.id" />
              </div>
              <select id="initial-openai-ws-mode" v-model="draft.openai_ws_mode" data-testid="initial-ws-mode-select" class="input w-full">
                <option v-for="mode in openAIWSModeOptions" :key="mode.value" :value="mode.value">{{ mode.label }}</option>
              </select>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.ws.modeHint') }}</p>
              <p v-if="openAIWSModeHintKey" class="text-xs leading-5 text-gray-500">{{ t(openAIWSModeHintKey) }}</p>
            </fieldset>
            <fieldset v-if="draft.platform === 'openai'" class="space-y-2" data-testid="initial-ws-sse-acceleration">
              <div class="flex flex-wrap items-center gap-2">
                <label class="flex items-center gap-2 text-sm"><input v-model="draft.openai_ws_sse_acceleration" data-testid="initial-ws-sse-acceleration-enabled" type="checkbox" />{{ t('admin.accounts.openai.wsSseAcceleration') }}</label>
                <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.oauthInitialWSSettings" :user-id="auth.user?.id" />
              </div>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.ws.accelerationHint') }}</p>
              <p class="text-xs leading-5 text-gray-500">{{ t('admin.accounts.openai.wsSseAccelerationDesc') }}</p>
            </fieldset>
            <fieldset><legend class="field-label">{{ t('autoConfig.groups') }}</legend><div class="group-list"><label v-for="g in initialGroups" :key="g.id" class="group-option"><input v-model="draft.group_ids" :value="g.id" type="checkbox" :data-testid="'initial-group-' + g.id" /><span>{{ g.name }} <small class="text-gray-400">#{{ g.id }}</small></span></label><p v-if="!initialGroups.length" class="text-sm text-gray-500">{{ t('autoConfig.noGroups') }}</p></div></fieldset>
            <fieldset class="space-y-3" data-testid="initial-quality-rule">
              <legend class="field-label">{{ t('autoConfig.quality.title') }}</legend>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.quality.hint') }}</p>
              <select v-model="selectedQualityRule" data-testid="quality-rule-select" class="input w-full" :aria-label="t('autoConfig.quality.title')" @change="selectQualityRule">
                <option value="">{{ t('autoConfig.quality.none') }}</option>
                <option v-if="draft.quality_rule" value="saved">{{ t('autoConfig.quality.saved') }}</option>
                <option v-for="plan in availableQualityPlans" :key="plan.id" :value="String(plan.id)">{{ plan.account_name || '#' + plan.account_id }} · #{{ plan.id }} · {{ plan.model_id }} · {{ qualityActionLabel(plan) }}</option>
              </select>
              <p v-if="qualityLoadFailed" role="alert" class="text-sm text-red-600">{{ t('autoConfig.quality.loadFailed') }} <button type="button" class="underline" @click="loadQualityPlans">{{ t('autoConfig.retry') }}</button></p>
              <p v-if="draft.quality_rule" data-testid="quality-rule-summary" class="text-sm text-gray-500">{{ draft.quality_rule.model_id }} · {{ draft.quality_rule.cron_expression }} · {{ t(draft.quality_rule.enabled ? 'qualityOps.activeShort' : 'qualityOps.paused') }}</p>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.quality.copyHint') }}</p>
            </fieldset>
            <OAuthInitialModelSelection v-model="draft.selected_models" :platform="draft.platform" :mappings="draft.model_mappings" :user-id="auth.user?.id" />
            <fieldset class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700" data-testid="initial-model-mappings">
              <legend class="field-label">{{ t('autoConfig.mapping.title') }}</legend>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.mapping.hint') }}</p>
              <div v-for="(rule, index) in draft.model_mappings" :key="index" class="grid min-w-0 gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-end">
                <label class="min-w-0"><span class="field-label">{{ t('autoConfig.mapping.from') }}</span><input v-model="rule.from" :data-testid="'mapping-from-' + index" class="input w-full" type="text" placeholder="gpt-5.4" maxlength="256" required /></label>
                <label class="min-w-0"><span class="field-label">{{ t('autoConfig.mapping.to') }}</span><input v-model="rule.to" :data-testid="'mapping-to-' + index" class="input w-full" type="text" placeholder="gpt-5.5" maxlength="256" required /></label>
                <button type="button" :data-testid="'mapping-remove-' + index" class="btn btn-secondary" :aria-label="t('autoConfig.mapping.remove') + ' ' + (index + 1)" @click="draft.model_mappings.splice(index, 1)">{{ t('autoConfig.mapping.remove') }}</button>
              </div>
              <button type="button" data-testid="mapping-add" class="btn btn-secondary" :disabled="draft.model_mappings.length >= 100" @click="draft.model_mappings.push({ from: '', to: '' })">{{ t('autoConfig.mapping.add') }}</button>
              <p class="text-xs leading-5 text-gray-500">{{ t('autoConfig.mapping.allowlistHint') }}</p>
            </fieldset>
          </section>
          <section class="card space-y-5">
            <header class="flex items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('autoConfig.upgrade') }}</h2><label class="flex items-center gap-2 text-sm"><input v-model="draft.upgrade_enabled" data-testid="upgrade-enabled" type="checkbox" role="switch" />{{ t('autoConfig.enable') }}</label></header>
            <p class="text-sm leading-6 text-gray-500">{{ t('autoConfig.upgradeHint') }}</p>
            <div class="grid gap-4 sm:grid-cols-2"><label v-for="field in upgradeFields" :key="field.key"><span class="field-label">{{ t('autoConfig.' + field.key) }}</span><input v-model.number="draft[field.key]" :data-testid="field.key" type="number" min="1" :max="field.max" step="1" required class="input w-full" /></label></div>
            <p class="rounded-xl bg-emerald-50 p-3 text-sm leading-6 text-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-200">{{ t('autoConfig.rule', { count: draft.successes_per_step, step: draft.upgrade_step, max: draft.max_concurrency }) }}</p>
            <fieldset><legend class="field-label">{{ t('autoConfig.upgradeGroups') }}</legend><div class="group-list"><label v-for="g in groups" :key="g.id" class="group-option"><input v-model="draft.upgrade_group_ids" :value="g.id" type="checkbox" :data-testid="'upgrade-group-' + g.id" /><span>{{ g.name }} <small class="text-gray-400">{{ g.platform }} · #{{ g.id }}</small></span></label></div></fieldset>
          </section>
        </fieldset>
        <div class="flex flex-wrap items-center gap-4"><button type="submit" class="btn btn-primary" :disabled="saving">{{ t(saving ? 'autoConfig.saving' : 'common.save') }}</button><span class="text-xs text-gray-500">{{ t('autoConfig.saveHint') }}</span></div>
      </form>
      <AutoConfigHistory v-if="auth.user?.role === 'admin'" :refresh-key="historyRefreshKey" />
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import AppLayout from '@/components/layout/AppLayout.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import OAuthDegradationCard from '@/components/admin/operations/OAuthDegradationCard.vue'
import OpenAIAPIKeyFallbackCard from '@/components/admin/operations/OpenAIAPIKeyFallbackCard.vue'
import { openaiAPIKeyFallbackPayload, openaiAPIKeyFallbackError, type OpenAIAPIKeyFallbackConfig, type OpenAIAPIKeyFallbackStatus } from '@/utils/openaiAPIKeyFallback'
import { oauthDegradationPayload, oauthDegradationError, type OAuthDegradationConfig } from '@/utils/oauthDegradation'
import BPSDefaultsCard from '@/components/admin/operations/BPSDefaultsCard.vue'
import OpenAITwoFAAdmissionCard from '@/components/admin/operations/OpenAITwoFAAdmissionCard.vue'
import { defaultOpenAITwoFAAdmissionConfig, openaiTwoFAAdmissionError, type OpenAITwoFAAdmissionConfig } from '@/utils/openaiTwoFAAdmission'
import { OPENAI_WS_MODE_OFF, OPENAI_WS_MODE_CTX_POOL, OPENAI_WS_MODE_PASSTHROUGH, OPENAI_WS_MODE_HTTP_BRIDGE, resolveOpenAIWSModeHintKey, type OpenAIWSMode } from '@/utils/openaiWsMode'
import { defaultOAuthModelMappings, oauthModelMappingsError, oauthModelMappingsPayload, type OAuthModelMappingRule } from '@/utils/oauthModelMappings'
import { defaultExcelBPSDefaults, excelBPSDefaultsError, excelBPSDefaultsPayload, type ExcelBPSDefaults } from '@/utils/excelBPSDefaults'
import OAuthInitialModelSelection from '@/components/admin/operations/OAuthInitialModelSelection.vue'
import AutoConfigHistory from '@/components/admin/operations/AutoConfigHistory.vue'
import { getModelBillingGroups } from '@/api/admin/autoConfig'
import { getAll } from '@/api/admin/groups'
import { getAutoConfig, saveAutoConfig, type AutoConfig } from '@/api/admin/autoConfig'
import { listQualityPlans } from '@/api/admin/accountQuality'
import type { ScheduledTestPlan } from '@/types'
import type { Group } from '@/types'
import { defaultModelBillingConfig, modelBillingConfigError, modelBillingPayload, type ModelBillingConfig, type ModelBillingRule } from '@/utils/modelBilling'
const { t } = useI18n(), auth = useAuthStore()
const DEFAULT_DISABLE_SCHEDULING_429_7D_THRESHOLD_PERCENT = 98
const draft = ref<(AutoConfig & { openai_apikey_fallback: OpenAIAPIKeyFallbackConfig; openai_disable_scheduling_on_429_7d_threshold_percent: number; degradation_action: OAuthDegradationConfig; openai_ws_mode: OpenAIWSMode; openai_ws_sse_acceleration: boolean; openai_two_fa_admission: OpenAITwoFAAdmissionConfig; excel_bps: ExcelBPSDefaults; model_billing: ModelBillingConfig; model_mappings: OAuthModelMappingRule[] }) | null>(null), groups = ref<Group[]>([])
const modelBillingGroups = ref<Group[]>([]), modelBillingGroupsLoading = ref(true), modelBillingGroupsFailed = ref(false)
const loading = ref(false), saving = ref(false), error = ref(''), notice = ref('')
const fallbackCard = ref<InstanceType<typeof OpenAIAPIKeyFallbackCard> | null>(null)
const fallbackStatus = ref<OpenAIAPIKeyFallbackStatus | null>(null), fallbackStatusLoading = ref(false), fallbackStatusFailed = ref(false)
const qualityPlans = ref<ScheduledTestPlan[]>([]), selectedQualityRule = ref(''), qualityLoadFailed = ref(false)
const availableQualityPlans = computed(() => qualityPlans.value.filter(p => p.pelican_config?.quality && p.pelican_config.quality.action !== 'openai_two_fa_admission' && (draft.value?.platform === 'openai' || p.pelican_config.question_kind !== 'state_probe')))
function qualityActionLabel(plan: ScheduledTestPlan) {
 const keys: Record<string, string> = { isolate_models: 'isolateModels', observe_only: 'observeOnly', remove_groups: 'removeGroups', remove_models: 'removeModels', disable_scheduling: 'disableScheduling', enable_bps: 'enableBPSShort' }
 return t('qualityOps.' + keys[plan.pelican_config?.quality?.action ?? 'observe_only'])
}
async function loadQualityPlans() {
 const v = generation
 qualityLoadFailed.value = false
 try { const plans = await listQualityPlans(); if (validGeneration(v)) qualityPlans.value = plans }
 catch { if (validGeneration(v)) qualityLoadFailed.value = true }
}
function selectQualityRule() {
 if (!draft.value || selectedQualityRule.value === 'saved') return
 const plan = availableQualityPlans.value.find(p => String(p.id) === selectedQualityRule.value)
 draft.value.quality_rule = plan ? JSON.parse(JSON.stringify({ model_id: plan.model_id, cron_expression: plan.cron_expression, enabled: plan.enabled, max_results: plan.max_results, pelican_config: plan.pelican_config })) : null
}
const historyRefreshKey = ref(0)
let generation = 0, alive = true
const validGeneration = (v: number) => alive && generation === v
const platforms = [{ value: 'openai', label: 'OpenAI' }, { value: 'anthropic', label: 'Anthropic' }, { value: 'gemini', label: 'Gemini' }, { value: 'antigravity', label: 'Antigravity' }, { value: 'grok', label: 'Grok' }]
const initialFields = [{ key: 'priority', min: 0 }, { key: 'load_factor', min: 1 }, { key: 'concurrency', min: 1 }] as const
const upgradeFields = [{ key: 'successes_per_step', max: 100000 }, { key: 'upgrade_step', max: 1000 }, { key: 'max_concurrency', max: 10000 }, { key: 'cooldown_seconds', max: 86400 }] as const
const initialGroups = computed(() => groups.value.filter(g => g.platform === draft.value?.platform))
const openAIWSModeOptions = computed(() => [
 { value: OPENAI_WS_MODE_OFF, label: t('admin.accounts.openai.wsModeOff') },
 { value: OPENAI_WS_MODE_CTX_POOL, label: t('admin.accounts.openai.wsModeCtxPool') },
 { value: OPENAI_WS_MODE_PASSTHROUGH, label: t('admin.accounts.openai.wsModePassthrough') },
 { value: OPENAI_WS_MODE_HTTP_BRIDGE, label: t('admin.accounts.openai.wsModeHttpBridge') }
])
const openAIWSModeHintKey = computed(() => resolveOpenAIWSModeHintKey(draft.value?.openai_ws_mode ?? OPENAI_WS_MODE_CTX_POOL))
async function loadModelBillingGroups() {
 const v = generation
 modelBillingGroupsLoading.value = true; modelBillingGroupsFailed.value = false
 try { const available = await getModelBillingGroups(); if (validGeneration(v)) modelBillingGroups.value = available }
 catch { if (validGeneration(v)) modelBillingGroupsFailed.value = true }
 finally { if (validGeneration(v)) modelBillingGroupsLoading.value = false }
}
function unavailableModelBillingGroups(rule: ModelBillingRule) {
 if (modelBillingGroupsLoading.value || modelBillingGroupsFailed.value) return []
 return (rule.group_ids ?? []).filter(id => !modelBillingGroups.value.some(group => group.id === id))
}

async function load() {
 const v = generation; loading.value = true; error.value = ''
 try { const [config, available] = await Promise.all([getAutoConfig(), getAll()]); if (validGeneration(v)) { draft.value = { ...config, degradation_action: oauthDegradationPayload(config.degradation_action), openai_apikey_fallback: openaiAPIKeyFallbackPayload(config.openai_apikey_fallback), openai_ws_mode: config.openai_ws_mode ?? OPENAI_WS_MODE_CTX_POOL, openai_ws_sse_acceleration: config.openai_ws_sse_acceleration ?? false, openai_totp_rotate_on_429: config.openai_totp_rotate_on_429 ?? false, openai_disable_scheduling_on_429: config.openai_disable_scheduling_on_429 ?? false, openai_disable_scheduling_on_429_7d_threshold_percent: config.openai_disable_scheduling_on_429_7d_threshold_percent ?? DEFAULT_DISABLE_SCHEDULING_429_7D_THRESHOLD_PERCENT, openai_two_fa_admission: config.openai_two_fa_admission ?? defaultOpenAITwoFAAdmissionConfig(), cost_multiplier: config.cost_multiplier ?? 0.07, model_mappings: config.model_mappings ?? defaultOAuthModelMappings(config.platform), model_billing: modelBillingPayload(config.model_billing ?? defaultModelBillingConfig()), excel_bps: config.excel_bps ?? defaultExcelBPSDefaults() }; fallbackStatus.value = config.openai_apikey_fallback_status ?? null; selectedQualityRule.value = config.quality_rule ? 'saved' : ''; groups.value = available.filter(g => g.status === 'active') } }
 catch { if (validGeneration(v)) error.value = t('autoConfig.loadFailed') }
 finally { if (validGeneration(v)) loading.value = false }
}
async function refreshFallbackStatus() {
 const v = generation; fallbackStatusLoading.value = true; fallbackStatusFailed.value = false
 try { const config = await getAutoConfig(); if (validGeneration(v)) fallbackStatus.value = config.openai_apikey_fallback_status ?? null }
 catch { if (validGeneration(v)) fallbackStatusFailed.value = true }
 finally { if (validGeneration(v)) fallbackStatusLoading.value = false }
}
async function save() {
 if (!draft.value || saving.value) return
 const c = draft.value
 const fallbackError = openaiAPIKeyFallbackError(c.openai_apikey_fallback) ?? fallbackCard.value?.validationError()
 if (fallbackError) { error.value = t(fallbackError); return }
 if (oauthDegradationError(c.degradation_action)) { error.value = t('autoConfig.degradation.invalid'); return }
 const admissionError = openaiTwoFAAdmissionError(c.openai_two_fa_admission)
 if (admissionError || c.openai_two_fa_admission.enabled && !groups.value.some(g => g.id === c.openai_two_fa_admission.isolation_group_id && g.platform === 'openai' && g.status === 'active')) { error.value = t(admissionError ?? 'autoConfig.admission.invalid'); return }
 const modelError = modelBillingConfigError(c.model_billing)
 if (modelError) { error.value = t(modelError); return }
 const bpsError = excelBPSDefaultsError(c.excel_bps)
 if (bpsError) { error.value = t(bpsError); return }
 const mappingError = oauthModelMappingsError(c.model_mappings)
 if (mappingError) { error.value = t(mappingError); return }
 const shutdownThreshold = c.openai_disable_scheduling_on_429_7d_threshold_percent
 if (!Number.isFinite(shutdownThreshold) || shutdownThreshold < 0 || shutdownThreshold > 100) { error.value = t('autoConfig.disableScheduling429.invalidThreshold'); return }
 const costMultiplier = c.cost_multiplier ?? 0.07
 if (c.enabled && !c.group_ids.length || c.upgrade_enabled && !c.upgrade_group_ids.length || !Number.isFinite(costMultiplier) || costMultiplier < 0 || costMultiplier > 1000000 || [...initialFields, ...upgradeFields].some(f => !Number.isInteger(c[f.key]) || c[f.key] < ('min' in f ? f.min : 1) || c[f.key] > ('max' in f ? f.max : 10000))) { error.value = t('autoConfig.invalid'); return }
 const v = generation; saving.value = true; error.value = ''; notice.value = ''
 try { const { runtime_blocked: _blocked, openai_apikey_fallback_status: _fallbackStatus, ...payload } = c; const result = await saveAutoConfig({ ...payload, cost_multiplier: costMultiplier, openai_apikey_fallback: openaiAPIKeyFallbackPayload(c.openai_apikey_fallback), model_mappings: oauthModelMappingsPayload(c.model_mappings), model_billing: modelBillingPayload(c.model_billing), excel_bps: excelBPSDefaultsPayload(c.excel_bps) }); if (validGeneration(v)) { draft.value = { ...result, degradation_action: oauthDegradationPayload(result.degradation_action), openai_apikey_fallback: openaiAPIKeyFallbackPayload(result.openai_apikey_fallback), openai_ws_mode: result.openai_ws_mode ?? OPENAI_WS_MODE_CTX_POOL, openai_ws_sse_acceleration: result.openai_ws_sse_acceleration ?? false, openai_totp_rotate_on_429: result.openai_totp_rotate_on_429 ?? false, openai_disable_scheduling_on_429: result.openai_disable_scheduling_on_429 ?? false, openai_disable_scheduling_on_429_7d_threshold_percent: result.openai_disable_scheduling_on_429_7d_threshold_percent ?? DEFAULT_DISABLE_SCHEDULING_429_7D_THRESHOLD_PERCENT, openai_two_fa_admission: result.openai_two_fa_admission ?? defaultOpenAITwoFAAdmissionConfig(), cost_multiplier: result.cost_multiplier ?? 0.07, model_mappings: result.model_mappings ?? defaultOAuthModelMappings(result.platform), model_billing: modelBillingPayload(result.model_billing ?? defaultModelBillingConfig()), excel_bps: result.excel_bps ?? defaultExcelBPSDefaults() }; if (result.openai_apikey_fallback_status) fallbackStatus.value = result.openai_apikey_fallback_status; selectedQualityRule.value = result.quality_rule ? 'saved' : ''; notice.value = t('autoConfig.saved'); historyRefreshKey.value++ } }
 catch { if (validGeneration(v)) error.value = t('autoConfig.saveFailed') }
 finally { if (validGeneration(v)) saving.value = false }
}
watch(() => auth.user ? auth.user.id + ':' + auth.user.role : '', () => { generation++; draft.value = null; fallbackStatus.value = null; fallbackStatusLoading.value = fallbackStatusFailed.value = false; groups.value = []; modelBillingGroups.value = []; modelBillingGroupsFailed.value = false; modelBillingGroupsLoading.value = true; qualityPlans.value = []; selectedQualityRule.value = ''; qualityLoadFailed.value = false; error.value = notice.value = ''; loading.value = saving.value = false }, { flush: 'sync' })
onMounted(() => { void load(); void loadQualityPlans(); void loadModelBillingGroups() })
onBeforeUnmount(() => { alive = false; generation++ })
</script>
<style scoped>
.card { @apply min-w-0 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.field-label { @apply mb-2 block text-sm font-medium text-gray-600 dark:text-gray-300; }
.group-list { @apply max-h-48 space-y-2 overflow-y-auto rounded-xl border border-gray-200 p-3 dark:border-dark-700; }
.group-option { @apply flex items-center gap-2 text-sm; }
input[type=checkbox] { @apply h-4 w-4 shrink-0 rounded text-primary-600; }
</style>
