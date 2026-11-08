<template>
  <AppLayout>
    <div class="guard-v2">
      <SmartOpsNav />
      <header class="page-heading">
        <div>
          <p class="eyebrow">{{ t('accountOps.smartTitle') }}</p>
          <h2 class="flex items-center gap-2"><Icon name="credentialOps" size="lg" class="shrink-0" aria-hidden="true" />{{ t('tokenGuardV2.title') }}</h2>
          <p class="subtitle">{{ t('tokenGuardV2.description') }}</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <button class="btn btn-secondary inline-flex items-center gap-2" :disabled="loading || mutationBusy" @click="load()">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />{{ t('tokenGuardV2.refresh') }}
          </button>
          <button class="btn btn-primary" :disabled="!encryptionReady || mutationBusy" @click="openCreate">{{ t('tokenGuardV2.addAccount') }}</button>
        </div>
      </header>

      <p v-if="error && !editorOpen" role="alert" class="error-banner">{{ error }}</p>
      <p v-if="notice" role="status" class="success-banner">{{ notice }}</p>

      <CredentialEncryptionSetup class="mb-5" @ready="encryptionReady = $event">
        <div class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="credential-runtime-controls" :aria-busy="runtimeSaving">
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="field-label">{{ t('tokenGuardV2.reloginEngine') }}
              <select id="token-guard-v2-engine" :value="runtimeDraft.engine" class="input w-full" :disabled="mutationBusy || loading || !runtimeLoaded" @change="changeRuntime('engine', $event)">
                <option v-if="!runtimeDraft.engine" value="" disabled>{{ t('tokenGuardV2.legacyEngine') }}</option>
                <option value="local_worker">{{ t('tokenGuardV2.localWorkerEngine') }}</option>
                <option value="session_studio">{{ t('tokenGuardV2.sessionStudioEngine') }}</option>
              </select>
            </label>
            <label class="field-label">{{ t('tokenGuardV2.workerConcurrency') }}
              <select id="token-guard-v2-concurrency" :value="runtimeDraft.worker_concurrency" class="input w-full" :disabled="mutationBusy || loading || !runtimeLoaded" @change="changeRuntime('worker_concurrency', $event)">
                <option v-for="count in 16" :key="count" :value="count">{{ count }}</option>
              </select>
            </label>
          </div>
          <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t(runtimeDraft.engine === 'session_studio' ? 'tokenGuardV2.sessionStudioGlobalHint' : runtimeDraft.engine === 'local_worker' ? 'tokenGuardV2.localWorkerEngineHint' : 'tokenGuardV2.legacyEngineHint') }}</p>
          <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('tokenGuardV2.workerConcurrencyHint') }}</p>
          <p v-if="runtimeSaving" role="status" class="mt-2 text-xs">{{ t('tokenGuardV2.saving') }}</p>
        </div>
      </CredentialEncryptionSetup>

      <section v-if="status.worker" class="mb-5 rounded-xl border border-gray-200 p-4 dark:border-gray-700" data-testid="reauth-runtime-status" role="status">
        <strong>{{ t('tokenGuardV2.runtimeTitle') }}</strong>
        <p>{{ t(`tokenGuardV2.runtimeStates.${runtimeState}`) }}</p>
        <p v-if="runtimeReason">{{ t(`tokenGuardV2.runtimeReasons.${runtimeReason}`) }}</p>
        <small>{{ t(status.worker.mode === 'external' ? 'tokenGuardV2.runtimeExternal' : 'tokenGuardV2.runtimeManaged') }}</small>
      </section>

      <section class="summary-grid">
        <article class="summary-card"><span>{{ t('tokenGuardV2.monitored') }}</span><strong>{{ accounts.length }}</strong><small>{{ t('tokenGuardV2.intervalHint', { minutes: Math.round(status.probe_interval_seconds / 60) }) }}</small></article>
        <article class="summary-card"><span>{{ t('tokenGuardV2.healthy') }}</span><strong>{{ healthyCount }}</strong><small>{{ t('tokenGuardV2.enabledCount', { count: enabledCount }) }}</small></article>
        <article class="summary-card"><span>{{ t('tokenGuardV2.needsAttention') }}</span><strong>{{ issueCount }}</strong><small>{{ t('tokenGuardV2.failureThreshold', { count: status.fail_streak_threshold }) }}</small></article>
        <article class="summary-card"><span>{{ t('tokenGuardV2.activeTasks') }}</span><strong>{{ activeTaskCount }}</strong><small>{{ t('tokenGuardV2.workerHint') }}</small></article>
      </section>

      <nav class="section-tabs" :aria-label="t('tokenGuardV2.pageTabs')">
        <button type="button" role="tab" data-testid="guard-tab-accounts" :aria-selected="activeSection === 'accounts'" :class="{ active: activeSection === 'accounts' }" @click="activeSection = 'accounts'">{{ t('tokenGuardV2.accountsTab') }}</button>
        <button type="button" role="tab" data-testid="guard-tab-rules" :aria-selected="activeSection === 'rules'" :class="{ active: activeSection === 'rules' }" @click="activeSection = 'rules'">{{ t('tokenGuardV2.rulesTab') }}</button>
      </nav>

      <section v-if="activeSection === 'accounts'" class="table-card">
        <div class="list-toolbar">
          <div class="filter-tabs" role="tablist" :aria-label="t('tokenGuardV2.statusFilter')">
            <button v-for="filter in accountFilters" :key="filter.value" type="button" role="tab" :aria-selected="accountFilter === filter.value" :data-filter="filter.value" :class="{ active: accountFilter === filter.value }" @click="accountFilter = filter.value">
              {{ t(filter.label) }} <span>{{ filter.count }}</span>
            </button>
          </div>
          <label class="search-box">
            <Icon name="search" size="sm" />
            <input id="token-guard-v2-search" v-model.trim="searchQuery" type="search" :placeholder="t('tokenGuardV2.searchPlaceholder')" />
          </label>
        </div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{{ t('tokenGuardV2.account') }}</th>
                <th>{{ t('tokenGuardV2.loginMode') }}</th>
                <th>{{ t('tokenGuardV2.configuration') }}</th>
                <th>{{ t('tokenGuardV2.automation') }}</th>
                <th>{{ t('tokenGuardV2.inspection') }}</th>
                <th>{{ t('tokenGuardV2.failureStreak') }}</th>
                <th>{{ t('tokenGuardV2.lastInspection') }}</th>
                <th>{{ t('tokenGuardV2.lastRelogin') }}</th>
                <th>{{ t('tokenGuardV2.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in pagedAccounts" :key="item.account_id">
                <td><strong>{{ item.account_name || `#${item.account_id}` }}</strong><small>#{{ item.account_id }} · {{ item.account_status }}</small></td>
                <td>{{ modeLabel(item.login_config?.credential_mode) }}<small>{{ item.login_config?.login_email || '-' }}</small></td>
                <td><span class="badge" :class="item.login_config?.configured ? 'ok' : 'danger'">{{ item.login_config?.configured ? t('tokenGuardV2.configured') : t('tokenGuardV2.incomplete') }}</span><small>{{ configurationDetail(item) }}</small></td>
                <td>
                  <div class="flex flex-col gap-2">
                    <label class="inline-flex items-center gap-2" :title="t('tokenGuardV2.autoInspectHint')"><input type="checkbox" role="switch" class="automation-switch" :checked="item.enabled" :aria-label="t('tokenGuardV2.autoInspect')" :data-testid="'auto-inspect-' + item.account_id" :disabled="mutationBusy || loading" @change="toggleSwitch(item, 'enabled', $event)" />{{ t('tokenGuardV2.autoInspect') }}</label>
                    <label class="inline-flex items-center gap-2" :title="t('tokenGuardV2.autoReloginHint')"><input type="checkbox" role="switch" class="automation-switch" :checked="item.auto_relogin_enabled" :aria-label="t('tokenGuardV2.autoRelogin')" :data-testid="'auto-relogin-' + item.account_id" :disabled="mutationBusy || loading" @change="toggleSwitch(item, 'auto_relogin_enabled', $event)" />{{ t('tokenGuardV2.autoRelogin') }}</label>
                    <small>{{ t('tokenGuardV2.reloginBudgetCount', { count: item.relogin_fail_streak || 0, limit: status.relogin_max_failures || 3 }) }}</small>
                    <small :data-testid="'automation-state-' + item.account_id" :class="{ 'text-red-700 dark:text-red-300': item.auto_relogin_exhausted }">{{ automationLabel(item) }}</small>
                  </div>
                </td>
                <td><span class="badge" :class="probeClass(item.probe_state)">{{ t(`tokenGuardV2.probeStates.${item.probe_state || 'pending'}`) }}</span><details v-if="item.refresh_failure"><summary>{{ t('tokenGuardV2.operationFailure.refreshAttempt') }}</summary><CredentialOperationFailureView :failure="item.refresh_failure" /></details></td>
                <td class="tabular-nums">{{ item.fail_streak }}</td>
                <td>{{ date(item.last_probe_at) }}<details v-if="probeOperationFailure(item)" :data-testid="'probe-history-' + item.account_id"><summary>{{ t('tokenGuardV2.lastInspectionFailure') }}</summary><CredentialOperationFailureView :failure="probeOperationFailure(item)!" /></details><small v-if="item.enabled">{{ t('tokenGuardV2.next') }} {{ date(item.next_probe_at) }}</small><small v-else>{{ t('tokenGuardV2.paused') }}</small></td>
                <td>{{ date(item.latest_task?.finished_at || item.latest_task?.created_at || item.last_reauth_at) }}<small v-if="item.latest_task">{{ taskLabel(item.latest_task) }}</small><CredentialOperationFailureView v-if="taskOperationFailure(item.latest_task)" :failure="taskOperationFailure(item.latest_task)!" /></td>
                <td>
                  <div class="actions">
                    <button class="link-btn" :disabled="mutationBusy" @click="openEdit(item)">{{ t('tokenGuardV2.edit') }}</button>
                    <button class="link-btn" :disabled="mutationBusy" @click="probe(item)">{{ t('tokenGuardV2.inspectNow') }}</button>
                    <button class="link-btn" :disabled="mutationBusy" @click="relogin(item)">{{ t('tokenGuardV2.reloginNow') }}</button>
                    <button v-if="item.auto_relogin_exhausted" class="link-btn" :data-testid="'resume-budget-' + item.account_id" :disabled="mutationBusy || loading" @click="resumeBudget(item)">{{ t('tokenGuardV2.resumeReloginBudget') }}</button>
                    <OpenAITOTPDialog :account-id="item.account_id" :account-name="item.account_name" :configured="!!(item.login_config?.password_configured && item.login_config?.totp_configured)" />
                    <button class="link-btn danger-text" :disabled="mutationBusy" @click="remove(item)">{{ t('tokenGuardV2.remove') }}</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!loading && !filteredAccounts.length" class="empty-state">
            <Icon name="shield" size="xl" />
            <h3>{{ t(accounts.length ? 'tokenGuardV2.noResultsTitle' : 'tokenGuardV2.emptyTitle') }}</h3>
            <p>{{ t(accounts.length ? 'tokenGuardV2.noResultsHint' : 'tokenGuardV2.emptyHint') }}</p>
          </div>
        </div>
        <Pagination
          v-if="filteredAccounts.length"
          :total="filteredAccounts.length"
          :page="accountPage"
          :page-size="accountPageSize"
          :page-size-options="[10, 20, 50]"
          @update:page="accountPage = $event"
          @update:pageSize="handleAccountPageSize"
        />
      </section>

      <section v-else class="rules-card">
        <div class="rules-heading">
          <div><h3>{{ t('tokenGuardV2.rulesTitle') }}</h3><p>{{ t('tokenGuardV2.rulesDescription') }}</p></div>
          <span class="badge" :class="{ ok: !rulesDirty }">{{ t(rulesDirty ? 'tokenGuardV2.rulesUnsaved' : 'tokenGuardV2.rulesConfigurable') }}</span>
        </div>
        <form id="token-guard-v2-rules" class="rules-form" @submit.prevent="saveRulesConfig">
          <div class="rule-metrics">
            <label><span>{{ t('tokenGuardV2.ruleProbeInterval') }}</span><input id="token-guard-v2-rule-probe" v-model.number="rulesDraft.probe_interval_minutes" class="input" type="number" min="1" max="1440" required :disabled="mutationBusy" @input="rulesDirty = true" /><small>{{ t('tokenGuardV2.minutes') }}</small></label>
            <label><span>{{ t('tokenGuardV2.ruleRetryInterval') }}</span><input id="token-guard-v2-rule-retry" v-model.number="rulesDraft.retry_interval_minutes" class="input" type="number" min="1" max="1440" required :disabled="mutationBusy" @input="rulesDirty = true" /><small>{{ t('tokenGuardV2.minutes') }}</small></label>
            <label><span>{{ t('tokenGuardV2.ruleFailureThreshold') }}</span><input id="token-guard-v2-rule-threshold" v-model.number="rulesDraft.fail_streak_threshold" class="input" type="number" min="1" max="10" required :disabled="mutationBusy" @input="rulesDirty = true" /><small>{{ t('tokenGuardV2.times') }}</small></label>
            <label><span>{{ t('tokenGuardV2.ruleCooldown') }}</span><input id="token-guard-v2-rule-cooldown" v-model.number="rulesDraft.relogin_cooldown_minutes" class="input" type="number" min="1" max="10080" required :disabled="mutationBusy" @input="rulesDirty = true" /><small>{{ t('tokenGuardV2.minutes') }}</small></label>
            <label><span>{{ t('tokenGuardV2.ruleReloginBackoffMax') }}</span><input id="token-guard-v2-rule-backoff-max" v-model.number="rulesDraft.relogin_backoff_max_minutes" class="input" type="number" :min="rulesDraft.relogin_cooldown_minutes" max="10080" required :disabled="mutationBusy" @input="rulesDirty = true" /><small>{{ t('tokenGuardV2.minutes') }}</small></label>
            <label><span>{{ t('tokenGuardV2.ruleReloginMaxFailures') }}</span><input id="token-guard-v2-rule-max-failures" v-model.number="rulesDraft.relogin_max_failures" class="input" type="number" min="1" max="20" required :disabled="mutationBusy" @input="rulesDirty = true" /><small>{{ t('tokenGuardV2.reloginBudgetHint') }}</small></label>
          </div>
          <div class="rules-actions"><button type="submit" class="btn btn-primary" :disabled="mutationBusy || !rulesDirty">{{ rulesSaving ? t('tokenGuardV2.saving') : t('tokenGuardV2.saveRules') }}</button></div>
        </form>
        <div class="rule-list">
          <article><span>1</span><div><strong>{{ t('tokenGuardV2.ruleScopeTitle') }}</strong><p>{{ t('tokenGuardV2.ruleScopeHint') }}</p></div></article>
          <article><span>2</span><div><strong>{{ t('tokenGuardV2.ruleHealthyTitle') }}</strong><p>{{ t('tokenGuardV2.ruleHealthyHint') }}</p></div></article>
          <article><span>3</span><div><strong>{{ t('tokenGuardV2.ruleAuthTitle') }}</strong><p>{{ t('tokenGuardV2.ruleAuthHint') }}</p></div></article>
          <article><span>4</span><div><strong>{{ t('tokenGuardV2.ruleTransientTitle') }}</strong><p>{{ t('tokenGuardV2.ruleTransientHint') }}</p></div></article>
          <article><span>5</span><div><strong>{{ t('tokenGuardV2.ruleReloginTitle') }}</strong><p>{{ t('tokenGuardV2.ruleReloginHint', { count: status.fail_streak_threshold }) }}</p></div></article>
        </div>
      </section>

      <BaseDialog :show="editorOpen" :title="editing ? t('tokenGuardV2.editTitle') : t('tokenGuardV2.addTitle')" width="wide" @close="closeEditor">
        <form id="token-guard-v2-editor" class="editor-form" @submit.prevent="save">
          <p v-if="error" role="alert" class="error-banner">{{ error }}</p>
          <p v-if="!encryptionReady" role="alert" class="error-banner">{{ t('tokenGuardV2.encryption.editorHint') }}</p>
          <div v-if="!editing" class="account-picker">
            <label class="search-box picker-search">
              <Icon name="search" size="sm" />
              <input id="token-guard-v2-account-search" v-model.trim="accountSearch" type="search" :placeholder="t('tokenGuardV2.accountSearchPlaceholder')" />
            </label>
            <div class="group-tabs" role="tablist" :aria-label="t('tokenGuardV2.accountGroup')">
              <button type="button" role="tab" data-group-id="all" :aria-selected="!accountGroup" :class="{ active: !accountGroup }" @click="accountGroup = ''">{{ t('tokenGuardV2.allGroups') }}</button>
              <button type="button" role="tab" data-group-id="ungrouped" :aria-selected="accountGroup === 'ungrouped'" :class="{ active: accountGroup === 'ungrouped' }" @click="accountGroup = 'ungrouped'">{{ t('tokenGuardV2.ungrouped') }}</button>
              <button v-for="group in groups" :key="group.id" type="button" role="tab" :data-group-id="group.id" :aria-selected="accountGroup === String(group.id)" :class="{ active: accountGroup === String(group.id) }" @click="accountGroup = String(group.id)">{{ group.name }}</button>
            </div>
            <div class="account-picker-list" role="listbox" :aria-label="t('tokenGuardV2.selectAccount')">
              <button v-for="account in selectableAccounts" :key="account.id" type="button" role="option" :data-account-id="account.id" :aria-selected="draft.account_id === account.id" :class="{ selected: draft.account_id === account.id }" @click="selectAccount(account)">
                <span><strong>{{ account.name }}</strong><small>#{{ account.id }} · {{ accountEmail(account) || '-' }}</small></span>
                <span v-if="accountPlanLabel(account)" class="plan-badge">{{ accountPlanLabel(account) }}</span>
              </button>
              <p v-if="!selectableAccounts.length" class="picker-empty">{{ t('tokenGuardV2.noSelectableAccounts') }}</p>
            </div>
          </div>
          <div v-else class="selected-account"><span>{{ t('tokenGuardV2.account') }}</span><strong>{{ editing.account_name }} (#{{ editing.account_id }})</strong></div>
          <label class="field-label">{{ t('tokenGuardV2.loginEmail') }}<input v-model.trim="draft.login_email" class="input w-full" type="email" required /></label>
          <label class="field-label">{{ t('tokenGuardV2.loginProxy') }}
            <select id="token-guard-v2-proxy" v-model="proxyChoice" :disabled="editorEngine === 'session_studio'" class="input w-full">
              <option value="account">{{ t('tokenGuardV2.accountProxyDefault') }}</option>
              <option value="mihomo">{{ t('tokenGuardV2.mihomoManagedPool') }}</option>
              <option v-for="proxy in proxies" :key="proxy.id" :value="`proxy:${proxy.id}`">{{ proxyOptionLabel(proxy) }}</option>
            </select>
            <small class="field-hint">{{ t('tokenGuardV2.loginProxyHint') }}</small>
          </label>
          <fieldset class="mode-grid">
            <legend>{{ t('tokenGuardV2.loginMode') }}</legend>
            <label class="mode-option"><input v-model="draft.credential_mode" type="radio" value="password_totp" /><span><strong>{{ t('tokenGuardV2.passwordMode') }}</strong><small>{{ t('tokenGuardV2.passwordModeHint') }}</small></span></label>
            <label class="mode-option"><input v-model="draft.credential_mode" type="radio" value="email_otp_url" /><span><strong>{{ t('tokenGuardV2.mailboxMode') }}</strong><small>{{ t('tokenGuardV2.mailboxModeHint') }}</small></span></label>
          </fieldset>
          <template v-if="draft.credential_mode === 'password_totp'">
            <label class="field-label">{{ t('tokenGuardV2.password') }}<input v-model="draft.password" class="input w-full" type="password" :placeholder="editing?.login_config?.password_configured ? t('tokenGuardV2.keepSecret') : ''" :required="!editing?.login_config?.password_configured" autocomplete="new-password" /></label>
            <label class="field-label">{{ t('tokenGuardV2.totpSecret') }}<input v-model.trim="draft.totp_secret" class="input w-full" type="password" :placeholder="editing?.login_config?.totp_configured ? t('tokenGuardV2.keepSecret') : t('tokenGuardV2.totpOptionalHint')" autocomplete="off" /></label>
            <label v-if="editing?.login_config?.totp_configured" class="check-row"><input v-model="draft.clear_totp" type="checkbox" />{{ t('tokenGuardV2.clearTotp') }}</label>
          </template>
          <label v-else class="field-label">{{ t('tokenGuardV2.otpUrl') }}<input v-model.trim="draft.otp_url" class="input w-full" type="url" :placeholder="editing?.login_config?.otp_url_masked ? `${editing.login_config.otp_url_masked} · ${t('tokenGuardV2.keepSecret')}` : 'https://mail.example.com/latest'" :required="editing?.login_config?.credential_mode !== 'email_otp_url' || !editing?.login_config?.otp_url_masked" /></label>


        </form>
        <template #footer>
          <button type="button" class="btn btn-secondary" :disabled="mutationBusy" @click="closeEditor">{{ t('tokenGuardV2.cancel') }}</button>
          <button type="submit" form="token-guard-v2-editor" class="btn btn-primary" :disabled="mutationBusy || !draft.account_id || !encryptionReady">{{ saving ? t('tokenGuardV2.saving') : t('tokenGuardV2.save') }}</button>
        </template>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import CredentialOperationFailureView from '@/components/account/CredentialOperationFailure.vue'
import { probeOperationFailure, taskOperationFailure, safeDiagnosticToken } from '@/utils/credentialOperationFailure'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import CredentialEncryptionSetup from '@/components/account/CredentialEncryptionSetup.vue'
import OpenAITOTPDialog from '@/components/account/OpenAITOTPDialog.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import Icon from '@/components/icons/Icon.vue'
import { BaseDialog, Pagination } from '@/components/common'
import { list as listAccounts } from '@/api/admin/accounts'
import { getByPlatform as listGroups } from '@/api/admin/groups'
import { getAllWithCount as listProxies } from '@/api/admin/proxies'
import { openAIPlanTypeLabel } from '@/utils/planType'
import type { AccountListItem, AdminGroup, Proxy } from '@/types'
import {
  createTokenGuardV2Account,
  deleteTokenGuardV2Account,
  listTokenGuardV2Accounts,
  probeTokenGuardV2Account,
  reloginTokenGuardV2Account,
  resumeTokenGuardV2Budget,
  saveTokenGuardV2Rules,
  saveTokenGuardV2Runtime,
  updateTokenGuardV2Switches,
  type TokenGuardV2RuntimeSettings,
  updateTokenGuardV2Account,
  type SaveTokenGuardV2Account,
  type TokenGuardV2Account,
  type TokenGuardV2CredentialMode,
  type TokenGuardV2Engine,
  type TokenGuardV2ProxySource,
  type TokenGuardV2Rules,
  type TokenGuardV2Status,
  type TokenGuardV2Task
} from '@/api/admin/accountTokenGuardV2'

const { t } = useI18n()
const encryptionReady = ref(false)
const emptyStatus = (): TokenGuardV2Status => ({
  accounts: [],
  probe_interval_seconds: 1800,
  retry_interval_seconds: 300,
  relogin_cooldown_seconds: 1800,
  fail_streak_threshold: 2
})
const status = reactive<TokenGuardV2Status>(emptyStatus())
const candidates = ref<AccountListItem[]>([])
const groups = ref<AdminGroup[]>([])
const accountGroup = ref('')
const accountSearch = ref('')
const proxies = ref<Proxy[]>([])
const loading = ref(false), saving = ref(false), busyId = ref(0), editorOpen = ref(false)
const editing = ref<TokenGuardV2Account | null>(null)
const activeSection = ref<'accounts' | 'rules'>('accounts')
const accountFilter = ref<'all' | 'healthy' | 'attention' | 'paused'>('all')
const searchQuery = ref('')
const accountPage = ref(1)
const accountPageSize = ref(20)
const runtimeDraft = reactive<TokenGuardV2RuntimeSettings>({ engine: '', worker_concurrency: 3 })
const runtimeSaving = ref(false)
const runtimeLoaded = ref(false)
const rulesSaving = ref(false)
const rulesDirty = ref(false)
const mutationBusy = computed(() => saving.value || busyId.value !== 0 || runtimeSaving.value || rulesSaving.value)
const error = ref(''), notice = ref('')
let timer: ReturnType<typeof setInterval> | undefined
let loadGeneration = 0
const currentTime = ref(Date.now())

const blankDraft = (): SaveTokenGuardV2Account => ({
  account_id: 0,
  login_email: '',
  credential_mode: 'password_totp',
  proxy_source: 'account',
  proxy_id: null,
  password: '',
  totp_secret: '',
  otp_url: '',
  clear_totp: false
})
const draft = reactive<SaveTokenGuardV2Account>(blankDraft())
const rulesDraft = reactive({
  relogin_backoff_max_minutes: 60,
  relogin_max_failures: 3,
  probe_interval_minutes: 30,
  retry_interval_minutes: 5,
  relogin_cooldown_minutes: 30,
  fail_streak_threshold: 2
})

const accountEmail = (account?: AccountListItem) => {
  const email = account?.extra?.email_address || account?.extra?.email || account?.credentials?.email
  return typeof email === 'string' ? email.trim() : ''
}
const accountPlanLabel = (account?: AccountListItem) => {
  const rawPlan = account?.credentials?.plan_type
  return typeof rawPlan === 'string' ? openAIPlanTypeLabel(rawPlan) || rawPlan.trim() : ''
}
const accountGroupNames = (account: AccountListItem) => groups.value
  .filter(group => account.group_ids?.includes(group.id))
  .map(group => group.name)
  .join(' ')
const runtimeReason = computed(() => {
  const reason = status.worker?.reason || ''
  return ['unsupported_platform', 'release_required', 'runtime_install_failed', 'worker_start_failed', 'worker_exited', 'external_not_configured', 'external_offline', 'api_unreachable'].includes(reason) ? reason : ''
})
const runtimeState = computed(() => {
  const state = status.worker?.state || 'idle'
  return ['idle', 'preparing', 'running', 'unavailable', 'stopped'].includes(state) ? state : 'unavailable'
})
const accounts = computed(() => status.accounts)
const enabledCount = computed(() => accounts.value.filter(item => item.enabled).length)
const healthyCount = computed(() => accounts.value.filter(item => item.probe_state === 'ok').length)
const issueCount = computed(() => accounts.value.filter(item => item.probe_state === 'auth' || item.blocked_reason).length)
const activeTaskCount = computed(() => accounts.value.filter(item => ['queued', 'running', 'callback_processing'].includes(item.latest_task?.status || '')).length)
const isHealthy = (item: TokenGuardV2Account) => item.enabled && item.probe_state === 'ok' && item.login_config?.configured && !item.blocked_reason
const needsAttention = (item: TokenGuardV2Account) => Boolean(item.blocked_reason || !item.login_config?.configured || ['auth', 'transient'].includes(item.probe_state))
const accountFilters = computed(() => [
  { value: 'all' as const, label: 'tokenGuardV2.filterAll', count: accounts.value.length },
  { value: 'healthy' as const, label: 'tokenGuardV2.filterHealthy', count: accounts.value.filter(isHealthy).length },
  { value: 'attention' as const, label: 'tokenGuardV2.filterAttention', count: accounts.value.filter(needsAttention).length },
  { value: 'paused' as const, label: 'tokenGuardV2.filterPaused', count: accounts.value.filter(item => !item.enabled).length }
])
const filteredAccounts = computed(() => {
  const query = searchQuery.value.toLowerCase()
  return accounts.value.filter(item => {
    if (accountFilter.value === 'healthy' && !isHealthy(item)) return false
    if (accountFilter.value === 'attention' && !needsAttention(item)) return false
    if (accountFilter.value === 'paused' && item.enabled) return false
    if (!query) return true
    const candidate = candidates.value.find(account => account.id === item.account_id)
    return [
      item.account_name,
      item.account_id,
      item.account_status,
      item.login_config?.login_email,
      item.probe_detail,
      item.blocked_reason,
      accountPlanLabel(candidate)
    ].some(value => String(value || '').toLowerCase().includes(query))
  })
})
const pagedAccounts = computed(() => {
  const start = (accountPage.value - 1) * accountPageSize.value
  return filteredAccounts.value.slice(start, start + accountPageSize.value)
})
const matchesAccountGroup = (account: AccountListItem) => {
  if (!accountGroup.value) return true
  if (accountGroup.value === 'ungrouped') return !account.group_ids?.length
  return account.group_ids?.includes(Number(accountGroup.value)) ?? false
}
const selectableAccounts = computed(() => {
  if (editing.value) return candidates.value
  const monitored = new Set(accounts.value.map(item => item.account_id))
  const query = accountSearch.value.toLowerCase()
  return candidates.value
    .filter(item => !monitored.has(item.id))
    .filter(matchesAccountGroup)
    .filter(item => !query || [item.name, item.id, accountEmail(item), accountPlanLabel(item), accountGroupNames(item)]
      .some(value => String(value || '').toLowerCase().includes(query)))
})

const manualErrors: Record<string, string> = {
  TOKEN_GUARD_V2_PROBE_BUSY: 'busy',
  OPENAI_REAUTH_TASK_ACTIVE: 'taskBusy', OPENAI_REAUTH_TASK_ALREADY_ACTIVE: 'taskBusy',
  TOTP_ACCOUNT_BUSY: 'totpBusy', TOKEN_GUARD_V2_BUDGET_BUSY: 'budgetBusy',
  OPENAI_REAUTH_WORKER_UNAVAILABLE: 'worker', OPENAI_REAUTH_UNAVAILABLE: 'worker',
  OPENAI_REAUTH_CONFIG_REQUIRED: 'configuration', OPENAI_REAUTH_ENCRYPTION_KEY_REQUIRED: 'encryption',
  OPENAI_REAUTH_ACCOUNT_CHANGED: 'changed', OPENAI_REAUTH_CONFIG_CHANGED: 'changed',
  OPENAI_REAUTH_IDENTITY_MISMATCH: 'changed',
  OPENAI_REAUTH_TASK_NOT_ACTIVE: 'changed'
}
const message = (value: unknown) => {
  const reason = (value as { reason?: unknown })?.reason
  return typeof reason === 'string' && Object.prototype.hasOwnProperty.call(manualErrors, reason)
    ? `${t(`tokenGuardV2.operationFailure.manual.${manualErrors[reason]}`)} (${reason})`
    : t('tokenGuardV2.error')
}
const date = (value?: string) => value ? new Date(value).toLocaleString() : '-'
const modeLabel = (mode?: TokenGuardV2CredentialMode) => !mode ? '-' : mode === 'password_totp' ? t('tokenGuardV2.passwordMode') : t('tokenGuardV2.mailboxMode')
const engineLabel = (engine?: TokenGuardV2Engine) => engine === 'session_studio' ? t('tokenGuardV2.sessionStudioEngine') : t('tokenGuardV2.localWorkerEngine')
const effectiveEngine = (config?: TokenGuardV2Account['login_config']) => config?.credential_mode === 'email_otp_url' ? 'local_worker' : runtimeDraft.engine || config?.engine || 'local_worker'
const editorEngine = computed(() => draft.credential_mode === 'email_otp_url' ? 'local_worker' : runtimeDraft.engine || editing.value?.login_config?.engine || 'local_worker')
const proxyChoice = computed({
  get: () => draft.proxy_source === 'managed_proxy' && draft.proxy_id ? `proxy:${draft.proxy_id}` : draft.proxy_source,
  set: (value: string) => {
    if (value.startsWith('proxy:')) {
      draft.proxy_source = 'managed_proxy'
      draft.proxy_id = Number(value.slice(6))
      return
    }
    draft.proxy_source = value as TokenGuardV2ProxySource
    draft.proxy_id = null
  }
})
const proxyOptionLabel = (proxy: Proxy) => `${proxy.protocol.toUpperCase()} · ${proxy.name} · ${proxy.host}:${proxy.port}`
const proxyLabel = (source: TokenGuardV2ProxySource = 'account', proxyId?: number) => {
  if (source === 'mihomo') return t('tokenGuardV2.mihomoManagedPool')
  if (source !== 'managed_proxy' || !proxyId) return t('tokenGuardV2.accountProxyDefault')
  const proxy = proxies.value.find(item => item.id === proxyId)
  return proxy ? proxyOptionLabel(proxy) : `#${proxyId}`
}
const probeClass = (state: string) => state === 'ok' ? 'ok' : state === 'auth' ? 'danger' : ''
const activeTaskStates = ['queued', 'running', 'callback_processing']
function automationState(item: TokenGuardV2Account) {
  const task = item.latest_task
  if (task?.status === 'queued') return 'queued'
  if (task && activeTaskStates.includes(task.status)) return 'running'
  if (!item.enabled) return 'paused'
  const finishedAt = Date.parse(task?.finished_at || '')
  const probeAt = Date.parse(item.last_probe_at || '')
  if (task?.status === 'succeeded' && Number.isFinite(finishedAt) && (!Number.isFinite(probeAt) || probeAt < finishedAt)) return 'verifying'
  if (!item.auto_relogin_enabled) return 'disabled'
  if (item.auto_relogin_exhausted) return 'exhausted'
  if (!item.login_config?.configured) return 'configuration'
  const cooldownUntil = Date.parse(item.cooldown_until || '')
  if (cooldownUntil > currentTime.value) return 'cooldown'
  const expiredCooldown = item.blocked_reason === 'automatic re-login cooling down'
    && Number.isFinite(cooldownUntil) && cooldownUntil <= currentTime.value
  if (item.blocked_reason && !expiredCooldown) return 'blocked'
  if (item.probe_state === 'auth') return item.fail_streak < status.fail_streak_threshold ? 'waiting' : 'scheduled'
  if (item.probe_state === 'ok') return 'healthy'
  if (item.probe_state === 'transient') return 'retryInspection'
  return 'inspectionPending'
}
function automationLabel(item: TokenGuardV2Account) {
  const state = automationState(item)
  if (state === 'exhausted') return t('tokenGuardV2.reloginBudgetExhausted')
  if (state === 'waiting') return t('tokenGuardV2.automationStates.waiting', { count: item.fail_streak, limit: status.fail_streak_threshold })
  if (state === 'cooldown') return t('tokenGuardV2.automationStates.cooldown', { until: date(item.cooldown_until) })
  return t(`tokenGuardV2.automationStates.${state}`)
}
function invalidateLoad() {
  // A response fetched before a mutation must never replace its newer result.
  loadGeneration++
  loading.value = false
}
const safeTaskToken = (value: string) => ['queued', 'running', 'callback_processing', 'failed', 'succeeded', 'starting', 'protocol_connecting', 'email_submitted', 'waiting_otp', 'otp_submitted', 'password_submitted', 'mfa_submitted', 'waiting_callback', 'exchanging_token', 'applying_credentials'].includes(value) ? value : 'unknown'
const taskLabel = (task: TokenGuardV2Task) => task.diagnostic
  ? `${safeTaskToken(task.status)}: ${safeDiagnosticToken(task.diagnostic.stage)} · ${safeDiagnosticToken(task.diagnostic.reason)} · ${Number.isFinite(task.diagnostic.elapsed_ms) ? task.diagnostic.elapsed_ms : 0} ms${task.diagnostic.http_status && task.diagnostic.http_status >= 100 && task.diagnostic.http_status <= 599 ? ` · HTTP ${task.diagnostic.http_status}` : ''}`
  : `${safeTaskToken(task.status)} · ${safeTaskToken(task.stage)}`
const configurationDetail = (item: TokenGuardV2Account) => {
  const config = item.login_config
  if (!config) return '-'
  const credentials = config.credential_mode === 'email_otp_url'
    ? config.otp_url_masked || '-'
    : [config.password_configured ? t('tokenGuardV2.passwordSaved') : '', config.totp_configured ? t('tokenGuardV2.totpSaved') : ''].filter(Boolean).join(' · ') || '-'
  if (effectiveEngine(config) === 'session_studio') return `${credentials} · ${engineLabel(effectiveEngine(config))} · ${t('tokenGuardV2.remoteEngineEgress')}`
  return `${credentials} · ${engineLabel(effectiveEngine(config))} · ${t('tokenGuardV2.loginProxy')}: ${proxyLabel(config.proxy_source, config.proxy_id)}`
}

function resetDraft() { Object.assign(draft, blankDraft()) }
function syncRulesDraft(rules: TokenGuardV2Rules) {
  Object.assign(rulesDraft, {
    relogin_backoff_max_minutes: Math.round((rules.relogin_backoff_max_seconds ?? Math.max(3600, rules.relogin_cooldown_seconds)) / 60),
    relogin_max_failures: rules.relogin_max_failures ?? 3,
    probe_interval_minutes: Math.round(rules.probe_interval_seconds / 60),
    retry_interval_minutes: Math.round(rules.retry_interval_seconds / 60),
    relogin_cooldown_minutes: Math.round(rules.relogin_cooldown_seconds / 60),
    fail_streak_threshold: rules.fail_streak_threshold
  })
}
function handleAccountPageSize(size: number) { accountPageSize.value = size; accountPage.value = 1 }
function openCreate() { if (mutationBusy.value) return; error.value = ''; notice.value = ''; editing.value = null; accountGroup.value = ''; accountSearch.value = ''; resetDraft(); editorOpen.value = true }
function selectAccount(account: AccountListItem) { draft.account_id = account.id; draft.login_email = accountEmail(account) }
function openEdit(item: TokenGuardV2Account) {
  if (mutationBusy.value) return
  error.value = ''; notice.value = ''
  editing.value = item
  Object.assign(draft, blankDraft(), {
    account_id: item.account_id,
    login_email: item.login_config?.login_email || '',
    credential_mode: item.login_config?.credential_mode || 'password_totp',
    proxy_source: item.login_config?.proxy_source || (item.login_config?.proxy_id ? 'managed_proxy' : 'account'),
    proxy_id: item.login_config?.proxy_id ?? null
  })
  editorOpen.value = true
}
function closeEditor() { if (!mutationBusy.value) editorOpen.value = false }

async function load(silent = false, afterAction = false) {
  if ((!afterAction && (loading.value || mutationBusy.value)) || runtimeSaving.value) return
  const generation = ++loadGeneration
  loading.value = true
  if (!silent) error.value = ''
  try {
    const [guard, accountPage, managedProxies, openAIGroups] = await Promise.all([
      listTokenGuardV2Accounts(),
      listAccounts(1, 200, { platform: 'openai', type: 'oauth', lite: '1' }),
      listProxies(),
      listGroups('openai')
    ])
    if (generation !== loadGeneration) return
    currentTime.value = Date.now()
    Object.assign(status, guard)
    if (guard.runtime_settings && !runtimeSaving.value) {
      Object.assign(runtimeDraft, guard.runtime_settings)
      runtimeLoaded.value = true
    }
    if (!rulesDirty.value) syncRulesDraft(guard)
    candidates.value = accountPage.items.filter(item => !item.parent_account_id)
    proxies.value = managedProxies
    groups.value = openAIGroups
  } catch (value) {
    if (generation === loadGeneration && !silent) error.value = message(value)
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

async function saveRulesConfig() {
  if (mutationBusy.value || !rulesDirty.value) return
  invalidateLoad()
  rulesSaving.value = true; error.value = ''; notice.value = ''
  try {
    const saved = await saveTokenGuardV2Rules({
      relogin_backoff_max_seconds: Math.max(rulesDraft.relogin_backoff_max_minutes, rulesDraft.relogin_cooldown_minutes) * 60,
      relogin_max_failures: rulesDraft.relogin_max_failures,
      probe_interval_seconds: rulesDraft.probe_interval_minutes * 60,
      retry_interval_seconds: rulesDraft.retry_interval_minutes * 60,
      relogin_cooldown_seconds: rulesDraft.relogin_cooldown_minutes * 60,
      fail_streak_threshold: rulesDraft.fail_streak_threshold
    })
    Object.assign(status, saved)
    syncRulesDraft(saved)
    rulesDirty.value = false
    notice.value = t('tokenGuardV2.rulesSaved')
  } catch (value) {
    error.value = message(value)
  } finally {
    rulesSaving.value = false
  }
}

async function save() {
  if (mutationBusy.value || !draft.account_id || !encryptionReady.value) return
  invalidateLoad()
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const payload = { ...draft }
    if (editing.value) await updateTokenGuardV2Account(draft.account_id!, payload)
    else await createTokenGuardV2Account(payload)
    notice.value = t('tokenGuardV2.saved')
    editorOpen.value = false
    await load(true, true)
  } catch (value) {
    error.value = message(value)
  } finally {
    saving.value = false
  }
}

async function act(item: TokenGuardV2Account, action: () => Promise<unknown>, successKey: string) {
  if (mutationBusy.value) return
  invalidateLoad()
  busyId.value = item.account_id; error.value = ''; notice.value = ''
  try {
    const result = await action()
    const failure = successKey === 'tokenGuardV2.inspectionQueued'
      ? probeOperationFailure(result as TokenGuardV2Account)
      : successKey === 'tokenGuardV2.reloginQueued' ? taskOperationFailure(result as TokenGuardV2Task) : undefined
    const recoveryInProgress = successKey === 'tokenGuardV2.inspectionQueued' && failure?.category === 'requires_relogin'
      && ['waiting', 'scheduled', 'queued', 'running', 'cooldown', 'verifying'].includes(automationState(result as TokenGuardV2Account))
    if (failure && !recoveryInProgress) error.value = `${t(`tokenGuardV2.operationFailure.reasons.${failure.category}`)} ${t(`tokenGuardV2.operationFailure.actions.${failure.action}`)}`
    else notice.value = t(successKey, { account: item.account_name })
  } catch (value) {
    error.value = message(value)
  } finally {
    // Keep duplicate actions blocked until the post-action read is applied.
    await load(true, true)
    busyId.value = 0
  }
}

const probe = (item: TokenGuardV2Account) => act(item, () => probeTokenGuardV2Account(item.account_id), 'tokenGuardV2.inspectionQueued')
const relogin = (item: TokenGuardV2Account) => act(item, () => reloginTokenGuardV2Account(item.account_id), 'tokenGuardV2.reloginQueued')
async function resumeBudget(item: TokenGuardV2Account) {
  if (mutationBusy.value) return
  if (!window.confirm(t('tokenGuardV2.resumeReloginBudgetConfirm'))) return
  await act(item, async () => { Object.assign(item, await resumeTokenGuardV2Budget(item.account_id)) }, 'tokenGuardV2.reloginBudgetResumed')
}
async function toggleSwitch(item: TokenGuardV2Account, field: 'enabled' | 'auto_relogin_enabled', event: Event) {
  const input = event.target as HTMLInputElement
  if (mutationBusy.value) { input.checked = item[field]; return }
  invalidateLoad()
  busyId.value = item.account_id; error.value = ''; notice.value = ''
  try {
    const saved = await updateTokenGuardV2Switches(item.account_id, { [field]: !item[field] })
    Object.assign(item, saved)
    notice.value = t('tokenGuardV2.switchSaved')
  } catch (value) {
    error.value = message(value)
  } finally {
    input.checked = item[field]
    busyId.value = 0
  }
}

async function changeRuntime(field: keyof TokenGuardV2RuntimeSettings, event: Event) {
  const select = event.target as HTMLSelectElement
  if (mutationBusy.value || !runtimeLoaded.value) return
  invalidateLoad()
  const input = { ...runtimeDraft, [field]: field === 'worker_concurrency' ? Number(select.value) : select.value }
  runtimeSaving.value = true; error.value = ''; notice.value = ''
  try {
    Object.assign(runtimeDraft, await saveTokenGuardV2Runtime(input))
    notice.value = t('tokenGuardV2.runtimeSaved')
  } catch (value) {
    error.value = message(value)
  } finally {
    select.value = String(runtimeDraft[field])
    runtimeSaving.value = false
  }
}

const remove = async (item: TokenGuardV2Account) => {
  if (mutationBusy.value) return
  if (!window.confirm(t('tokenGuardV2.removeConfirm', { account: item.account_name }))) return
  await act(item, () => deleteTokenGuardV2Account(item.account_id), 'tokenGuardV2.removedNotice')
}


watch([accountFilter, searchQuery], () => { accountPage.value = 1 })
watch(() => filteredAccounts.value.length, (total) => {
  accountPage.value = Math.min(accountPage.value, Math.max(1, Math.ceil(total / accountPageSize.value)))
})

onMounted(() => {
  void load()
  timer = setInterval(() => {
    currentTime.value = Date.now()
    if (document.visibilityState === 'visible' && !editorOpen.value) void load(true)
  }, 30_000)
})
onBeforeUnmount(() => { loadGeneration++; if (timer) clearInterval(timer) })
</script>

<style scoped>
.guard-v2 { @apply w-full min-w-0 text-gray-900 dark:text-gray-100; }
.page-heading { @apply mb-6 flex flex-wrap items-center justify-between gap-4; }
.eyebrow { @apply mb-1 text-[11px] font-semibold tracking-widest text-primary-600; }
.page-heading h2 { @apply text-2xl font-semibold tracking-tight; }
.subtitle { @apply mt-2 max-w-3xl text-sm leading-6 text-gray-500 dark:text-gray-400; }
.summary-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(190px,1fr)); gap:20px; @apply mb-5; }
.summary-card,.table-card,.rules-card { @apply overflow-hidden rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.summary-card { @apply p-5; }
.summary-card span { @apply block text-xs font-medium text-gray-400; }
.summary-card strong { @apply mt-2 block text-2xl font-semibold tabular-nums; }
.summary-card small { @apply mt-2 block truncate text-xs text-gray-400; }
.section-tabs { @apply mb-4 flex w-fit gap-1 rounded-xl bg-gray-100 p-1 dark:bg-dark-800; }
.section-tabs button { @apply rounded-lg px-4 py-2 text-xs font-medium text-gray-500 transition-colors dark:text-gray-400; }
.section-tabs button.active { @apply bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white; }
.list-toolbar { @apply flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-4 dark:border-dark-800; }
.filter-tabs,.group-tabs { @apply flex max-w-full gap-1 overflow-x-auto; }
.filter-tabs button,.group-tabs button { @apply whitespace-nowrap rounded-lg border border-transparent px-3 py-1.5 text-xs font-medium text-gray-500 transition-colors hover:bg-gray-50 dark:text-gray-400 dark:hover:bg-dark-800; }
.filter-tabs button span { @apply ml-1 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] tabular-nums dark:bg-dark-700; }
.filter-tabs button.active,.group-tabs button.active { @apply border-primary-200 bg-primary-50 text-primary-700 dark:border-primary-900 dark:bg-primary-950/30 dark:text-primary-300; }
.search-box { @apply flex min-w-64 items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 text-gray-400 focus-within:border-primary-400 dark:border-dark-600 dark:bg-dark-900; }
.search-box input { @apply min-w-0 flex-1 border-0 bg-transparent py-2 text-sm text-gray-800 outline-none placeholder:text-gray-400 dark:text-gray-100; }
.table-wrap { max-height:65vh; @apply overflow-auto overscroll-contain; scrollbar-gutter:stable; }
table { min-width:1180px; @apply w-full text-left text-xs; }
thead { @apply sticky top-0 z-10 bg-white text-gray-400 dark:bg-dark-900; }
th { @apply whitespace-nowrap px-4 py-3 font-medium; }
td { @apply border-b border-gray-100 px-4 py-4 align-top dark:border-dark-800; }
td strong { @apply block max-w-52 truncate text-[13px] font-medium; }
td small { @apply mt-1.5 block max-w-64 text-[10px] leading-4 text-gray-400; }
tbody tr:hover { @apply bg-gray-50/70 dark:bg-dark-800/50; }
.badge { @apply inline-flex rounded-md bg-amber-50 px-2 py-1 text-[11px] font-medium text-amber-700 dark:bg-amber-950/30 dark:text-amber-300; }
.badge.ok { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300; }
.badge.danger { @apply bg-red-50 text-red-600 dark:bg-red-950/30 dark:text-red-300; }
.actions { @apply flex max-w-64 flex-wrap gap-1.5; }
.link-btn { @apply rounded-lg border border-gray-200 px-2.5 py-1 text-[11px] font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-40 dark:border-dark-600 dark:hover:bg-dark-800; }
.danger-text { @apply text-red-600 hover:bg-red-50 dark:hover:bg-red-950/30; }
.empty-state { @apply flex min-h-72 flex-col items-center justify-center gap-3 p-8 text-center text-gray-400; }
.empty-state h3 { @apply text-sm font-medium text-gray-600 dark:text-gray-300; }
.empty-state p { @apply max-w-md text-xs leading-5; }
.rules-card { @apply p-5; }
.rules-heading { @apply flex flex-wrap items-start justify-between gap-3; }
.rules-heading h3 { @apply text-base font-semibold; }
.rules-heading p { @apply mt-1 max-w-3xl text-xs leading-5 text-gray-500 dark:text-gray-400; }
.rule-metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(160px,1fr)); @apply mt-5 gap-3; }
.rule-metrics label { @apply rounded-xl border border-gray-100 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/60; }
.rule-metrics span { @apply block text-xs text-gray-400; }
.rule-metrics input { @apply mt-2 inline-block w-28 text-lg font-semibold tabular-nums; }
.rule-metrics small { @apply ml-1 text-xs text-gray-400; }
.rules-actions { @apply mt-4 flex justify-end; }
.rule-list { @apply mt-5 space-y-3; }
.rule-list article { @apply flex gap-3 rounded-xl border border-gray-100 p-4 dark:border-dark-700; }
.rule-list article > span { @apply flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary-50 text-xs font-semibold text-primary-700 dark:bg-primary-950/30 dark:text-primary-300; }
.rule-list strong { @apply text-sm font-medium; }
.rule-list p { @apply mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400; }
.editor-form { @apply space-y-5; }
.field-label { @apply block text-xs font-medium text-gray-600 dark:text-gray-300; }
.field-label .input { @apply mt-2; }
.field-hint { @apply mt-1.5 block text-[11px] font-normal leading-4 text-gray-400; }
.account-picker { @apply space-y-3; }
.picker-search { @apply w-full; }
.account-picker-list { @apply max-h-72 space-y-2 overflow-y-auto rounded-xl border border-gray-200 p-2 dark:border-dark-700; }
.account-picker-list button { @apply flex w-full items-center justify-between gap-3 rounded-lg border border-transparent px-3 py-3 text-left transition-colors hover:bg-gray-50 dark:hover:bg-dark-800; }
.account-picker-list button.selected { @apply border-primary-300 bg-primary-50/50 dark:border-primary-800 dark:bg-primary-950/20; }
.account-picker-list strong { @apply block text-sm font-medium; }
.account-picker-list small { @apply mt-1 block text-xs text-gray-400; }
.plan-badge { @apply shrink-0 rounded-md bg-blue-50 px-2 py-1 text-[11px] font-medium text-blue-700 dark:bg-blue-950/30 dark:text-blue-300; }
.picker-empty { @apply py-8 text-center text-xs text-gray-400; }
.selected-account { @apply rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/60; }
.selected-account span { @apply block text-xs text-gray-400; }
.selected-account strong { @apply mt-1 block text-sm font-medium; }
.mode-grid { @apply grid gap-3 sm:grid-cols-2; }
.mode-grid legend { @apply col-span-full mb-1 text-xs font-medium text-gray-600 dark:text-gray-300; }
.mode-option,.toggle-row { @apply flex items-start gap-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700; }
.mode-option:has(input:checked),.toggle-row:has(input:checked) { @apply border-primary-300 bg-primary-50/40 dark:border-primary-800 dark:bg-primary-950/20; }
.mode-option input,.toggle-row input,.check-row input { @apply mt-1 rounded text-primary-600; }
.mode-option strong,.toggle-row strong { @apply block text-sm font-medium; }
.mode-option small,.toggle-row small { @apply mt-1 block text-xs leading-5 text-gray-400; }
.automation-switch { @apply relative h-5 w-9 shrink-0 cursor-pointer appearance-none rounded-full border-0 bg-gray-300 transition-colors checked:bg-primary-600 disabled:cursor-wait dark:bg-dark-600 dark:checked:bg-primary-500; }
.automation-switch::after { content:''; @apply absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform; }
.automation-switch:checked::after { @apply translate-x-4; }
.check-row { @apply flex items-center gap-2 text-xs text-gray-500; }
.error-banner { @apply mb-4 rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; }
.success-banner { @apply mb-4 rounded-xl bg-emerald-50 p-3 text-sm text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300; }
</style>
