<template>
  <section v-if="auth.isAdmin" class="space-y-5" data-testid="ai-control">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <div class="flex flex-wrap items-center gap-2"><h2 class="text-xl font-semibold">{{ t('aiOpsControl.title') }}</h2><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.aiOpsCodex" :user-id="auth.user?.id" /></div>
        <p class="mt-2 max-w-4xl text-sm leading-6 text-gray-500">{{ t('aiOpsControl.description') }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary" :disabled="busy || halting || loading" data-testid="ai-control-refresh" @click="load">{{ t('aiOpsControl.refresh') }}</button>
        <button type="button" class="btn btn-danger" :disabled="halting" data-testid="ai-control-halt" @click="halt">{{ t(halting ? 'aiOpsControl.haltPending' : 'aiOpsControl.halt') }}</button>
      </div>
    </header>
    <p class="text-xs leading-5 text-gray-500">{{ t('aiOpsControl.haltHint') }}</p>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">{{ notice }}</p>
    <p v-if="loading && !draft" class="text-sm text-gray-500">{{ t('aiOpsControl.loading') }}</p>
    <p v-if="policy && !policy.control_enabled" class="rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300" data-testid="ai-control-disabled">{{ t('aiOpsControl.deploymentDisabled') }}</p>
    <p v-if="policy?.halted || haltRequested" role="status" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="ai-control-halted">{{ t('aiOpsControl.halted') }}</p>

    <details v-if="draft" class="card" :open="policy?.mode === 'disabled'">
      <summary class="cursor-pointer p-5 font-semibold">{{ t('aiOpsControl.policy') }} · {{ t('aiOpsControl.revision', { revision: policy?.revision }) }}</summary>
      <form class="space-y-5 border-t border-gray-100 p-5 dark:border-dark-700" @submit.prevent="savePolicy">
        <fieldset :disabled="busy || halting || loading || needsRefresh || !policy?.control_enabled" class="min-w-0 space-y-5">
          <div class="grid gap-4 sm:grid-cols-2">
            <label><span class="control-label">{{ t('aiOpsControl.mode') }}</span><select v-model="draft.mode" class="input w-full" data-testid="ai-policy-mode"><option v-for="mode in modes" :key="mode" :value="mode">{{ t(`aiOpsControl.modes.${mode}`) }}</option></select></label>
            <label><span class="control-label">{{ t('aiOpsControl.group') }}</span><input v-model.number="draft.group_id" type="number" min="1" step="1" class="input w-full" data-testid="ai-policy-group" /></label>
          </div>
          <p class="text-xs leading-5 text-gray-500">{{ t('aiOpsControl.scopeHint') }}</p>
          <fieldset class="space-y-3">
            <legend class="mb-2 font-medium">{{ t('aiOpsControl.whitelist') }}</legend>
            <div v-for="(account, index) in draft.accounts" :key="index" class="grid items-end gap-3 sm:grid-cols-[8rem_minmax(0,1fr)_auto]">
              <label><span class="control-label">{{ t('aiOpsControl.accountId') }}</span><input v-model.number="account.account_id" type="number" min="1" step="1" class="input w-full" :data-testid="`ai-policy-account-${index}`" /></label>
              <label><span class="control-label">{{ t('aiOpsControl.domain') }}</span><input v-model.trim="account.capacity_domain" type="text" maxlength="64" pattern="[a-zA-Z0-9][a-zA-Z0-9_.:\-]{0,63}" class="input w-full" :data-testid="`ai-policy-domain-${index}`" /></label>
              <button type="button" class="btn btn-secondary" @click="draft.accounts.splice(index, 1)">{{ t('aiOpsControl.remove') }}</button>
            </div>
            <p v-if="!draft.accounts.length" class="text-sm text-gray-500">{{ t('aiOpsControl.allowlistEmpty') }}</p>
            <p class="text-xs leading-5 text-gray-500">{{ t('aiOpsControl.domainHint') }}</p>
            <button type="button" class="btn btn-secondary" :disabled="draft.accounts.length >= 200" data-testid="ai-policy-add-account" @click="draft.accounts.push({ account_id: 0, capacity_domain: '' })">{{ t('aiOpsControl.addAccount') }}</button>
          </fieldset>
          <fieldset>
            <legend class="mb-3 font-medium">{{ t('aiOpsControl.thresholds') }}</legend>
            <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              <label v-for="field in numberFields" :key="field.key"><span class="control-label">{{ t(`aiOpsControl.fields.${field.key}`) }}</span><input v-model.number="draft[field.key]" class="input w-full" type="number" :min="field.min" :max="field.max" :step="field.step" :data-testid="`ai-policy-${field.key}`" /></label>
            </div>
            <p class="mt-3 text-xs leading-5 text-gray-500">{{ t('aiOpsControl.thresholdHint') }}</p>
          </fieldset>
          <fieldset class="space-y-2">
            <legend class="mb-2 font-medium">{{ t('aiOpsControl.autoActions') }}</legend>
            <div class="flex flex-wrap gap-4"><label v-for="action in autoActions" :key="action" class="flex items-center gap-2 text-sm"><input v-model="draft.auto_actions" :value="action" type="checkbox" :data-testid="`ai-policy-auto-${action}`" />{{ t(`aiOps.action.${action}`) }}</label></div>
            <p class="text-xs leading-5 text-gray-500">{{ t('aiOpsControl.autoHint') }}</p>
          </fieldset>
          <div class="space-y-2 rounded-lg bg-gray-50 p-4 dark:bg-dark-800">
            <label class="flex items-start gap-2 text-sm"><input v-model="draft.halted" type="checkbox" class="mt-0.5" data-testid="ai-policy-halted" /><span>{{ t('aiOpsControl.releaseHalt') }}</span></label>
            <p class="text-xs leading-5 text-gray-500">{{ t('aiOpsControl.releaseHint') }}</p>
          </div>
          <p v-if="dirty" class="text-sm text-amber-700 dark:text-amber-300" data-testid="ai-policy-dirty">{{ t('aiOpsControl.dirty') }}</p>
          <p v-if="!validPolicy" class="text-sm text-red-600">{{ t('aiOpsControl.invalid') }}</p>
          <label v-if="draft.mode !== 'disabled' || (policy?.halted && !draft.halted)" class="flex items-start gap-2 text-sm"><input v-model="policyConsent" type="checkbox" class="mt-0.5" data-testid="ai-policy-consent" /><span>{{ t('aiOpsControl.policyConsent') }}</span></label>
          <label v-if="draft.mode === 'bounded_auto'" class="flex items-start gap-2 text-sm"><input v-model="autoConsent" type="checkbox" class="mt-0.5" data-testid="ai-policy-auto-consent" /><span>{{ t('aiOpsControl.autoConsent') }}</span></label>
          <button type="submit" class="btn btn-primary" :disabled="!canSave" data-testid="ai-policy-save">{{ t('aiOpsControl.save') }}</button>
        </fieldset>
      </form>
    </details>

    <section v-if="policy" class="card space-y-4 p-5">
      <h3 class="font-semibold">{{ t('aiOpsControl.preview') }}</h3>
      <p class="text-sm leading-6 text-gray-500">{{ t('aiOpsControl.chooseHint') }}</p>
      <p v-if="!recommendations.length" class="text-sm text-gray-500">{{ t('aiOpsControl.noRecommendations') }}</p>
      <div v-for="item in recommendations" :key="item.index" class="flex flex-wrap items-start justify-between gap-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
        <div class="min-w-0 flex-1"><strong class="text-sm">{{ t(`aiOps.action.${item.proposal.action}`) }} · {{ t('aiOpsControl.account', { id: item.proposal.account_id }) }}</strong><p class="mt-2 whitespace-pre-wrap break-words text-sm leading-6">{{ item.proposal.reason }}</p></div>
        <button type="button" class="btn btn-secondary" :disabled="!canPreview" :data-testid="`ai-control-preview-${item.index}`" @click="preparePreview(item.index)">{{ t('aiOpsControl.previewButton') }}</button>
      </div>
      <article v-if="frozen" class="space-y-4 rounded-xl border border-primary-200 p-4 dark:border-primary-900" data-testid="ai-control-frozen">
        <h4 class="font-semibold">{{ t('aiOpsControl.previewTitle') }} · {{ t(`aiOps.action.${frozen.action}`) }} · {{ t('aiOpsControl.account', { id: frozen.account_id }) }}</h4>
        <div class="grid gap-3 text-sm sm:grid-cols-2">
          <p><strong>{{ t('aiOpsControl.before') }}：</strong>{{ changeValue(frozen.action, frozen.before_value) }}</p><p><strong>{{ t('aiOpsControl.after') }}：</strong>{{ frozenAfterValue }}</p>
          <p><strong>{{ t('aiOpsControl.impact') }}：</strong>{{ frozen.impact_group_ids.map(id => `#${id}`).join(', ') || '—' }}</p><p>{{ t('aiOpsControl.targetRevision', { revision: frozen.target_revision }) }}</p>
          <p>{{ t('aiOpsControl.revision', { revision: frozen.policy_revision }) }}</p><p>{{ t('aiOpsControl.expires') }}：{{ time(frozen.expires_at) }}</p>
          <p class="sm:col-span-2">{{ t('aiOpsControl.observedAt') }}：{{ time(frozen.evidence.observed_at) }}</p><p class="sm:col-span-2">{{ t('aiOpsControl.window') }}：{{ time(frozen.evidence.window_start) }} → {{ time(frozen.evidence.window_end) }}</p>
          <p>{{ t('aiOpsControl.samples') }}：{{ frozen.evidence.samples }}</p><p>{{ t('aiOpsControl.errorRate') }}：{{ percent(frozen.evidence.error_rate) }}</p>
          <p>{{ t('aiOpsControl.inflight') }}：{{ frozen.evidence.inflight >= 0 ? frozen.evidence.inflight : t('aiOpsControl.unknown') }}</p><p>{{ t('aiOpsControl.eligible') }}：{{ frozen.evidence.eligible_accounts }}</p>
          <p v-if="frozen.evidence.model" class="break-words">{{ t('aiOpsControl.probes.model') }}：{{ frozen.evidence.model }}</p><p v-if="frozen.evidence.effort">{{ t('aiOpsControl.effort') }}：{{ frozen.evidence.effort }}</p><p v-if="frozen.evidence.protocol">{{ t('aiOpsControl.protocol') }}：{{ frozen.evidence.protocol }}</p>
          <p>{{ t('aiOpsControl.totalConcurrency') }}：{{ frozen.evidence.total_concurrency }}</p><p>{{ t('aiOpsControl.coverage') }}：{{ t(frozen.evidence.model_coverage_complete ? 'aiOpsControl.complete' : 'aiOpsControl.incomplete') }}</p>
          <p v-if="frozen.action === 'resume_account'">{{ t('aiOpsControl.probes.title') }}：{{ t(frozen.evidence.probe_passed ? 'aiOpsControl.probes.status.passed' : 'aiOpsControl.probes.status.unknown') }}</p>
        </div>
        <p class="whitespace-pre-wrap break-words text-sm"><strong>{{ t('aiOpsControl.reason') }}：</strong>{{ frozenReason }}</p>
        <p class="break-words text-xs text-gray-500">{{ t('aiOpsControl.evidenceRefs') }}：{{ frozenEvidenceIds.join(', ') || '—' }}</p>
        <details><summary class="cursor-pointer text-xs text-gray-500">{{ t('aiOpsControl.digest') }}</summary><code class="mt-2 block break-all text-xs">{{ frozen.digest }}</code></details>
        <div v-if="frozen.status !== 'ready' || frozen.blocked_reasons.length" role="alert" class="space-y-1 text-sm text-amber-700 dark:text-amber-300" data-testid="ai-control-blocked"><strong>{{ t('aiOpsControl.blocked') }}</strong><p v-for="reason in frozen.blocked_reasons" :key="reason">{{ reasonLabel(reason) }}</p><p v-if="!frozen.blocked_reasons.length">{{ frozen.status }}</p></div>
        <p v-if="previewExpired" role="alert" class="text-sm text-amber-700 dark:text-amber-300">{{ t('aiOpsControl.expired') }}</p>
        <label class="flex items-start gap-2 text-sm"><input v-model="applyConsent" type="checkbox" class="mt-0.5" :disabled="!previewReady || busy || halting" data-testid="ai-control-apply-consent" /><span>{{ t('aiOpsControl.applyConsent') }}</span></label>
        <button type="button" class="btn btn-primary" :disabled="!canApply" data-testid="ai-control-apply" @click="apply">{{ t('aiOpsControl.apply') }}</button>
      </article>
    </section>

    <AIOpsRecoveryProbes v-if="policy" :policy="policy" :disabled="dirty || busy || halting || needsRefresh" />

    <section class="card space-y-4 p-5">
      <h3 class="font-semibold">{{ t('aiOpsControl.history') }}</h3>
      <p class="text-xs text-gray-500">{{ t('aiOpsControl.count', { shown: actions.length, total }) }}</p>
      <p v-if="!actions.length" class="text-sm text-gray-500">{{ t('aiOpsControl.empty') }}</p>
      <article v-for="action in actions" :key="action.id" class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700" data-testid="ai-control-action">
        <div class="flex flex-wrap items-center justify-between gap-2"><strong class="text-sm">{{ t(`aiOps.action.${action.action}`) }} · {{ t('aiOpsControl.account', { id: action.account_id }) }}</strong><span class="rounded bg-gray-100 px-2 py-1 text-xs dark:bg-dark-800">{{ t(`aiOpsControl.status.${action.status}`) }}</span></div>
        <p class="text-sm">{{ t(action.auto ? 'aiOpsControl.automatic' : 'aiOpsControl.manual') }} · {{ changeValue(action.action, action.before_value) }} → {{ changeValue(action.action, action.after_value) }}</p>
        <dl class="grid gap-2 text-xs text-gray-500 sm:grid-cols-2"><div><dt>{{ t('aiOpsControl.created') }}</dt><dd>{{ time(action.created_at) }}</dd></div><div><dt>{{ t('aiOpsControl.updated') }}</dt><dd>{{ time(action.updated_at) }}</dd></div><div v-if="action.observe_until"><dt>{{ t('aiOpsControl.observeUntil') }}</dt><dd>{{ time(action.observe_until) }}</dd></div><div v-if="action.reason_code"><dt>{{ t('aiOpsControl.reasonCode') }}</dt><dd class="break-words">{{ reasonLabel(action.reason_code) }}</dd></div></dl>
        <p class="break-all font-mono text-xs text-gray-400">{{ action.id }}</p>
        <button v-if="rollbackCandidate(action)" type="button" class="btn btn-secondary btn-sm" :disabled="busy || halting || loading || needsRefresh || !policy?.control_enabled" :data-testid="`ai-control-rollback-${action.id}`" @click="rollbackTarget = action; rollbackConsent = false">{{ t('aiOpsControl.rollback') }}</button>
      </article>
    </section>

    <BaseDialog :show="!!rollbackTarget" :title="t('aiOpsControl.rollbackTitle')" width="normal" @close="closeRollback">
      <div v-if="rollbackTarget" class="space-y-4">
        <p class="text-sm leading-6">{{ t('aiOpsControl.rollbackHint') }}</p>
        <p class="text-sm">{{ t('aiOpsControl.account', { id: rollbackTarget.account_id }) }} · {{ t(`aiOps.action.${rollbackTarget.action}`) }} · {{ changeValue(rollbackTarget.action, rollbackTarget.after_value) }} → {{ changeValue(rollbackTarget.action, rollbackTarget.before_value) }}</p>
        <p class="break-all font-mono text-xs text-gray-500">{{ rollbackTarget.id }}</p>
        <label class="flex items-start gap-2 text-sm"><input v-model="rollbackConsent" type="checkbox" class="mt-0.5" :disabled="busy || halting" data-testid="ai-control-rollback-consent" /><span>{{ t('aiOpsControl.rollbackConsent') }}</span></label>
      </div>
      <template #footer><div class="flex justify-end gap-3"><button type="button" class="btn btn-secondary" :disabled="busy" @click="closeRollback">{{ t('aiOpsControl.cancel') }}</button><button type="button" class="btn btn-danger" :disabled="!rollbackConsent || busy || halting || needsRefresh" data-testid="ai-control-rollback-confirm" @click="rollback">{{ t('aiOpsControl.rollbackSubmit') }}</button></div></template>
    </BaseDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { aiOpsControlAPI, type AIOpsActionRecord, type AIOpsAutoAction, type AIOpsFrozenProposal, type AIOpsPolicy } from '@/api/admin/aiOpsControl'
import type { AIOpsRun } from '@/api/admin/aiOps'
import BaseDialog from '@/components/common/BaseDialog.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import AIOpsRecoveryProbes from './AIOpsRecoveryProbes.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

const props = defineProps<{ run: AIOpsRun | null }>()
const { t, te, locale } = useI18n()
const auth = useAuthStore()
const policy = ref<AIOpsPolicy | null>(null)
const draft = ref<AIOpsPolicy | null>(null)
const actions = ref<AIOpsActionRecord[]>([])
const total = ref(0)
const frozen = ref<AIOpsFrozenProposal | null>(null)
const frozenReason = ref('')
const frozenEvidenceIds = ref<string[]>([])
const rollbackTarget = ref<AIOpsActionRecord | null>(null)
const policyConsent = ref(false), autoConsent = ref(false), applyConsent = ref(false), rollbackConsent = ref(false)
const loading = ref(false), busy = ref(false), halting = ref(false), haltRequested = ref(false), needsRefresh = ref(false)
let previewPending = false
const error = ref(''), notice = ref(''), clock = ref(Date.now())
const modes = ['disabled', 'approve_each', 'bounded_auto'] as const
const autoActions: AIOpsAutoAction[] = ['set_concurrency', 'pause_account', 'resume_account']
type NumberField = keyof Omit<AIOpsPolicy, 'mode' | 'halted' | 'revision' | 'group_id' | 'accounts' | 'auto_actions' | 'control_enabled'>
const numberFields: { key: NumberField; min: number; max?: number; step: number }[] = [
  { key: 'min_healthy_accounts', min: 1, max: 200, step: 1 }, { key: 'min_total_concurrency', min: 1, max: 2000000, step: 1 },
  { key: 'min_concurrency', min: 1, max: 10000, step: 1 }, { key: 'max_concurrency', min: 1, max: 10000, step: 1 },
  { key: 'max_step_percent', min: 1, max: 25, step: 0.1 }, { key: 'daily_action_limit', min: 1, max: 100, step: 1 },
  { key: 'cooldown_seconds', min: 60, max: 86400, step: 1 }, { key: 'observation_seconds', min: 60, max: 3600, step: 1 },
  { key: 'evidence_max_age_seconds', min: 30, max: 600, step: 1 }, { key: 'min_samples', min: 10, max: 100000, step: 1 },
  { key: 'error_rate_threshold', min: 0.01, max: 1, step: 0.01 }, { key: 'rollback_error_rate_delta', min: 0, max: 1, step: 0.01 },
  { key: 'max_pause_seconds', min: 60, max: 86400, step: 1 },
]
const dirty = computed(() => !!draft.value && JSON.stringify(draft.value) !== JSON.stringify(policy.value))
const validPolicy = computed(() => {
  const value = draft.value
  if (!value || !numberFields.every(field => typeof value[field.key] === 'number' && Number.isFinite(value[field.key]) && value[field.key] >= field.min && (field.max == null || value[field.key] <= field.max) && (field.step !== 1 || Number.isInteger(value[field.key])))) return false
  if (value.min_concurrency > value.max_concurrency) return false
  if (value.mode === 'disabled') return true
  return Number.isSafeInteger(value.group_id) && value.group_id > 0 && value.accounts.length > 0 && value.accounts.length <= 200 && new Set(value.accounts.map(item => item.account_id)).size === value.accounts.length && value.accounts.every(item => Number.isSafeInteger(item.account_id) && item.account_id > 0 && /^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,63}$/.test(item.capacity_domain)) && (value.mode !== 'bounded_auto' || value.auto_actions.length > 0)
})
const canSave = computed(() => auth.isAdmin && policy.value?.control_enabled && !!draft.value && !busy.value && !halting.value && !loading.value && !needsRefresh.value && dirty.value && validPolicy.value && ((draft.value.mode === 'disabled' && !(policy.value?.halted && !draft.value.halted)) || policyConsent.value) && (draft.value.mode !== 'bounded_auto' || autoConsent.value))
const recommendations = computed(() => props.run?.status === 'completed' ? (props.run.result?.proposals ?? []).map((proposal, index) => ({ proposal, index })).filter(item => item.proposal.action !== 'none' && item.proposal.account_id != null) : [])
const canPreview = computed(() => auth.isAdmin && policy.value?.control_enabled && policy.value.mode !== 'disabled' && !policy.value.halted && !haltRequested.value && !busy.value && !halting.value && !loading.value && !dirty.value && !needsRefresh.value)
const previewExpired = computed(() => !!frozen.value && (!Number.isFinite(Date.parse(frozen.value.expires_at)) || Date.parse(frozen.value.expires_at) <= clock.value))
const previewReady = computed(() => !!frozen.value && frozen.value.status === 'ready' && !frozen.value.blocked_reasons.length && frozen.value.policy_revision === policy.value?.revision && frozen.value.run_id === props.run?.id && !!frozen.value.digest && !previewExpired.value)
const frozenAfterValue = computed(() => frozen.value ? changeValue(frozen.value.action, frozen.value.action === 'pause_account' ? 1 : frozen.value.action === 'resume_account' ? 0 : frozen.value.proposed_value) : '—')
const canApply = computed(() => canPreview.value && previewReady.value && applyConsent.value)

let alive = true, readGeneration = 0, previewGeneration = 0, mutationGeneration = 0
let readController: AbortController | undefined, previewController: AbortController | undefined, mutationController: AbortController | undefined, haltController: AbortController | undefined
let expiryTimer: ReturnType<typeof setInterval> | undefined
const clonePolicy = (value: AIOpsPolicy): AIOpsPolicy => JSON.parse(JSON.stringify(value)) as AIOpsPolicy

function invalidatePreview(): void {
  if (previewPending) { busy.value = false; previewPending = false }
  previewGeneration++
  previewController?.abort()
  frozen.value = null
  frozenReason.value = ''
  frozenEvidenceIds.value = []
  applyConsent.value = false
}
watch(() => props.run?.id, invalidatePreview)
watch(draft, () => { policyConsent.value = false; autoConsent.value = false; invalidatePreview() }, { deep: true, flush: 'sync' })

function time(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale.value, { timeZoneName: 'short' }) : '—'
}
function percent(value: number): string { return Number.isFinite(value) ? `${(value * 100).toFixed(1)}%` : '—' }
function changeValue(action: string, value: number | null): string {
  if ((action === 'pause_account' || action === 'resume_account') && (value === 0 || value === 1)) return t(value === 1 ? 'aiOpsControl.aiPauseSet' : 'aiOpsControl.aiPauseClear')
  return value == null ? '—' : String(value)
}
function reasonLabel(code: string): string { return te(`aiOpsControl.reasons.${code}`) ? t(`aiOpsControl.reasons.${code}`) : code }
function rollbackCandidate(action: AIOpsActionRecord): boolean { return ['propagation_pending', 'observing', 'succeeded', 'inconclusive'].includes(action.status) && action.owner_revision > 0 }
function acceptPolicy(value: AIOpsPolicy): void {
  policy.value = clonePolicy(value)
  draft.value = clonePolicy(value)
  haltRequested.value = value.halted
  policyConsent.value = false
  autoConsent.value = false
}
function operationError(value: unknown): void {
  const response = value as { status?: number; code?: unknown; reason?: unknown }
  const code = typeof response?.reason === 'string' ? response.reason : typeof response?.code === 'string' ? response.code : ''
  error.value = t(response?.status === 409 ? 'aiOpsControl.conflict' : 'aiOpsControl.operationFailed') + (/^[A-Z][A-Z0-9_]{0,100}$/.test(code) ? ` (${code})` : '')
  needsRefresh.value = true
  invalidatePreview()
  closeRollback()
}

async function load(): Promise<void> {
  if (!auth.isAdmin || busy.value || halting.value) return
  readController?.abort()
  const controller = new AbortController(), generation = ++readGeneration
  readController = controller
  loading.value = true
  error.value = ''
  invalidatePreview()
  closeRollback()
  try {
    const [nextPolicy, history] = await Promise.all([aiOpsControlAPI.policy(controller.signal), aiOpsControlAPI.actions(controller.signal)])
    if (!alive || controller.signal.aborted || generation !== readGeneration) return
    acceptPolicy(nextPolicy)
    actions.value = history.items
    total.value = history.total
    needsRefresh.value = false
  } catch (value) {
    if (alive && !controller.signal.aborted && generation === readGeneration) {
      error.value = t((value as { status?: number })?.status === 404 ? 'aiOpsControl.unavailable' : 'aiOpsControl.loadFailed')
      needsRefresh.value = true
    }
  } finally { if (alive && generation === readGeneration) loading.value = false }
}

async function savePolicy(): Promise<void> {
  if (!canSave.value || !draft.value) return
  const value = clonePolicy(draft.value), generation = ++mutationGeneration
  mutationController = new AbortController()
  busy.value = true; error.value = ''; notice.value = ''
  invalidatePreview()
  try {
    const saved = await aiOpsControlAPI.savePolicy(value, mutationController.signal)
    if (!alive || generation !== mutationGeneration) return
    acceptPolicy(saved)
    needsRefresh.value = false
    notice.value = t('aiOpsControl.saved')
  } catch (value) { if (alive && generation === mutationGeneration) operationError(value) }
  finally { if (alive && generation === mutationGeneration) busy.value = false }
}

async function preparePreview(index: number): Promise<void> {
  if (!canPreview.value || !props.run) return
  const run = props.run, original = run.result?.proposals[index]
  if (!original || original.action === 'none' || original.account_id == null) return
  invalidatePreview()
  const controller = new AbortController(), generation = ++previewGeneration
  previewController = controller
  previewPending = true
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const proposal = await aiOpsControlAPI.preview(run.id, index, controller.signal)
    if (!alive || controller.signal.aborted || generation !== previewGeneration || props.run?.id !== run.id) return
    frozen.value = proposal
    frozenReason.value = original.reason
    frozenEvidenceIds.value = [...original.evidence_ids]
    clock.value = Date.now()
  } catch (value) { if (alive && !controller.signal.aborted && generation === previewGeneration) operationError(value) }
  finally { if (alive && generation === previewGeneration) { busy.value = false; previewPending = false } }
}

async function apply(): Promise<void> {
  clock.value = Date.now()
  if (!canApply.value || !frozen.value) return
  const proposal = frozen.value, generation = ++mutationGeneration
  mutationController = new AbortController()
  busy.value = true; error.value = ''; notice.value = ''; applyConsent.value = false
  try {
    const action = await aiOpsControlAPI.apply(proposal.id, proposal.digest, mutationController.signal)
    if (!alive || generation !== mutationGeneration) return
    rememberAction(action)
    invalidatePreview()
    notice.value = action.status === 'conflict' ? t('aiOpsControl.conflict') : t('aiOpsControl.submitted')
  } catch (value) { if (alive && generation === mutationGeneration) operationError(value) }
  finally { if (alive && generation === mutationGeneration) busy.value = false }
}

function rememberAction(action: AIOpsActionRecord): void {
  if (!actions.value.some(item => item.id === action.id)) total.value++
  actions.value = [action, ...actions.value.filter(item => item.id !== action.id)]
}
function closeRollback(): void { rollbackTarget.value = null; rollbackConsent.value = false }
async function rollback(): Promise<void> {
  if (!auth.isAdmin || !policy.value?.control_enabled || !rollbackTarget.value || !rollbackConsent.value || busy.value || halting.value || needsRefresh.value) return
  const action = rollbackTarget.value, generation = ++mutationGeneration
  mutationController = new AbortController()
  busy.value = true; error.value = ''; notice.value = ''; rollbackConsent.value = false
  try {
    const rolledBack = await aiOpsControlAPI.rollback(action.id, action.owner_revision, mutationController.signal)
    if (!alive || generation !== mutationGeneration) return
    rememberAction(rolledBack)
    closeRollback()
    invalidatePreview()
    if (rolledBack.status === 'conflict') { error.value = t('aiOpsControl.conflict'); needsRefresh.value = true }
    else notice.value = t('aiOpsControl.rollbackDone')
  } catch (value) { if (alive && generation === mutationGeneration) operationError(value) }
  finally { if (alive && generation === mutationGeneration) busy.value = false }
}

async function halt(): Promise<void> {
  if (!auth.isAdmin || halting.value) return
  halting.value = true; haltRequested.value = true; error.value = ''; notice.value = ''
  mutationGeneration++; readGeneration++
  mutationController?.abort(); readController?.abort()
  busy.value = false; loading.value = false
  invalidatePreview(); closeRollback()
  haltController = new AbortController()
  try {
    const halted = await aiOpsControlAPI.halt(haltController.signal)
    if (!alive) return
    acceptPolicy(halted)
    needsRefresh.value = false
  } catch (value) { if (alive) operationError(value) }
  finally { if (alive) halting.value = false }
}

onMounted(() => {
  if (auth.isAdmin) {
    void load()
    expiryTimer = setInterval(() => { clock.value = Date.now() }, 1000)
  }
})
onBeforeUnmount(() => {
  alive = false; readGeneration++; previewGeneration++; mutationGeneration++
  if (expiryTimer != null) clearInterval(expiryTimer)
  readController?.abort(); previewController?.abort(); mutationController?.abort(); haltController?.abort()
})
</script>

<style scoped>
.control-label { @apply mb-1 block text-xs font-medium leading-5 text-gray-600 dark:text-gray-400; }
</style>
