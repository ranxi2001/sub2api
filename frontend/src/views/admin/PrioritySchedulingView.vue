<template>
  <AppLayout>
    <div ref="readingRoot" class="min-w-0 space-y-5">
      <header class="flex flex-wrap items-center gap-3"><h1 class="text-2xl font-semibold">{{ t('priorityScheduling.title') }}</h1><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.priorityAccountPerformance" :user-id="auth.user?.id" /><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.priorityCacheWeight" :user-id="auth.user?.id" /></header>
      <SmartOpsNav />
      <div v-if="error" role="alert" class="text-red-600">{{ error }} <button v-if="!draft" class="underline" @click="load">{{ t('priorityScheduling.retry') }}</button></div>
      <p v-if="notice" role="status" class="text-emerald-600">{{ notice }}</p>
      <p v-if="loading">{{ t('priorityScheduling.loading') }}</p>
      <details v-if="draft" class="card">
        <summary class="cursor-pointer px-5 py-4 font-semibold">{{ t('priorityScheduling.reading.configuration') }}</summary>
      <form class="p-5 pt-0" @submit.prevent="save">
        <fieldset :disabled="saving" class="min-w-0 space-y-5">
          <label class="flex items-center gap-3 font-medium"><input v-model="draft.enabled" data-testid="enabled" type="checkbox" role="switch" />{{ t('priorityScheduling.enabled') }}</label>
          <label class="block text-sm"><span class="flex items-center gap-3"><input v-model="draft.balance_protocols" data-testid="balance-protocols" type="checkbox" />{{ t('priorityScheduling.balanceProtocols') }}</span><small class="mt-1 block text-gray-500">{{ t('priorityScheduling.balanceProtocolsHint') }}</small></label>
          <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
            <label class="flex items-center gap-3 font-medium"><input v-model="draft.oauth_quota_priority" data-testid="oauth-quota-priority" type="checkbox" role="switch" />{{ t('priorityScheduling.oauthQuotaPriority') }}</label>
            <p class="mt-2 text-sm leading-6 text-gray-500">{{ t('priorityScheduling.oauthQuotaHint') }}</p>
            <label v-if="draft.oauth_quota_priority" class="mt-3 block max-w-xs"><span class="field-title">{{ t('priorityScheduling.oauthQuotaThreshold') }}</span><input v-model.number="draft.oauth_quota_threshold" data-testid="oauth-quota-threshold" type="number" min="1" max="100" step="1" required class="input w-full" /></label>
          </section>
          <div class="grid gap-5 lg:grid-cols-2">
            <section class="min-w-0 space-y-4">
              <label class="block"><span class="field-title">{{ t('priorityScheduling.strategy') }}</span><select v-model="draft.mode" class="input w-full" data-testid="mode"><option v-for="mode in modes" :key="mode" :value="mode">{{ t(`priorityScheduling.modes.${mode}`) }}</option></select></label>
              <div class="grid grid-cols-2 gap-3"><label v-for="(weight, index) in weightKeys" :key="weight"><span class="field-title">{{ t(`priorityScheduling.${weight}`) }}</span><input v-if="draft.mode === 'custom'" v-model.number="draft[weight]" type="number" min="0" max="100" step="0.1" required class="input w-full" /><div v-else class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ presets[draft.mode][index] }}</div></label></div>
              <label class="block">
                <span class="field-title">{{ t('priorityScheduling.cache_weight') }} <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.priorityCacheWeight" :user-id="auth.user?.id" compact /></span>
                <input v-model.number="draft.cache_weight" data-testid="cache-weight" type="number" min="0" max="100" step="0.1" required class="input w-full" />
              </label>
              <p data-testid="cache-weight-hint" class="text-xs leading-5 text-gray-500">{{ t('priorityScheduling.cacheWeightHint') }}</p>
              <p class="text-sm leading-6 text-gray-500">{{ t('priorityScheduling.rule') }}</p>
              <p class="text-xs leading-5 text-gray-500">{{ t('priorityScheduling.costHint') }}</p>
              <label><span class="field-title">{{ t('priorityScheduling.error_rate_weight') }}</span><input v-model.number="draft.error_rate_weight" data-testid="error-rate-weight" type="number" min="0" max="100" step="1" required class="input w-full" /></label>
              <p class="text-xs leading-5 text-gray-500">{{ t('priorityScheduling.errorRateHint') }}</p>
            </section>
            <section>
              <label class="mb-3 block"><span class="field-title">{{ t('priorityScheduling.latencyPenaltyMode') }}</span><select v-model="draft.latency_penalty_mode" data-testid="latency-penalty-mode" class="input w-full"><option value="deprioritize">{{ t('priorityScheduling.latencyPenaltyModes.deprioritize') }}</option><option value="stop">{{ t('priorityScheduling.latencyPenaltyModes.stop') }}</option></select><small class="mt-2 block text-gray-500">{{ t('priorityScheduling.latencyPenaltyModeHint') }}</small></label>

              <h2 class="mb-3 font-medium">{{ t('priorityScheduling.thresholds') }}</h2><div class="grid gap-3 sm:grid-cols-2"><label v-for="field in fields" :key="field.key"><span class="field-title">{{ t(`priorityScheduling.${field.key}`) }} <NewFeatureBadge v-if="field.key === 'target_tps'" :notice-id="NEW_FEATURE_NOTICES.priorityAccountPerformance" :user-id="auth.user?.id" compact /></span><input v-model.number="draft[field.key]" type="number" :data-testid="field.key" :min="field.min" :max="field.max" step="1" required class="input w-full" /></label></div></section>
          </div>
          <div class="grid gap-5 sm:grid-cols-2"><label><span class="field-title">{{ t('priorityScheduling.groupIds') }}</span><input v-model="groups" class="input w-full" data-testid="groups" /><small class="text-gray-500">{{ t('priorityScheduling.groupHint') }}</small></label><label><span class="field-title">{{ t('priorityScheduling.models') }}</span><textarea v-model="modelsText" class="input w-full" rows="3" /><small class="text-gray-500">{{ t('priorityScheduling.modelHint') }}</small></label></div>
          <button class="btn btn-primary" type="submit">{{ t(saving ? 'priorityScheduling.saving' : 'priorityScheduling.save') }}</button>
        </fieldset>
      </form>
      </details>
      <section class="card grid gap-3 p-4 sm:grid-cols-2 lg:grid-cols-4" data-testid="priority-filters">
        <label class="min-w-0"><span class="field-title">{{ t('priorityScheduling.model') }}</span><Select v-model="selectedModel" data-testid="priority-model-filter" :disabled="!initialized" :options="modelOptions" :searchable="true" :creatable="true" :aria-label="t('priorityScheduling.model')" /></label>
        <label class="min-w-0"><span class="field-title">{{ t('priorityScheduling.group') }}</span><Select v-model="selectedGroup" data-testid="priority-group-filter" :disabled="!initialized" :options="groupOptions" :searchable="true" :aria-label="t('priorityScheduling.group')" /></label>
        <label><span class="field-title">{{ t('priorityScheduling.window_minutes') }}</span><select id="priority-profit-window" v-model.number="financeMinutes" :disabled="!initialized" class="input w-full" data-testid="latency-window"><option :value="15">15m</option><option :value="60">60m</option><option :value="180">180m</option></select></label>
        <div class="flex items-end"><button class="btn btn-secondary w-full" :disabled="refreshing || catalogLoading" data-testid="latency-refresh" @click="refresh">{{ t('priorityScheduling.refresh') }}</button></div>
        <p v-if="catalogError" class="text-sm text-red-600 sm:col-span-2 lg:col-span-4" role="alert">{{ t('priorityScheduling.filters.catalogError') }}</p>
      </section>
      <section class="card p-5">
        <header class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('priorityScheduling.filters.candidates') }}</h2><div class="flex flex-wrap items-center gap-2"><button class="btn btn-secondary" :disabled="recomputeLoading" data-testid="recompute" @click="recompute">{{ t(recomputeLoading ? 'priorityScheduling.recompute.running' : 'priorityScheduling.filters.recomputeLatest') }}</button><button class="btn btn-secondary" :disabled="refreshing" @click="refresh">{{ t('priorityScheduling.refresh') }}</button></div></header>
        <p v-if="recomputeError" role="alert" class="mt-3 text-red-600">{{ recomputeError }}</p>
        <details class="my-3 text-xs text-gray-500"><summary class="cursor-pointer">{{ t('priorityScheduling.reading.diagnostics') }}</summary><p class="mt-2 leading-5">{{ t('priorityScheduling.filters.analysisHint') }}</p></details>
        <p v-if="!snapshot" data-testid="snapshot-initial-state" :role="snapshotError ? 'alert' : 'status'" class="py-8 text-center text-sm text-gray-500">{{ t(snapshotError ? 'priorityScheduling.error' : refreshing ? 'priorityScheduling.loading' : 'priorityScheduling.filters.choose') }}</p>
        <template v-else>
          <p v-if="isHistoricalAnalysis" data-testid="priority-analysis-notice" class="mb-3 text-sm text-gray-500">{{ t('priorityScheduling.analysis.notice') }}</p>
          <p data-testid="priority-snapshot-at" class="mb-4 text-sm text-gray-500">{{ new Date(snapshot.at).toLocaleString() }} · {{ t('priorityScheduling.model') }}: {{ snapshot.model }} · {{ t('priorityScheduling.group') }}: {{ snapshot.group_id ?? '—' }} · {{ t(`priorityScheduling.modes.${snapshot.mode}`) }} · {{ t('priorityScheduling.latencyPenaltyMode') }}: {{ t(`priorityScheduling.latencyPenaltyModes.${snapshot.latency_penalty_mode || 'deprioritize'}`) }}</p>
          <p v-if="snapshot.history_status && snapshot.history_status !== 'ready'" role="status" class="text-sm text-amber-600">{{ t(`priorityScheduling.historyStates.${snapshot.history_status}`) }}</p>
          <p v-else-if="!snapshot.history_ready" class="text-sm text-amber-600">{{ t('priorityScheduling.historyPending') }}</p>
          <p v-if="snapshot.history_error && snapshot.history_ready" class="text-sm text-amber-600">{{ t('priorityScheduling.historyRetry') }}</p>
          <p v-if="snapshot.evaluated_at && snapshot.evaluated_at !== snapshot.at" data-testid="priority-evaluated-at" class="mb-3 text-xs text-gray-500">{{ t('priorityScheduling.evaluatedAt') }}: {{ new Date(snapshot.evaluated_at).toLocaleString() }}<template v-if="snapshot.history_observed_at"> · {{ t('priorityScheduling.historyObservedAt') }}: {{ new Date(snapshot.history_observed_at).toLocaleString() }}</template></p>
          <p v-if="snapshot.latency_fallback" role="status" class="mb-3 text-sm text-amber-600" data-testid="priority-latency-fallback">{{ t('priorityScheduling.latencyFallback') }}</p>
          <p class="mb-3 text-xs text-gray-500">{{ t('priorityScheduling.performanceSource') }} · {{ t('priorityScheduling.window_minutes') }}: {{ snapshot.window_minutes ?? '—' }} · {{ t('priorityScheduling.configRevision') }}: {{ snapshot.config_revision ?? '—' }}<template v-if="snapshot.history_window_end"> · {{ t('priorityScheduling.windowEnd') }}: {{ new Date(snapshot.history_window_end).toLocaleString() }}</template></p>
          <p data-testid="candidate-status" :role="snapshotError ? 'alert' : 'status'" class="reading-status mb-3 text-xs" :class="snapshotError ? 'text-red-600' : 'text-gray-500'">{{ t(snapshotError ? 'priorityScheduling.reading.stale' : refreshing ? 'priorityScheduling.reading.refreshing' : 'priorityScheduling.filters.updated') }}</p>
          <div class="mb-3 flex flex-wrap items-center justify-between gap-3"><span class="text-sm font-medium">{{ t('priorityScheduling.account') }} ({{ candidateRows.length }})</span><label class="flex items-center gap-2 text-sm sm:hidden">{{ t('priorityScheduling.reading.pageSize') }}<select v-model.number="candidatePageSize" class="input" data-testid="candidate-mobile-size"><option v-for="size in pageSizeOptions" :key="size" :value="size">{{ size }}</option></select></label></div>
          <div v-if="candidateRows.length" data-testid="priority-candidate-scroll" class="overflow-x-auto">
          <DataTable :key="displayScope(snapshot)" data-testid="candidate-table" data-reading-scroll="candidates" class="reading-table candidate-table" :columns="candidateColumns" :data="candidatePageRows" row-key="account_id">
            <template #cell-account="{ row }"><div class="account-cell" :data-reading-account="row.account_id"><strong class="font-medium" :title="row.account_name">{{ row.account_name }}</strong><small class="block text-gray-400">#{{ row.account_id }}</small></div></template>
            <template #cell-tier="{ row }"><div><span :class="candidateTier(row) === 'eligible' ? 'text-emerald-600' : ['degraded', 'recovery'].includes(candidateTier(row)) ? 'text-amber-600' : 'text-gray-500'">{{ candidateTierLabel(row) }}</span></div></template>
            <template #cell-priority="{ row }"><div>{{ row.priority }}</div></template>
            <template #cell-score="{ row }"><div>{{ row.score.toFixed(1) }}</div></template>
            <template #cell-performance="{ row }"><AccountPerformanceCell :metrics="row.performance" :unavailable="!!snapshotError" :can-configure="false" :notice-user-id="auth.user?.id" /></template>

            <template #cell-rate="{ row }"><div>{{ row.rate == null ? '—' : `${row.rate.toFixed(3)}×` }}</div></template>
            <template #cell-profit="{ row }"><div><template v-if="row.economics_source === 'usage'"><span :class="(row.profit ?? 0) < 0 ? 'text-red-600' : 'text-emerald-600'">${{ row.profit?.toFixed(4) }} · {{ row.margin == null ? '—' : `${(row.margin * 100).toFixed(1)}%` }}</span></template><span v-else class="text-xs text-gray-500">{{ t(`priorityScheduling.economics.${row.economics_source || 'unknown'}`) }}</span></div></template>
            <template #cell-details="{ row }">
              <details class="text-xs text-gray-500">
                <summary class="cursor-pointer">{{ t('priorityScheduling.reading.diagnostics') }}</summary>
                <div class="diagnostic-copy mt-2 space-y-2">
                  <small v-if="row.bound_groups != null" class="block text-gray-400">{{ t('priorityScheduling.boundGroups') }}: {{ row.bound_groups }}</small>
                  <small v-if="row.oauth_quota_role" class="block font-medium text-primary-600">{{ t(`priorityScheduling.oauthQuotaRoles.${row.oauth_quota_role}`) }}</small>
                  <small v-if="row.history_status && row.history_status !== 'ready'" class="block text-amber-600">{{ t(`priorityScheduling.historyStates.${row.history_status}`) }}</small>
                  <small v-if="row.latency_stopped" class="block font-medium text-red-600">{{ t('priorityScheduling.latencyPenaltyModes.stop') }}</small>
                  <small v-if="row.reasons.includes('latency_above_target') && snapshot.latency_penalty_mode !== 'stop'" class="block font-medium text-amber-600">{{ t('priorityScheduling.softPenalty') }}</small>
                  <small v-if="row.latency_recovery" class="block font-medium text-amber-600">{{ t('priorityScheduling.recoveryNote') }}</small>
                  <small v-if="row.latency_penalty_until && !row.latency_recovery" class="block text-gray-400">{{ t('priorityScheduling.latencyPenaltyUntil') }}: {{ new Date(row.latency_penalty_until).toLocaleString() }}</small>
                  <small v-for="reason in row.reasons" :key="reason" class="block text-gray-400">{{ t(`priorityScheduling.reasons.${reason}`) }}</small>
                  <small v-if="row.selection_weight != null" class="block text-gray-400">{{ t(isHistoricalAnalysis ? 'priorityScheduling.analysis.estimatedWeight' : 'priorityScheduling.selectionWeight') }}: {{ row.selection_weight.toFixed(2) }}</small>
                  <small v-if="row.exploration_eligible" class="block text-gray-400">{{ t('priorityScheduling.explorationEligible') }}</small>
                  <small class="block">{{ t('priorityScheduling.baseScore') }}: {{ row.base_score?.toFixed(1) ?? '—' }} · {{ t('priorityScheduling.errorPenalty') }}: {{ row.error_penalty?.toFixed(3) ?? '—' }}</small>
                  <small v-if="row.performance" class="block">{{ t('accountPerformance.samples', { tps: row.performance.tps_samples, ttft: row.performance.ttft_samples, quality: row.performance.quality_samples }) }}</small>
                  <small v-if="row.performance" class="block">{{ new Date(row.performance.window_start).toLocaleString() }} – {{ new Date(row.performance.window_end).toLocaleString() }}</small>
                  <div v-if="row.economics_source === 'usage'"><small class="block text-gray-400">${{ row.revenue.toFixed(4) }} − ${{ row.theoretical_cost.toFixed(4) }}</small></div>
                </div>
              </details>
            </template>
          </DataTable>
          </div><p v-else class="py-4 text-sm text-gray-500" data-testid="priority-no-candidates">{{ t('priorityScheduling.noCandidates') }}</p>
          <Pagination v-if="candidateRows.length" data-testid="candidate-pagination" :total="candidateRows.length" v-model:page="candidatePage" v-model:page-size="candidatePageSize" />
        </template>
        <section v-if="recomputeResult" class="mt-5 border-t border-gray-100 pt-4 dark:border-dark-700" data-testid="recompute-diff">
          <header class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="font-medium">{{ t('priorityScheduling.recompute.title') }}</h3>
            <button v-if="recomputeCanClearSticky" class="btn btn-secondary" :disabled="stickyLoading || !changedDiffRows.length" data-testid="clear-changed-sticky" @click="clearChangedSticky">{{ t('priorityScheduling.recompute.clearChanged') }}</button>
          </header>
          <p class="my-2 text-xs text-gray-500" data-testid="recompute-scope">{{ t('priorityScheduling.recompute.scope', { model: recomputeResult.before?.model ?? '—', group: recomputeResult.before?.group_id ?? '—', accounts: changedDiffRows.map(row => `${row.account_name} (#${row.account_id})`).join(', ') || '—' }) }}</p>
          <p class="my-2 text-xs leading-5 text-gray-500">{{ t('priorityScheduling.recompute.hint') }}</p>
          <p v-if="recomputeResult.timed_out" class="text-amber-600">{{ t('priorityScheduling.recompute.timedOut') }}</p>
          <p v-if="!changedDiffRows.length" class="text-sm text-gray-500">{{ t('priorityScheduling.recompute.noChange') }}</p>
          <div v-else class="overflow-x-auto">
            <table class="w-full whitespace-nowrap text-left text-sm">
              <thead><tr><th v-for="key in ['account','rank','score','weight','tier','average','errors']" :key="key" class="px-3 py-2 text-xs font-medium text-gray-500">{{ t(`priorityScheduling.recompute.${key}`) }}</th></tr></thead>
              <tbody>
                <tr v-for="row in changedDiffRows" :key="row.account_id" class="border-t border-gray-100 dark:border-dark-700">
                  <td class="px-3 py-2"><strong class="font-medium">{{ row.account_name }}</strong><small class="block text-gray-400">#{{ row.account_id }}</small></td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_rank || '—' }} → {{ row.new_rank || '—' }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_score.toFixed(1) }} → {{ row.new_score.toFixed(1) }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_weight.toFixed(2) }} → {{ row.new_weight.toFixed(2) }}</td>
                  <td class="px-3 py-2">{{ tierLabel(row.new_tier) }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ formatMs(row.old_avg_ttft_ms) }} → {{ formatMs(row.new_avg_ttft_ms) }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_error_samples }} → {{ row.new_error_samples }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </section>
      <section class="card p-5" data-testid="sticky-panel">
        <header class="flex flex-wrap items-center justify-between gap-3">
          <h2 class="text-lg font-semibold">{{ t('priorityScheduling.sticky.title') }}</h2>
          <div class="flex flex-wrap items-center gap-2">
            <label class="flex items-center gap-2 text-sm">{{ t('priorityScheduling.sticky.group') }}<input v-model.number="stickyGroupID" type="number" min="1" class="input w-24" data-testid="sticky-group" /></label>
            <button class="btn btn-secondary" :disabled="stickyLoading" data-testid="sticky-refresh" @click="loadSticky()">{{ t('priorityScheduling.sticky.refresh') }}</button>
            <button class="btn btn-secondary" :disabled="stickyLoading || !stickyVisible?.total" data-testid="sticky-clear-all" @click="clearDisplayedSticky">{{ t('priorityScheduling.sticky.clearAll') }}</button>
          </div>
        </header>
        <p class="my-3 text-xs leading-5 text-gray-500">{{ t('priorityScheduling.sticky.hint') }}</p>
        <p v-if="stickyVisible" class="mb-3 text-xs text-gray-500" data-testid="sticky-scope-account">{{ t('priorityScheduling.sticky.scopeAccount', { group: stickyVisible.group_id, accounts: stickyVisible.by_account.map(row => `${accountName(row.account_id)} (#${row.account_id})`).join(', ') || '—' }) }}</p>
        <p v-if="stickyError" role="alert" class="text-red-600">{{ stickyError }}</p>
        <p v-if="stickyNotice" role="status" class="text-emerald-600">{{ stickyNotice }}</p>
        <p v-if="stickyLoading" class="text-sm text-gray-500">{{ t('priorityScheduling.loading') }}</p>
        <template v-else-if="stickyVisible">
          <p class="mb-3 text-sm text-gray-500" data-testid="sticky-scope">{{ t('priorityScheduling.sticky.total', { count: stickyVisible.total }) }} · {{ t('priorityScheduling.group') }}: {{ stickyVisible.group_id }}</p>
          <div v-if="stickyVisible.by_account.length" class="overflow-x-auto">
            <table class="w-full whitespace-nowrap text-left text-sm">
              <thead><tr><th class="px-3 py-2 text-xs font-medium text-gray-500">{{ t('priorityScheduling.account') }}</th><th class="px-3 py-2 text-xs font-medium text-gray-500">{{ t('priorityScheduling.sticky.count') }}</th><th class="px-3 py-2"></th></tr></thead>
              <tbody>
                <tr v-for="row in stickyVisible.by_account" :key="row.account_id" class="border-t border-gray-100 dark:border-dark-700">
                  <td class="px-3 py-2"><strong class="font-medium">{{ accountName(row.account_id) }}</strong><small class="block text-gray-400">#{{ row.account_id }}</small></td>
                  <td class="px-3 py-2 tabular-nums">{{ row.count }}</td>
                  <td class="px-3 py-2 text-right"><button class="btn btn-secondary" :disabled="stickyLoading" :data-testid="`sticky-clear-${row.account_id}`" @click="clearStickyAccount(row.account_id)">{{ t('priorityScheduling.sticky.clear') }}</button></td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="py-4 text-center text-sm text-gray-500">{{ t('priorityScheduling.sticky.empty') }}</p>
        </template>
        <p v-else class="py-4 text-center text-sm text-gray-500">{{ t('priorityScheduling.sticky.loadHint') }}</p>
      </section>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { nextTick, computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import AccountPerformanceCell from '@/components/account/AccountPerformanceCell.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { performanceWindowMinutes } from '@/types/accountPerformance'
import { getAll as getGroups } from '@/api/admin/groups'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { getConfiguredTablePageSizeOptions } from '@/utils/tablePreferences'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import {
  getPriorityConfig, savePriorityConfig, getPrioritySnapshot, getPriorityAnalysis, getPriorityModels,
  recomputePriorityScheduling, getPriorityStickyBindings, clearPriorityStickyBindings,
  type PrioritySchedulingConfig, type PrioritySnapshot, type PriorityCandidate,
  type PrioritySchedulingRecomputeResult, type PriorityStickyBindings,
} from '@/api/admin/priorityScheduling'
const { t } = useI18n(), auth = useAuthStore()
const draft = ref<PrioritySchedulingConfig | null>(null), snapshot = ref<PrioritySnapshot | null>(null)
const mutationSnapshot = ref<PrioritySnapshot | null>(null)
const isHistoricalAnalysis = computed(() => snapshot.value?.selection_policy === 'read_only_analysis')
function candidateTier(row: PriorityCandidate) { return row.tier }
function candidateTierLabel(row: PriorityCandidate) {
  return t(`priorityScheduling.${isHistoricalAnalysis.value ? 'analysis' : 'tiers'}.${candidateTier(row)}`)
}
const selectedModel = ref(''), selectedGroup = ref(0)
const catalogLoading = ref(false), catalogError = ref(false)
const availableModels = ref<string[]>([]), availableGroups = ref<{ id: number; name: string }[]>([])
const modelOptions = computed(() => [...new Set([...availableModels.value, ...(selectedModel.value ? [selectedModel.value] : [])])].sort().map(value => ({ value, label: value })))
const groupOptions = computed(() => [{ value: 0, label: t('priorityScheduling.filters.ungrouped') }, ...availableGroups.value.map(group => ({ value: group.id, label: `${group.name} (#${group.id})` }))])
const scopeQuery = () => ({ model: selectedModel.value, ...(selectedGroup.value > 0 ? { group_id: selectedGroup.value } : {}), minutes: financeMinutes.value })
const scopeKey = () => JSON.stringify([auth.user?.id, auth.user?.role, selectedModel.value, selectedGroup.value, financeMinutes.value])
const groups = ref(''), modelsText = ref(''), error = ref(''), snapshotError = ref(''), notice = ref('')
const loading = ref(false), saving = ref(false), refreshing = ref(false)
const financeMinutes = ref(60)
const recomputeResult = ref<PrioritySchedulingRecomputeResult | null>(null), recomputeLoading = ref(false), recomputeError = ref('')
const stickyGroupID = ref(2), sticky = ref<PriorityStickyBindings | null>(null), stickyLoading = ref(false), stickyError = ref(''), stickyNotice = ref('')
const modes = ['experience', 'balanced', 'profit', 'custom'] as const
const presets = { experience: [40,35,20,5], balanced: [30,25,25,20], profit: [20,15,20,45] }
const weightKeys = ['quality_weight','latency_weight','load_weight','cost_weight'] as const
const fields = [
  { key: 'target_ttft_ms', min: 100, max: 120000 }, { key: 'target_tps', min: 1, max: 1000000 },
  { key: 'min_quality_percent', min: 0, max: 100 }, { key: 'window_minutes', min: 5, max: 1440 },
  { key: 'min_samples', min: 1, max: 1000 }, { key: 'max_load_percent', min: 10, max: 100 }
] as const
let alive = true, generation = 0, recomputeRequestGeneration = 0, stickyRequestGeneration = 0, snapshotRequestGeneration = 0
let catalogRequestGeneration = 0
let historyTimer: ReturnType<typeof setTimeout> | undefined
let analysisController: AbortController | undefined
let catalogController: AbortController | undefined
function invalidateCatalog() {
  catalogRequestGeneration++
  catalogController?.abort(); catalogController = undefined
}
function stopHistoryRefresh() { clearTimeout(historyTimer); historyTimer = undefined }
function invalidateAnalysis() {
  stopHistoryRefresh(); snapshotRequestGeneration++
  analysisController?.abort(); analysisController = undefined
  refreshing.value = false
}
const active = (version: number) => alive && generation === version
const readingRoot = ref<HTMLElement | null>(null)
const candidatePage = ref(1)
const candidatePageSize = ref(getPersistedPageSize())
const pageSizeOptions = computed(() => [...new Set([...getConfiguredTablePageSizeOptions(), candidatePageSize.value])].sort((a, b) => a - b))
const candidateOrder = ref<number[]>([])
// Keep existing accounts in their reading order, append new IDs, remove deleted IDs.
function reconcileOrder(previous: number[], rows: { account_id: number }[]) {
  const ids = new Set(rows.map(row => row.account_id))
  const retained = previous.filter(id => ids.delete(id))
  return [...retained, ...[...ids].sort((a, b) => a - b)]
}
function orderedRows<T extends { account_id: number }>(rows: T[], order: number[]) {
  const byID = new Map(rows.map(row => [row.account_id, row]))
  return order.flatMap(id => { const row = byID.get(id); return row ? [row] : [] })
}
const candidateRows = computed(() => orderedRows(snapshot.value?.candidates ?? [], candidateOrder.value))
const candidatePageRows = computed(() => candidateRows.value.slice((candidatePage.value - 1) * candidatePageSize.value, candidatePage.value * candidatePageSize.value))
const performanceMinutes = computed(() => performanceWindowMinutes(candidateRows.value[0]?.performance))
const candidateColumns = computed(() => ['account','tier','priority','score','performance','rate','profit','details'].map(key => ({ key, label: key === 'performance' ? t('accountPerformance.column', { minutes: performanceMinutes.value }) : t(key === 'details' ? 'priorityScheduling.reading.diagnostics' : `priorityScheduling.${key}`), class: ['account','profit','performance'].includes(key) ? 'w-56' : 'w-28' })))
function clampPage(page: number, size: number, total: number) { return Math.max(1, Math.min(page, Math.ceil(total / size))) }
watch([candidateRows, candidatePageSize], () => { candidatePage.value = clampPage(candidatePage.value, candidatePageSize.value, candidateRows.value.length) })
// Capture both scroll axes and the first visible account before a reactive patch.
// Do not move a reader who scrolled again while Vue was patching.
function readingAnchors(el: HTMLElement) {
  const rows = [...el.querySelectorAll<HTMLElement>('[data-row-id]')]
  return rows.length ? rows : [...el.querySelectorAll<HTMLElement>('[data-field="account"] [data-reading-account]')].map(anchor => {
    // The responsive DataTable puts the account slot inside a field/value/card.
    // Anchor the whole card so reading below its title keeps the same account.
    const card = anchor.closest('[data-field]')?.parentElement?.parentElement
    if (card && el.contains(card)) { card.dataset.readingAccount = anchor.dataset.readingAccount; return card }
    return anchor
  })
}
function readingRestore() {
  const saved = [...(readingRoot.value?.querySelectorAll<HTMLElement>('[data-reading-scroll]') ?? [])].map(el => {
    const bounds = el.getBoundingClientRect()
    const rows = readingAnchors(el)
    const row = rows.find(item => item.getBoundingClientRect().bottom > bounds.top + 48) ?? rows.at(-1)
    return { el, left: el.scrollLeft, top: el.scrollTop, id: row?.dataset.rowId ?? row?.dataset.readingAccount, offset: row ? row.getBoundingClientRect().top - bounds.top : 0 }
  })
  return async () => {
    await nextTick()
    for (const item of saved.reverse()) {
      if (!item.el.isConnected) continue
      item.el.scrollLeft = item.left
      const anchor = readingAnchors(item.el).find(row => (row.dataset.rowId ?? row.dataset.readingAccount) === item.id)
      if (item.el.scrollTop === item.top && anchor) item.el.scrollTop += anchor.getBoundingClientRect().top - item.el.getBoundingClientRect().top - item.offset
    }
  }
}
function displayScope(value: PrioritySnapshot | null | undefined) {
  if (!value) return ''
  return JSON.stringify([auth.user?.id, auth.user?.role, value.model, value.group_id, value.config_revision, value.mode, value.selection_policy, value.window_minutes, value.min_samples, value.latency_percentile, value.latency_measurement, value.latency_penalty_mode])
}
const canRead = () => alive && auth.user?.role === 'admin'

const changedDiffRows = computed(() => (recomputeResult.value?.diff ?? []).filter(row => row.changed))
const stickyVisible = computed(() => sticky.value?.group_id === Number(stickyGroupID.value) ? sticky.value : null)
function snapshotScope(value: PrioritySnapshot | null | undefined) {
  if (!value) return ''
  const ids = value.candidates.map(row => row.account_id).sort((a, b) => a - b).join(',')
  return [value.at, value.model, value.group_id ?? '', value.config_revision ?? '', ids].join('|')
}
const recomputeCanClearSticky = computed(() => {
  const result = recomputeResult.value
  return !!result && !result.timed_out && !!result.before && !!result.after &&
    result.after.group_id != null && result.after.group_id > 0 &&
    snapshotScope(result.before) === snapshotScope(result.after) && snapshotScope(result.after) === snapshotScope(mutationSnapshot.value) &&
    result.after.history_status === 'ready' && result.after.history_ready && !result.after.history_refreshing && !result.after.history_error
})
function formatMs(value?: number | null) { return value != null && value >= 0 ? `${Math.round(value)} ms` : '—' }
function tierLabel(tier?: string) {
  return t(`priorityScheduling.tiers.${tier || 'insufficient'}`)
}
function accountName(accountID: number) {
  return snapshot.value?.candidates.find(row => row.account_id === accountID)?.account_name ??
    `#${accountID}`
}
async function load() {
  if (!canRead() || loading.value) return
  const version = generation; loading.value = true; error.value = ''
  try {
    const config = await getPriorityConfig()
    if (!active(version)) return
    const { teams: _legacyTeams, ...currentConfig } = config as PrioritySchedulingConfig & { teams?: unknown }
    draft.value = { ...currentConfig, cache_weight: currentConfig.cache_weight ?? 0, target_tps: currentConfig.target_tps ?? 30, error_rate_weight: currentConfig.error_rate_weight ?? 0, error_rate_window_minutes: currentConfig.error_rate_window_minutes ?? 10, error_rate_prior_samples: currentConfig.error_rate_prior_samples ?? 5, latency_percentile: currentConfig.latency_percentile ?? 90, latency_measurement: currentConfig.latency_measurement ?? 'legacy_forward', recovery_exploration_percent: currentConfig.recovery_exploration_percent ?? 0, latency_penalty_mode: currentConfig.latency_penalty_mode ?? 'deprioritize', latency_penalty_minutes: currentConfig.latency_penalty_minutes ?? 10, balance_protocols: currentConfig.balance_protocols ?? true, oauth_quota_priority: currentConfig.oauth_quota_priority ?? false, oauth_quota_threshold: currentConfig.oauth_quota_threshold ?? 90 }; groups.value = (config.group_ids ?? []).join(', '); modelsText.value = (config.models ?? []).join('\n')
    const configuredGroup = config.group_ids?.[0]
    if (configuredGroup && configuredGroup > 0) stickyGroupID.value = configuredGroup
  } catch { if (active(version)) error.value = t('priorityScheduling.error') }
  finally { if (active(version)) loading.value = false }
}
async function save() {
  if (!canRead() || !draft.value || saving.value) return
  const ids = groups.value.trim() ? groups.value.split(/[,，]/).map(v => Number(v.trim())) : []
  if (ids.some(id => !Number.isSafeInteger(id) || id <= 0)) { error.value = t('priorityScheduling.invalid'); return }
  const version = generation; saving.value = true; error.value = ''; notice.value = ''
  const submitted = JSON.parse(JSON.stringify(draft.value)) as PrioritySchedulingConfig
  const submittedGroups = groups.value, submittedModels = modelsText.value
  try {
    const config = await savePriorityConfig({ ...submitted, group_ids: [...new Set(ids)], models: [...new Set(submittedModels.split('\n').map(v => v.trim()).filter(Boolean))] })
    if (active(version) && draft.value) {
      // Apply normalization only to fields the user has not edited since PUT.
      const newer = Object.fromEntries(Object.entries(draft.value).filter(([key, value]) => JSON.stringify(value) !== JSON.stringify(submitted[key as keyof PrioritySchedulingConfig])))
      draft.value = { ...config, ...newer }
      if (groups.value === submittedGroups) groups.value = (config.group_ids ?? []).join(', ')
      if (modelsText.value === submittedModels) modelsText.value = (config.models ?? []).join('\n')
      invalidateAnalysis()
      invalidateCatalog(); void loadCatalog()
      recomputeRequestGeneration++; recomputeResult.value = null; mutationSnapshot.value = null
      notice.value = t('priorityScheduling.saved')
      void loadSnapshot(0)
    }
  } catch { if (active(version)) error.value = t('priorityScheduling.error') }
  finally { if (active(version)) saving.value = false }
}
async function loadCatalog(initial = false) {
  if (!canRead()) return
  invalidateCatalog()
  const version = generation, request = ++catalogRequestGeneration
  let group = selectedGroup.value
  const controller = new AbortController(); catalogController = controller
  catalogLoading.value = true; catalogError.value = false
  try {
    const [openaiGroups, compositeGroups, models] = await Promise.all([getGroups('openai', controller.signal), getGroups('composite', controller.signal), getPriorityModels(group || undefined, controller.signal)])
    if (!active(version) || request !== catalogRequestGeneration || group !== selectedGroup.value) return
    availableGroups.value = [...new Map([...openaiGroups, ...compositeGroups].map(group => [group.id, group])).values()]
    let catalogModels = models
    if (initial && !initialized.value) {
      const configuredGroup = draft.value?.group_ids?.find(id => availableGroups.value.some(group => group.id === id))
      const defaultGroup = configuredGroup ?? openaiGroups.find(group => /gpt/i.test(group.name))?.id ?? openaiGroups[0]?.id ?? 0
      if (defaultGroup !== group) {
        selectedGroup.value = group = defaultGroup
        catalogModels = await getPriorityModels(group || undefined, controller.signal)
        if (!active(version) || request !== catalogRequestGeneration || group !== selectedGroup.value) return
      }
    }
    availableModels.value = catalogModels
    if (initial && !selectedModel.value) selectedModel.value = draft.value?.models?.find(model => catalogModels.includes(model)) ?? catalogModels[0] ?? ''
  } catch {
    // Promise.all can fail before its sibling requests finish. Keep those reads
    // from escaping the scope/lifecycle cancellation boundary.
    controller.abort()
    if (active(version) && request === catalogRequestGeneration) catalogError.value = true
  }
  finally {
    if (catalogController === controller) catalogController = undefined
    if (active(version) && request === catalogRequestGeneration) catalogLoading.value = false
  }
}
async function refresh() {
  if (!canRead() || !initialized.value) return
  stopHistoryRefresh()
  recomputeRequestGeneration++; recomputeResult.value = null
  if (!selectedModel.value || catalogError.value) await loadCatalog(!selectedModel.value)
  await loadSnapshot(0)
}
function validAnalysis(value: Awaited<ReturnType<typeof getPriorityAnalysis>>, query: ReturnType<typeof scopeQuery>) {
  const current = value?.snapshot
  return !!current && current.model === query.model && current.group_id === (query.group_id ?? null) && current.window_minutes === query.minutes &&
    current.history_ready && !current.history_error && current.history_status === 'ready'
}
async function loadSnapshot(attempt: number) {
  if (!canRead() || !selectedModel.value || refreshing.value) return
  const request = ++snapshotRequestGeneration, key = scopeKey(), query = scopeQuery()
  const priorScope = displayScope(snapshot.value)
  recomputeRequestGeneration++; recomputeLoading.value = false
  const version = generation; refreshing.value = true; snapshotError.value = ''
  const controller = new AbortController(); analysisController = controller
  try {
    const result = await getPriorityAnalysis(query, controller.signal)
    if (!active(version) || request !== snapshotRequestGeneration || key !== scopeKey()) return
    if (!validAnalysis(result, query)) throw new Error('invalid analysis scope')
    const restore = readingRestore()
    if (displayScope(result.snapshot) !== priorScope) { candidateOrder.value = []; candidatePage.value = 1 }
    snapshot.value = result.snapshot
    candidateOrder.value = reconcileOrder(candidateOrder.value, result.snapshot.candidates)
    void restore()
    scheduleHistoryRefresh(version, 30000, attempt)
  } catch {
    if (active(version) && request === snapshotRequestGeneration && key === scopeKey()) {
      snapshotError.value = t('priorityScheduling.error')
      scheduleHistoryRefresh(version, 15000, 0)
    }
  } finally {
    if (analysisController === controller) analysisController = undefined
    if (active(version) && request === snapshotRequestGeneration) { refreshing.value = false }
  }
}
function scheduleHistoryRefresh(version: number, delay: number, attempt: number) {
  stopHistoryRefresh()
  if (!canRead() || !active(version)) return
  historyTimer = setTimeout(() => {
    if (!active(version)) return
    if (document.visibilityState === 'hidden') { scheduleHistoryRefresh(version, 30000, attempt); return }
    void loadSnapshot(attempt)
  }, delay)
}
async function loadSticky(requestedGroupID?: number) {
  if (!canRead()) return
  if (requestedGroupID != null && requestedGroupID > 0) stickyGroupID.value = requestedGroupID
  const groupID = Number(stickyGroupID.value)
  if (!Number.isSafeInteger(groupID) || groupID <= 0) { stickyError.value = t('priorityScheduling.sticky.invalidGroup'); return }
  const version = generation, request = ++stickyRequestGeneration
  stickyLoading.value = true; stickyError.value = ''; stickyNotice.value = ''
  try {
    const value = await getPriorityStickyBindings(groupID)
    if (active(version) && request === stickyRequestGeneration) {
      if (value.group_id !== groupID) { sticky.value = null; stickyError.value = t('priorityScheduling.sticky.error') }
      else sticky.value = value
    }
  } catch { if (active(version) && request === stickyRequestGeneration) stickyError.value = t('priorityScheduling.sticky.error') }
  finally { if (active(version) && request === stickyRequestGeneration) stickyLoading.value = false }
}
async function recompute() {
  if (!canRead() || recomputeLoading.value) return
  const version = generation, request = ++recomputeRequestGeneration, snapshotRequest = snapshotRequestGeneration
  recomputeResult.value = null; recomputeLoading.value = true; recomputeError.value = ''
  try {
    const captured = await getPrioritySnapshot()
    if (!active(version) || request !== recomputeRequestGeneration || snapshotRequest !== snapshotRequestGeneration) return
    mutationSnapshot.value = captured
    const scope = snapshotScope(captured)
    const result = await recomputePriorityScheduling()
    if (!active(version) || request !== recomputeRequestGeneration || snapshotRequest !== snapshotRequestGeneration) return
    const resultScope = snapshotScope(result.before)
    if (result.timed_out || !result.before || !result.after || !scope || resultScope !== scope || snapshotScope(result.after) !== resultScope || result.after.history_status !== 'ready' || !result.after.history_ready || result.after.history_refreshing || result.after.history_error) {
      recomputeError.value = t('priorityScheduling.recompute.error')
      return
    }
    recomputeResult.value = result
    const restore = readingRestore()
    mutationSnapshot.value = result.after
    void restore()
    if (result.after.group_id != null && result.after.group_id > 0) void loadSticky(result.after.group_id)
  } catch { if (active(version) && request === recomputeRequestGeneration && snapshotRequest === snapshotRequestGeneration) recomputeError.value = t('priorityScheduling.recompute.error') }
  finally { if (active(version) && request === recomputeRequestGeneration && snapshotRequest === snapshotRequestGeneration) recomputeLoading.value = false }
}
async function clearSticky(accountIDs: number[], requestedGroupID?: number) {
  if (!canRead()) return
  const groupID = requestedGroupID ?? Number(stickyVisible.value?.group_id)
  if (!Number.isSafeInteger(groupID) || groupID <= 0) { stickyError.value = t('priorityScheduling.sticky.invalidGroup'); return }
  let message = t('priorityScheduling.sticky.confirmAll', { count: sticky.value?.group_id === groupID ? sticky.value.total : 0, group: groupID })
  if (accountIDs.length === 1) {
    const row = sticky.value?.group_id === groupID ? sticky.value.by_account.find(item => item.account_id === accountIDs[0]) : undefined
    message = t('priorityScheduling.sticky.confirmAccount', { count: row?.count ?? 0, group: groupID, account: row ? `${accountName(row.account_id)} (#${row.account_id})` : `#${accountIDs[0]}` })
  } else if (accountIDs.length > 1) {
    message = t('priorityScheduling.sticky.confirmSome', { count: accountIDs.length, group: groupID })
  }
  if (!window.confirm(message)) return
  const version = generation, request = ++stickyRequestGeneration
  stickyLoading.value = true; stickyError.value = ''; stickyNotice.value = ''
  try {
    const result = await clearPriorityStickyBindings({ group_id: groupID, account_ids: accountIDs })
    if (!active(version) || request !== stickyRequestGeneration || result.group_id !== groupID) return
    stickyNotice.value = t('priorityScheduling.sticky.cleared', { count: result.deleted })
    sticky.value = { group_id: result.group_id, total: result.remaining, by_account: result.by_account }
    await loadSnapshot(0)
  } catch { if (active(version) && request === stickyRequestGeneration) stickyError.value = t('priorityScheduling.sticky.clearError') }
  finally { if (active(version) && request === stickyRequestGeneration) stickyLoading.value = false }
}
function clearDisplayedSticky() { const value = stickyVisible.value; if (value) void clearSticky([], value.group_id) }
function clearStickyAccount(accountID: number) { const value = stickyVisible.value; if (value) void clearSticky([accountID], value.group_id) }
async function clearChangedSticky() {
  const result = recomputeResult.value, version = generation, request = snapshotRequestGeneration
  if (!recomputeCanClearSticky.value || !changedDiffRows.value.length || result?.after?.group_id == null) return
  try {
    const latest = await getPrioritySnapshot()
    if (!active(version) || request !== snapshotRequestGeneration || result !== recomputeResult.value) return
    if (snapshotScope(latest) !== snapshotScope(result.after) || latest?.history_status !== 'ready' || !latest.history_ready || latest.history_error || latest.history_refreshing) { recomputeResult.value = null; return }
    mutationSnapshot.value = latest
    if (recomputeCanClearSticky.value) await clearSticky(changedDiffRows.value.map(row => row.account_id), result.after.group_id)
  } catch { if (active(version) && request === snapshotRequestGeneration && result === recomputeResult.value) recomputeError.value = t('priorityScheduling.recompute.error') }
}
watch(stickyGroupID, () => {
  stickyRequestGeneration++
  sticky.value = null; stickyError.value = ''; stickyNotice.value = ''; stickyLoading.value = false
}, { flush: 'sync' })
const initialized = ref(false)
watch([selectedModel, selectedGroup, financeMinutes], () => {
  if (!initialized.value) return
  invalidateAnalysis()
  recomputeRequestGeneration++; recomputeResult.value = null; recomputeLoading.value = false
  snapshot.value = null; candidateOrder.value = []; candidatePage.value = 1; snapshotError.value = ''
  void loadSnapshot(0)
}, { flush: 'sync' })
watch(selectedGroup, () => { if (initialized.value) void loadCatalog() })
watch(() => auth.user ? `${auth.user.id}:${auth.user.role}` : '', () => {
  invalidateAnalysis(); invalidateCatalog(); generation++; initialized.value = false; draft.value = null; snapshot.value = null; mutationSnapshot.value = null; selectedModel.value = ''; selectedGroup.value = 0; availableModels.value = []; availableGroups.value = []; catalogLoading.value = false; catalogError.value = false
  groups.value = modelsText.value = error.value = notice.value = snapshotError.value = ''
  candidateOrder.value = []; candidatePage.value = 1
  recomputeResult.value = null; recomputeError.value = ''; recomputeLoading.value = false
  recomputeRequestGeneration++; stickyRequestGeneration++; snapshotRequestGeneration++
  sticky.value = null; stickyError.value = ''; stickyNotice.value = ''; stickyLoading.value = false; stickyGroupID.value = 2
  loading.value = saving.value = refreshing.value = false
}, { flush: 'sync' })
onMounted(async () => {
  const version = generation
  await load()
  if (!active(version) || !canRead()) return
  await loadCatalog(true)
  if (!active(version) || !canRead()) return
  initialized.value = true
  void loadSticky(); void refresh()
})
onBeforeUnmount(() => { invalidateAnalysis(); invalidateCatalog(); alive = false; generation++ })
</script>
<style scoped>
.card { @apply rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.reading-status { min-height: 3rem; }
.field-title { @apply mb-2 block text-xs font-medium text-gray-600 dark:text-gray-300; }
input[type=checkbox] { @apply h-4 w-4 rounded text-primary-600; }
.reading-table { max-height: min(32rem, 55vh); max-height: min(32rem, 55dvh); overflow: auto; overscroll-behavior: contain; overflow-anchor: none; }
.reading-table :deep(table) { table-layout: fixed; min-width: 76rem; }
.candidate-table :deep(table) { min-width: 83rem; }
.account-cell { width: 10rem; max-width: 100%; overflow-wrap: anywhere; white-space: normal; }
.account-cell > strong { display: block; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.diagnostic-copy { max-width: 17rem; white-space: normal; overflow-wrap: anywhere; }
button:disabled { @apply cursor-not-allowed opacity-50; }
</style>
