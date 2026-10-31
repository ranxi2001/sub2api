<template>
  <AppLayout>
    <div class="min-w-0 space-y-5">
      <header><h1 class="text-2xl font-semibold">{{ t('priorityScheduling.title') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('priorityScheduling.description') }}</p></header>
      <SmartOpsNav />
      <p class="rounded-xl bg-primary-50 p-4 text-sm leading-6 text-primary-800 dark:bg-primary-950/30 dark:text-primary-200">{{ t('priorityScheduling.scopeNote') }}</p>
      <div v-if="error" role="alert" class="text-red-600">{{ error }} <button v-if="!draft" class="underline" @click="load">{{ t('priorityScheduling.retry') }}</button></div>
      <p v-if="notice" role="status" class="text-emerald-600">{{ notice }}</p>
      <p v-if="loading">{{ t('priorityScheduling.loading') }}</p>
      <form v-if="draft" class="card p-5" @submit.prevent="save">
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
              <div class="grid grid-cols-2 gap-3"><label v-for="(weight, index) in weightKeys" :key="weight"><span class="field-title">{{ t(`priorityScheduling.${weight}`) }}</span><input v-if="draft.mode === 'custom'" v-model.number="draft[weight]" type="number" min="0" max="100" step="0.1" required class="input w-full" /><div v-else class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ presets[draft.mode][index] }}%</div></label></div>
              <p class="text-sm leading-6 text-gray-500">{{ t('priorityScheduling.rule') }}</p>
              <p class="text-xs leading-5 text-gray-500">{{ t('priorityScheduling.costHint') }}</p>
              <label><span class="field-title">{{ t('priorityScheduling.error_rate_weight') }}</span><input v-model.number="draft.error_rate_weight" data-testid="error-rate-weight" type="number" min="0" max="100" step="1" required class="input w-full" /></label>
              <p class="text-xs leading-5 text-gray-500">{{ t('priorityScheduling.errorRateHint') }}</p>
            </section>
            <section>
              <div class="mb-3 grid gap-3 sm:grid-cols-2">
                <label><span class="field-title">{{ t('priorityScheduling.latencyPercentile') }}</span><select v-model.number="draft.latency_percentile" data-testid="latency-percentile" class="input w-full"><option v-for="q in [50,70,90,95]" :key="q" :value="q">P{{ q }}</option></select></label>
                <label><span class="field-title">{{ t('priorityScheduling.latencyMeasurement') }}</span><select v-model="draft.latency_measurement" data-testid="latency-measurement" class="input w-full"><option value="legacy_forward">{{ t('priorityScheduling.legacyForward') }}</option><option value="attributed_v1">{{ t('priorityScheduling.attributedV1') }}</option></select></label>
              </div>
              <label class="mb-3 block"><span class="field-title">{{ t('priorityScheduling.latencyPenaltyMode') }}</span><select v-model="draft.latency_penalty_mode" data-testid="latency-penalty-mode" class="input w-full"><option value="deprioritize">{{ t('priorityScheduling.latencyPenaltyModes.deprioritize') }}</option><option value="stop">{{ t('priorityScheduling.latencyPenaltyModes.stop') }}</option></select><small class="mt-2 block text-gray-500">{{ t('priorityScheduling.latencyPenaltyModeHint') }}</small></label>
              <p class="mb-3 text-xs leading-5 text-gray-500">{{ t('priorityScheduling.measurementHint') }}</p>
              <h2 class="mb-3 font-medium">{{ t('priorityScheduling.thresholds') }}</h2><div class="grid gap-3 sm:grid-cols-2"><label v-for="field in fields" :key="field.key"><span class="field-title">{{ t(`priorityScheduling.${field.key}`) }}</span><input v-model.number="draft[field.key]" type="number" :data-testid="field.key" :min="field.min" :max="field.max" step="1" required class="input w-full" /></label></div><p class="mt-3 text-xs leading-5 text-gray-500">{{ t('priorityScheduling.latencyPenaltyHint') }}</p></section>
          </div>
          <div class="grid gap-5 sm:grid-cols-2"><label><span class="field-title">{{ t('priorityScheduling.groupIds') }}</span><input v-model="groups" class="input w-full" data-testid="groups" /><small class="text-gray-500">{{ t('priorityScheduling.groupHint') }}</small></label><label><span class="field-title">{{ t('priorityScheduling.models') }}</span><textarea v-model="modelsText" class="input w-full" rows="3" /><small class="text-gray-500">{{ t('priorityScheduling.modelHint') }}</small></label></div>
          <button class="btn btn-primary" type="submit">{{ t(saving ? 'priorityScheduling.saving' : 'priorityScheduling.save') }}</button>
        </fieldset>
      </form>
      <section class="card p-5" data-testid="latency-panel">
        <header class="flex flex-wrap items-center justify-between gap-3">
          <h2 class="text-lg font-semibold">{{ t('priorityScheduling.latencyPanel.title') }}</h2>
          <div class="flex items-center gap-2">
            <select v-model.number="latencyMinutes" class="input" data-testid="latency-window">
              <option :value="15">15m</option>
              <option :value="60">60m</option>
              <option :value="180">180m</option>
            </select>
            <button class="btn btn-secondary" :disabled="latencyLoading" data-testid="latency-refresh" @click="loadLatency">{{ t('priorityScheduling.latencyPanel.refresh') }}</button>
          </div>
        </header>
        <p class="my-3 text-xs leading-5 text-gray-500">{{ t('priorityScheduling.latencyPanel.hint') }}</p>
        <p v-if="latencyError" role="alert" class="text-red-600">{{ latencyError }}</p>
        <p v-if="latencyLoading" class="text-sm text-gray-500">{{ t('priorityScheduling.loading') }}</p>
        <template v-else-if="latency">
          <p class="mb-3 text-xs text-gray-500">{{ new Date(latency.at).toLocaleString() }} · {{ latency.model }} · {{ t('priorityScheduling.group') }}: {{ latency.group_id ?? '—' }} · P{{ latency.latency_percentile ?? 90 }} · {{ latency.latency_measurement ?? 'legacy_forward' }} · {{ latency.bucket_minutes }}m</p>
          <div v-if="latencyRows.length" class="overflow-x-auto">
            <table class="w-full whitespace-nowrap text-left text-sm">
              <thead><tr><th v-for="key in ['account','samples','p50','p90','p99','errors','slow','errorRate']" :key="key" class="px-3 py-3 text-xs font-medium text-gray-500">{{ t(`priorityScheduling.latencyPanel.${key}`) }}</th></tr></thead>
              <tbody>
                <tr v-for="row in latencyRows" :key="row.account_id" class="border-t border-gray-100 dark:border-dark-700">
                  <td class="px-3 py-3"><strong class="font-medium">{{ row.account_name }}</strong><small class="block text-gray-400">#{{ row.account_id }}</small></td>
                  <td class="px-3 py-3 tabular-nums">{{ row.samples }}</td>
                  <td class="px-3 py-3 tabular-nums">{{ formatMs(row.p50_ttft_ms) }}</td>
                  <td class="px-3 py-3 tabular-nums" :class="latencyClass(row.p90_ttft_ms)">{{ formatMs(row.p90_ttft_ms) }}</td>
                  <td class="px-3 py-3 tabular-nums">{{ formatMs(row.p99_ttft_ms) }}</td>
                  <td class="px-3 py-3 tabular-nums">{{ row.error_samples }}</td>
                  <td class="px-3 py-3 tabular-nums">{{ row.slow_samples }}</td>
                  <td class="px-3 py-3 tabular-nums">{{ formatRate(row.error_rate) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="py-6 text-center text-sm text-gray-500">{{ t('priorityScheduling.latencyPanel.empty') }}</p>
          <div v-if="latencyBuckets.length" class="mt-4 overflow-x-auto">
            <table class="min-w-full text-left text-xs">
              <thead><tr><th class="sticky left-0 z-10 bg-white px-2 py-2 dark:bg-dark-900">{{ t('priorityScheduling.account') }}</th><th v-for="bucket in latencyBuckets" :key="bucket" class="px-2 py-2 tabular-nums text-gray-500">{{ formatBucket(bucket) }}</th></tr></thead>
              <tbody>
                <tr v-for="row in latencyRows" :key="row.account_id" class="border-t border-gray-100 dark:border-dark-700">
                  <td class="sticky left-0 z-10 bg-white px-2 py-2 dark:bg-dark-900">{{ row.account_name }}</td>
                  <td v-for="bucket in latencyBuckets" :key="bucket" class="px-2 py-2 tabular-nums" :class="latencyClass(bucketLatency(row, bucket) ?? 0)">{{ bucketText(row, bucket) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
        <p v-else class="py-6 text-center text-sm text-gray-500">{{ t('priorityScheduling.latencyPanel.empty') }}</p>
      </section>
      <section class="card p-5">
        <header class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('priorityScheduling.recent') }}</h2><div class="flex flex-wrap items-center gap-2"><button class="btn btn-secondary" :disabled="recomputeLoading" data-testid="recompute" @click="recompute">{{ t(recomputeLoading ? 'priorityScheduling.recompute.running' : 'priorityScheduling.recompute.action') }}</button><button class="btn btn-secondary" :disabled="refreshing" @click="refresh">{{ t('priorityScheduling.refresh') }}</button></div></header>
        <p v-if="recomputeError" role="alert" class="mt-3 text-red-600">{{ recomputeError }}</p>
        <p class="my-3 text-xs leading-5 text-gray-500">{{ t('priorityScheduling.snapshotHint') }}</p>
        <p v-if="snapshotError" role="alert" class="text-red-600">{{ snapshotError }}</p>
        <p v-if="!snapshot" class="py-8 text-center text-sm text-gray-500">{{ t('priorityScheduling.empty') }}</p>
        <template v-else>
          <p data-testid="priority-snapshot-at" class="mb-4 text-sm text-gray-500">{{ new Date(snapshot.at).toLocaleString() }} · {{ t('priorityScheduling.model') }}: {{ snapshot.model }} · {{ t('priorityScheduling.group') }}: {{ snapshot.group_id ?? '—' }} · {{ t(`priorityScheduling.modes.${snapshot.mode}`) }} · {{ t('priorityScheduling.latencyPenaltyMode') }}: {{ t(`priorityScheduling.latencyPenaltyModes.${snapshot.latency_penalty_mode || 'deprioritize'}`) }}</p>
          <p v-if="snapshot.history_status && snapshot.history_status !== 'ready'" role="status" class="text-sm text-amber-600">{{ t(`priorityScheduling.historyStates.${snapshot.history_status}`) }}</p>
          <p v-else-if="!snapshot.history_ready" class="text-sm text-amber-600">{{ t('priorityScheduling.historyPending') }}</p>
          <p v-if="snapshot.history_error && snapshot.history_ready" class="text-sm text-amber-600">{{ t('priorityScheduling.historyRetry') }}</p>
          <p v-if="snapshot.evaluated_at" data-testid="priority-evaluated-at" class="mb-3 text-xs text-gray-500">{{ t('priorityScheduling.evaluatedAt') }}: {{ new Date(snapshot.evaluated_at).toLocaleString() }}<template v-if="snapshot.history_observed_at"> · {{ t('priorityScheduling.historyObservedAt') }}: {{ new Date(snapshot.history_observed_at).toLocaleString() }}</template></p>
          <p v-if="snapshot.latency_fallback" role="status" class="mb-3 text-sm text-amber-600" data-testid="priority-latency-fallback">{{ t('priorityScheduling.latencyFallback') }}</p>
          <p class="mb-3 text-xs text-gray-500">P{{ snapshot.latency_percentile ?? 90 }} · {{ snapshot.latency_measurement ?? 'legacy_forward' }} · {{ t('priorityScheduling.window_minutes') }}: {{ snapshot.window_minutes ?? '—' }} · {{ t('priorityScheduling.configRevision') }}: {{ snapshot.config_revision ?? '—' }}<template v-if="snapshot.history_window_end"> · {{ t('priorityScheduling.windowEnd') }}: {{ new Date(snapshot.history_window_end).toLocaleString() }}</template></p>
          <div v-if="snapshot.candidates.length" data-testid="priority-candidate-scroll" class="overflow-x-auto"><table class="w-full whitespace-nowrap text-left text-sm"><thead><tr><th v-for="key in ['account','tier','priority','score','errorRate','quality','latency','load','rate','profit']" :key="key" class="px-3 py-3 text-xs font-medium text-gray-500">{{ t(`priorityScheduling.${key}`) }}</th></tr></thead><tbody><tr v-for="row in snapshot.candidates" :key="row.account_id" class="border-t border-gray-100 dark:border-dark-700"><td class="px-3 py-4"><strong class="font-medium">{{ row.account_name }}</strong><small class="block text-gray-400">#{{ row.account_id }}</small><small v-if="row.bound_groups != null" class="block text-gray-400">{{ t('priorityScheduling.boundGroups') }}: {{ row.bound_groups }}</small></td><td class="px-3 py-4"><span :class="row.tier === 'eligible' ? 'text-emerald-600' : row.tier === 'degraded' ? 'text-amber-600' : 'text-gray-500'">{{ t(`priorityScheduling.tiers.${row.tier}`) }}</span><small v-if="row.oauth_quota_role" class="block font-medium text-primary-600">{{ t(`priorityScheduling.oauthQuotaRoles.${row.oauth_quota_role}`) }}</small><small v-if="row.history_status && row.history_status !== 'ready'" class="block text-amber-600">{{ t(`priorityScheduling.historyStates.${row.history_status}`) }}</small><small v-if="row.latency_stopped" class="block font-medium text-red-600">{{ t('priorityScheduling.latencyPenaltyModes.stop') }}</small><small v-if="row.reasons.includes('latency_above_target') && snapshot.latency_penalty_mode !== 'stop'" class="block font-medium text-amber-600">{{ t('priorityScheduling.softPenalty') }}</small><small v-if="row.latency_recovery" class="block font-medium text-amber-600">{{ t('priorityScheduling.recoveryNote') }}</small><small v-if="row.latency_penalty_until && !row.latency_recovery" class="block text-gray-400">{{ t('priorityScheduling.latencyPenaltyUntil') }}: {{ new Date(row.latency_penalty_until).toLocaleString() }}</small><small v-for="reason in row.reasons" :key="reason" class="block text-gray-400">{{ t(`priorityScheduling.reasons.${reason}`) }}</small></td><td class="px-3 py-4 tabular-nums">{{ row.priority }}</td><td class="px-3 py-4 tabular-nums">{{ row.score.toFixed(1) }}<small v-if="row.selection_weight != null" class="block text-gray-400">{{ t('priorityScheduling.selectionWeight') }}: {{ row.selection_weight.toFixed(2) }}</small><small v-if="row.exploration_eligible" class="block text-gray-400">{{ t('priorityScheduling.explorationEligible') }}</small></td><td class="px-3 py-4" :data-testid="`error-rate-${row.account_id}`" :title="t('priorityScheduling.errorRateHint')"><span v-if="row.error_rate == null">{{ t('priorityScheduling.errorNoSamples') }}</span><span v-else>{{ (row.error_rate * 100).toFixed(1) }}%</span><small v-if="(row.error_successes ?? 0) + (row.error_failures ?? 0) > 0" class="block text-gray-400">{{ t('priorityScheduling.errorAttempts') }}: {{ row.error_failures ?? 0 }} / {{ (row.error_successes ?? 0) + (row.error_failures ?? 0) }}</small><small v-if="errorSamplesLow(row)" class="block text-amber-600">{{ t('priorityScheduling.errorInsufficientSamples') }}</small><small v-if="row.error_observed_at" class="block text-gray-400">{{ t('priorityScheduling.errorObservedAt') }}: {{ new Date(row.error_observed_at).toLocaleString() }}</small><small class="block text-gray-400">{{ t('priorityScheduling.errorWindow') }}: {{ row.error_window_minutes ?? '—' }} min · {{ row.error_endpoint ?? '—' }}</small><small v-if="row.error_penalty != null" class="block text-gray-400">{{ t('priorityScheduling.errorPenalty') }}: {{ row.error_penalty.toFixed(3) }} · {{ t('priorityScheduling.baseScore') }}: {{ row.base_score?.toFixed(1) ?? '—' }}</small><small class="block text-gray-400">{{ t('priorityScheduling.errorLocalScope') }}</small></td><td class="px-3 py-4">{{ row.quality_samples ? `${row.quality_passed}/${row.quality_samples}` : '—' }}</td><td class="px-3 py-4">P{{ row.latency_percentile ?? 90 }}: {{ row.samples && selectedLatency(row) != null ? `${Math.round(selectedLatency(row)!)} ms` : t('priorityScheduling.unknown') }}<small class="block text-gray-400">{{ row.samples }} {{ t('priorityScheduling.samples') }}</small><small v-if="row.samples < (snapshot.min_samples ?? 5) || row.samples < tailSampleFloor(row.latency_percentile ?? 90)" class="block text-amber-600">{{ t('priorityScheduling.lowSamples') }}</small><small class="block text-gray-400">{{ t('priorityScheduling.unknown') }}: {{ row.latency_unknown_samples ?? '—' }}</small><small class="block text-gray-400">P90: {{ row.samples ? `${Math.round(row.p90_ttft_ms)} ms` : '—' }}</small><small class="block text-gray-400">{{ t('priorityScheduling.experienceP90') }}: {{ row.experience_samples ? `${Math.round(row.experience_p90_ms ?? 0)} ms (${row.experience_samples})` : '—' }}</small></td><td class="px-3 py-4">{{ row.load_percent == null ? '—' : `${row.load_percent}%` }}</td><td class="px-3 py-4">{{ row.rate == null ? '—' : `${row.rate.toFixed(3)}×` }}</td><td class="px-3 py-4"><template v-if="row.economics_source === 'usage'"><span :class="(row.profit ?? 0) < 0 ? 'text-red-600' : 'text-emerald-600'">${{ row.profit?.toFixed(4) }} · {{ row.margin == null ? '—' : `${(row.margin * 100).toFixed(1)}%` }}</span><small class="block text-gray-400">${{ row.revenue.toFixed(4) }} − ${{ row.theoretical_cost.toFixed(4) }}</small></template><span v-else class="text-xs text-gray-500">{{ t(`priorityScheduling.economics.${row.economics_source || 'unknown'}`) }}</span></td></tr></tbody></table></div><p v-else class="py-4 text-sm text-gray-500" data-testid="priority-no-candidates">{{ t('priorityScheduling.noCandidates') }}</p>
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
              <thead><tr><th v-for="key in ['account','rank','score','weight','tier','p90','errors']" :key="key" class="px-3 py-2 text-xs font-medium text-gray-500">{{ t(`priorityScheduling.recompute.${key}`) }}</th></tr></thead>
              <tbody>
                <tr v-for="row in changedDiffRows" :key="row.account_id" class="border-t border-gray-100 dark:border-dark-700">
                  <td class="px-3 py-2"><strong class="font-medium">{{ row.account_name }}</strong><small class="block text-gray-400">#{{ row.account_id }}</small></td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_rank || '—' }} → {{ row.new_rank || '—' }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_score.toFixed(1) }} → {{ row.new_score.toFixed(1) }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ row.old_weight.toFixed(2) }} → {{ row.new_weight.toFixed(2) }}</td>
                  <td class="px-3 py-2">{{ tierLabel(row.new_tier) }}</td>
                  <td class="px-3 py-2 tabular-nums">{{ formatMs(row.old_p90_ttft_ms) }} → {{ formatMs(row.new_p90_ttft_ms) }}</td>
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
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import {
  getPriorityConfig, savePriorityConfig, getPrioritySnapshot, getPriorityLatency,
  recomputePriorityScheduling, getPriorityStickyBindings, clearPriorityStickyBindings,
  type PrioritySchedulingConfig, type PrioritySnapshot, type PriorityCandidate,
  type PriorityLatencySeries, type PriorityLatencyAccount,
  type PrioritySchedulingRecomputeResult, type PriorityStickyBindings,
} from '@/api/admin/priorityScheduling'
function selectedLatency(row: PriorityCandidate) {
  if (row.selected_ttft_ms !== undefined) return row.selected_ttft_ms
  return (row.latency_percentile ?? 90) === 90 && (row.latency_measurement ?? 'legacy_forward') === 'legacy_forward' ? row.p90_ttft_ms : null
}
function errorSamplesLow(row: PriorityCandidate) {
  const observed = (row.error_successes ?? 0) + (row.error_failures ?? 0)
  return observed > 0 && observed < (row.error_prior_samples ?? 0)
}
const tailSampleFloor = (q: number) => Math.max(20, Math.ceil(500 / (100 - q)))
const { t } = useI18n(), auth = useAuthStore()
const draft = ref<PrioritySchedulingConfig | null>(null), snapshot = ref<PrioritySnapshot | null>(null)
const groups = ref(''), modelsText = ref(''), error = ref(''), snapshotError = ref(''), notice = ref('')
const loading = ref(false), saving = ref(false), refreshing = ref(false)
const latency = ref<PriorityLatencySeries | null>(null), latencyMinutes = ref(60), latencyLoading = ref(false), latencyError = ref('')
const recomputeResult = ref<PrioritySchedulingRecomputeResult | null>(null), recomputeLoading = ref(false), recomputeError = ref('')
const stickyGroupID = ref(2), sticky = ref<PriorityStickyBindings | null>(null), stickyLoading = ref(false), stickyError = ref(''), stickyNotice = ref('')
const modes = ['experience', 'balanced', 'profit', 'custom'] as const
const presets = { experience: [40,35,20,5], balanced: [30,25,25,20], profit: [20,15,20,45] }
const weightKeys = ['quality_weight','latency_weight','load_weight','cost_weight'] as const
const fields = [
  { key: 'error_rate_window_minutes', min: 1, max: 60 },
  { key: 'error_rate_prior_samples', min: 1, max: 1000 },
  { key: 'recovery_exploration_percent', min: 0, max: 10 },
  { key: 'target_ttft_ms', min: 100, max: 120000 }, { key: 'latency_penalty_minutes', min: 0, max: 1440 }, { key: 'max_load_percent', min: 10, max: 100 },
  { key: 'min_quality_percent', min: 0, max: 100 }, { key: 'window_minutes', min: 5, max: 1440 },
  { key: 'min_samples', min: 1, max: 1000 }, { key: 'quality_max_age_hours', min: 1, max: 168 }
] as const
let alive = true, generation = 0, recomputeRequestGeneration = 0, stickyRequestGeneration = 0, snapshotRequestGeneration = 0, latencyRequestGeneration = 0
let historyTimer: ReturnType<typeof setTimeout> | undefined
function stopHistoryRefresh() { clearTimeout(historyTimer); historyTimer = undefined }
const active = (version: number) => alive && generation === version
const latencyRows = computed(() => [...(latency.value?.accounts ?? [])].sort((a, b) => bucketMetric(b) - bucketMetric(a)))
const latencyBuckets = computed(() => {
  const buckets = new Set<string>()
  for (const account of latency.value?.accounts ?? []) for (const bucket of account.buckets ?? []) buckets.add(bucket.bucket)
  return [...buckets].sort()
})
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
    snapshotScope(result.before) === snapshotScope(result.after) &&
    result.after.history_status === 'ready' && result.after.history_ready && !result.after.history_refreshing && !result.after.history_error
})
function metricValue(item: { selected_ttft_ms?: number; p50_ttft_ms: number; p70_ttft_ms?: number; p90_ttft_ms: number; p95_ttft_ms?: number; p99_ttft_ms: number }, percentile: number) {
  if (item.selected_ttft_ms != null) return item.selected_ttft_ms
  if (percentile === 50) return item.p50_ttft_ms
  if (percentile === 70) return item.p70_ttft_ms ?? item.p90_ttft_ms
  if (percentile === 95) return item.p95_ttft_ms ?? item.p90_ttft_ms
  if (percentile === 99) return item.p99_ttft_ms
  return item.p90_ttft_ms
}
function bucketMetric(account: PriorityLatencyAccount) {
  return account.selected_ttft_ms != null && account.selected_ttft_ms > 0 ? account.selected_ttft_ms : metricValue(account, account.latency_percentile ?? latency.value?.latency_percentile ?? 90)
}
function bucketLatency(account: PriorityLatencyAccount, bucket: string) {
  const value = (account.buckets ?? []).find(item => item.bucket === bucket)
  return value ? value.selected_ttft_ms != null && value.selected_ttft_ms > 0 ? value.selected_ttft_ms : metricValue(value, account.latency_percentile ?? latency.value?.latency_percentile ?? 90) : null
}
function bucketText(account: PriorityLatencyAccount, bucket: string) {
  const value = bucketLatency(account, bucket)
  return value == null || value <= 0 ? '—' : String(Math.round(value))
}
function formatBucket(value: string) {
  const date = new Date(value)
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}
function formatMs(value: number) {
  return value > 0 ? `${Math.round(value)} ms` : '—'
}
function formatRate(value: number) {
  return value > 0 ? `${(value * 100).toFixed(1)}%` : '—'
}
function latencyClass(value: number) {
  const target = draft.value?.target_ttft_ms ?? 3000
  if (!value) return 'text-gray-400'
  if (value <= target) return 'text-emerald-600'
  if (value <= target * 2) return 'text-amber-600'
  return 'text-red-600'
}
function tierLabel(tier?: string) {
  return t(`priorityScheduling.tiers.${tier || 'insufficient'}`)
}
function accountName(accountID: number) {
  return snapshot.value?.candidates.find(row => row.account_id === accountID)?.account_name ??
    latency.value?.accounts.find(row => row.account_id === accountID)?.account_name ?? `#${accountID}`
}
async function load() {
  if (loading.value) return
  const version = generation; loading.value = true; error.value = ''
  try {
    const config = await getPriorityConfig()
    if (!active(version)) return
    const { teams: _legacyTeams, ...currentConfig } = config as PrioritySchedulingConfig & { teams?: unknown }
    draft.value = { ...currentConfig, error_rate_weight: currentConfig.error_rate_weight ?? 0, error_rate_window_minutes: currentConfig.error_rate_window_minutes ?? 10, error_rate_prior_samples: currentConfig.error_rate_prior_samples ?? 5, latency_percentile: currentConfig.latency_percentile ?? 90, latency_measurement: currentConfig.latency_measurement ?? 'legacy_forward', recovery_exploration_percent: currentConfig.recovery_exploration_percent ?? 0, latency_penalty_mode: currentConfig.latency_penalty_mode ?? 'deprioritize', latency_penalty_minutes: currentConfig.latency_penalty_minutes ?? 10, balance_protocols: currentConfig.balance_protocols ?? true, oauth_quota_priority: currentConfig.oauth_quota_priority ?? false, oauth_quota_threshold: currentConfig.oauth_quota_threshold ?? 90 }; groups.value = (config.group_ids ?? []).join(', '); modelsText.value = (config.models ?? []).join('\n')
    const configuredGroup = config.group_ids?.[0]
    if (configuredGroup && configuredGroup > 0) stickyGroupID.value = configuredGroup
  } catch { if (active(version)) error.value = t('priorityScheduling.error') }
  finally { if (active(version)) loading.value = false }
}
async function save() {
  if (!draft.value || saving.value) return
  const ids = groups.value.trim() ? groups.value.split(/[,，]/).map(v => Number(v.trim())) : []
  if (ids.some(id => !Number.isSafeInteger(id) || id <= 0)) { error.value = t('priorityScheduling.invalid'); return }
  const version = generation; saving.value = true; error.value = ''; notice.value = ''
  try {
    const config = await savePriorityConfig({ ...draft.value, group_ids: [...new Set(ids)], models: [...new Set(modelsText.value.split('\n').map(v => v.trim()).filter(Boolean))] })
    if (active(version)) { draft.value = config; notice.value = t('priorityScheduling.saved') }
  } catch { if (active(version)) error.value = t('priorityScheduling.error') }
  finally { if (active(version)) saving.value = false }
}
async function refresh() {
  stopHistoryRefresh()
  recomputeRequestGeneration++
  recomputeResult.value = null
  await loadSnapshot(0)
}
async function loadSnapshot(attempt: number) {
  if (refreshing.value) return
  const request = ++snapshotRequestGeneration
  const priorScope = snapshotScope(snapshot.value)
  recomputeRequestGeneration++
  recomputeLoading.value = false
  const version = generation; refreshing.value = true; snapshotError.value = ''
  try {
    const value = await getPrioritySnapshot()
    if (active(version) && request === snapshotRequestGeneration) {
      snapshot.value = value
      if (snapshotScope(value) !== priorScope) {
        recomputeResult.value = null
        latencyRequestGeneration++; latency.value = null; latencyLoading.value = false
        if (snapshotScope(value)) void loadLatency()
      }
      // Short follow-ups collect an in-flight query; subsequent polling sees
      // usage committed after a successful empty read. Only one timer/request
      // exists, and hidden tabs pause reads. Backend cache bounds SQL work.
      const pending = value?.history_refreshing || value?.history_status === 'loading' || value?.history_status === 'partial'
      // Also discover the first selection when this page opened before any pool
      // existed. A null snapshot schedules no history SQL on the server.
      scheduleHistoryRefresh(version, pending && attempt < 3 ? 1000 : 5000, pending ? attempt + 1 : 0)
    }
  }
  catch { if (active(version) && request === snapshotRequestGeneration) { snapshotError.value = t('priorityScheduling.error'); scheduleHistoryRefresh(version, 15000, 0) } }
  finally { if (active(version) && request === snapshotRequestGeneration) refreshing.value = false }
}
function scheduleHistoryRefresh(version: number, delay: number, attempt: number) {
  stopHistoryRefresh()
  historyTimer = setTimeout(() => {
    if (!active(version)) return
    if (document.visibilityState === 'hidden') { scheduleHistoryRefresh(version, 5000, attempt); return }
    void loadSnapshot(attempt)
  }, delay)
}
async function loadLatency() {
  const version = generation, request = ++latencyRequestGeneration, scope = snapshotScope(snapshot.value), minutes = latencyMinutes.value
  latency.value = null
  latencyLoading.value = true; latencyError.value = ''
  try { const value = await getPriorityLatency(minutes, 5); if (active(version) && request === latencyRequestGeneration && minutes === latencyMinutes.value && scope === snapshotScope(snapshot.value) && value.model === snapshot.value?.model && value.group_id === snapshot.value?.group_id) latency.value = value }
  catch { if (active(version) && request === latencyRequestGeneration && scope === snapshotScope(snapshot.value)) latencyError.value = t('priorityScheduling.latencyPanel.error') }
  finally { if (active(version) && request === latencyRequestGeneration) latencyLoading.value = false }
}
async function loadSticky(requestedGroupID?: number) {
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
  if (recomputeLoading.value) return
  const version = generation, request = ++recomputeRequestGeneration, snapshotRequest = snapshotRequestGeneration, scope = snapshotScope(snapshot.value)
  recomputeResult.value = null; recomputeLoading.value = true; recomputeError.value = ''
  try {
    const result = await recomputePriorityScheduling()
    if (!active(version) || request !== recomputeRequestGeneration || snapshotRequest !== snapshotRequestGeneration) return
    const resultScope = snapshotScope(result.before)
    if (result.timed_out || !result.before || !result.after || !scope || resultScope !== scope || snapshotScope(result.after) !== resultScope || result.after.history_status !== 'ready' || !result.after.history_ready || result.after.history_refreshing || result.after.history_error) {
      recomputeError.value = t('priorityScheduling.recompute.error')
      return
    }
    recomputeResult.value = result
    snapshot.value = result.after
    void loadLatency()
    if (result.after.group_id != null && result.after.group_id > 0) void loadSticky(result.after.group_id)
  } catch { if (active(version) && request === recomputeRequestGeneration && snapshotRequest === snapshotRequestGeneration) recomputeError.value = t('priorityScheduling.recompute.error') }
  finally { if (active(version) && request === recomputeRequestGeneration && snapshotRequest === snapshotRequestGeneration) recomputeLoading.value = false }
}
async function clearSticky(accountIDs: number[], requestedGroupID?: number) {
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
function clearChangedSticky() {
  const result = recomputeResult.value
  if (recomputeCanClearSticky.value && result?.after?.group_id != null) void clearSticky(changedDiffRows.value.map(row => row.account_id), result.after.group_id)
}
watch(stickyGroupID, () => {
  stickyRequestGeneration++
  sticky.value = null; stickyError.value = ''; stickyNotice.value = ''; stickyLoading.value = false
}, { flush: 'sync' })
watch(latencyMinutes, () => { void loadLatency() })
watch(() => auth.user ? `${auth.user.id}:${auth.user.role}` : '', () => {
  stopHistoryRefresh(); generation++; draft.value = null; snapshot.value = null
  groups.value = modelsText.value = error.value = notice.value = snapshotError.value = ''
  latency.value = null; latencyError.value = ''; latencyLoading.value = false
  recomputeResult.value = null; recomputeError.value = ''; recomputeLoading.value = false
  recomputeRequestGeneration++; stickyRequestGeneration++; snapshotRequestGeneration++; latencyRequestGeneration++
  sticky.value = null; stickyError.value = ''; stickyNotice.value = ''; stickyLoading.value = false; stickyGroupID.value = 2
  loading.value = saving.value = refreshing.value = false
}, { flush: 'sync' })
onMounted(() => { void load().then(() => { void loadSticky() }); void refresh(); void loadLatency() })
onBeforeUnmount(() => { stopHistoryRefresh(); alive = false; generation++ })
</script>
<style scoped>
.card { @apply rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.field-title { @apply mb-2 block text-xs font-medium text-gray-600 dark:text-gray-300; }
input[type=checkbox] { @apply h-4 w-4 rounded text-primary-600; }
button:disabled { @apply cursor-not-allowed opacity-50; }
</style>
