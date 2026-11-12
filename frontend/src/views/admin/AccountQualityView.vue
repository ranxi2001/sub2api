<template>
  <AppLayout>
    <div class="quality-workspace">
      <SmartOpsNav />
      <div class="workspace-heading">
        <div><p class="workspace-eyebrow">{{ t('qualityOps.workspaceLabel') }}</p><h2>{{ t('qualityOps.workspaceTitle') }}</h2><p class="workspace-subtitle">{{ t('qualityOps.workspaceHint') }}</p></div>
        <div class="heading-actions"><span class="updated-label" aria-live="polite">{{ store.updatedAt ? t('qualityOps.updatedAt', { time: clock(store.updatedAt) }) : t('qualityOps.loading') }}</span><button class="btn btn-secondary" :disabled="refreshing" @click="load"><Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />{{ t('qualityOps.refresh') }}</button><button class="btn btn-primary" data-testid="quality-create-rule" @click="newPlan"><Icon name="plus" size="sm" />{{ t('qualityOps.create') }}</button></div>
      </div>
      <div v-if="error || notice" class="workspace-message" :class="error ? 'message-error' : 'message-success'" :role="error ? 'alert' : 'status'">{{ error || notice }}<button :aria-label="t('qualityOps.dismiss')" @click="error = ''; notice = ''"><Icon name="x" size="sm" /></button></div>
      <div class="workspace-summary">
        <div class="summary-cell"><span class="summary-icon"><Icon name="users" size="md" /></span><div><span>{{ t('qualityOps.rulesTotal') }}</span><strong>{{ store.rulesLoaded ? plans.length : '—' }}</strong></div></div>
        <div class="summary-cell"><span class="summary-icon is-teal"><Icon name="play" size="md" /></span><div><span>{{ t('qualityOps.rulesActive') }}</span><strong>{{ store.rulesLoaded ? enabledCount : '—' }}<small v-if="store.rulesLoaded"> / {{ plans.length }}</small></strong></div></div>
        <div class="summary-cell"><span class="summary-icon is-amber"><Icon name="exclamationCircle" size="md" /></span><div><span>{{ t('qualityOps.latestAttention') }}</span><strong>{{ store.operationsLoaded ? attentionCount : '—' }}</strong></div><button v-if="attentionCount" class="summary-link" @click="store.selectedPlanId = null; store.operationFilter = 'attention'">{{ t('qualityOps.view') }}<Icon name="arrowRight" size="xs" /></button></div>
        <div class="summary-cell"><span class="summary-icon"><Icon name="document" size="md" /></span><div><span>{{ t('qualityOps.loadedRounds') }}</span><strong>{{ store.operationsLoaded ? operations.length : '—' }}</strong></div></div>
      </div>
      <div class="workspace-columns">
        <section class="workspace-panel rules-panel" aria-labelledby="quality-rules-heading">
          <header class="panel-heading"><div><h3 id="quality-rules-heading">{{ t('qualityOps.ruleLibrary') }}<span class="count-label">{{ plans.length }}</span></h3><p>{{ t('qualityOps.ruleLibraryHint') }}</p></div></header>
          <div class="rule-search"><Icon name="search" size="sm" /><input v-model="store.search" :aria-label="t('qualityOps.search')" :placeholder="t('qualityOps.searchRule')" /><button v-if="store.search" :aria-label="t('qualityOps.clearSearch')" @click="store.search = ''"><Icon name="x" size="sm" /></button></div>
          <div class="rule-selection-toolbar">
            <label class="rule-check-all"><input type="checkbox" data-testid="quality-select-rules" :checked="allFilteredSelected" :indeterminate="someFilteredSelected && !allFilteredSelected" :disabled="!filteredPlans.length || busy" @change="toggleFilteredSelection" />{{ t('qualityOps.selectFilteredRules') }}</label>
            <span aria-live="polite">{{ t('qualityOps.rulesSelected', { count: selectedRuleIds.length }) }}</span>
            <button v-if="selectedRuleIds.length" :disabled="busy" data-testid="quality-clear-rules" @click="selectedRuleIds = []">{{ t('qualityOps.clearAccountSelection') }}</button>
            <div class="rule-bulk-actions">
              <button class="btn btn-primary bulk-edit-button" data-testid="quality-bulk-edit" :disabled="!plans.length || busy || selectedRulesPending" @click="editSelectedRules"><Icon name="edit" size="sm" />{{ t('qualityOps.bulkEdit') }}</button>
              <button class="btn bulk-delete-button" data-testid="quality-bulk-delete" :disabled="!selectedRuleIds.length || busy || selectedRulesPending" @click="requestDelete(selectedRuleIds)">{{ t('qualityOps.bulkDelete') }}</button>
            </div>
          </div>
          <button class="all-accounts" :class="{ selected: store.selectedPlanId === null }" :aria-pressed="store.selectedPlanId === null" @click="store.selectedPlanId = null"><Icon name="users" size="sm" />{{ t('qualityOps.allAccounts') }}<span>{{ plans.length }}</span></button>
          <div class="rules-scroll" data-testid="rules-scroll" :aria-busy="store.rulesLoading">
            <div v-if="store.rulesError" class="panel-error" role="alert">{{ store.rulesError }}<button @click="store.refreshRules(true)">{{ t('qualityOps.retry') }}</button></div>
            <div v-if="!store.rulesLoaded && store.rulesLoading" class="skeleton-stack" role="status" :aria-label="t('qualityOps.loading')"><div v-for="n in 4" :key="n" class="rule-skeleton"><span /><span /><span /></div></div>
            <section v-if="templates.length" class="template-section" data-testid="quality-templates" :aria-label="t('qualityOps.templateSection')">
              <p class="template-section-title">{{ t('qualityOps.templateSection') }}<span>{{ t('qualityOps.templateSectionHint') }}</span></p>
              <article v-for="tpl in templates" :key="tpl.id" class="template-card" :data-template-id="tpl.id">
                <div class="rule-card-top"><span class="account-avatar template-avatar"><Icon name="users" size="sm" /></span><span class="min-w-0 flex-1"><strong :title="templateFilterSummary(tpl)" data-testid="quality-template-filter">{{ templateFilterSummary(tpl) }}</strong><span class="rule-meta">{{ t('qualityOps.templateTag', { id: tpl.id }) }}<span>·</span>{{ t('qualityOps.templateCovered', { count: tpl.plan_ids.length }) }}</span></span><button class="state-toggle" :class="tpl.enabled ? 'state-enabled' : 'state-paused'" :disabled="busy || !!templatePending[tpl.id]" :title="t('qualityOps.templatePauseHint')" data-testid="quality-template-toggle" @click="toggleTemplate(tpl)"><span />{{ t(tpl.enabled ? 'qualityOps.activeShort' : 'qualityOps.paused') }}</button></div>
                <div class="rule-model"><code v-for="model in ruleModels(tpl)" :key="model">{{ model }}</code><span v-if="tpl.pelican_config?.question_kind === STATE_PROBE_QUESTION" class="probe-tag">{{ t('qualityOps.probeTag') }}</span></div>
                <div class="rule-target" :title="policyTarget(tpl.pelican_config?.quality)"><Icon name="shield" size="xs" /><span>{{ policyTarget(tpl.pelican_config?.quality) }}</span></div>
                <footer class="rule-actions"><button :disabled="busy || !!templatePending[tpl.id]" data-testid="quality-template-edit" @click="editTemplate(tpl)">{{ t('qualityOps.editShort') }}</button></footer>
              </article>
            </section>
            <article v-for="plan in filteredPlans" :key="plan.id" class="rule-card" :class="{ selected: store.selectedPlanId === plan.id, checked: selectedRuleIds.includes(plan.id) }" :data-plan-id="plan.id">
              <div class="rule-card-top"><label class="rule-checkbox"><input v-model="selectedRuleIds" type="checkbox" :value="plan.id" :disabled="busy || !!pending[plan.id] || plan.pelican_config?.quality?.action === 'openai_two_fa_admission'" :aria-label="t('qualityOps.selectRule', { account: name(plan), id: plan.id })" /></label><button class="rule-select" :aria-pressed="store.selectedPlanId === plan.id" @click="store.selectedPlanId = plan.id"><span class="account-avatar">{{ name(plan).slice(0, 1) }}</span><span class="min-w-0"><strong :title="name(plan)">{{ name(plan) }}</strong><span class="rule-meta">#{{ plan.account_id }}<span>·</span>{{ t('qualityOps.rule') }} {{ plan.id }}</span></span></button><button class="state-toggle" :class="plan.enabled ? 'state-enabled' : 'state-paused'" :disabled="busy || !!pending[plan.id] || plan.pelican_config?.quality?.action === 'openai_two_fa_admission'" :title="t(plan.enabled ? 'qualityOps.pause' : 'qualityOps.enable')" @click="toggle(plan)"><span />{{ t(plan.enabled ? 'qualityOps.activeShort' : 'qualityOps.paused') }}</button></div>
              <p v-if="plan.pelican_config?.rule_name" class="rule-target" :title="plan.pelican_config.rule_name" data-testid="quality-rule-name">{{ plan.pelican_config.rule_name }}</p>
              <div class="rule-model"><code v-for="model in ruleModels(plan)" :key="model">{{ model }}</code><span v-if="isProbePlan(plan)" class="probe-tag" data-testid="quality-probe-tag">{{ t('qualityOps.probeTag') }}</span><span v-if="planTemplate[plan.id]" class="template-tag" data-testid="quality-template-tag">{{ t('qualityOps.templateTag', { id: planTemplate[plan.id] }) }}</span><span v-if="running(plan)" class="running-label">{{ t('qualityOps.running') }}</span></div>
              <div class="rule-target" :title="planGroups(plan)"><Icon name="users" size="xs" /><span>{{ planGroups(plan) }}</span></div>
              <div class="rule-schedule"><span>{{ t('qualityOps.nextRun') }}</span><time :datetime="plan.next_run_at || undefined">{{ plan.enabled ? date(plan.next_run_at) : '—' }}</time></div>
              <div v-if="!isProbePlan(plan) && !plan.pelican_config?.quality?.judge" class="rule-warning">{{ t('qualityOps.configureJudge') }}</div>
              <footer class="rule-actions"><button @click="history(plan)"><Icon name="document" size="xs" />{{ t('qualityOps.historyShort') }}</button><button :disabled="!!pending[plan.id] || plan.pelican_config?.quality?.action === 'openai_two_fa_admission'" @click="edit(plan)">{{ t('qualityOps.editShort') }}</button><button :disabled="!!pending[plan.id] || !plan.enabled || running(plan)" @click="run(plan)"><Icon name="play" size="xs" />{{ pending[plan.id] === 'run' ? t('qualityOps.submitting') : t('qualityOps.runShort') }}</button><button data-testid="quality-rule-copy" :disabled="busy || !!pending[plan.id] || !canCopyRules || !plan.pelican_config?.quality || plan.pelican_config.quality.action === 'openai_two_fa_admission'" :title="t(!canCopyRules ? 'qualityOps.copyPermission' : plan.pelican_config?.quality?.action === 'openai_two_fa_admission' ? 'qualityOps.copyUnsupported' : 'qualityOps.copy')" @click="copyRule(plan)"><Icon name="copy" size="xs" />{{ t('qualityOps.copy') }}</button><button data-testid="quality-rule-save-preset" :disabled="busy || !!pending[plan.id] || !canCopyRules || !plan.pelican_config?.quality || plan.pelican_config.quality.action === 'openai_two_fa_admission'" :title="t(!canCopyRules ? 'qualityOps.reusePermission' : 'qualityOps.savePreset')" @click="saveRuleAsPreset(plan)">{{ t('qualityOps.savePreset') }}</button></footer>
            </article>
            <div v-if="store.rulesLoaded && !filteredPlans.length && (plans.length || !templates.length)" class="panel-empty"><Icon name="inbox" size="lg" /><p>{{ plans.length ? t('qualityOps.noMatchingRules') : t('qualityOps.empty') }}</p><button v-if="!plans.length" class="btn btn-secondary" @click="newPlan">{{ t('qualityOps.create') }}</button></div>
          </div>
        </section>
        <section class="workspace-panel operations-panel" aria-labelledby="quality-operations-heading">
          <header class="panel-heading"><div><h3 id="quality-operations-heading">{{ t('qualityOps.operations') }}</h3><p>{{ t('qualityOps.operationsSubtitle') }}</p></div><button class="icon-button" :disabled="store.operationsLoading" :aria-label="t('qualityOps.refreshOperations')" @click="refreshOperations"><Icon name="refresh" size="sm" :class="{ 'animate-spin': store.operationsLoading }" /></button></header>
          <div class="operations-toolbar"><div class="scope-label"><span class="scope-dot" />{{ selectedPlan ? name(selectedPlan) : t('qualityOps.allAccounts') }}<button v-if="selectedPlan" :aria-label="t('qualityOps.clearScope')" @click="store.selectedPlanId = null"><Icon name="x" size="xs" /></button></div><select v-model="store.operationFilter" :aria-label="t('qualityOps.filterRecords')"><option value="all">{{ t('qualityOps.allOutcomes') }}</option><option value="attention">{{ t('qualityOps.needsAttention') }}</option><option value="passed">{{ t('qualityOps.passedRounds') }}</option><option value="failed">{{ t('qualityOps.otherRounds') }}</option></select></div>
          <div v-if="store.operationsError" role="alert" class="panel-error">{{ store.operationsError }}<button @click="refreshOperations">{{ t('qualityOps.retry') }}</button></div>
          <div class="operations-scroll" data-testid="operations-scroll" :aria-busy="store.operationsLoading">
            <table class="operations-table"><thead><tr><th>{{ t('qualityOps.time') }}</th><th>{{ t('qualityOps.accounts') }}</th><th>{{ t('qualityOps.testResult') }}</th><th>{{ t('qualityOps.accountAction') }}</th><th><span class="sr-only">{{ t('qualityOps.details') }}</span></th></tr></thead><tbody>
              <template v-if="!store.operationsLoaded && store.operationsLoading"><tr v-for="n in 6" :key="`loading-${n}`" class="loading-row"><td v-for="c in 5" :key="c"><span class="cell-skeleton" /></td></tr></template>
              <tr v-for="operation in filteredOperations" :key="operation.id" :class="{ 'selected-row': detailOperation?.id === operation.id && !!historyPlan }" :data-operation-id="operation.id">
                <td class="time-cell" :data-label="t('qualityOps.time')"><strong>{{ clock(operation.started_at) }}</strong><span>{{ day(operation.started_at) }}</span></td>
                <td class="account-cell"><button :title="operation.account_name" @click="store.selectedPlanId = operation.plan_id"><strong>{{ operation.account_name || `#${operation.account_id}` }}</strong></button><span>{{ t('qualityOps.rule') }} {{ operation.plan_id }}<span class="mx-1">·</span>#{{ operation.account_id }}</span></td>
                <td :data-label="t('qualityOps.testResult')"><span class="test-count" :class="allPassed(operation) ? 'test-passed' : 'test-other'"><Icon :name="allPassed(operation) ? 'checkCircle' : 'exclamationCircle'" size="xs" />{{ operation.passed_count }} / {{ operation.total_count }}</span><span class="cell-secondary">{{ t(operation.status === 'skipped' ? 'qualityOps.skipped' : allPassed(operation) ? 'qualityOps.roundPassed' : 'qualityOps.roundNotPassed') }}<template v-if="operation.skipped_count"> · {{ t('qualityOps.skippedCount', { n: operation.skipped_count }) }}</template></span></td>
                <td class="action-cell" :data-label="t('qualityOps.accountAction')"><button class="outcome-badge" :class="tone(operation.quality_action)" @click="operationDetails(operation)"><span />{{ operationLabel(operation) }}</button><span class="cell-secondary" :title="operationGroups(operation)">{{ operationGroups(operation) }}</span></td>
                <td class="detail-cell"><button class="detail-button" :aria-label="t('qualityOps.openRound', { account: operation.account_name, time: date(operation.started_at) })" @click="operationDetails(operation)"><span>{{ t('qualityOps.details') }}</span><Icon name="arrowRight" size="sm" /></button></td>
              </tr>
            </tbody></table>
            <div v-if="store.operationsLoaded && !filteredOperations.length" class="panel-empty"><Icon name="document" size="lg" /><h4>{{ t('qualityOps.noResults') }}</h4><p>{{ t('qualityOps.filteredEmptyHint') }}</p></div>
          </div>
          <footer class="operations-footer"><span>{{ t('qualityOps.showingLoaded', { shown: filteredOperations.length, loaded: operations.length }) }}</span><button v-if="store.cursor" :disabled="store.moreLoading || store.operationsLoading" @click="moreOperations">{{ store.moreLoading ? t('qualityOps.loading') : t('qualityOps.loadMore') }}<Icon name="arrowDown" size="xs" /></button><span v-else>{{ t('qualityOps.loadedEnd') }}</span></footer>
        </section>
      </div>
    </div>

    <!-- Existing rule editor moves into a focused drawer instead of shifting both lists. -->
    <BaseDialog :show="showForm" :title="savingPreset ? t(editingPreset ? 'qualityOps.editPreset' : 'qualityOps.savePreset') : copying ? t('qualityOps.copy') : bulkEditing ? t('qualityOps.bulkEditTitle', { count: bulkRuleIds.length }) : editingTemplate ? t('qualityOps.editTemplate') : editing ? t('qualityOps.edit') : t('qualityOps.create')" placement="right" width="wide" :close-on-escape="!busy && !deleteTargets.length && !templateDeleteTarget && !discardPrompt" @close="closeForm">
      <p v-if="error" role="alert" class="editor-error">{{ error }}</p>
      <p v-if="store.groupsError" role="alert" class="editor-error">{{ store.groupsError }} <button class="underline" @click="store.refreshGroups">{{ t('qualityOps.retry') }}</button></p>
            <form id="quality-rule-form" class="quality-editor space-y-5" @submit.prevent="save"><fieldset :disabled="busy" class="space-y-5">

        <div v-if="bulkEditing" class="bulk-editor-intro">
          <p>{{ t('qualityOps.bulkEditHint') }}</p>
          <details><summary>{{ t('qualityOps.rulesSelected', { count: bulkRuleIds.length }) }}</summary><ul><li v-for="id in bulkRuleIds" :key="id">{{ name(plans.find(plan => plan.id === id) || { id } as ScheduledTestPlan) }} · {{ t('qualityOps.rule') }} {{ id }}</li></ul></details>
          <div class="bulk-fields"><label v-for="field in qualityRuleFields" :key="field"><input v-model="bulkFields" type="checkbox" :value="field" :data-testid="`quality-bulk-field-${field}`" />{{ t(`qualityOps.bulkFields.${field}`) }}</label></div>
          <p v-if="bulkProgress" role="status">{{ bulkProgress }}</p>
        </div>
        <p v-if="copying" class="template-hint" data-testid="quality-copy-hint">{{ t('qualityOps.copyHint') }}</p>
        <template v-if="savingPreset">
          <p class="template-hint" data-testid="quality-save-preset-hint">{{ t('qualityOps.savePresetHint') }}</p>
          <label class="block space-y-1"><span>{{ t('qualityOps.presetName') }}</span><input v-model.trim="presetName" required maxlength="100" class="input" data-testid="quality-preset-name" /></label>
        </template>
        <fieldset v-if="!editingTemplate && !savingPreset" class="space-y-3 rounded-lg border p-3 dark:border-dark-600" data-testid="quality-reuse-controls">
          <legend>{{ t('qualityOps.reuseConfig') }}</legend>
          <div class="space-y-2" data-testid="quality-account-source-option">
            <p class="font-medium">{{ t('qualityOps.copy') }}</p>
            <label class="block space-y-1"><span>{{ t('qualityOps.sourceAccount') }}</span><select v-model="sourceAccountId" class="input" data-testid="quality-source-account" :disabled="!canCopyRules" @change="sourceRuleId = ''"><option value="">{{ t('qualityOps.selectSourceAccount') }}</option><option v-for="account in sourceAccounts" :key="account.id" :value="String(account.id)">{{ account.name }} #{{ account.id }}</option></select></label>
            <label class="block space-y-1"><span>{{ t('qualityOps.sourceRule') }}</span><select v-model="sourceRuleId" class="input" data-testid="quality-source-rule" :disabled="!canCopyRules || !sourceAccountId"><option value="">{{ t('qualityOps.selectSourceRule') }}</option><option v-for="rule in sourceRules" :key="rule.id" :value="String(rule.id)">#{{ rule.id }} · {{ rule.pelican_config?.rule_name || rule.model_id }} · {{ planGroups(rule) }}</option></select></label>
            <button type="button" class="btn btn-secondary" data-testid="quality-apply-source" :disabled="!canCopyRules || !sourceRuleId" @click="applySourceRule">{{ t('qualityOps.copy') }}</button>
          </div>
          <p v-if="store.presetsError" role="alert">{{ t('qualityOps.presetsLoadFailed') }} {{ store.presetsError }} <button type="button" class="underline" @click="store.refreshPresets">{{ t('qualityOps.retry') }}</button></p>
          <p class="font-medium">{{ t('qualityOps.selectPreset') }}</p>
          <label class="block space-y-1"><span>{{ t('qualityOps.savedPresets') }}</span><select v-model="selectedPresetId" class="input" data-testid="quality-preset-select" :disabled="store.presetsLoading || !canCopyRules"><option value="">{{ t(store.presetsLoading ? 'qualityOps.loading' : 'qualityOps.selectPreset') }}</option><option v-for="preset in presets" :key="preset.id" :value="String(preset.id)">{{ preset.name }}</option></select></label>
          <button type="button" class="btn btn-secondary" data-testid="quality-edit-preset" :disabled="!selectedPresetId || !canCopyRules || store.presetsLoading" @click="editPreset">{{ t('qualityOps.editPreset') }}</button>
          <button type="button" class="btn btn-secondary" data-testid="quality-apply-preset" :disabled="!selectedPresetId || !canCopyRules || store.presetsLoading" @click="applyPreset">{{ t('qualityOps.applyPreset') }}</button>
          <p class="text-xs text-gray-500">{{ t('qualityOps.reuseHint') }}</p>
          <section v-if="reuseSourceLabel" class="space-y-3 border-t pt-3" data-testid="quality-reuse-preview">
            <p class="font-medium">{{ t('qualityOps.reusePreview') }} · {{ reuseSourceLabel }}</p>
            <p v-if="editing && reuseEditingPlan" data-testid="quality-reuse-edit-target">{{ t('qualityOps.reuseTarget') }}: {{ name(reuseEditingPlan) }} · #{{ reuseEditingPlan.account_id }} · {{ t('qualityOps.rule') }} #{{ editing }}</p>
            <p v-if="bulkEditing" class="text-sm" data-testid="quality-reuse-bulk-hint">{{ t('qualityOps.reuseBulkHint') }}</p>
            <table class="w-full table-fixed text-left text-xs"><thead><tr><th>{{ t('qualityOps.reuseField') }}</th><th v-if="!bulkEditing">{{ t('qualityOps.reuseBefore') }}</th><th>{{ t('qualityOps.reuseDraft') }}</th></tr></thead><tbody><tr v-for="field in qualityRuleFields" :key="field" :data-reuse-field="field"><th class="p-2"><label v-if="bulkEditing" class="flex items-center gap-2"><input v-model="bulkFields" type="checkbox" :value="field" :data-testid="`quality-reuse-field-${field}`" />{{ t(`qualityOps.bulkFields.${field}`) }}</label><template v-else>{{ t(`qualityOps.bulkFields.${field}`) }}</template></th><td v-if="!bulkEditing" class="p-2"><pre class="max-h-24 overflow-auto whitespace-pre-wrap break-words">{{ reuseBefore ? reuseFieldSummary(reuseBefore, field) : '—' }}</pre></td><td class="p-2"><pre class="max-h-24 overflow-auto whitespace-pre-wrap break-words">{{ reuseFieldSummary(form, field) }}</pre></td></tr><template v-if="!bulkEditing"><tr><th class="p-2">{{ t('qualityOps.maxResults') }}</th><td class="p-2">{{ reuseBefore?.max_results }}</td><td class="p-2">{{ form.max_results }}</td></tr><tr><th class="p-2">{{ t('qualityOps.trigger5xx') }}</th><td class="p-2">{{ t(reuseBefore?.pelican_config.quality.trigger_on_upstream_5xx ? 'qualityOps.on' : 'qualityOps.off') }}</td><td class="p-2">{{ t(form.pelican_config.quality.trigger_on_upstream_5xx ? 'qualityOps.on' : 'qualityOps.off') }}</td></tr></template></tbody></table>
            <template v-if="bulkEditing">
              <div class="flex flex-wrap gap-3">
                <button type="button" class="btn btn-secondary btn-sm" data-testid="quality-reuse-select-all" @click="bulkFields = [...qualityRuleFields]">{{ t('qualityOps.reuseSelectAllFields') }}</button>
                <button type="button" class="text-sm text-primary-600" data-testid="quality-reuse-clear-fields" @click="bulkFields = []">{{ t('qualityOps.reuseClearFields') }}</button>
              </div>
              <p v-if="!bulkFields.length" role="status" class="text-sm text-amber-700 dark:text-amber-300" data-testid="quality-reuse-empty-fields">{{ t('qualityOps.reuseSelectFieldsHint') }}</p>
              <p>{{ t('qualityOps.reuseSelectedFields') }}: {{ bulkFields.map(field => t(`qualityOps.bulkFields.${field}`)).join(' / ') || '—' }}</p>
              <details v-for="target in reuseTargets" :key="target.id" :data-reuse-target="target.id"><summary>{{ target.name }} · #{{ target.accountId }} · {{ t('qualityOps.rule') }} #{{ target.id }}</summary><p v-if="target.error" role="alert">{{ target.error }}</p><table v-else class="w-full table-fixed text-left text-xs"><thead><tr><th>{{ t('qualityOps.reuseField') }}</th><th>{{ t('qualityOps.reuseBefore') }}</th><th>{{ t('qualityOps.reuseDraft') }}</th></tr></thead><tbody><tr v-for="change in target.changes" :key="change.field"><th class="p-2">{{ t(`qualityOps.bulkFields.${change.field}`) }}</th><td class="p-2"><pre class="max-h-24 overflow-auto whitespace-pre-wrap break-words">{{ change.before }}</pre></td><td class="p-2"><pre class="max-h-24 overflow-auto whitespace-pre-wrap break-words">{{ change.after }}</pre></td></tr></tbody></table></details>
              <label class="flex items-start gap-2"><input type="checkbox" data-testid="quality-reuse-confirm" :checked="reuseConfirmed" :disabled="!bulkFields.length || !bulkRuleIds.length || reuseTargets.some(target => target.error)" @change="confirmReuse" />{{ t('qualityOps.reuseConfirm', { count: bulkRuleIds.length }) }}</label>
            </template>
          </section>
        </fieldset>
        <p v-if="reuseSourceLabel" class="template-hint" data-testid="quality-draft-source">{{ t('qualityOps.draftSource') }}: {{ reuseSourceLabel }} · {{ t('qualityOps.draftNotSaved') }}</p>
        <p v-if="editing && reuseEditingPlan" data-testid="quality-saved-models">{{ t('qualityOps.savedModels') }}: {{ ruleModels(reuseEditingPlan).join(', ') }} · {{ t('qualityOps.nameIsLabel') }}</p>
        <QualityOwnershipReview v-if="editing" :plan-id="editing" :draft-action="form.pelican_config.quality.action" :draft-models="[form.model_id, ...form.pelican_config.model_ids]" :draft-group-ids="form.pelican_config.quality.remove_group_ids" @blocked="ownershipBlocked = $event" @reconciled="store.refreshRules(true)" :disabled="busy || !canCopyRules" @working="ownershipWorking = $event" @model-removed="ownedModelRemoved" />
        <label v-if="!bulkEditing && !savingPreset" class="block space-y-1"><span>{{ t('qualityOps.ruleName') }}</span><input v-model.trim="form.pelican_config.rule_name" maxlength="100" class="input" data-testid="quality-rule-name-input" /></label>
        <p v-if="editingTemplate" class="template-hint" data-testid="quality-template-edit-hint">{{ t('qualityOps.templateEditHint', { count: editingTemplateRecord?.plan_ids.length ?? 0 }) }}</p>
        <p v-else-if="editing && planTemplate[editing]" class="template-hint" data-testid="quality-plan-template-hint">{{ t('qualityOps.planFromTemplate', { id: planTemplate[editing] }) }}</p>
        <QualityAccountSelector v-if="!editing && !savingPreset" v-model="pickerAccountIds" v-model:search="search" v-model:group="accountGroup" v-model:type="accountType" data-testid="quality-account-picker"
          v-model:statuses="accountStatuses" v-model:scope="accountScope"
          :accounts="accounts" :groups="groups" :accounts-loading="accountsLoading" :selecting-accounts="selectingAccounts" :accounts-error="accountsError"
          :account-page="accountPage" :account-pages="accountPages" :accounts-total="accountsTotal" :bulk="bulkEditing" :scope-locked="!!editingTemplate" :hide-scope="copying" :selection-hint="copying ? t('qualityOps.copyAccountHint') : undefined" :disabled-reason="accountDisabledReason"
          @search="searchAccounts" @select-page="selectCurrentPage" @select-all="selectMatchingAccounts" @clear="clearAccountSelection" />
        <div v-if="editsField('test')" class="space-y-2">
          <label class="block space-y-1"><span>{{ t('qualityOps.questionKind') }}</span><select v-model="form.pelican_config.question_kind" class="input" data-testid="quality-question-kind" @change="selectQuestionKind"><option value="candy">{{ t('qualityOps.questionCandy') }}</option><option value="random_reasoning">{{ t('qualityOps.questionRandom') }}</option><option :value="STATE_PROBE_QUESTION">{{ t('qualityOps.questionStateProbe') }}</option></select></label>
          <p v-if="isProbe" class="text-sm text-gray-500" data-testid="quality-probe-hint">{{ t('qualityOps.probeHint') }}</p>
          <label v-else class="block space-y-1"><span>{{ t('qualityOps.testChannel') }}</span><select v-model="form.pelican_config.test_channel" class="input" data-testid="quality-test-channel" @change="selectTestChannel"><option value="account">{{ t('qualityOps.accountChannel') }}</option><option value="bps">{{ t('qualityOps.bpsChannel') }}</option></select></label>
          <p v-if="form.pelican_config.test_channel === 'bps'" class="text-sm text-gray-500" data-testid="quality-bps-observation-hint">{{ t('qualityOps.bpsObservationHint') }}</p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div v-if="editsField('model')" class="space-y-2 sm:col-span-2">
            <label class="block space-y-1"><span>{{ t(isProbe ? 'qualityOps.model' : 'qualityOps.models') }}</span><input v-model.trim="form.model_id" required maxlength="100" class="input" placeholder="gpt-6-astra" /></label>
            <template v-if="!isProbe"><div v-for="(_, index) in form.pelican_config.model_ids" :key="index" class="flex gap-2">
              <input v-model.trim="form.pelican_config.model_ids[index]" required maxlength="100" class="input flex-1" data-testid="quality-extra-model" :aria-label="t('qualityOps.modelNumber', { n: index + 2 })" placeholder="gpt-6-sol" />
              <button type="button" class="btn btn-secondary" :aria-label="t('qualityOps.removeModelNumber', { n: index + 2 })" @click="form.pelican_config.model_ids.splice(index, 1)"><Icon name="x" size="sm" /></button>
            </div></template>
            <button v-if="!isProbe" type="button" class="btn btn-secondary btn-sm" data-testid="quality-add-model" :disabled="form.pelican_config.model_ids.length >= 49" @click="form.pelican_config.model_ids.push('')"><Icon name="plus" size="xs" />{{ t('qualityOps.addModel') }}</button>
            <QualityModelShortcuts :selected-models="selectedTestModels" :user-id="auth.user?.id" :disabled="!isProbe && !canAddTestModel" @select="addTestModel" />
            <p class="text-sm text-gray-500">{{ t(isProbe ? 'qualityOps.probeSingleModel' : 'qualityOps.modelsHint') }}</p>
          </div>
          <template v-if="editsField('schedule')">
            <QualityProbeSchedule v-if="isProbe" v-model="form.cron_expression" />
            <label v-else class="space-y-1"><span>{{ t('qualityOps.cron') }}</span><input v-model.trim="form.cron_expression" required class="input" placeholder="*/30 * * * *" /></label>
          </template>
          <template v-if="editsField('test') && !isProbe">
            <label class="space-y-1"><span>{{ t('qualityOps.effort') }}</span><select v-model="form.pelican_config.reasoning_effort" class="input"><option v-for="effort in ['minimal', 'low', 'medium', 'high', 'xhigh']" :key="effort">{{ effort }}</option></select></label>
            <label class="space-y-1"><span>{{ t('qualityOps.parallel') }}</span><input v-model.number="form.pelican_config.parallel_count" type="number" min="1" max="8" required class="input" /></label>
          </template>
        </div>
        <template v-if="editsField('test') && !isProbe">
        <p v-if="isRandom" class="text-sm text-gray-500" data-testid="quality-random-hint">{{ t('qualityOps.randomHint') }}</p>
        <template v-else>
        <div><div class="mb-2 flex items-center justify-between"><label for="quality-prompt">{{ t('qualityOps.prompt') }}</label><button type="button" class="text-sm text-primary-600" @click="useCandy">{{ t('qualityOps.candy') }}</button></div><textarea id="quality-prompt" v-model="form.pelican_config.prompt" required maxlength="32000" rows="5" class="input text-sm" /></div>
        <label class="block space-y-1"><span>{{ t('qualityOps.answer') }}</span><input v-model="form.pelican_config.quality.expected_answer" required maxlength="4000" class="input" /></label>
        </template>
        <fieldset class="space-y-3 rounded-lg border p-4 dark:border-dark-600">
          <legend class="px-2 font-medium">{{ t('qualityOps.judgeTitle') }}</legend>
          <label class="block space-y-1"><span>{{ t('qualityOps.judgeKind') }}</span>
            <select :value="form.pelican_config.quality.judge.kind || 'llm'" class="input" data-testid="quality-judge-kind" @change="selectJudgeKind">
              <option value="llm">{{ t('qualityOps.judgeLLM') }}</option>
              <option value="jev">{{ t('qualityOps.judgeJev') }}</option>
            </select>
          </label>
          <p v-if="form.pelican_config.quality.judge.kind === 'jev'" class="text-sm text-gray-500" data-testid="quality-jev-hint">{{ t('qualityOps.judgeJevHint') }}</p>
          <div v-else class="grid gap-4 sm:grid-cols-2">
            <label class="space-y-1"><span>{{ t('qualityOps.judgeGroup') }}</span>
              <select v-model.number="form.pelican_config.quality.judge.group_id" required class="input" @change="loadJudgeModels">
                <option disabled :value="0">{{ t('qualityOps.selectJudgeGroup') }}</option>
                <option v-for="group in groups.filter(g => g.status === 'active')" :key="group.id" :value="group.id">{{ group.name }} #{{ group.id }}</option>
              </select>
            </label>
            <label class="space-y-1"><span>{{ t('qualityOps.judgeModel') }}</span>
              <input v-model.trim="form.pelican_config.quality.judge.model_id" list="quality-judge-models" required maxlength="100" class="input" :placeholder="t('qualityOps.selectJudgeModel')" />
              <datalist id="quality-judge-models"><option v-for="model in judgeModels" :key="model" :value="model" /></datalist>
            </label>
          </div>
          <label class="block space-y-1"><span>{{ t('qualityOps.judgePrompt') }}</span><textarea v-model="form.pelican_config.quality.judge.prompt" required maxlength="16000" rows="3" class="input text-sm" /></label>
          <p class="text-sm text-gray-500">{{ t(form.pelican_config.quality.judge.kind === 'jev' ? 'qualityOps.jevGrading' : 'qualityOps.grading') }}</p>
        </fieldset>
        </template>
        <fieldset v-if="editsField('action') && form.pelican_config.test_channel !== 'bps'" class="space-y-3 rounded-lg border p-4 dark:border-dark-600">
          <legend class="px-2 font-medium">{{ t(isProbe ? 'qualityOps.probeFailureAction' : 'qualityOps.failureAction') }}</legend>
          <label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.action" type="radio" value="observe_only" data-testid="quality-action-observe-only" />{{ t('qualityOps.observeOnly') }}</label>
          <label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.action" type="radio" value="remove_groups" data-testid="quality-action-remove-groups" />{{ t('qualityOps.removeGroups') }}</label>
          <label v-if="!isProbe" class="flex items-center gap-2"><input v-model="form.pelican_config.quality.action" type="radio" value="isolate_models" data-testid="quality-action-isolate-models" />{{ t('qualityOps.isolateModels') }}</label>
          <p v-if="form.pelican_config.quality.action === 'isolate_models'" class="pl-6 text-sm text-gray-500" data-testid="quality-isolation-help">{{ t('qualityOps.isolationHelp') }}</p>
          <div v-if="qualityUsesGroups(form.pelican_config.quality.action)" class="grid max-h-40 gap-2 overflow-auto pl-6 sm:grid-cols-2">
            <label v-for="group in groups" :key="group.id" class="flex items-center gap-2 text-sm"><input v-model="form.pelican_config.quality.remove_group_ids" type="checkbox" :value="group.id" :data-testid="`quality-target-group-${group.id}`" />{{ group.name }} #{{ group.id }}</label>
          </div>
          <label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.action" type="radio" value="disable_scheduling" data-testid="quality-action-disable-scheduling" />{{ t('qualityOps.disableScheduling') }}</label>
          <label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.action" type="radio" value="remove_models" data-testid="quality-action-remove-models" />{{ t('qualityOps.removeModels') }}</label>
          <label v-if="form.pelican_config.quality.action === 'remove_models'" class="block space-y-1 pl-6"><span>{{ t('qualityOps.removeModelsInput') }}</span><input :value="removeModelsInput" class="input" data-testid="quality-remove-models-input" placeholder="gpt-6-astra, gpt-5.6-sol" @input="updateRemoveModelsInput" /><span class="block text-xs text-gray-500">{{ t('qualityOps.cooldownRecoveryHint') }}</span></label>
          <p v-if="form.pelican_config.quality.action === 'remove_models'" class="pl-6 text-sm text-gray-500" data-testid="quality-model-independence">{{ t('qualityOps.cooldownPerModelHint') }}</p>
          <label v-if="form.pelican_config.quality.action === 'remove_models'" class="block space-y-1 pl-6"><span>{{ t('qualityOps.recoveryConcurrency') }}</span><input v-model.number="form.pelican_config.quality.recovery_concurrency" type="number" min="1" max="10000" class="input" data-testid="quality-recovery-concurrency" /></label>
          <div v-if="qualityUsesRetestRest(form.pelican_config.quality.action)" class="space-y-1" data-testid="quality-retest-rest-setting">
            <div class="flex items-center gap-2"><label for="quality-retest-rest">{{ t('qualityOps.retestRest') }}</label><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.qualityRetestRest" :user-id="auth.user?.id" /></div>
            <input id="quality-retest-rest" v-model.number="form.pelican_config.quality.retest_rest_minutes" type="number" min="0" max="10080" step="1" required class="input" data-testid="quality-retest-rest" />
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.retestRestHint') }}</p>
          </div>
          <template v-if="isProbe">
            <label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.action" type="radio" value="enable_bps" data-testid="quality-action-enable-bps" />{{ t('qualityOps.enableBPS') }}</label>
            <QualityBPSSettings v-if="form.pelican_config.quality.action === 'enable_bps'" v-model:bps="form.pelican_config.quality.bps" v-model:auto-restore="form.pelican_config.quality.auto_restore" class="pl-6" :target-groups="bpsTargetGroups" :show-auto-restore="editsField('restore')" />
          </template>
        </fieldset>
        <template v-if="editsField('restore') && !bpsSettingsShown && form.pelican_config.quality.action !== 'observe_only'">
          <QualityBPSRestoreOptions v-if="form.pelican_config.quality.action === 'enable_bps'" v-model:bps="form.pelican_config.quality.bps" v-model:auto-restore="form.pelican_config.quality.auto_restore" class="text-sm" always-show-hold />
          <template v-else><label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.auto_restore" type="checkbox" data-testid="quality-auto-restore" />{{ t('qualityOps.autoRestore') }}</label><p class="text-sm text-gray-500">{{ t(form.pelican_config.quality.action === 'isolate_models' ? 'qualityOps.isolationRestoreHelp' : 'qualityOps.restoreHelp') }}</p></template>
        </template>
        <div v-if="editsField('oauth401')" class="space-y-1" data-testid="quality-oauth-401-setting">
          <div class="flex items-center gap-2"><label for="quality-oauth-401-max-rounds">{{ t('qualityOps.oauth401MaxRounds') }}</label><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.qualityOAuth401Cleanup" :user-id="auth.user?.id" /></div>
          <input id="quality-oauth-401-max-rounds" v-model.number="form.pelican_config.quality.oauth_401_max_rounds" type="number" min="0" max="100" step="1" required class="input" data-testid="quality-oauth-401-max-rounds" aria-describedby="quality-oauth-401-hint" />
          <p id="quality-oauth-401-hint" class="text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.oauth401MaxRoundsHint') }}</p>
        </div>
        <template v-if="editsField('trigger')">
          <label class="flex items-center gap-2"><input v-model="form.pelican_config.quality.trigger_on_upstream_5xx" type="checkbox" data-testid="quality-trigger-5xx" />{{ t('qualityOps.trigger5xx') }}</label>
          <p class="text-sm text-gray-500">{{ t('qualityOps.trigger5xxHint') }}</p>
        </template>
        <label v-if="editsField('retention')" class="block space-y-1"><span>{{ t('qualityOps.maxResults') }}</span><input v-model.number="form.max_results" type="number" min="1" max="200" required class="input" data-testid="quality-copy-max-results" /></label>
        <label v-if="editsField('enabled')" class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" data-testid="quality-rule-enabled" />{{ t('qualityOps.enabled') }}</label>
      </fieldset></form>
      <template #footer><div class="w-full"><p v-if="bulkSaveHint" id="quality-bulk-save-hint" class="mb-3 text-sm text-amber-700 dark:text-amber-300" role="status" data-testid="quality-bulk-save-hint">{{ bulkSaveHint }}</p><div class="editor-footer"><button v-if="editing || bulkEditing" type="button" class="delete-rule" data-testid="quality-editor-delete" :disabled="busy || selectingAccounts || (bulkEditing && !bulkRuleIds.length)" @click="requestDelete(bulkEditing ? bulkRuleIds : editing ? [editing] : [])">{{ bulkEditing ? t('qualityOps.bulkDelete') : t('qualityOps.delete') }}</button><button v-else-if="editingTemplate" type="button" class="delete-rule" :disabled="busy" data-testid="quality-delete-template" @click="openTemplateDelete">{{ t('qualityOps.deleteTemplate') }}</button><span class="flex-1" /><button class="btn btn-secondary" :disabled="busy" @click="closeForm">{{ t('qualityOps.cancel') }}</button><button form="quality-rule-form" type="submit" class="btn btn-primary" :aria-describedby="bulkSaveHint ? 'quality-bulk-save-hint' : undefined" :disabled="busy || ownershipWorking || selectingAccounts || (!!editing && ownershipBlocked) || (bulkEditing ? !bulkFields.length || !bulkRuleIds.length || (!!reuseSourceLabel && !reuseConfirmed) : !savingPreset && !editing && !editingTemplate && accountScope !== 'all' && !selectedAccounts.length)">{{ busy ? t('qualityOps.saving') : savingPreset ? t('qualityOps.savePreset') : bulkEditing ? t('qualityOps.applyToRules', { count: bulkRuleIds.length }) : t('qualityOps.save') }}</button></div></div></template>
    </BaseDialog>
    <BaseDialog :show="!!historyPlan" :title="detailOperation ? t('qualityOps.roundDetail') : t('qualityOps.history')" placement="right" width="extra-wide" close-on-click-outside @close="closeDetails">
      <template v-if="historyPlan">
        <div class="detail-heading"><span class="account-avatar">{{ detailAccountName.slice(0, 1) }}</span><div><h3>{{ detailAccountName }}</h3><p>{{ t('qualityOps.rule') }} {{ historyPlan.id }}<span class="mx-2">·</span>{{ detailOperation ? date(detailOperation.started_at) : t('qualityOps.historyHelp') }}</p></div><a class="account-management-link" href="/admin/accounts">{{ t('qualityOps.manageAccount') }}<Icon name="externalLink" size="xs" /></a></div>
        <div v-if="detailOperation" class="round-overview"><div><span>{{ t('qualityOps.testResult') }}</span><strong :class="allPassed(detailOperation) ? 'text-emerald-600' : 'text-rose-600'">{{ detailOperation.passed_count }} / {{ detailOperation.total_count }} {{ t('qualityOps.passed') }}</strong><span v-if="detailOperation.skipped_count">{{ t('qualityOps.skippedCount', { n: detailOperation.skipped_count }) }}</span></div><div><span>{{ t('qualityOps.accountAction') }}</span><strong>{{ operationLabel(detailOperation) }}</strong></div></div>
        <aside v-if="detailAction === 'restore_conflict'" class="conflict-explanation" role="note"><Icon name="exclamationTriangle" size="md" /><div v-if="detailRestoreDiagnostic" data-testid="quality-restore-diagnostic"><h4>{{ t('qualityOps.restoreDiagnosticTitle') }}</h4><p>{{ restoreDiagnosticReason }}</p><p v-if="detailRestoreDiagnostic.disposition === 'external_membership_present'">{{ t('qualityOps.externalMembershipHelp') }}</p><p v-if="detailRestoreDiagnostic.scope === 'isolate_models'">{{ t('qualityOps.isolationRestoreRolledBack') }}</p><details class="conflict-causes"><summary>{{ t('qualityOps.restoreDiagnosticEvidence') }}</summary><pre class="whitespace-pre-wrap break-words text-xs">{{ JSON.stringify(detailRestoreDiagnostic, null, 2) }}</pre></details><strong>{{ t('qualityOps.restoreDiagnosticNextStep') }}</strong><QualityOwnershipReview v-if="historyPlan" :plan-id="historyPlan.id" :disabled="!canCopyRules" @reconciled="store.refreshRules(true)" /></div><div v-else-if="detailBPS"><h4>{{ t('qualityOps.conflictTitle') }}</h4><p>{{ t('qualityOps.bpsConflictExplanation') }}</p><details class="conflict-causes"><summary>{{ t('qualityOps.conflictChecks') }}</summary><ul><li>{{ t('qualityOps.bpsConflictOptions') }}</li><li>{{ t('qualityOps.bpsConflict403') }}</li><li>{{ t('qualityOps.bpsConflictAvailability') }}</li></ul></details><p class="conflict-limit">{{ t('qualityOps.conflictUnknown') }}</p><strong>{{ t('qualityOps.bpsConflictNextStep') }}</strong></div>
          <div v-else><h4>{{ t('qualityOps.conflictTitle') }}</h4><p>{{ t('qualityOps.conflictExplanation') }}</p><details class="conflict-causes"><summary>{{ t('qualityOps.conflictChecks') }}</summary><ul><li>{{ t('qualityOps.conflictAccount') }}</li><li>{{ t('qualityOps.conflictMembership') }}</li><li>{{ t('qualityOps.conflictAvailability') }}</li></ul></details><p class="conflict-limit">{{ t('qualityOps.conflictUnknown') }}</p><strong>{{ t('qualityOps.conflictNextStep') }}</strong></div></aside>
        <div v-else-if="detailAction" class="action-explanation"><Icon name="infoCircle" size="sm" /><p>{{ actionExplanation(detailAction) }}</p></div>
        <section v-if="detailModelResults" class="model-summary">
          <h4>{{ t('qualityOps.roundModelAction') }}</h4>
          <table data-testid="quality-model-summary"><thead><tr><th>{{ t('qualityOps.model') }}</th><th>{{ t('qualityOps.testResult') }}</th><th>{{ t(detailModelIsolation ? 'qualityOps.modelAction' : 'qualityOps.newCooldown') }}</th></tr></thead><tbody><tr v-for="model in detailTestModels" :key="model"><td><code>{{ model }}</code></td><td>{{ modelVerdictLabel(model) }}</td><td>{{ modelCooldownLabel(model) }}</td></tr></tbody></table>
        </section>
        <dl v-if="detailOperation?.pelican_config?.quality" class="detail-policy"><div><dt>{{ t(detailModelCooldown ? 'qualityOps.cooldownTargets' : detailBPS ? 'qualityOps.ruleAction' : 'qualityOps.targetGroups') }}</dt><dd>{{ detailModelCooldown ? detailOperation.pelican_config.quality.remove_models?.join(' / ') : operationGroups(detailOperation) }}<p v-if="detailModelCooldown">{{ t('qualityOps.cooldownTargetsHint') }}</p></dd></div><div><dt>{{ t(detailBPS ? 'qualityOps.bpsAutoDisableShort' : 'qualityOps.autoRestoreShort') }}</dt><dd>{{ t(detailOperation.pelican_config.quality.auto_restore ? 'qualityOps.on' : 'qualityOps.off') }}</dd></div></dl>
        <div v-if="detailsError" class="panel-error" role="alert">{{ detailsError }}<button @click="retryDetails">{{ t('qualityOps.retry') }}</button></div>
        <div v-if="detailsLoading" class="detail-loading" role="status"><span class="cell-skeleton" /><span class="cell-skeleton" />{{ t('qualityOps.loading') }}</div>
        <div v-else class="detail-grid">
          <nav class="result-navigation" :aria-label="t('qualityOps.probes')"><p>{{ t('qualityOps.probes') }}<span>{{ results.length }}</span></p><button v-for="(result, index) in results" :key="result.id" :class="{ selected: selectedResultId === result.id }" :aria-pressed="selectedResultId === result.id" @click="selectResult(result)"><span>{{ detailOperation ? t('qualityOps.probeNumber', { n: index + 1 }) : date(result.started_at) }}</span><code v-if="result.status && result.pelican_config?.model_id" class="text-xs break-all">{{ result.pelican_config.model_id }}</code><span :class="resultTextClass(result)">{{ resultLabel(result) }}</span></button></nav>
          <section class="answer-detail" :aria-busy="answerLoading">
            <div v-if="answerLoading" class="detail-loading" role="status"><span class="cell-skeleton" /><span class="cell-skeleton" />{{ t('qualityOps.loadingAnswer') }}</div>
            <template v-else-if="selectedResult"><header class="answer-heading"><h4>{{ t(isProbeResult(selectedResult) ? 'qualityOps.probeVerdictTitle' : 'qualityOps.answerAndVerdict') }}</h4><span class="outcome-badge" :class="resultTone(selectedResult)" data-testid="quality-result-badge">{{ resultLabel(selectedResult) }}</span></header>
              <details v-if="selectedResult.pelican_config?.random_question" class="answer-reference" data-testid="quality-random-question-detail"><summary>{{ t('qualityOps.generatedQuestion') }} · {{ selectedResult.pelican_config.random_question.id }}</summary><pre class="whitespace-pre-wrap">{{ selectedResult.pelican_config.prompt }}</pre></details>
              <div v-if="selectedResult.pelican_config?.question_kind === 'random_reasoning'" class="answer-reference" data-testid="quality-batch-evidence">
                <template v-if="selectedResult.pelican_config.quality_batch">
                  <strong>{{ t('qualityOps.sharedBatch') }} · {{ selectedResult.pelican_config.quality_batch.id }}</strong>
                  <p>{{ batchDecisionLabel(selectedResult.pelican_config.quality_batch.decision) }}</p>
                  <p>{{ t('qualityOps.batchCondition', { model: selectedResult.pelican_config.model_id || '—', mapped: selectedResult.pelican_config.quality_batch.mapped_model || '—', effort: selectedResult.pelican_config.reasoning_effort || '—', route: selectedResult.pelican_config.quality_batch.route || '—' }) }}</p>
                  <p>{{ t('qualityOps.batchPeers', { total: selectedResult.pelican_config.quality_batch.peer_accounts, passed: selectedResult.pelican_config.quality_batch.peer_passed, failed: selectedResult.pelican_config.quality_batch.peer_failed, unknown: selectedResult.pelican_config.quality_batch.peer_inconclusive }) }}</p>
                  <p>{{ t('qualityOps.batchWindow', { from: date(selectedResult.pelican_config.quality_batch.started_at), to: date(selectedResult.pelican_config.quality_batch.start_deadline) }) }}</p>
                </template>
                <p v-else>{{ t('qualityOps.batchLegacy') }}</p>
              </div>
              <div v-if="selectedResult.status !== 'skipped' && !isProbeResult(selectedResult)" class="answer-reference"><span>{{ t('qualityOps.answer') }}</span><p>{{ selectedResult.pelican_config?.quality?.expected_answer || detailOperation?.pelican_config?.quality?.expected_answer || '—' }}</p></div>
              <div class="response-heading">{{ t(selectedResult.status === 'skipped' ? 'qualityOps.skipReason' : isProbeResult(selectedResult) ? 'qualityOps.probeDetail' : 'qualityOps.actualAnswer') }}<span v-if="selectedResult.latency_ms">{{ (selectedResult.latency_ms / 1000).toFixed(1) }}s</span></div>
              <pre class="response-content">{{ resultContent(selectedResult) }}</pre>
              <div v-if="selectedResult.quality_judgment && !isProbeResult(selectedResult)" class="judge-reason"><h5>{{ t('qualityOps.judgeReason') }}</h5><p>{{ judgmentReason(selectedResult) }}</p><span>{{ selectedResult.quality_judgment.model_id || '—' }} · {{ selectedResult.quality_judgment.kind === 'jev' ? t('qualityOps.judgeJev') : groupNames[selectedResult.quality_judgment.group_id || 0] || selectedResult.quality_judgment.group_id || '—' }}</span><p v-if="selectedResult.quality_judgment.kind === 'jev'">{{ t('qualityOps.judgeJevLatency', { ms: selectedResult.quality_judgment.latency_ms ?? '—' }) }} · {{ selectedResult.quality_judgment.rules_version }}</p></div>
            </template><div v-else-if="!detailsError" class="panel-empty">{{ t('qualityOps.noResults') }}</div>
          </section>
        </div>
      </template>
      <template #footer><div class="detail-footer"><button class="btn btn-secondary" :disabled="operationIndex <= 0 || !detailOperation" @click="navigateOperation(-1)"><Icon name="arrowLeft" size="sm" />{{ t('qualityOps.previousRound') }}</button><span>{{ detailOperation ? `${operationIndex + 1} / ${filteredOperations.length}` : t('qualityOps.history') }}</span><button class="btn btn-secondary" :disabled="!detailOperation || operationIndex < 0 || operationIndex >= filteredOperations.length - 1" @click="navigateOperation(1)">{{ t('qualityOps.nextRound') }}<Icon name="arrowRight" size="sm" /></button></div></template>
    </BaseDialog>
    <BaseDialog :show="!!deleteTargets.length" :title="t('qualityOps.deleteRulesTitle', { count: deleteTargets.length })" width="normal" :close-on-escape="!deleting" :show-close-button="!deleting" @close="!deleting && (deleteTargets = [])">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ deleteTargets.length > 1 ? t('qualityOps.bulkDeleteConfirm', { count: deleteTargets.length }) : t('qualityOps.deleteConfirm') }}</p>
      <ul class="mt-3 max-h-48 space-y-1 overflow-auto break-words text-sm" data-testid="quality-delete-targets"><li v-for="plan in deleteTargets" :key="plan.id">{{ name(plan) }} · {{ t('qualityOps.rule') }} #{{ plan.id }}</li></ul>
      <div v-for="plan in deleteReviewTargets" :key="plan.id" class="mt-3" :data-delete-review="plan.id"><h4>{{ t('qualityOps.rule') }} #{{ plan.id }}</h4><QualityOwnershipReview :plan-id="plan.id" deleting :disabled="deleting" @deletable="deleteReady[plan.id] = $event" @deletion-fence="deleteFences[plan.id] = $event" @reconciled="store.refreshRules(true)" /></div>
      <p v-if="deleteProgress" class="mt-3 text-sm" role="status">{{ deleteProgress }}</p>
      <p v-if="deleteError" class="editor-error mt-3" role="alert" data-testid="quality-delete-error">{{ deleteError }}</p>
      <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="deleting" data-testid="quality-cancel-delete" @click="deleteTargets = []">{{ t('qualityOps.cancel') }}</button><button class="btn bg-red-600 text-white hover:bg-red-700" :disabled="deleting || deleteReviewTargets.some(plan => !deleteReady[plan.id])" data-testid="quality-confirm-delete" @click="confirmDelete">{{ deleting ? t('qualityOps.deleting') : t('qualityOps.delete') }}</button></div></template>
    </BaseDialog>
    <BaseDialog :show="!!templateDeleteTarget" :title="t('qualityOps.deleteTemplate')" width="narrow" :close-on-escape="!templateDeleting" @close="!templateDeleting && (templateDeleteTarget = null)">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('qualityOps.deleteTemplateConfirm') }}</p>
      <p class="mt-3 font-medium">{{ templateDeleteTarget ? templateFilterSummary(templateDeleteTarget) : '' }}</p>
      <template v-if="templateDeleteTarget?.plan_ids.length">
        <label class="mt-4 flex items-start gap-2 text-sm"><input v-model="deletePlansWithTemplate" type="checkbox" class="mt-0.5" data-testid="quality-delete-template-plans" />{{ t('qualityOps.deleteTemplatePlans', { count: templateDeleteTarget.plan_ids.length }) }}</label>
        <p v-if="deletePlansWithTemplate" class="mt-2 text-xs text-amber-600">{{ t('qualityOps.deleteTemplatePlansWarning') }}</p>
      </template>
      <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="templateDeleting" @click="templateDeleteTarget = null">{{ t('qualityOps.cancel') }}</button><button class="btn bg-red-600 text-white hover:bg-red-700" :disabled="templateDeleting" data-testid="quality-confirm-delete-template" @click="confirmTemplateDelete">{{ t('qualityOps.delete') }}</button></div></template>
    </BaseDialog>
    <BaseDialog :show="discardPrompt" :title="t('qualityOps.unsavedTitle')" width="narrow" @close="discardPrompt = false"><p>{{ t('qualityOps.unsavedHint') }}</p><template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="discardPrompt = false">{{ t('qualityOps.keepEditing') }}</button><button class="btn btn-primary" @click="discardPrompt = false; showForm = false">{{ t('qualityOps.discard') }}</button></div></template></BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import QualityAccountSelector from '@/components/admin/operations/QualityAccountSelector.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import Icon from '@/components/icons/Icon.vue'
import QualityBPSSettings from '@/components/admin/operations/QualityBPSSettings.vue'
import QualityBPSRestoreOptions from '@/components/admin/operations/QualityBPSRestoreOptions.vue'
import { useAccountQualityStore } from '@/stores/accountQuality'
import { useAuthStore } from '@/stores/auth'
import { updateQualityPreset, createQualityPreset, createQualityTemplate, deleteQualityTemplate, runQualityPlan, updateQualityTemplate, type QualityOperation, type QualityRuleAccountFilter, type QualityRuleTemplate } from '@/api/admin/accountQuality'
import scheduledTests from '@/api/admin/scheduledTests'
import * as accountsAPI from '@/api/admin/accounts'
import * as groupsAPI from '@/api/admin/groups'
import { CANDY_PROMPT, STATE_PROBE_QUESTION, stateProbeVerdict, type StateProbeVerdict } from '@/utils/intelligenceTest'
import { buildQualityRulePatch, copyQualityRuleConfig, qualityRuleFieldValue, qualityActionPayload, qualityUsesGroups, qualityUsesModelResults, qualityUsesRetestRest, defaultQualityBPS, qualityBPSError, qualityBPSForm, qualityJudgeConfigured, qualityRuleFields, type QualityRuleField } from '@/utils/qualityRulePatch'
import { DEFAULT_STATE_PROBE_CRON } from '@/utils/intelligenceTest'
import QualityOwnershipReview from '@/components/admin/operations/QualityOwnershipReview.vue'
import QualityProbeSchedule from '@/components/admin/operations/QualityProbeSchedule.vue'
import QualityModelShortcuts from '@/components/admin/operations/QualityModelShortcuts.vue'
import { qualityAccountStatusOptions, qualityAccountTypeOptions } from '@/utils/qualityAccountFilter'
import type { AccountListItem, QualityBPSPolicy, QualityJudgeConfig, QualityPolicy, ScheduledTestPlan, ScheduledTestResult } from '@/types'

const { t, te } = useI18n()
const store = useAccountQualityStore(), auth = useAuthStore()
const { plans, groups, operations, templates, presets } = storeToRefs(store)
const groupNames = computed(() => Object.fromEntries(groups.value.map(g => [g.id, g.name])))
const accountNames = computed(() => {
  const names: Record<number, string> = {}
  for (const op of operations.value) if (op.account_name) names[op.account_id] = op.account_name
  for (const p of plans.value) if (p.account_name) names[p.account_id] = p.account_name
  for (const account of accounts.value) names[account.id] = account.name
  return names
})
const accounts = ref<AccountListItem[]>([]), accountsLoading = ref(false)
const search = ref(''), accountPage = ref(1), accountPages = ref(1)
const accountGroup = ref(''), accountType = ref(''), accountsError = ref(''), selectingAccounts = ref(false)
// 状态筛选默认「正常」（全不勾 = 全部状态）；范围默认「所有匹配账户」，保存时按筛选自动包含。
const accountStatuses = ref<string[]>(['active']), accountScope = ref<'all' | 'manual'>('all'), accountsTotal = ref(0)
function ruleScope(action?: QualityPolicy['action']) { return action === 'enable_bps' ? 'bps' : action === 'observe_only' ? 'observation' : 'quarantine' }
const bulkSourceScope = ref('quarantine')
const existingAccountIds = computed(() => new Set(plans.value.filter(plan =>
  ruleScope(plan.pelican_config?.quality?.action) === (bulkEditing.value ? bulkSourceScope.value : ruleScope(form.value.pelican_config.quality.action))
).map(plan => plan.account_id)))
const selectableAccounts = computed(() => accounts.value.filter(account => !accountDisabledReason(account)))
let accountSelectionRequest = 0
const accountFilters = () => ({ search: search.value.trim(), group: accountGroup.value || undefined, type: accountType.value || undefined,
  status: accountStatuses.value.join(',') || undefined, lite: 'true', sort_by: 'id', sort_order: 'asc' as const })
function invalidateAccountRequests() {
  accountRequest++; accountSelectionRequest++
  accountsLoading.value = selectingAccounts.value = false
  accounts.value = []; accountPage.value = accountPages.value = 1; accountsTotal.value = 0; accountsError.value = ''
}
watch([search, accountGroup, accountType, accountStatuses], invalidateAccountRequests, { flush: 'sync' })
function showAccountPicker() { return showForm.value && !editing.value && !savingPreset.value }

const selectedAccounts = ref<number[]>([]), editing = ref<number | null>(null)
const ownershipBlocked = ref(false)
const ownershipWorking = ref(false)
function ownedModelRemoved(model: string) {
  form.value.pelican_config.model_ids = form.value.pelican_config.model_ids.filter(value => value !== model)
  if (form.value.model_id === model) form.value.model_id = form.value.pelican_config.model_ids[0] || ''
}
const copying = ref(false), copySourceAccountId = ref<number | null>(null)
const editingPreset = ref<number | null>(null)
const savingPreset = ref(false), presetName = ref(''), selectedPresetId = ref('')
const sourceAccountId = ref(''), sourceRuleId = ref('')
const reuseSourceLabel = ref(''), reuseBefore = ref<ReturnType<typeof defaults> | null>(null), reuseConfirmation = ref('')
const reuseSourceRuleId = ref<number | null>(null)
const reuseEditingPlan = computed(() => plans.value.find(plan => plan.id === editing.value))
const applyingReuse = ref(false)
const reusablePlans = computed(() => plans.value.filter(plan => plan.pelican_config?.quality && plan.pelican_config.quality.action !== 'openai_two_fa_admission'))
const sourceAccounts = computed(() => [...new Map(reusablePlans.value.map(plan => [plan.account_id, { id: plan.account_id, name: name(plan) }] as const)).values()])
const sourceRules = computed(() => reusablePlans.value.filter(plan => String(plan.account_id) === sourceAccountId.value))
const canCopyRules = computed(() => auth.user?.role === 'admin')
// 正在编辑的分组规则 id，和 editing（单条账号规则）互斥；planTemplate：账号规则 id → 建它的分组规则 id。
const editingTemplate = ref<number | null>(null), templatePending = ref<Record<number, string>>({})
const templateDeleteTarget = ref<QualityRuleTemplate | null>(null), deletePlansWithTemplate = ref(true), templateDeleting = ref(false)
const editingTemplateRecord = computed(() => templates.value.find(tpl => tpl.id === editingTemplate.value))
const planTemplate = computed(() => {
  const owners: Record<number, number> = {}
  for (const tpl of templates.value) for (const id of tpl.plan_ids) owners[id] = tpl.id
  return owners
})
const templateAccountIds = computed(() => new Set(plans.value.filter(plan => planTemplate.value[plan.id] === editingTemplate.value).map(plan => plan.account_id)))
const selectedRuleIds = ref<number[]>([]), bulkRuleIds = ref<number[]>([])
const bulkEditing = ref(false), bulkFields = ref<QualityRuleField[]>([]), bulkProgress = ref('')
const pickerAccountIds = computed({
  get: () => bulkEditing.value ? plans.value.filter(plan => bulkRuleIds.value.includes(plan.id)).map(plan => plan.account_id) : selectedAccounts.value,
  set: (ids: number[]) => {
    if (bulkEditing.value) bulkRuleIds.value = plans.value.filter(plan => ids.includes(plan.account_id)).map(plan => plan.id)
    else selectedAccounts.value = ids
  },
})
function accountDisabledReason(account: AccountListItem) {
  if (bulkEditing.value) return existingAccountIds.value.has(account.id) ? '' : t('qualityOps.accountNoRule')
  if (editingTemplate.value && templateAccountIds.value.has(account.id)) return t('qualityOps.accountCoveredByTemplate')
  if (!copying.value && existingAccountIds.value.has(account.id)) return t('qualityOps.accountHasRule')
  if (isProbe.value && !supportsStateProbeAccount(account)) return t('qualityOps.probeAccountUnsupported')
  return ''
}
function supportsStateProbeAccount(account: AccountListItem) {
  return account.platform === 'openai' && ['oauth', 'setup-token'].includes(account.type) && account.extra?.synthetic_ui_test !== true
}
const knownAccounts = new Map<number, AccountListItem>()
function rememberAccounts(items: AccountListItem[]) { for (const account of items) knownAccounts.set(account.id, account) }
const busy = ref(false), error = ref(''), notice = ref(''), showForm = ref(false)
watch(showAccountPicker, (show) => { if (!show) invalidateAccountRequests() }, { flush: 'sync' })
const pending = ref<Record<number, string>>({}), deleteTargets = ref<ScheduledTestPlan[]>([]), deleting = ref(false)
const deleteError = ref(''), deleteProgress = ref('')
const deleteFences = ref<Record<number, string>>({})
const deleteReady = ref<Record<number, boolean>>({}), deleteConflicts = ref<number[]>([])
const deleteReviewTargets = computed(() => deleteTargets.value.filter(plan => plan.pelican_config?.quality?.action === 'isolate_models' || deleteConflicts.value.includes(plan.id)))
const discardPrompt = ref(false), initialForm = ref('')
const judgeModels = ref<string[]>([])
let judgeModelsRequest = 0, accountRequest = 0, detailRequest = 0, answerRequest = 0
let alive = true, poll: ReturnType<typeof setInterval> | undefined
const historyPlan = ref<ScheduledTestPlan | null>(null), detailOperation = ref<QualityOperation | null>(null)
const results = ref<ScheduledTestResult[]>([]), selectedResultId = ref<number | null>(null)
const selectedResult = ref<ScheduledTestResult | null>(null)
const detailsLoading = ref(false), answerLoading = ref(false), detailsError = ref('')
const loadedAnswers = new Map<number, ScheduledTestResult>()
const identity = () => auth.user ? `${auth.user.id}:${auth.user.role}` : ''
const refreshing = computed(() => store.rulesLoading || store.operationsLoading)
const enabledCount = computed(() => plans.value.filter(p => p.enabled).length)
// 开 BPS 被 403 记录挡住或账号不支持 BPS 时，规则无法自动处理，也列为待处理。
const attentionActions = new Set(['restore_conflict', 'action_conflict', 'action_error', 'bps_blocked_403', 'bps_unsupported', 'admission_conflict', 'admission_isolation_pending'])
const attentionCount = computed(() => {
  const latest = new Map<number, QualityOperation>()
  for (const operation of operations.value) if (!latest.has(operation.plan_id)) latest.set(operation.plan_id, operation)
  return [...latest.values()].filter(op => attentionActions.has(op.quality_action || '')).length
})
const selectedPlan = computed(() => plans.value.find(p => p.id === store.selectedPlanId))
const filteredPlans = computed(() => {
  const query = store.search.trim().toLowerCase()
  return plans.value.filter(p => !query || `${name(p)} ${p.account_id} ${p.id} ${p.model_id} ${p.pelican_config?.rule_name || ''}`.toLowerCase().includes(query))
})
const allFilteredSelected = computed(() => filteredPlans.value.length > 0 && filteredPlans.value.every(plan => selectedRuleIds.value.includes(plan.id)))
const someFilteredSelected = computed(() => filteredPlans.value.some(plan => selectedRuleIds.value.includes(plan.id)))
const selectedRulesPending = computed(() => selectedRuleIds.value.some(id => !!pending.value[id]))
watch(plans, (current) => {
  const ids = new Set(current.map(plan => plan.id))
  selectedRuleIds.value = selectedRuleIds.value.filter(id => ids.has(id))
})
function toggleFilteredSelection() {
  if (busy.value) return
  const ids = new Set(filteredPlans.value.map(plan => plan.id))
  selectedRuleIds.value = allFilteredSelected.value
    ? selectedRuleIds.value.filter(id => !ids.has(id))
    : [...new Set([...selectedRuleIds.value, ...ids])]
}
function editsField(field: QualityRuleField) { return !bulkEditing.value || bulkFields.value.includes(field) }
function resetReuse() { reuseSourceLabel.value = ''; reuseBefore.value = null; reuseConfirmation.value = ''; reuseSourceRuleId.value = null }
function reuseSnapshot() {
  const targets = bulkEditing.value ? bulkRuleIds.value.map(id => {
    const plan = plans.value.find(item => item.id === id)
    return plan ? [id, plan.account_id, plan.max_results, ...qualityRuleFields.map(field => qualityRuleFieldValue(plan, field))] : [id]
  }) : null
  return JSON.stringify([reuseSourceLabel.value, reuseSourceRuleId.value, bulkRuleIds.value, bulkFields.value, form.value, targets])
}
const reuseConfirmed = computed(() => !!reuseSourceLabel.value && reuseConfirmation.value === reuseSnapshot())
function confirmReuse(event: Event) { reuseConfirmation.value = (event.target as HTMLInputElement).checked ? reuseSnapshot() : '' }
function reuseFieldSummary(rule: Pick<ScheduledTestPlan, 'model_id' | 'cron_expression' | 'enabled' | 'pelican_config'> & { max_results?: number }, field: QualityRuleField): string {
  const cfg = rule.pelican_config, q = cfg?.quality
  if (field === 'model') return [...new Set([rule.model_id, ...(cfg?.model_ids || [])])].join(' / ')
  if (field === 'schedule') return rule.cron_expression
  if (field === 'retention') return String(('max_results' in rule ? rule.max_results : '') ?? '')
  if (field === 'trigger') return t(q?.trigger_on_upstream_5xx ? 'qualityOps.on' : 'qualityOps.off')
  if (field === 'oauth401') return q?.oauth_401_max_rounds ? t('qualityOps.oauth401Rounds', { count: q.oauth_401_max_rounds }) : t('qualityOps.off')
  if (field === 'enabled') return t(rule.enabled ? 'qualityOps.on' : 'qualityOps.off')
  if (field === 'action') return policyTarget(q)
  if (field === 'restore') return [t(q?.auto_restore ? 'qualityOps.on' : 'qualityOps.off'),
    q?.bps ? `${t('qualityOps.bpsPassThreshold')}: ${q.bps.pass_threshold} · ${t('qualityOps.bpsHoldOnUsage')}: ${t(q.bps.hold_on_usage ? 'qualityOps.on' : 'qualityOps.off')}` : ''].filter(Boolean).join(' · ')
  const question = cfg?.question_kind === 'state_probe' ? 'qualityOps.questionStateProbe' : cfg?.question_kind === 'random_reasoning' ? 'qualityOps.questionRandom' : 'qualityOps.questionCandy'
  return [t(question), t(cfg?.test_channel === 'bps' ? 'qualityOps.bpsChannel' : 'qualityOps.accountChannel'),
    `${t('qualityOps.effort')}: ${cfg?.reasoning_effort || '—'}`, `${t('qualityOps.parallel')}: ${cfg?.parallel_count || 1}`,
    cfg?.prompt, q?.expected_answer ? `${t('qualityOps.answer')}: ${q.expected_answer}` : '',
    q?.judge && cfg?.question_kind !== 'state_probe' ? `${t('qualityOps.judgeTitle')}: ${q.judge.kind === 'jev' ? t('qualityOps.judgeJev') : `#${q.judge.group_id} / ${q.judge.model_id}`}\n${q.judge.prompt}` : ''].filter(Boolean).join('\n')
}
function reusedBulkPatch(plan: ScheduledTestPlan) {
  const body = buildQualityRulePatch(plan, form.value, bulkFields.value)
  if (reuseSourceLabel.value) {
    if (!bulkFields.value.includes('retention') && body.max_results && body.max_results !== plan.max_results) throw new Error(t('qualityOps.reuseRetention', { before: plan.max_results, after: body.max_results }))
    const after = { ...plan, ...body }
    for (const field of qualityRuleFields) {
      if (!bulkFields.value.includes(field) && JSON.stringify(qualityRuleFieldValue(plan, field)) !== JSON.stringify(qualityRuleFieldValue(after, field))) {
        throw new Error(t('qualityOps.reuseDependentField', { field: t(`qualityOps.bulkFields.${field}`) }))
      }
    }
  }
  return body
}
const reuseTargets = computed(() => {
  if (!bulkEditing.value || !reuseSourceLabel.value) return []
  return bulkRuleIds.value.map(id => {
    const plan = plans.value.find(item => item.id === id)
    const target = { id, accountId: plan?.account_id, name: plan ? name(plan) : `#${id}`,
      changes: [] as { field: QualityRuleField; before: string; after: string }[], error: '' }
    if (!plan) { target.error = t('qualityOps.ruleUnavailable', { id }); return target }
    if (id === reuseSourceRuleId.value) { target.error = t('qualityOps.reuseSourceIsTarget'); return target }
    if (!bulkFields.value.length) return target
    try {
      const body = reusedBulkPatch(plan), after = { ...plan, ...body }
      target.changes = bulkFields.value.map(field => ({ field, before: reuseFieldSummary(plan, field), after: reuseFieldSummary(after, field) }))
    } catch (cause) { const detail = message(cause); target.error = te(detail) ? t(detail) : detail }
    return target
  })
})
const bulkSaveHint = computed(() => {
  if (!bulkEditing.value || busy.value) return ''
  if (!bulkFields.value.length) return t(reuseSourceLabel.value ? 'qualityOps.reuseSelectFieldsHint' : 'qualityOps.selectBulkFields')
  if (!bulkRuleIds.value.length) return t('qualityOps.bulkSelectTargets')
  if (reuseSourceLabel.value) {
    if (reuseTargets.value.some(target => target.error)) return t('qualityOps.reuseResolveErrors')
    if (!reuseConfirmed.value) return t('qualityOps.reuseConfirmRequired')
  }
  return ''
})
const filteredOperations = computed(() => operations.value.filter(op => {
  if (store.selectedPlanId !== null && op.plan_id !== store.selectedPlanId) return false
  if (store.operationFilter === 'attention') return attentionActions.has(op.quality_action || '')
  if (store.operationFilter === 'passed') return allPassed(op)
  if (store.operationFilter === 'failed') return !allPassed(op)
  return true
}))
const operationIndex = computed(() => filteredOperations.value.findIndex(op => op.id === detailOperation.value?.id))
const detailAccountName = computed(() => detailOperation.value?.account_name || (historyPlan.value ? name(historyPlan.value) : ''))
const detailAction = computed(() => detailOperation.value?.quality_action || selectedResult.value?.quality_action)
const detailRestoreDiagnostic = computed(() => (detailOperation.value ?? selectedResult.value)?.pelican_config?.quality_restore_diagnostic)
const restoreDiagnosticReason = computed(() => {
  const key = `qualityOps.restoreReasons.${detailRestoreDiagnostic.value?.reason}`
  return te(key) ? t(key) : t('qualityOps.restoreReasonUnknown')
})
const detailBPS = computed(() => (detailOperation.value ?? selectedResult.value)?.pelican_config?.quality?.action === 'enable_bps')
const detailModelIsolation = computed(() => detailOperation.value?.pelican_config?.quality?.action === 'isolate_models')
const detailModelResults = computed(() => qualityUsesModelResults(detailOperation.value?.pelican_config?.quality?.action))
const detailModelCooldown = computed(() => detailOperation.value?.pelican_config?.quality?.action === 'remove_models')
const detailTestModels = computed(() => {
  const cfg = detailOperation.value?.pelican_config
  return cfg?.model_ids?.length ? cfg.model_ids : cfg?.model_id ? [cfg.model_id] : []
})
function modelVerdictLabel(model: string) {
  const verdict = detailOperation.value?.pelican_config?.quality_model_outcomes?.[model]
  if (verdict) return t({ passed: 'qualityOps.passed', failed: 'qualityOps.roundNotPassed', inconclusive: 'qualityOps.judgeUnknown', skipped: 'qualityOps.skipped' }[verdict])
  const samples = results.value.filter(result => result.status && result.pelican_config?.model_id === model)
  if (!samples.length || samples.some(result => !loadedAnswers.has(result.id))) return t('qualityOps.notLoaded')
  return samples.every(result => result.status === 'success') ? t('qualityOps.passed') : resultLabel(samples.find(result => result.status !== 'success')!)
}
function modelCooldownLabel(model: string) {
  if (detailAction.value === 'action_error') return t('qualityOps.cooldownUnconfirmed')
  const actions = detailOperation.value?.pelican_config?.quality_model_actions
  const action = actions?.[model] ?? ''
  if (detailModelIsolation.value && actions) {
    if (['models_cooled', 'model_cooldown_refreshed'].includes(action)) return t('qualityOps.modelIsolated')
    if (['restored', 'models_partially_restored'].includes(action)) return t('qualityOps.modelRestored')
    return t('qualityOps.modelUnchanged')
  }
  if (action === 'models_cooled' || action === 'model_cooldown_refreshed') return t('qualityOps.cooldownApplied')
  if (actions || ['action_conflict', 'restore_conflict', 'stale_run', 'account_deleted', 'passed', 'inconclusive', 'no_change'].includes(detailAction.value || '')) return t('qualityOps.noNewCooldown')
  return t('qualityOps.cooldownNotRecorded')
}
function name(plan: ScheduledTestPlan) { return plan.account_name || accountNames.value[plan.account_id] || `#${plan.account_id ?? plan.id}` }
function date(value: string | null | undefined) { return value ? new Date(value).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '—' }
function clock(value: string | number | undefined) { return value ? new Date(value).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '—' }
function day(value: string | undefined) { return value ? new Date(value).toLocaleDateString() : '—' }
function running(plan: ScheduledTestPlan) { return !!plan.running_until && Date.parse(plan.running_until) > Date.now() }
function allPassed(op: QualityOperation) { return op.total_count > 0 && op.passed_count === op.total_count }
function bpsTrigger(policy?: QualityBPSPolicy) {
  const parts = [
    policy?.failure_threshold ? t('qualityOps.bpsTriggerCount', { n: policy.failure_threshold }) : '',
    policy?.usage_percent ? t('qualityOps.bpsTriggerUsage', { n: policy.usage_percent }) : '',
  ].filter(Boolean)
  return parts.join(policy?.require_all ? ' + ' : ' / ')
}
function policyTarget(quality?: QualityPolicy) {
  if (quality?.action === 'openai_two_fa_admission') return t('autoConfig.admission.title')
  if (quality?.action === 'observe_only') return t('qualityOps.observeOnly')
  if (quality && qualityUsesGroups(quality.action)) return quality.remove_group_ids.map(id => groupNames.value[id] || `#${id}`).join(' / ')
  if (quality?.action === 'remove_models') return `${t('qualityOps.cooldownTargets')}: ${(quality.remove_models || []).join(' / ')}`
  if (quality?.action !== 'enable_bps') return t('qualityOps.disableSchedulingShort')
  const trigger = bpsTrigger(quality.bps)
  return trigger ? `${t('qualityOps.enableBPSShort')}（${trigger}）` : t('qualityOps.enableBPSShort')
}
function planGroups(plan: ScheduledTestPlan) { return policyTarget(plan.pelican_config?.quality) }
function operationGroups(op: QualityOperation) { return policyTarget(op.pelican_config?.quality) }
// 「降智开 BPS」未开启时后端记为 failure_counted:N/M，已开启但满血次数未到关闭次数时记为 restore_counted:N/M，
// 展示时拆出进度；「同时满足」下次数已够、只差用量时 failure_counted 的 N 会 ≥ M。
function countedOutcome(action?: string) {
  const match = /^(failure_counted|restore_counted):(\d+)\/(\d+)$/.exec(action || '')
  return match ? { kind: match[1], n: Number(match[2]), m: Number(match[3]) } : null
}
function actionKey(action?: string) { return countedOutcome(action)?.kind || action || '' }
function actionLabel(action?: string) {
  if (action?.startsWith('admission_')) return t('autoConfig.admission.outcomes.' + action)
  const counted = countedOutcome(action)
  if (counted?.kind === 'restore_counted') return t('qualityOps.outcomes.restore_counted_n', counted)
  if (counted) return t(counted.n >= counted.m ? 'qualityOps.outcomes.failure_counted_usage' : 'qualityOps.outcomes.failure_counted_n', counted)
  const key = `qualityOps.outcomes.${action}`; return action && te(key) ? t(key) : '—'
}
function operationLabel(op: QualityOperation) {
  if (op.quality_action === 'inconclusive' && op.pelican_config?.quality_batch?.decision === 'no_passing_peer') return t('qualityOps.batchNoControl')
  const policy = op.pelican_config?.quality?.action, bpsKey = `qualityOps.bpsOutcomes.${actionKey(op.quality_action)}`
  const diagnostic = op.pelican_config?.quality_restore_diagnostic
  if (op.quality_action === 'restore_conflict' && diagnostic?.scope === 'remove_groups' && diagnostic.disposition === 'external_membership_present') return t('qualityOps.externalMembershipPresent')
  if (op.quality_action === 'restored' && policy === 'remove_groups') return t('qualityOps.groupsRestored')
  if (policy === 'enable_bps' && op.quality_action && te(bpsKey)) return t(bpsKey)
  return actionLabel(op.quality_action)
}
function batchDecisionLabel(decision: string) {
  const key = `qualityOps.batchDecisions.${decision}`
  return te(key) ? t(key) : t('qualityOps.batchLegacy')
}
function tone(action?: string) {
  const key = actionKey(action)
  if (attentionActions.has(key)) return 'tone-warning'
  if (['restored', 'passed', 'bps_enabled', 'bps_enabled_usage'].includes(key)) return 'tone-success'
  if (['groups_removed', 'scheduling_disabled', 'already_quarantined', 'bps_kept_usage', 'bps_already_enabled', 'failure_counted', 'restore_counted'].includes(key)) return 'tone-muted'
  return 'tone-neutral'
}
function actionExplanation(action: string) {
  const key = actionKey(action), bpsKey = `qualityOps.bpsActionHelp.${key}`
  if (detailBPS.value && te(bpsKey)) return t(bpsKey)
  return te(`qualityOps.actionHelp.${key}`) ? t(`qualityOps.actionHelp.${key}`) : t('qualityOps.actionHelp.no_change')
}
const probeLabels: Record<StateProbeVerdict, string> = { healthy: 'qualityOps.probeHealthy', degraded: 'qualityOps.probeDegraded', inconclusive: 'qualityOps.probeInconclusive' }
function isProbePlan(plan: ScheduledTestPlan) { return plan.pelican_config?.question_kind === STATE_PROBE_QUESTION }
function isProbeResult(result: ScheduledTestResult) { return result.pelican_config?.question_kind === STATE_PROBE_QUESTION }
function resultLabel(result: ScheduledTestResult) {
  if (result.status === 'skipped') return t(result.error_message === 'model_catalog_unavailable' ? 'qualityOps.catalogUnavailable' : result.error_message === 'model_unavailable' ? 'qualityOps.modelUnavailableSkipped' : 'qualityOps.modelUnsupportedSkipped')
  if (!result.status) return t('qualityOps.notLoaded')
  const verdict = stateProbeVerdict(result)
  if (verdict) return t(probeLabels[verdict])
  return t(result.status === 'success' ? 'qualityOps.passed' : result.error_message === 'answer_mismatch' ? 'qualityOps.wrongAnswer' : result.quality_judgment?.verdict === 'unknown' || result.error_message?.startsWith('judge_') ? 'qualityOps.judgeUnknown' : 'qualityOps.requestError')
}
// 探针「无法判断」不是账号问题，用中性色，避免和降智混在一起。
function resultTextClass(result: ScheduledTestResult) {
  if (!result.status) return 'text-gray-400'
  if (result.status === 'skipped') return 'text-gray-500 dark:text-gray-400'
  if (stateProbeVerdict(result) === 'inconclusive') return 'text-gray-500'
  return result.status === 'success' ? 'text-emerald-600' : 'text-rose-600'
}
function resultTone(result: ScheduledTestResult) {
  if (result.status === 'skipped') return 'tone-neutral'
  if (stateProbeVerdict(result) === 'inconclusive') return 'tone-neutral'
  return result.status === 'success' ? 'tone-success' : 'tone-danger'
}
function resultContent(result: ScheduledTestResult) {
  if (result.status === 'skipped') return t(result.error_message === 'model_catalog_unavailable' ? 'qualityOps.catalogSkipHelp' : result.error_message === 'model_unavailable' ? 'qualityOps.modelUnavailableSkipHelp' : 'qualityOps.unsupportedSkipHelp')
  return result.response_text || result.error_message || t('qualityOps.noAnswer')
}
function defaults() {
  return { model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: true, max_results: 100, auto_recover: false,
    pelican_config: { rule_name: '', question_kind: 'candy' as 'candy' | 'random_reasoning' | typeof STATE_PROBE_QUESTION, test_channel: 'account' as 'account' | 'bps', prompt: CANDY_PROMPT, reasoning_effort: 'high', parallel_count: 1, model_ids: [] as string[],
      quality: { trigger_on_upstream_5xx: false, oauth_401_max_rounds: 6, expected_answer: '21', action: 'remove_groups' as QualityPolicy['action'], remove_group_ids: [] as number[], remove_models: [] as string[], recovery_concurrency: 5, retest_rest_minutes: 0, auto_restore: false, judge: { group_id: 0, model_id: '', prompt: t('qualityOps.defaultJudgePrompt') } as QualityJudgeConfig, bps: defaultQualityBPS() } } }
}
const form = ref(defaults())
const removeModelsInput = ref('')
// Keep imported/edit drafts in sync, but render the raw text while typing so
// trailing delimiters and whitespace survive normalization of the model array.
watch(() => form.value.pelican_config.quality.remove_models, models => {
  removeModelsInput.value = (models || []).join(', ')
}, { immediate: true, deep: true, flush: 'sync' })
function updateRemoveModelsInput(event: Event) {
  const text = (event.target as HTMLInputElement).value
  form.value.pelican_config.quality.remove_models = [...new Set(text.split(/[,，\r\n]+/).map(model => model.trim()).filter(Boolean))]
  removeModelsInput.value = text
}
const isProbe = computed(() => form.value.pelican_config.question_kind === STATE_PROBE_QUESTION)
const selectedTestModels = computed(() => [form.value.model_id, ...(isProbe.value ? [] : form.value.pelican_config.model_ids)].map(model => model.trim()).filter(Boolean))
const canAddTestModel = computed(() => !form.value.model_id.trim() || form.value.pelican_config.model_ids.some(model => !model.trim()) || form.value.pelican_config.model_ids.length < 49)
function addTestModel(model: string) {
  if (selectedTestModels.value.includes(model)) return
  if (isProbe.value || !form.value.model_id.trim()) { form.value.model_id = model; return }
  const models = form.value.pelican_config.model_ids
  const empty = models.findIndex(value => !value.trim())
  if (empty >= 0) models[empty] = model
  else if (models.length < 49) models.push(model)
}
const isRandom = computed(() => form.value.pelican_config.question_kind === 'random_reasoning')
const bps = computed(() => form.value.pelican_config.quality.bps)
// BPS 设置可见时，「满血后自动关闭 BPS」放在设置里，底部的通用恢复开关不再重复出现。
const bpsSettingsShown = computed(() => editsField('action') && isProbe.value && form.value.pelican_config.quality.action === 'enable_bps')
// 与账号批量编辑的 403 目标分组候选一致：OpenAI 分组，非简易模式下另含混合分组。
const bpsTargetGroups = computed(() => groups.value.filter(group => group.platform === 'openai' || (!auth.isSimpleMode && group.platform === 'composite')))
const formSnapshot = () => JSON.stringify([form.value, removeModelsInput.value, presetName.value, selectedAccounts.value, bulkRuleIds.value, bulkFields.value,
  editingTemplate.value ? [accountGroup.value, accountType.value, search.value, accountStatuses.value] : null])
function message(e: unknown) { const err = e as { response?: { data?: { message?: string; error?: string } }; message?: string }; return err.response?.data?.message || err.response?.data?.error || err.message || t('qualityOps.error') }
async function load() { await store.refresh() }
async function refreshOperations() { await store.refreshOperations() }
async function moreOperations() { await store.refreshOperations(true) }
async function searchAccounts(page = 1) {
  accountSelectionRequest++; selectingAccounts.value = false
  const request = ++accountRequest
  accountsLoading.value = true; accountsError.value = ''; accounts.value = []
  try {
    const data = await accountsAPI.list(page, 50, accountFilters())
    if (!alive || request !== accountRequest) return
    rememberAccounts(data.items)
    accounts.value = data.items; accountPage.value = page; accountPages.value = Math.max(1, Math.ceil(data.total / 50)); accountsTotal.value = data.total
  } catch (e) { if (alive && request === accountRequest) accountsError.value = message(e) }
  finally { if (request === accountRequest) accountsLoading.value = false }
}
function selectCurrentPage() {
  if (accountsLoading.value || selectingAccounts.value) return
  pickerAccountIds.value = [...new Set([...pickerAccountIds.value, ...selectableAccounts.value.map(account => account.id)])]
}
function clearAccountSelection() {
  accountSelectionRequest++; selectingAccounts.value = false; pickerAccountIds.value = []
}
// 按当前筛选翻完所有页，返回没有禁用原因（已有同类规则、探针不支持等）的账号 id。
// 中途被打断（切筛选、关弹窗）返回 null；翻页失败抛错，绝不返回半批结果。
async function collectSelectableAccountIds(aborted: () => boolean): Promise<number[] | null> {
  const filters = accountFilters()
  const ids = new Set<number>()
  for (let page = 1, pages = 1; page <= pages; page++) {
    const data = await accountsAPI.list(page, 50, filters)
    if (aborted()) return null
    pages = Math.max(1, Math.ceil(data.total / 50))
    rememberAccounts(data.items)
    for (const account of data.items) if (!accountDisabledReason(account)) ids.add(account.id)
  }
  return [...ids]
}
async function selectMatchingAccounts() {
  if (accountsLoading.value || selectingAccounts.value || busy.value) return
  const request = ++accountSelectionRequest
  selectingAccounts.value = true; accountsError.value = ''
  try {
    const ids = await collectSelectableAccountIds(() => !alive || request !== accountSelectionRequest)
    if (ids == null) return
    pickerAccountIds.value = [...new Set([...pickerAccountIds.value, ...ids])].filter(id => copying.value || bulkEditing.value || !existingAccountIds.value.has(id))
  } catch (e) { if (alive && request === accountSelectionRequest) accountsError.value = message(e) }
  finally { if (request === accountSelectionRequest) selectingAccounts.value = false }
}
function newPlan() {
  if (busy.value) return
  resetReuse()
  copying.value = false; copySourceAccountId.value = null; savingPreset.value = false; editingPreset.value = null; presetName.value = ''
  sourceAccountId.value = sourceRuleId.value = selectedPresetId.value = ''
  bulkEditing.value = false; bulkRuleIds.value = []; bulkFields.value = []; bulkProgress.value = ''
  closeDetails(); invalidateAccountRequests(); error.value = ''; editing.value = null; editingTemplate.value = null; form.value = defaults()
  selectedAccounts.value = []; search.value = ''; accountGroup.value = ''; accountType.value = ''
  accountStatuses.value = ['active']; accountScope.value = 'all'
  showForm.value = true; initialForm.value = formSnapshot(); void searchAccounts()
  if (!groups.value.length) void store.refreshGroups()
}
function reusedForm(rule: Pick<ScheduledTestPlan, 'model_id' | 'cron_expression' | 'enabled' | 'max_results' | 'pelican_config'>): ReturnType<typeof defaults> {
  if (!rule.pelican_config) throw new Error('qualityOps.copyUnsupported')
  const config = copyQualityRuleConfig(rule.pelican_config), draft = defaults()
  return { ...draft, model_id: rule.model_id, cron_expression: rule.cron_expression, enabled: rule.enabled, max_results: rule.max_results,
    pelican_config: { ...draft.pelican_config, ...config, rule_name: config.rule_name || '',
      question_kind: config.question_kind === 'state_probe' || config.question_kind === 'random_reasoning' ? config.question_kind : 'candy',
      test_channel: config.test_channel || 'account', model_ids: (config.model_ids || []).filter(model => model !== rule.model_id),
      quality: { ...draft.pelican_config.quality, ...config.quality,
        oauth_401_max_rounds: config.quality.oauth_401_max_rounds ?? 0,
        judge: config.quality.judge || draft.pelican_config.quality.judge, bps: qualityBPSForm(config.quality.bps) } } }
}
function copyRule(plan: ScheduledTestPlan) {
  if (busy.value || pending.value[plan.id]) return
  if (!canCopyRules.value) { error.value = t('qualityOps.copyPermission'); return }
  try {
    const next = reusedForm(plan)
    newPlan()
    copying.value = true; copySourceAccountId.value = plan.account_id
    sourceAccountId.value = String(plan.account_id); sourceRuleId.value = String(plan.id)
    form.value = next
    selectedAccounts.value = [plan.account_id]; accountStatuses.value = []; accountScope.value = 'manual'
    initialForm.value = formSnapshot(); void searchAccounts(); void loadJudgeModels()
  } catch (cause) { error.value = t(cause instanceof Error ? cause.message : 'qualityOps.copyUnsupported') }
}
function saveRuleAsPreset(plan: ScheduledTestPlan) {
  if (busy.value || pending.value[plan.id]) return
  if (!canCopyRules.value) { error.value = t('qualityOps.reusePermission'); return }
  try {
    const next = reusedForm(plan)
    newPlan()
    savingPreset.value = true; presetName.value = plan.pelican_config?.rule_name || ''
    form.value = next
    selectedAccounts.value = []; initialForm.value = formSnapshot(); invalidateAccountRequests()
    void loadJudgeModels()
  } catch (cause) { error.value = t(cause instanceof Error ? cause.message : 'qualityOps.copyUnsupported') }
}
function prefillReusable(rule: Parameters<typeof reusedForm>[0], label: string, sourceAccount: number | null, sourceRule: number | null) {
  const next = reusedForm(rule), ruleName = form.value.pelican_config.rule_name
  if (!reuseBefore.value) reuseBefore.value = JSON.parse(JSON.stringify(form.value))
  // Importing configuration never changes the editor mode, target IDs or field
  // checkboxes. Only creation mode uses copying for manual target validation.
  applyingReuse.value = true
  try {
    form.value = next
    form.value.pelican_config.rule_name = ruleName
    if (!editing.value && !bulkEditing.value) {
      copying.value = true; accountScope.value = 'manual'; copySourceAccountId.value = sourceAccount
    } else copying.value = false
    reuseSourceLabel.value = label; reuseSourceRuleId.value = sourceRule; reuseConfirmation.value = ''; error.value = ''
  } finally { applyingReuse.value = false }
  void loadJudgeModels()
}
function applySourceRule() {
  if (busy.value || editingTemplate.value || savingPreset.value || !canCopyRules.value) return
  const plan = sourceRules.value.find(rule => String(rule.id) === sourceRuleId.value)
  if (!plan) { error.value = t('qualityOps.sourceUnavailable'); return }
  if (pending.value[plan.id]) return
  if (plan.id === editing.value || (bulkEditing.value && bulkRuleIds.value.includes(plan.id))) { error.value = t('qualityOps.reuseSourceIsTarget'); return }
  try { prefillReusable(plan, `${name(plan)} · ${t('qualityOps.rule')} #${plan.id}`, plan.account_id, plan.id) }
  catch (cause) { error.value = t(cause instanceof Error ? cause.message : 'qualityOps.copyUnsupported') }
}
function applyPreset() {
  if (busy.value || editingTemplate.value || savingPreset.value || !canCopyRules.value || store.presetsLoading) return
  const preset = presets.value.find(item => String(item.id) === selectedPresetId.value)
  if (!preset) { error.value = t('qualityOps.presetUnavailable'); return }
  try { prefillReusable(preset, preset.name, null, null) }
  catch (cause) { error.value = t(cause instanceof Error ? cause.message : 'qualityOps.copyUnsupported') }
}
function edit(plan: ScheduledTestPlan) {
  if (busy.value || pending.value[plan.id] || plan.pelican_config?.quality?.action === 'openai_two_fa_admission') return
  resetReuse()
  copying.value = false; copySourceAccountId.value = null; savingPreset.value = false; editingPreset.value = null; presetName.value = ''
  sourceAccountId.value = sourceRuleId.value = selectedPresetId.value = ''
  bulkEditing.value = false; bulkRuleIds.value = []; bulkFields.value = []; bulkProgress.value = ''
  closeDetails(); error.value = ''; editing.value = plan.id; editingTemplate.value = null; selectedAccounts.value = []
  form.value = { ...defaults(), model_id: plan.model_id, cron_expression: plan.cron_expression, enabled: plan.enabled, max_results: plan.max_results, pelican_config: { ...defaults().pelican_config, ...JSON.parse(JSON.stringify(plan.pelican_config || {})) } }
  form.value.pelican_config.model_ids = (form.value.pelican_config.model_ids || []).filter(model => model !== form.value.model_id)
  form.value.pelican_config.quality.trigger_on_upstream_5xx ??= false
  form.value.pelican_config.quality.oauth_401_max_rounds ??= 0
  form.value.pelican_config.quality.retest_rest_minutes ??= 0
  form.value.pelican_config.quality.judge ||= defaults().pelican_config.quality.judge
  form.value.pelican_config.quality.bps = qualityBPSForm(form.value.pelican_config.quality.bps)
  showForm.value = true; initialForm.value = formSnapshot(); void loadJudgeModels()
}
// 编辑分组规则：沿用单条规则的表单，账户选择器只改筛选，范围固定为「所有匹配账户」。
function editTemplate(tpl: QualityRuleTemplate) {
  if (busy.value) return
  resetReuse()
  copying.value = false; copySourceAccountId.value = null; savingPreset.value = false; editingPreset.value = null; presetName.value = ''
  sourceAccountId.value = sourceRuleId.value = selectedPresetId.value = ''
  bulkEditing.value = false; bulkRuleIds.value = []; bulkFields.value = []; bulkProgress.value = ''
  closeDetails(); error.value = ''; editing.value = null; editingTemplate.value = tpl.id; selectedAccounts.value = []
  form.value = reusedForm(tpl)
  form.value.pelican_config.model_ids = (form.value.pelican_config.model_ids || []).filter(model => model !== form.value.model_id)
  form.value.pelican_config.quality.judge ||= defaults().pelican_config.quality.judge
  form.value.pelican_config.quality.bps = qualityBPSForm(form.value.pelican_config.quality.bps)
  const filter = tpl.account_filter || {}
  search.value = filter.search || ''; accountGroup.value = filter.group || ''; accountType.value = filter.type || ''
  accountStatuses.value = [...(filter.statuses ?? [])]; accountScope.value = 'all'
  showForm.value = true; initialForm.value = formSnapshot(); void searchAccounts(); void loadJudgeModels()
  if (!groups.value.length) void store.refreshGroups()
}
function editSelectedRules() {
  if (busy.value || selectedRulesPending.value) return
  const selected = plans.value.filter(plan => selectedRuleIds.value.includes(plan.id) && plan.pelican_config?.quality?.action !== 'openai_two_fa_admission')
  // Start from the first selected rule for convenience, but no field is applied
  // until explicitly checked. The request builder preserves each other value.
  if (selected.length) edit(selected[0])
  else newPlan()
  bulkSourceScope.value = ruleScope(form.value.pelican_config.quality.action)
  editing.value = null; bulkEditing.value = true
  bulkRuleIds.value = selected.map(plan => plan.id)
  search.value = ''; accountGroup.value = ''; accountType.value = ''; accountStatuses.value = []
  void searchAccounts()
  initialForm.value = formSnapshot()
}
function closeForm() {
  if (busy.value || deleteTargets.value.length) return
  if (initialForm.value !== formSnapshot()) { discardPrompt.value = true; return }
  showForm.value = false
}
// 新建规则选「开启 BPS」时默认勾上满血后自动关闭，改回其它处理方式时回到默认不勾（移出分组等仍需手动开启恢复）；
// 编辑已有规则或批量修改时保留原值。sync：打开表单时的替换在记录 initialForm 前就处理完。
watch(() => form.value.pelican_config.quality.action, (next, prev) => {
  if (applyingReuse.value || copying.value || savingPreset.value || editing.value || editingTemplate.value || bulkEditing.value) return
  if (next === 'enable_bps' || prev === 'enable_bps') form.value.pelican_config.quality.auto_restore = next === 'enable_bps'
  if (next === 'observe_only') form.value.pelican_config.quality.auto_restore = false
  selectedAccounts.value = selectedAccounts.value.filter(id => !existingAccountIds.value.has(id))
}, { flush: 'sync' })
function useCandy() { form.value.pelican_config.prompt = CANDY_PROMPT; form.value.pelican_config.quality.expected_answer = '21' }
function selectQuestionKind() {
  accountSelectionRequest++; selectingAccounts.value = false
  const config = form.value.pelican_config
  if (config.question_kind === STATE_PROBE_QUESTION) {
    config.test_channel = 'account'
    config.parallel_count = 1
    config.model_ids = []
    if (!copying.value && !editing.value && !editingTemplate.value && !bulkEditing.value && form.value.cron_expression === defaults().cron_expression) form.value.cron_expression = DEFAULT_STATE_PROBE_CRON
    if (!editing.value && !editingTemplate.value && !bulkEditing.value) selectedAccounts.value = selectedAccounts.value.filter(id => { const account = knownAccounts.get(id); return account && supportsStateProbeAccount(account) })
    return
  }
  // Switching auto-BPS rules to candy means observing BPS, not controlling it.
  const hadBPSAction = config.quality.action === 'enable_bps' || (bulkEditing.value && !bulkFields.value.includes('action') && plans.value.some(plan => bulkRuleIds.value.includes(plan.id) && plan.pelican_config?.quality?.action === 'enable_bps'))
  if (hadBPSAction) { config.test_channel = 'bps'; selectTestChannel() }
  config.quality.judge ||= defaults().pelican_config.quality.judge
  if (config.question_kind === 'random_reasoning') {
    config.prompt = ''
    config.quality.expected_answer = ''
    return
  }
  if (!config.prompt.trim()) useCandy()
}
function selectTestChannel() {
  if (form.value.pelican_config.test_channel !== 'bps') return
  form.value.pelican_config.quality.action = 'observe_only'
  form.value.pelican_config.quality.auto_restore = false
  if (bulkEditing.value) bulkFields.value = [...new Set<QualityRuleField>([...bulkFields.value, 'action', 'restore'])]
}
// 探针规则不发题目、不走判题模型：提交前去掉题目、参考答案和判题配置，并行固定为 1。
// BPS 设置只随「开启 BPS」提交。
function ruleModels(rule: { model_id: string; pelican_config?: { model_ids?: string[] } }) {
  return rule.pelican_config?.model_ids?.length ? rule.pelican_config.model_ids : [rule.model_id]
}
function payload() {
  const model_ids = isProbe.value ? [form.value.model_id] : [...new Set([form.value.model_id, ...form.value.pelican_config.model_ids].map(model => model.trim()))]
  if (model_ids.some(model => !model || model.length > 100)) throw new Error(t('qualityOps.modelRequired'))
  if (model_ids.length > 50 || model_ids.length * form.value.pelican_config.parallel_count > 100) throw new Error(t('qualityOps.tooManyModelSamples'))
  const max_results = Math.max(form.value.max_results, model_ids.length * form.value.pelican_config.parallel_count)
  const quality = form.value.pelican_config.quality
  let nextQuality: QualityPolicy
  try { nextQuality = qualityActionPayload(quality) } catch (cause) {
    throw new Error(t(cause instanceof Error ? cause.message : 'qualityOps.error'))
  }
  if (nextQuality.action === 'isolate_models' && isProbe.value) throw new Error(t('qualityOps.isolationRequiresCandy'))
  if (isRandom.value) {
    nextQuality.expected_answer = ''
    return { ...form.value, max_results, pelican_config: { ...form.value.pelican_config, model_ids, prompt: '', random_question: undefined, quality_batch: undefined, quality: nextQuality } }
  }
  if (!isProbe.value) return { ...form.value, max_results, pelican_config: { ...form.value.pelican_config, model_ids, quality: nextQuality } }
  nextQuality.expected_answer = ''
  delete nextQuality.judge
  return { ...form.value, pelican_config: { ...form.value.pelican_config, model_ids, prompt: '', parallel_count: 1, quality: nextQuality } }
}
function editPreset() {
  if (busy.value || !canCopyRules.value || store.presetsLoading) return
  const preset = presets.value.find(item => String(item.id) === selectedPresetId.value)
  if (!preset) { error.value = t('qualityOps.presetUnavailable'); return }
  try {
    const next = reusedForm(preset)
    newPlan(); savingPreset.value = true; editingPreset.value = preset.id; presetName.value = preset.name
    form.value = next; selectedAccounts.value = []; invalidateAccountRequests(); initialForm.value = formSnapshot()
    void loadJudgeModels()
  } catch (cause) { error.value = t(cause instanceof Error ? cause.message : 'qualityOps.copyUnsupported') }
}
async function save() {
  if (busy.value || ownershipWorking.value || selectingAccounts.value) return
  if ((copying.value || savingPreset.value) && !canCopyRules.value) { error.value = t(savingPreset.value ? 'qualityOps.reusePermission' : 'qualityOps.copyPermission'); return }
  if (editing.value && ownershipBlocked.value) { error.value = t('qualityOps.scopeConflictHelp'); return }
  if (bulkEditing.value) { await saveBulkRules(); return }
  busy.value = true; error.value = ''; notice.value = ''
  const scope = identity()
  let changed = false
  try {
    if (copying.value && !editing.value && !bulkEditing.value && !savingPreset.value) {
      if (!selectedAccounts.value.length) throw new Error(t('qualityOps.copyAccountRequired'))
      if (selectedAccounts.value.some(id => existingAccountIds.value.has(id))) throw new Error(t('qualityOps.copyAccountConflict'))
    }
    const judge = form.value.pelican_config.quality.judge
    if (!isProbe.value && !qualityJudgeConfigured(judge)) throw new Error(t('qualityOps.configureJudge'))
    if (form.value.pelican_config.quality.action === 'enable_bps') {
      if (!isProbe.value) throw new Error(t('qualityOps.bpsRequiresProbe'))
      const invalid = qualityBPSError(bps.value)
      if (invalid) throw new Error(t(invalid))
    }
    if (savingPreset.value) {
      const name = presetName.value.trim()
      if (!name || Array.from(name).length > 100) throw new Error(t('qualityOps.presetNameRequired'))
      const { model_id, cron_expression, enabled, max_results, pelican_config } = payload()
      const config = copyQualityRuleConfig(pelican_config)
      delete config.rule_name
      const presetBody = { name, model_id, cron_expression, enabled, max_results, pelican_config: config }
      if (editingPreset.value) await updateQualityPreset(editingPreset.value, presetBody)
      else await createQualityPreset(presetBody)
      if (!alive || scope !== identity()) return
      showForm.value = false; notice.value = t('qualityOps.presetSaved')
      await store.refreshPresets()
      if (alive && scope === identity() && store.presetsError) error.value = t('qualityOps.presetsLoadFailed')
      return
    }
    if (editingTemplate.value || (!copying.value && !editing.value && accountScope.value === 'all')) {
      // 「所有匹配账户」保存为分组规则：后端给现在和之后匹配的账户各建一条规则，已有同类规则或不支持的自动跳过。
      const { model_id, cron_expression, enabled, max_results, pelican_config } = payload()
      const body = { model_id, cron_expression, enabled, max_results, pelican_config, account_filter: templateFilter() }
      if (editingTemplate.value) {
        const result = await updateQualityTemplate(editingTemplate.value, body); changed = true
        if (!alive || scope !== identity()) return
        if (result.failed) {
          // 分组规则本身已保存，没同步上的账号规则再保存一次即可重试。
          error.value = t('qualityOps.templateUpdatePartial', { failed: result.failed, updated: result.updated })
          initialForm.value = formSnapshot(); await store.refreshRules(true); return
        }
        notice.value = t('qualityOps.templateUpdated', { updated: result.updated, created: result.created })
      } else {
        const result = await createQualityTemplate(body); changed = true
        if (!alive || scope !== identity()) return
        notice.value = result.created ? t('qualityOps.templateCreated', { count: result.created }) : t('qualityOps.templateCreatedNone')
      }
      showForm.value = false; await store.refreshRules(true); return
    }
    if (isProbe.value && !editing.value) {
      if (!selectedAccounts.value.length || selectedAccounts.value.some(id => {
        const account = knownAccounts.get(id)
        // The copied account can be outside the current picker page. Let the
        // create API validate it without fetching credential-bearing details.
        return account ? !supportsStateProbeAccount(account) : !copying.value || id !== copySourceAccountId.value
      })) throw new Error(t('qualityOps.probeAccountUnsupported'))
    }
    const body = payload()
    if (editing.value) { await scheduledTests.update(editing.value, reuseSourceLabel.value ? { ...body, preserve_quality_state: true } : body); changed = true }
    else {
      for (const id of [...selectedAccounts.value]) {
        await scheduledTests.create({ ...body, account_id: id }); changed = true
        if (!alive || scope !== identity()) return
        selectedAccounts.value = selectedAccounts.value.filter(value => value !== id)
      }
    }
    if (!alive || scope !== identity()) return
    showForm.value = false; notice.value = t('qualityOps.saved'); await store.refreshRules(true)
  } catch (e) { if (alive && scope === identity()) { error.value = savingPreset.value ? `${t('qualityOps.presetSaveFailed')} ${message(e)}` : copying.value ? `${t('qualityOps.copyFailed')} ${message(e)}` : message(e); if (changed) await store.refreshRules(true) } }
  finally { busy.value = false }
}
function templateFilter(): QualityRuleAccountFilter {
  return { group: accountGroup.value || undefined, type: accountType.value || undefined, search: search.value.trim() || undefined, statuses: [...accountStatuses.value] }
}
async function saveBulkRules() {
  if (reuseSourceRuleId.value && bulkRuleIds.value.includes(reuseSourceRuleId.value)) { error.value = t('qualityOps.reuseSourceIsTarget'); return }
  if (reuseSourceLabel.value && !reuseConfirmed.value) { error.value = t('qualityOps.reuseConfirmRequired'); return }
  if (!bulkRuleIds.value.length || !bulkFields.value.length) return
  busy.value = true; error.value = ''; notice.value = ''; bulkProgress.value = ''
  const scope = identity(), ids = [...bulkRuleIds.value]
  const failed: number[] = [], failures: string[] = []
  let completed = 0
  try {
    // Refresh before merging nested config, then validate the whole batch before
    // issuing any writes. A deleted rule must not silently disappear from a save.
    await store.refreshRules(true)
    if (!alive || scope !== identity()) return
    if (store.rulesError) throw new Error(store.rulesError)
    if (reuseSourceLabel.value && !reuseConfirmed.value) throw new Error(t('qualityOps.reuseConfirmRequired'))
    const requests = ids.map(id => {
      const plan = plans.value.find(item => item.id === id)
      if (!plan) throw new Error(t('qualityOps.ruleUnavailable', { id }))
      try {
        return { id, body: reusedBulkPatch(plan) }
      } catch (e) {
        const detail = message(e)
        throw new Error(`${t('qualityOps.rule')} #${id}: ${te(detail) ? t(detail) : detail}`)
      }
    })
    for (const { id, body } of requests) {
      if (!alive || scope !== identity()) return
      try {
        await scheduledTests.update(id, reuseSourceLabel.value ? { ...body, preserve_quality_state: true } : body)
        if (!alive || scope !== identity()) return
        selectedRuleIds.value = selectedRuleIds.value.filter(value => value !== id)
        bulkRuleIds.value = bulkRuleIds.value.filter(value => value !== id)
      } catch (e) {
        if (!alive || scope !== identity()) return
        failed.push(id); failures.push(`#${id}: ${message(e)}`)
      }
      completed++
      bulkProgress.value = t('qualityOps.bulkProgress', { completed, total: ids.length })
    }
    if (failed.length) {
      bulkRuleIds.value = failed
      error.value = `${t('qualityOps.bulkPartial', { saved: ids.length - failed.length, failed: failed.length })} ${failures.join(' / ')}`
    } else {
      showForm.value = false
      notice.value = t('qualityOps.bulkSaved', { count: ids.length })
    }
    await store.refreshRules(true)
  } catch (e) {
    if (alive && scope === identity()) {
      const detail = message(e)
      error.value = te(detail) ? t(detail) : detail
    }
  } finally { busy.value = false }
}
async function planAction(plan: ScheduledTestPlan, kind: string, fn: () => Promise<unknown>) {
  if (busy.value || pending.value[plan.id]) return
  pending.value[plan.id] = kind; error.value = ''; notice.value = ''
  const scope = identity()
  try {
    await fn()
    if (!alive || scope !== identity()) return
    if (kind === 'run') notice.value = t('qualityOps.queued')
    await store.refreshRules(true)
  } catch (e) { if (alive && scope === identity()) error.value = message(e) }
  finally { delete pending.value[plan.id] }
}
async function toggle(plan: ScheduledTestPlan) { await planAction(plan, 'toggle', () => scheduledTests.update(plan.id, { enabled: !plan.enabled })) }
async function toggleTemplate(tpl: QualityRuleTemplate) {
  if (templatePending.value[tpl.id]) return
  templatePending.value[tpl.id] = 'toggle'; error.value = ''; notice.value = ''
  const scope = identity()
  try {
    const result = await updateQualityTemplate(tpl.id, { enabled: !tpl.enabled })
    if (!alive || scope !== identity()) return
    if (result.failed) error.value = t('qualityOps.templateUpdatePartial', { failed: result.failed, updated: result.updated })
    await store.refreshRules(true)
  } catch (e) { if (alive && scope === identity()) error.value = message(e) }
  finally { delete templatePending.value[tpl.id] }
}
function openTemplateDelete() {
  templateDeleteTarget.value = templates.value.find(tpl => tpl.id === editingTemplate.value) || null
  deletePlansWithTemplate.value = true
}
async function confirmTemplateDelete() {
  if (!templateDeleteTarget.value || templateDeleting.value) return
  templateDeleting.value = true; error.value = ''; notice.value = ''
  const target = templateDeleteTarget.value, removePlans = deletePlansWithTemplate.value && target.plan_ids.length > 0, scope = identity()
  try {
    const { deleted } = await deleteQualityTemplate(target.id, removePlans)
    if (!alive || scope !== identity()) return
    templateDeleteTarget.value = null; showForm.value = false
    if (removePlans && historyPlan.value && target.plan_ids.includes(historyPlan.value.id)) closeDetails()
    notice.value = removePlans ? t('qualityOps.templateDeleted', { count: deleted }) : t('qualityOps.templateDeletedKept')
    await Promise.all([store.refreshRules(true), store.refreshOperations()])
  } catch (e) { if (alive && scope === identity()) error.value = message(e) }
  finally { templateDeleting.value = false }
}
// 分组规则卡片上的筛选说明，例如「全部分组 · 全部类型 · 正常」。
function templateFilterSummary(tpl: QualityRuleTemplate) {
  const filter = tpl.account_filter || {}
  const group = !filter.group ? t('qualityOps.allAccountGroups') : filter.group === 'ungrouped' ? t('qualityOps.ungroupedAccounts') : groupNames.value[Number(filter.group)] || `#${filter.group}`
  const typeOption = qualityAccountTypeOptions.find(option => option.value === filter.type)
  const type = !filter.type ? t('qualityOps.allAccountTypes') : typeOption ? t(typeOption.label) : filter.type
  const statuses = filter.statuses?.length
    ? filter.statuses.map(status => { const option = qualityAccountStatusOptions.find(item => item.value === status); return option ? t(option.label) : status }).join(' / ')
    : t('qualityOps.allAccountStatuses')
  return [group, type, statuses, filter.search ? t('qualityOps.templateSearch', { search: filter.search }) : ''].filter(Boolean).join(' · ')
}
async function run(plan: ScheduledTestPlan) { await planAction(plan, 'run', () => runQualityPlan(plan.id)) }
function requestDelete(ids: number[]) {
  if (busy.value || selectingAccounts.value || ids.some(id => pending.value[id])) return
  // Keep the confirmation scope fixed even if selection or search changes.
  const selected = new Set(ids)
  deleteTargets.value = plans.value.filter(plan => selected.has(plan.id))
  deleteError.value = deleteProgress.value = ''
  deleteReady.value = {}; deleteFences.value = {}; deleteConflicts.value = []
}
async function confirmDelete() {
  if (deleteReviewTargets.value.some(plan => !deleteReady.value[plan.id])) return
  if (!deleteTargets.value.length || deleting.value || busy.value || deleteTargets.value.some(plan => pending.value[plan.id])) return
  deleting.value = busy.value = true
  deleteError.value = deleteProgress.value = error.value = notice.value = ''
  const targets = [...deleteTargets.value], scope = identity()
  const failed: ScheduledTestPlan[] = [], failures: string[] = []
  let completed = 0
  try {
    for (const plan of targets) {
      if (!alive || scope !== identity()) return
      try {
        if (deleteReviewTargets.value.some(target => target.id === plan.id)) {
          await scheduledTests.delete(plan.id, { fence: deleteFences.value[plan.id], confirm: true })
        } else await scheduledTests.delete(plan.id)
        if (!alive || scope !== identity()) return
        selectedRuleIds.value = selectedRuleIds.value.filter(id => id !== plan.id)
        bulkRuleIds.value = bulkRuleIds.value.filter(id => id !== plan.id)
        store.plans = plans.value.filter(item => item.id !== plan.id)
        store.operations = operations.value.filter(item => item.plan_id !== plan.id)
        if (store.selectedPlanId === plan.id) store.selectedPlanId = null
        if (historyPlan.value?.id === plan.id) closeDetails()
      } catch (e) {
        if (!alive || scope !== identity()) return
        const failure = e as { reason?: string; response?: { data?: { reason?: string } } }
        if ((failure.reason ?? failure.response?.data?.reason) === 'QUALITY_ISOLATION_SCOPE_CONFLICT' || (failure.reason ?? failure.response?.data?.reason) === 'QUALITY_OWNERSHIP_PREVIEW_CHANGED') { deleteConflicts.value.push(plan.id); deleteReady.value[plan.id] = false }
        failed.push(plan); failures.push(`#${plan.id}: ${message(e)}`)
      }
      completed++
      deleteProgress.value = t('qualityOps.bulkProgress', { completed, total: targets.length })
    }
    deleteTargets.value = failed
    if (failed.length) {
      deleteError.value = `${t('qualityOps.bulkDeletePartial', { deleted: targets.length - failed.length, failed: failed.length })} ${failures.join(' / ')}`
    } else {
      showForm.value = false
      notice.value = t('qualityOps.rulesDeleted', { count: targets.length })
    }
    await Promise.all([store.refreshRules(true), store.refreshOperations()])
  } finally { deleting.value = busy.value = false }
}
function judgmentReason(result: ScheduledTestResult) {
  const judgment = result.quality_judgment
  if (judgment?.kind !== 'jev') return judgment?.reason || '—'
  const reason = judgment.reason.replace(/^judge_jev_/, '')
  const known = ['not_configured', 'config_unavailable', 'disabled', 'invalid_config', 'invalid_input', 'capacity', 'canceled', 'keys_unavailable', 'invalid_endpoint', 'transport_unavailable', 'timeout', 'credentials_rejected', 'rate_limited', 'invalid_response', 'request_failed', 'uncertain', 'correct', 'incorrect']
  return t(`qualityOps.jevReasons.${known.includes(reason) ? reason : 'uncertain'}`)
}
function selectJudgeKind(event: Event) {
  const judge = form.value.pelican_config.quality.judge
  judge.kind = (event.target as HTMLSelectElement).value === 'jev' ? 'jev' : 'llm'
  if (judge.kind === 'jev') { judge.group_id = 0; judge.model_id = '' }
  void loadJudgeModels()
}
async function loadJudgeModels() {
  const request = ++judgeModelsRequest
  const id = form.value.pelican_config.quality.judge.group_id
  judgeModels.value = []
  if (!id || form.value.pelican_config.quality.judge.kind === 'jev') return
  try { const models = await groupsAPI.getModelAllowlistCandidates(id); if (alive && request === judgeModelsRequest) judgeModels.value = models }
  catch { if (alive && request === judgeModelsRequest) error.value = t('qualityOps.judgeModelsUnavailable') }
}
function closeDetails() { detailRequest++; answerRequest++; historyPlan.value = null; detailOperation.value = null; selectedResult.value = null; selectedResultId.value = null; results.value = []; detailsError.value = ''; detailsLoading.value = answerLoading.value = false; loadedAnswers.clear() }
async function history(plan: ScheduledTestPlan) {
  closeDetails(); historyPlan.value = plan; detailsLoading.value = true
  const request = ++detailRequest
  try {
    const data = await scheduledTests.listResults(plan.id, 100, false)
    if (!alive || request !== detailRequest) return
    results.value = data
    if (data[0]) await selectResult(data[0])
  } catch (e) { if (alive && request === detailRequest) detailsError.value = message(e) }
  finally { if (request === detailRequest) detailsLoading.value = false }
}
async function operationDetails(operation: QualityOperation) {
  closeDetails(); detailOperation.value = operation
  historyPlan.value = plans.value.find(p => p.id === operation.plan_id) || { id: operation.plan_id, account_id: operation.account_id, account_name: operation.account_name } as ScheduledTestPlan
  results.value = operation.result_ids.map(id => ({ ...operation, id, status: '', error_message: '', response_text: '' }))
  if (results.value[0]) await selectResult(results.value[0])
}
async function selectResult(result: ScheduledTestResult) {
  if (!historyPlan.value) return
  const request = ++answerRequest, planId = historyPlan.value.id
  selectedResultId.value = result.id; selectedResult.value = null; detailsError.value = ''
  const cached = loadedAnswers.get(result.id)
  if (cached) { selectedResult.value = cached; answerLoading.value = false; return }
  answerLoading.value = true
  try {
    const answer = await scheduledTests.getResult(planId, result.id)
    if (!alive || request !== answerRequest) return
    loadedAnswers.set(result.id, answer); selectedResult.value = answer
    results.value = results.value.map(item => item.id === result.id ? answer : item)
  } catch (e) { if (alive && request === answerRequest) detailsError.value = message(e) }
  finally { if (request === answerRequest) answerLoading.value = false }
}
function retryDetails() {
  if (selectedResultId.value !== null) { const result = results.value.find(r => r.id === selectedResultId.value); if (result) void selectResult(result) }
  else if (historyPlan.value) void history(historyPlan.value)
}
function navigateOperation(offset: number) { const next = filteredOperations.value[operationIndex.value + offset]; if (next) void operationDetails(next) }
watch(() => identity(), () => { resetReuse(); error.value = notice.value = ''; closeDetails(); showForm.value = false; copying.value = false; copySourceAccountId.value = null; savingPreset.value = false; editingPreset.value = null; presetName.value = ''; sourceAccountId.value = sourceRuleId.value = selectedPresetId.value = ''; discardPrompt.value = false; deleteTargets.value = []; deleteError.value = deleteProgress.value = ''; editingTemplate.value = null; templateDeleteTarget.value = null; templatePending.value = {}; accounts.value = []; knownAccounts.clear(); selectedRuleIds.value = []; bulkRuleIds.value = []; bulkEditing.value = false; accountRequest++; judgeModelsRequest++ })
const operationsRoute = useRoute()
onMounted(() => {
  const planId = Number(operationsRoute?.query.plan_id)
  if (Number.isSafeInteger(planId) && planId > 0) store.selectedPlanId = planId
  void load()
  poll = setInterval(() => { if (document.visibilityState === 'visible' && !refreshing.value && !deleting.value) { void store.refreshRules(); void store.refreshOperations() } }, 30_000)
})
onBeforeUnmount(() => { alive = false; accountSelectionRequest++; detailRequest++; answerRequest++; accountRequest++; judgeModelsRequest++; if (poll) clearInterval(poll); loadedAnswers.clear() })
</script>

<style scoped>
.quality-workspace { height: calc(100dvh - 8rem - 1px); min-height: 34rem; @apply flex w-full min-w-0 flex-col gap-4 text-gray-900 dark:text-gray-100; }
.quality-workspace > :not(.workspace-columns) { flex-shrink: 0; }
.quality-workspace > nav { margin-bottom: 0; max-width: 100%; }
.workspace-heading { @apply flex flex-wrap items-center justify-between gap-4; }
.workspace-eyebrow { @apply mb-1 text-[11px] font-semibold tracking-widest text-primary-600 dark:text-primary-400; }
.workspace-heading h2 { @apply text-2xl font-semibold tracking-tight; }
.workspace-subtitle { @apply mt-1.5 text-sm text-gray-500 dark:text-gray-400; }
.heading-actions { @apply flex flex-wrap items-center gap-2; }
.heading-actions .btn, .detail-footer .btn { @apply inline-flex items-center gap-2; }
.updated-label { @apply mr-2 hidden text-xs tabular-nums text-gray-400 xl:block; }
.workspace-message { @apply flex items-center justify-between gap-4 rounded-xl border px-4 py-3 text-sm; }
.message-error, .editor-error { @apply border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; }
.message-success { @apply border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300; }
.workspace-summary { @apply grid grid-cols-2 rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900 lg:grid-cols-4; }
.summary-cell { @apply relative flex items-center gap-3 px-5 py-4; }
.summary-cell + .summary-cell { @apply lg:border-l lg:border-gray-100 lg:dark:border-dark-700; }
.summary-icon { @apply flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-slate-100 text-slate-500 dark:bg-dark-800 dark:text-gray-400; }
.summary-icon.is-teal { @apply bg-emerald-50 text-emerald-600 dark:bg-emerald-950/40; }
.summary-icon.is-amber { @apply bg-amber-50 text-amber-600 dark:bg-amber-950/40; }
.summary-cell div > span { @apply block text-xs text-gray-500 dark:text-gray-400; }
.summary-cell strong { @apply mt-1 block text-2xl font-semibold tabular-nums tracking-tight; }
.summary-cell strong small { @apply text-sm font-normal text-gray-400; }
.summary-link { @apply ml-auto inline-flex items-center gap-1 text-xs text-primary-600; }
.workspace-columns { display: grid; grid-template-columns: clamp(320px, 30%, 480px) minmax(0, 1fr); gap: 16px; flex: 1; min-height: 0; }
.workspace-panel { @apply flex min-h-0 min-w-0 flex-col overflow-hidden rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.panel-heading { @apply flex shrink-0 items-center justify-between gap-3 px-5 pb-4 pt-5; }
.panel-heading h3 { @apply flex items-center gap-2 text-base font-semibold; }
.panel-heading p { @apply mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400; }
.count-label { @apply rounded-md bg-gray-100 px-1.5 py-0.5 text-xs tabular-nums text-gray-500 dark:bg-dark-800; }
.icon-button { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-gray-200 text-gray-500 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800; }
.rule-search { @apply mx-4 mb-3 flex shrink-0 items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-3 text-gray-400 dark:border-dark-700 dark:bg-dark-800; }
.rule-search input { @apply h-9 min-w-0 flex-1 bg-transparent text-sm text-gray-800 outline-none dark:text-gray-100; }
.all-accounts { @apply mx-4 mb-3 flex shrink-0 items-center gap-2 rounded-lg px-3 py-2.5 text-left text-sm text-gray-500 dark:text-gray-400; }
.rule-selection-toolbar { @apply mx-4 mb-3 flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 text-xs text-gray-500 dark:text-gray-400; }
.rule-check-all { @apply flex cursor-pointer items-center gap-2; }
.rule-selection-toolbar > button { @apply text-primary-600; }
.rule-bulk-actions { @apply ml-auto flex items-center gap-2; }
.bulk-edit-button { @apply inline-flex items-center gap-1.5 px-3 py-2 text-xs; }
.bulk-delete-button { @apply px-3 py-2 text-xs text-red-600 dark:text-red-400; }
.rule-checkbox { @apply flex h-8 shrink-0 cursor-pointer items-center; }
.rule-checkbox input, .rule-check-all input, .bulk-fields input { @apply h-4 w-4 shrink-0 cursor-pointer accent-primary-600; }
.bulk-editor-intro { @apply space-y-3 rounded-xl border border-primary-100 bg-primary-50/50 p-4 text-sm dark:border-primary-900 dark:bg-primary-950/20; }
.bulk-editor-intro p { @apply leading-relaxed text-gray-600 dark:text-gray-300; }
.bulk-editor-intro summary { @apply cursor-pointer text-primary-700 dark:text-primary-300; }
.bulk-editor-intro ul { @apply mt-2 max-h-32 space-y-1 overflow-auto break-words text-xs text-gray-500; }
.bulk-fields { @apply grid gap-3 sm:grid-cols-2; }
.bulk-fields label { @apply flex cursor-pointer items-center gap-2; }
.all-accounts span:last-child { @apply ml-auto text-xs tabular-nums; }
.all-accounts.selected { @apply bg-primary-50 font-medium text-primary-700 dark:bg-primary-950/40 dark:text-primary-300; }
.rules-scroll { @apply min-h-0 flex-1 space-y-3 overflow-y-auto overscroll-contain px-4 pb-4; scrollbar-gutter: stable; }
.rule-card { @apply rounded-xl border border-gray-200 p-3.5 transition-colors dark:border-dark-700; }
.rule-card.selected { @apply border-primary-300 bg-primary-50/30 ring-1 ring-primary-100 dark:border-primary-700 dark:bg-primary-950/20 dark:ring-primary-900; }
.rule-card.checked { @apply border-primary-400 bg-primary-50/60 dark:border-primary-600 dark:bg-primary-950/40; }
.rule-card-top { @apply flex items-start gap-2; }
.rule-select { @apply flex min-w-0 flex-1 items-start gap-2.5 text-left; }
.account-avatar { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-slate-100 text-xs font-semibold text-slate-600 dark:bg-dark-700 dark:text-gray-300; }
.rule-select strong { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; @apply text-[13px] font-semibold leading-5; }
.rule-meta { @apply mt-1 flex gap-1.5 text-[11px] tabular-nums text-gray-400; }
.state-toggle { @apply inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2 py-1 text-[10px] font-medium; }
.state-toggle > span, .outcome-badge > span { @apply h-1.5 w-1.5 rounded-full bg-current; }
.state-enabled { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400; }
.state-paused { @apply bg-gray-100 text-gray-500 dark:bg-dark-800; }
.rule-model { @apply mt-3 flex flex-wrap items-center gap-2 text-[11px] text-gray-600 dark:text-gray-400; }
.rule-model code { overflow-wrap: anywhere; @apply min-w-0 rounded bg-gray-100 px-1.5 py-0.5 dark:bg-dark-800; }
.running-label { @apply text-primary-600; }
.probe-tag { @apply rounded bg-violet-50 px-1.5 py-0.5 text-violet-700 dark:bg-violet-950/40 dark:text-violet-300; }
.template-tag { @apply rounded bg-sky-50 px-1.5 py-0.5 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300; }
.template-section { @apply space-y-2 border-b border-dashed border-gray-200 pb-3 dark:border-dark-700; }
.template-section-title { @apply flex flex-wrap items-baseline gap-x-2 text-xs font-semibold text-sky-700 dark:text-sky-300; }
.template-section-title span { @apply font-normal text-gray-400; }
.template-card { @apply rounded-xl border border-sky-200 bg-sky-50/40 p-3.5 dark:border-sky-900 dark:bg-sky-950/20; }
.template-card strong { overflow-wrap: anywhere; @apply block text-[13px] font-semibold leading-5; }
.template-avatar { @apply bg-sky-100 text-sky-600 dark:bg-sky-950/60 dark:text-sky-300; }
.template-hint { @apply rounded-lg border border-sky-200 bg-sky-50/60 p-3 text-sm leading-relaxed text-sky-800 dark:border-sky-900 dark:bg-sky-950/20 dark:text-sky-200; }
.rule-target { @apply mt-2 flex min-w-0 items-center gap-1.5 text-[11px] text-gray-500; }
.rule-target span { @apply truncate; }
.rule-schedule { @apply mt-3 flex justify-between gap-2 text-[11px] tabular-nums text-gray-400; }
.rule-schedule time { @apply text-gray-600 dark:text-gray-300; }
.rule-warning { @apply mt-2 text-xs text-amber-600; }
.rule-actions { @apply mt-3 flex flex-wrap items-center gap-1 border-t border-gray-100 pt-2 dark:border-dark-700; }
.rule-actions button { @apply inline-flex min-h-8 flex-1 items-center justify-center whitespace-nowrap gap-1 rounded-md text-[11px] text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-800 dark:hover:text-gray-100; }
.rule-actions button:first-child { @apply text-primary-600; }
.operations-toolbar { @apply flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-t border-gray-100 bg-gray-50/60 px-5 py-3 dark:border-dark-700 dark:bg-dark-800/50; }
.scope-label { overflow-wrap: anywhere; @apply flex min-w-0 items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-300; max-width: 65%; }
.scope-dot { @apply h-1.5 w-1.5 shrink-0 rounded-full bg-primary-500; }
.operations-toolbar select { @apply max-w-full rounded-lg border border-gray-200 bg-white px-2 py-1.5 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-300; }
.operations-scroll { @apply min-h-0 flex-1 overflow-auto overscroll-contain; scrollbar-gutter: stable; }
.operations-table { @apply w-full text-left text-xs; min-width: 620px; }
.operations-table thead { @apply sticky top-0 z-10 bg-white text-gray-400 dark:bg-dark-900; box-shadow: 0 1px 0 rgb(148 163 184 / .15); }
.operations-table th { @apply whitespace-nowrap px-4 py-3 font-medium; }
.operations-table td { @apply border-b border-gray-100 px-4 py-4 align-middle dark:border-dark-800; }
.operations-table tbody tr:hover, .selected-row { @apply bg-slate-50/80 dark:bg-dark-800/70; }
.time-cell { @apply whitespace-nowrap tabular-nums; }
.time-cell strong { @apply block text-xs font-medium; }
.time-cell > span, .account-cell > span { @apply mt-1 block text-[10px] text-gray-400; }
.account-cell { max-width: 220px; }
.account-cell button { @apply max-w-full text-left hover:text-primary-600; }
.account-cell strong { @apply block truncate font-medium; max-width: 190px; }
.test-count { @apply inline-flex items-center gap-1.5 whitespace-nowrap font-medium tabular-nums; }
.test-passed { @apply text-emerald-600 dark:text-emerald-400; }
.test-other { @apply text-rose-500 dark:text-rose-400; }
.cell-secondary { @apply mt-1.5 block truncate text-[10px] text-gray-400; max-width: 170px; }
.outcome-badge { @apply inline-flex items-center gap-1.5 whitespace-nowrap rounded-md px-2 py-1 text-[11px] font-medium; }
.tone-success { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.tone-warning { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300; }
.tone-muted { @apply bg-slate-100 text-slate-600 dark:bg-dark-700 dark:text-gray-300; }
.tone-neutral { @apply bg-gray-100 text-gray-500 dark:bg-dark-800; }
.tone-danger { @apply bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-300; }
.detail-button { @apply inline-flex min-h-8 items-center gap-1 whitespace-nowrap rounded-md px-1 text-xs text-gray-400 hover:text-primary-600; }
.operations-footer { @apply flex min-h-14 shrink-0 items-center justify-between gap-3 border-t border-gray-100 px-5 text-[11px] text-gray-400 dark:border-dark-700; }
.operations-footer button { @apply inline-flex items-center gap-1.5 text-primary-600; }
.panel-error { @apply m-3 flex items-center justify-between gap-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-950/40 dark:text-red-300; }
.panel-error button { @apply shrink-0 underline; }
.panel-empty { @apply flex min-h-52 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-gray-400; }
.panel-empty p { @apply max-w-sm text-xs leading-relaxed; }
.rule-skeleton { @apply space-y-3 rounded-xl border border-gray-100 p-5 dark:border-dark-700; }
.cell-skeleton, .rule-skeleton span { @apply block h-3 rounded bg-gray-100 dark:bg-dark-700; animation: quality-pulse 1.7s ease-in-out infinite; }
.rule-skeleton span:first-child { @apply h-5 w-3/4; }
.rule-skeleton span:last-child { @apply w-1/2; }
.skeleton-stack { @apply space-y-3; }
.detail-heading { @apply mb-5 flex items-center gap-3; }
.detail-heading h3 { @apply text-base font-semibold; }
.detail-heading p { @apply mt-1 text-xs text-gray-400; }
.account-management-link { @apply ml-auto inline-flex shrink-0 items-center gap-1 text-xs text-primary-600; }
.round-overview { @apply mb-5 grid grid-cols-2 divide-x divide-gray-200 rounded-xl border border-gray-200 bg-gray-50 dark:divide-dark-700 dark:border-dark-700 dark:bg-dark-800; }
.round-overview > div { @apply p-4; }
.round-overview span { @apply block text-xs text-gray-500; }
.round-overview strong { @apply mt-2 block text-sm font-semibold; }
.conflict-explanation { @apply mb-5 flex items-start gap-3 rounded-xl border border-amber-200 bg-amber-50/80 p-4 text-amber-900 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200; }
.conflict-explanation > svg { @apply mt-0.5 shrink-0; }
.conflict-explanation h4 { @apply mb-2 text-sm font-semibold; }
.conflict-explanation p, .conflict-explanation li, .conflict-explanation strong { @apply text-xs leading-6; }
.conflict-causes summary { @apply cursor-pointer text-xs font-medium leading-7; }
.conflict-explanation ul { @apply my-2 list-disc pl-4; }
.conflict-limit { @apply mb-2 text-amber-700 dark:text-amber-400; }
.action-explanation { @apply mb-4 flex gap-2 rounded-lg bg-gray-50 p-3 text-xs leading-relaxed text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.action-explanation svg { @apply shrink-0; }
.detail-policy { @apply mb-5 space-y-2 text-xs; }
.detail-policy > div { @apply flex justify-between gap-4; }
.detail-policy dt { @apply shrink-0 text-gray-400; }
.detail-policy dd { @apply min-w-0 break-words text-right text-gray-600 dark:text-gray-300; }
.detail-policy dd p { @apply mt-1 text-gray-500 dark:text-gray-400; }
.model-summary { @apply mb-5 min-w-0; }
.model-summary h4 { @apply mb-2 text-sm font-semibold; }
.model-summary table { @apply w-full table-fixed text-left text-xs; }
.model-summary th { @apply py-2 font-normal text-gray-500 dark:text-gray-400; }
.model-summary td { @apply break-words border-t border-gray-200 py-3 pr-3 dark:border-dark-700; }
.model-summary th:first-child { width: 42%; }
.model-summary code { @apply break-all; }
.detail-grid { display: grid; grid-template-columns: 145px minmax(0, 1fr); @apply gap-4; }
.result-navigation { @apply max-h-[55dvh] space-y-2 overflow-y-auto; }
.result-navigation > p { @apply mb-3 flex justify-between text-xs text-gray-400; }
.result-navigation button { @apply block w-full rounded-lg border border-transparent p-2.5 text-left text-xs hover:bg-gray-50 dark:hover:bg-dark-800; }
.result-navigation button.selected { @apply border-primary-200 bg-primary-50 dark:border-primary-800 dark:bg-primary-950/30; }
.result-navigation button span { @apply block; }
.result-navigation button span + span { @apply mt-1.5 text-[10px]; }
.answer-detail { @apply min-w-0 rounded-xl border border-gray-200 p-4 dark:border-dark-700; }
.answer-heading { @apply mb-4 flex items-center justify-between gap-2; }
.answer-heading h4 { @apply text-sm font-semibold; }
.answer-reference { @apply rounded-lg bg-emerald-50/60 p-3 dark:bg-emerald-950/20; }
.answer-reference span { @apply text-[11px] text-emerald-700 dark:text-emerald-400; }
.answer-reference p { @apply mt-1 whitespace-pre-wrap break-words text-sm; }
.response-heading { @apply mb-2 mt-4 flex justify-between text-xs text-gray-500; }
.response-content { @apply max-h-[40dvh] overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-4 font-sans text-sm leading-7 text-gray-800 dark:bg-dark-800 dark:text-gray-200; }
.judge-reason { @apply mt-4 border-t border-gray-100 pt-4 dark:border-dark-700; }
.judge-reason h5 { @apply text-xs font-semibold; }
.judge-reason p { @apply mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-gray-600 dark:text-gray-300; }
.judge-reason > span { @apply mt-3 block text-[10px] text-gray-400; }
.detail-loading { @apply space-y-4 py-8 text-xs text-gray-400; }
.detail-footer { @apply flex items-center justify-between gap-2 text-xs text-gray-400; }
.editor-footer { @apply flex items-center gap-2; }
.editor-error { @apply mb-4 rounded-lg border p-3 text-sm; }
.delete-rule { @apply text-sm text-red-600; }
.quality-editor label, .quality-editor legend { @apply text-sm; }
.quality-editor .input { @apply w-full; }
button:disabled { @apply cursor-not-allowed opacity-40; }
button:focus-visible, input:focus-visible, select:focus-visible, a:focus-visible { @apply outline-none ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
@keyframes quality-pulse { 50% { opacity: .45; } }
@media (min-width: 1280px) and (max-height: 900px) {
  .quality-workspace { gap: 12px; }
  .workspace-eyebrow, .workspace-subtitle { display: none; }
  .workspace-heading h2 { font-size: 20px; }
  .summary-cell { padding-top: 10px; padding-bottom: 10px; }
  .summary-cell strong { font-size: 20px; }
  .panel-heading { padding-top: 12px; padding-bottom: 12px; }
  .panel-heading p { display: none; }
  .rule-card { padding: 12px; }
  .rule-model, .rule-schedule, .rule-actions { margin-top: 8px; }
}
@media (max-width: 1279px) {
  .quality-workspace { height: auto; min-height: 0; }
  .workspace-columns { grid-template-columns: minmax(0, 1fr); }
  .rules-panel { height: clamp(24rem, 60dvh, 42rem); }
  .operations-panel { height: clamp(28rem, 70dvh, 52rem); }
}
@media (max-width: 640px) {
  .operations-table { display: block; min-width: 0; }
  .operations-table thead { @apply sr-only; }
  .operations-table tbody { display: block; }
  .operations-table tbody tr { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; padding: 16px; @apply border-b border-gray-100 dark:border-dark-800; }
  .operations-table td { min-width: 0; max-width: none; padding: 0; border: 0; }
  .operations-table td::before { content: attr(data-label); @apply mb-1.5 block text-[10px] text-gray-400; }
  .operations-table .account-cell { grid-column: 1 / -1; grid-row: 1; }
  .operations-table .account-cell strong { max-width: none; white-space: normal; overflow-wrap: anywhere; }
  .operations-table .action-cell { grid-column: 1 / -1; }
  .operations-table .detail-cell { grid-column: 1 / -1; text-align: right; }
  .operations-table .outcome-badge { white-space: normal; text-align: left; }
  .operations-table .cell-secondary { max-width: 100%; }
  .operations-footer { @apply flex-wrap gap-1 px-4 py-3; }
  .scope-label { max-width: 100%; }
  .editor-footer { flex-wrap: wrap; }
}
@media (max-width: 640px) { .summary-cell { padding: 14px 12px; gap: 8px; } .summary-cell strong { font-size: 22px; } .summary-icon { width: 32px; height: 32px; } .summary-link { display: none; } .heading-actions { width: 100%; justify-content: flex-end; } .detail-grid { grid-template-columns: 1fr; } .result-navigation { display: flex; gap: 8px; overflow-x: auto; } .result-navigation > p { display: none; } .result-navigation button { min-width: 100px; } .account-management-link { font-size: 10px; } .detail-footer .btn { font-size: 11px; padding: 8px; } }
@media (prefers-reduced-motion: reduce) { .cell-skeleton, .rule-skeleton span { animation: none; } }
</style>
