<template>
  <AppLayout>
    <div class="pelican-tests">
      <SmartOpsNav />
      <header class="ops-heading">
        <div>
          <p class="eyebrow">{{ t('accountOps.smartTitle') }}</p>
          <h2>{{ t('pelicanTests.title') }}</h2>
          <p class="subtitle">{{ t('pelicanTests.description') }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <RouterLink to="/pelican-showcase" class="btn btn-secondary inline-flex items-center gap-2">
            <Icon name="eye" size="sm" />{{ t('pelicanTests.viewShowcase') }}
          </RouterLink>
          <button class="btn btn-secondary inline-flex items-center gap-2" :disabled="loading" @click="load">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />{{ t('pelicanTests.refresh') }}
          </button>
          <button class="btn btn-primary inline-flex items-center gap-2" data-testid="pelican-tests-create" @click="openEditor()">
            <Icon name="plus" size="sm" />{{ t('pelicanTests.create') }}
          </button>
        </div>
      </header>

      <p v-if="error" role="alert" class="error-banner">{{ error }}</p>
      <p v-if="notice" role="status" class="success-banner">{{ notice }}</p>

      <div class="ops-columns">
        <section class="settings-card" data-testid="pelican-showcase-settings">
          <div class="section-title">
            <span class="icon-tile"><Icon name="eye" size="md" /></span>
            <div><h3>{{ t('pelicanTests.showcase.title') }}</h3><p>{{ t('pelicanTests.showcase.hint') }}</p></div>
          </div>
          <div v-if="!draft" class="space-y-4 p-5" role="status">
            <span v-for="n in 4" :key="n" class="block h-8 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-800" />
          </div>
          <form v-else class="settings-form" @submit.prevent="saveSettings">
            <fieldset :disabled="savingSettings">
              <div class="enable-row">
                <span><strong>{{ t('pelicanTests.showcase.enabled') }}</strong><small>{{ t('pelicanTests.showcase.enabledHint') }}</small></span>
                <Toggle v-model="draft.enabled" data-testid="pelican-showcase-enabled" />
              </div>
              <div class="enable-row mt-5">
                <span><strong>{{ t('pelicanTests.showcase.apiEnabled') }}</strong><small>{{ t('pelicanTests.showcase.apiEnabledHint') }}</small></span>
                <Toggle v-model="draft.api_enabled" :aria-label="t('pelicanTests.showcase.apiEnabled')" data-testid="pelican-showcase-api-enabled" />
              </div>
              <p v-if="!draft.enabled" class="field-hint text-amber-600 dark:text-amber-400" data-testid="pelican-showcase-api-requires-gallery">
                {{ t('pelicanTests.showcase.apiRequiresGallery') }}
              </p>
              <button type="button" class="mt-2 inline-flex items-center gap-1.5 text-xs text-primary-600 hover:underline dark:text-primary-400" data-testid="pelican-showcase-api-info" @click="showApiInfo = true">
                <Icon name="link" size="xs" />{{ t('pelicanTests.showcase.apiInfo') }}
              </button>
              <label class="field-label" for="pelican-showcase-max-items">{{ t('pelicanTests.showcase.maxItems') }}</label>
              <div class="relative">
                <input id="pelican-showcase-max-items" v-model.number="draft.max_items" type="number" min="1" max="100" required class="input w-full pr-12" data-testid="pelican-showcase-max-items" />
                <span class="unit">{{ t('pelicanTests.showcase.maxItemsUnit') }}</span>
              </div>
              <p class="field-hint">{{ t('pelicanTests.showcase.maxItemsHint') }}</p>
              <div class="enable-row mt-5 mb-0">
                <span><strong>{{ t('pelicanTests.showcase.autoCleanup') }}</strong><small>{{ t('pelicanTests.showcase.autoCleanupHint') }}</small></span>
                <Toggle v-model="draft.auto_cleanup" data-testid="pelican-showcase-auto-cleanup" />
              </div>
              <template v-if="draft.auto_cleanup">
                <label class="field-label" for="pelican-showcase-retention">{{ t('pelicanTests.showcase.retentionDays') }}</label>
                <div class="relative">
                  <input id="pelican-showcase-retention" v-model.number="draft.retention_days" type="number" min="1" max="90" required class="input w-full pr-12" data-testid="pelican-showcase-retention-days" />
                  <span class="unit">{{ t('pelicanTests.showcase.retentionDaysUnit') }}</span>
                </div>
                <p class="field-hint">{{ t('pelicanTests.showcase.retentionDaysHint') }}</p>
              </template>
              <div class="settings-actions">
                <span>{{ settingsDirty ? t('pelicanTests.showcase.unsaved') : t('pelicanTests.showcase.savedState') }}</span>
                <button class="btn btn-primary" :disabled="savingSettings || !settingsDirty" data-testid="pelican-showcase-save">
                  {{ t(savingSettings ? 'pelicanTests.showcase.saving' : 'pelicanTests.showcase.save') }}
                </button>
              </div>
            </fieldset>
          </form>
        </section>

        <section class="plans-card" data-testid="pelican-group-tests">
          <header class="panel-heading">
            <div><h3>{{ t('pelicanTests.plans.title') }}<span class="count">{{ plans.length }}</span></h3><p>{{ t('pelicanTests.plans.hint') }}</p></div>
          </header>
          <div v-if="!plansLoaded" class="space-y-3 p-5" role="status">
            <span v-for="n in 3" :key="n" class="block h-24 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />
          </div>
          <div v-else-if="!plans.length" class="empty-state">
            <Icon name="beaker" size="xl" />
            <p>{{ t('pelicanTests.plans.empty') }}</p>
            <button class="btn btn-secondary" @click="openEditor()">{{ t('pelicanTests.create') }}</button>
          </div>
          <ul v-else class="plan-list">
            <li v-for="plan in plans" :key="plan.id" class="plan-row" :data-testid="`pelican-plan-${plan.id}`">
              <div class="plan-main">
                <span class="group-icon" :class="platformBadgeLightClass(plan.group_platform)">
                  <PlatformIcon :platform="plan.group_platform as GroupPlatform" size="sm" />
                </span>
                <div class="min-w-0">
                  <p class="plan-title">
                    <strong :title="plan.group_name">{{ plan.group_name }}</strong>
                    <span class="inline-flex flex-wrap items-center gap-x-2 text-xs font-normal tabular-nums text-gray-500 dark:text-gray-400" :title="t('pelicanTests.cost.hint')" :data-testid="`pelican-plan-cost-${plan.id}`">
                      <span class="whitespace-nowrap">{{ t('pelicanTests.cost.today', { amount: costLabel(plan.today_cost_usd, plan.today_cost_incomplete) }) }}</span>
                      <span aria-hidden="true">·</span>
                      <span class="whitespace-nowrap">{{ t('pelicanTests.cost.total', { amount: costLabel(plan.total_cost_usd, plan.total_cost_incomplete) }) }}</span>
                    </span>
                    <span class="state-chip" :class="plan.enabled ? 'state-on' : 'state-off'">{{ t(plan.enabled ? 'pelicanTests.plans.active' : 'pelicanTests.plans.paused') }}</span>
                    <span v-if="isRunning(plan)" class="state-chip state-running">{{ t('pelicanTests.plans.running') }}</span>
                  </p>
                  <p class="plan-meta">
                    <code v-if="plan.model_id">{{ plan.model_id }}</code>
                    <span v-else class="text-amber-600 dark:text-amber-400">{{ t('pelicanTests.plans.modelMissing') }}</span>
                    <span>·</span><span>{{ effortLabel(plan.pelican_config?.reasoning_effort) }}</span>
                    <span>·</span><span>{{ t('pelicanTests.plans.parallel', { count: plan.pelican_config?.parallel_count || 1 }) }}</span>
                    <span>·</span><span>{{ scheduleLabel(plan.cron_expression) }}</span>
                  </p>
                  <p v-if="plan.group_status !== 'active'" class="plan-warning">
                    {{ t(plan.group_status === 'deleted' ? 'pelicanTests.plans.groupDeleted' : 'pelicanTests.plans.groupDisabled') }}
                  </p>
                  <p class="plan-last" :data-testid="`pelican-plan-last-${plan.id}`">
                    <span class="text-gray-400">{{ t('pelicanTests.plans.lastRun') }}</span>
                    <template v-if="plan.last_result">
                      <span class="result-dot" :class="plan.last_result.status === 'success' ? 'dot-ok' : 'dot-bad'" />
                      <span>{{ formatDateTimeToMinute(plan.last_result.started_at) }}</span>
                      <span v-if="plan.last_result.account_id">{{ t('pelicanTests.plans.routedTo', { name: accountName(plan.last_result) }) }}</span>
                      <span v-else>{{ t('pelicanTests.plans.noAccount') }}</span>
                      <span v-if="plan.last_result.attempts.length" class="text-amber-600 dark:text-amber-400">{{ t('pelicanTests.plans.failedOver', { count: plan.last_result.attempts.length }) }}</span>
                      <span v-if="plan.last_result.status !== 'success'" class="truncate text-red-500" :title="plan.last_result.error_message">{{ plan.last_result.error_message }}</span>
                    </template>
                    <span v-else>{{ t('pelicanTests.plans.never') }}</span>
                  </p>
                  <p v-if="plan.enabled && plan.next_run_at" class="plan-next">
                    {{ t('pelicanTests.plans.nextRun') }} {{ formatDateTimeToMinute(plan.next_run_at) }}
                  </p>
                </div>
              </div>
              <div class="plan-actions">
                <button :disabled="!!pending[plan.id] || isRunning(plan) || !plan.model_id" :data-testid="`pelican-plan-run-${plan.id}`" @click="runNow(plan)">
                  <Icon name="play" size="xs" />{{ t('pelicanTests.plans.runNow') }}
                </button>
                <button :disabled="!!pending[plan.id]" :data-testid="`pelican-plan-toggle-${plan.id}`" @click="toggle(plan)">
                  {{ t(plan.enabled ? 'pelicanTests.plans.pause' : 'pelicanTests.plans.enable') }}
                </button>
                <button :disabled="!!pending[plan.id]" :data-testid="`pelican-plan-edit-${plan.id}`" @click="openEditor(plan)">
                  <Icon name="edit" size="xs" />{{ t('pelicanTests.plans.edit') }}
                </button>
                <button class="danger" :disabled="!!pending[plan.id]" :data-testid="`pelican-plan-delete-${plan.id}`" @click="deleting = plan">
                  <Icon name="trash" size="xs" />{{ t('pelicanTests.plans.delete') }}
                </button>
              </div>
            </li>
          </ul>
        </section>
      </div>

      <section class="history-card" data-testid="pelican-test-history">
        <header class="panel-heading">
          <div><h3>{{ t('pelicanTests.history.title') }}</h3><p>{{ t('pelicanTests.history.hint') }}</p></div>
        </header>
        <div class="history-scroll">
          <table>
            <thead>
              <tr>
                <th>{{ t('pelicanTests.history.time') }}</th>
                <th>{{ t('pelicanTests.history.group') }}</th>
                <th>{{ t('pelicanTests.history.model') }}</th>
                <th>{{ t('pelicanTests.history.account') }}</th>
                <th>{{ t('pelicanTests.history.result') }}</th>
                <th>{{ t('pelicanTests.history.duration') }}</th>
                <th>{{ t('pelicanTests.cost.column') }}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              <tr v-if="resultsLoading && !results.length">
                <td colspan="8" class="text-center" role="status">{{ t('pelicanTests.history.loading') }}</td>
              </tr>
              <tr v-for="result in results" :key="result.id" :data-testid="`pelican-result-${result.id}`">
                <td class="whitespace-nowrap">{{ formatDateTimeToMinute(result.started_at) }}</td>
                <td><strong :title="result.group_name">{{ result.group_name }}</strong></td>
                <td><code>{{ result.pelican_config?.model_id || '—' }}</code></td>
                <td>
                  <strong v-if="result.account_id" :title="accountName(result)">{{ accountName(result) }}</strong>
                  <span v-else class="text-gray-400">{{ t('pelicanTests.plans.noAccount') }}</span>
                  <small v-if="result.attempts.length" class="text-amber-600 dark:text-amber-400">
                    {{ t('pelicanTests.history.tried', { accounts: result.attempts.map(attemptLabel).join('、') }) }}
                  </small>
                </td>
                <td>
                  <span class="result-badge" :class="result.status === 'success' ? 'badge-ok' : 'badge-bad'">
                    {{ t(result.status === 'success' ? 'pelicanTests.history.success' : 'pelicanTests.history.failed') }}
                  </span>
                  <small v-if="result.status !== 'success'" class="error-text" :title="result.error_message">{{ result.error_message }}</small>
                </td>
                <td class="whitespace-nowrap tabular-nums">{{ pelicanDurationLabel(t, result.latency_ms) }}</td>
                <td class="whitespace-nowrap tabular-nums" :title="t('pelicanTests.cost.hint')" :data-testid="`pelican-result-cost-${result.id}`">{{ costLabel(result.cost_usd, result.cost_incomplete) }}</td>
                <td class="whitespace-nowrap text-right">
                  <button v-if="result.status === 'success'" class="link-button" :data-testid="`pelican-result-view-${result.id}`" @click="openPreview(result)">
                    {{ t('pelicanTests.history.view') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="resultsLoaded && !resultsLoading && !results.length" class="empty-state"><Icon name="document" size="xl" /><p>{{ t('pelicanTests.history.empty') }}</p></div>
        </div>
        <Pagination
          v-if="resultsTotal > 0"
          :total="resultsTotal"
          :page="resultsPage"
          :page-size="resultsPageSize"
          show-jump
          data-testid="pelican-history-pagination"
          @update:page="changeResultsPage"
          @update:page-size="changeResultsPageSize"
        />
      </section>

      <aside class="scope-note"><Icon name="infoCircle" size="sm" /><p>{{ t('pelicanTests.accountNote') }}</p></aside>
    </div>

    <BaseDialog
      :show="editor !== null"
      :title="t(editor?.id ? 'pelicanTests.editor.editTitle' : 'pelicanTests.editor.createTitle')"
      width="normal"
      @close="editor = null"
    >
      <form v-if="editor" id="pelican-test-editor" class="space-y-4" data-testid="pelican-test-editor" @submit.prevent="savePlan">
        <div>
          <label class="field-label mt-0">{{ t('pelicanTests.editor.group') }}</label>
          <Select
            v-model="editor.form.group_id"
            :options="groupOptions"
            :disabled="!!editor.id"
            searchable
            :placeholder="t('pelicanTests.editor.groupPlaceholder')"
            :aria-label="t('pelicanTests.editor.group')"
          />
          <p v-if="editor.id" class="field-hint">{{ t('pelicanTests.editor.groupLocked') }}</p>
          <p v-else-if="groupsLoadFailed" class="field-hint text-amber-600 dark:text-amber-400">{{ t('pelicanTests.editor.groupsLoadFailed') }}</p>
        </div>
        <div>
          <label class="field-label mt-0" for="pelican-test-model">{{ t('pelicanTests.editor.model') }}</label>
          <input id="pelican-test-model" v-model.trim="editor.form.model_id" maxlength="100" class="input w-full" :placeholder="t('pelicanTests.editor.modelPlaceholder')" data-testid="pelican-test-model" />
          <p class="field-hint">{{ t('pelicanTests.editor.modelHint') }}</p>
        </div>
        <div>
          <div class="flex items-center justify-between">
            <label class="field-label mt-0" for="pelican-test-prompt">{{ t('pelicanTests.editor.prompt') }}</label>
            <button type="button" class="link-button text-xs" @click="editor.form.prompt = PELICAN_PROMPT">{{ t('pelicanTests.editor.resetPrompt') }}</button>
          </div>
          <PelicanPromptMode v-model="editor.form.prompt_mode" />
          <textarea v-if="editor.form.prompt_mode !== 'random'" id="pelican-test-prompt" v-model="editor.form.prompt" rows="3" maxlength="32000" class="input w-full" data-testid="pelican-test-prompt" />
          <p class="field-hint">{{ t('pelicanTests.editor.promptHint') }}</p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="field-label mt-0">{{ t('pelicanTests.editor.effort') }}</label>
            <Select v-model="editor.form.reasoning_effort" :options="effortOptions" :aria-label="t('pelicanTests.editor.effort')" />
          </div>
          <div>
            <label class="field-label mt-0" for="pelican-test-parallel">{{ t('pelicanTests.editor.parallel') }}</label>
            <input id="pelican-test-parallel" v-model.number="editor.form.parallel_count" type="number" min="1" max="8" class="input w-full" />
            <p class="field-hint">{{ t('pelicanTests.editor.parallelHint') }}</p>
          </div>
        </div>
        <div>
          <label class="field-label mt-0">{{ t('pelicanTests.editor.schedule') }}</label>
          <Select v-model="editor.schedule" :options="scheduleOptions" :aria-label="t('pelicanTests.editor.schedule')" data-testid="pelican-test-schedule" />
          <template v-if="editor.schedule === 'custom'">
            <label class="field-label" for="pelican-test-cron">{{ t('pelicanTests.editor.cron') }}</label>
            <input id="pelican-test-cron" v-model.trim="editor.form.cron_expression" class="input w-full font-mono" placeholder="*/30 * * * *" data-testid="pelican-test-cron" />
            <p class="field-hint">{{ t('pelicanTests.editor.cronHint') }}</p>
          </template>
        </div>
        <div class="enable-row mb-0">
          <span><strong>{{ t('pelicanTests.editor.enabled') }}</strong></span>
          <Toggle v-model="editor.form.enabled" data-testid="pelican-test-enabled" />
        </div>
      </form>
      <template #footer>
        <div class="flex w-full justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="editor = null">{{ t('pelicanTests.editor.cancel') }}</button>
          <button type="submit" form="pelican-test-editor" class="btn btn-primary" :disabled="savingPlan" data-testid="pelican-test-save">
            {{ t('pelicanTests.editor.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="preview !== null"
      :title="previewTitle"
      width="full"
      content-class="h-[90dvh]"
      body-class="flex min-h-0 flex-col !overflow-hidden"
      close-on-click-outside
      @close="preview = null"
    >
      <div v-if="preview" class="flex min-h-0 flex-1 flex-col gap-3" data-testid="pelican-result-preview">
        <PelicanQuestionSnapshot :question="preview.result.pelican_config?.pelican_question" :actual-prompt="preview.result.pelican_config?.actual_prompt || preview.result.pelican_config?.prompt" />
        <div class="min-h-0 flex-1 overflow-hidden rounded-xl border border-gray-200 bg-gray-100 dark:border-dark-700 dark:bg-dark-900">
          <PelicanArtworkPreview v-if="preview.status === 'ready'" :html="preview.html" :title="previewTitle" />
          <div v-else class="flex h-full items-center justify-center p-6 text-sm text-gray-500">
            <span v-if="preview.status === 'loading'" class="animate-pulse">{{ t('pelicanTests.history.previewLoading') }}</span>
            <span v-else-if="preview.status === 'invalid'">{{ t('pelicanTests.history.invalidHtml') }}</span>
            <span v-else class="text-red-500">{{ t('pelicanTests.history.previewLoadFailed') }}</span>
          </div>
        </div>
        <p class="shrink-0 text-xs text-gray-400 dark:text-gray-500">{{ t('pelicanTests.history.sandboxNote') }}</p>
      </div>
      <template #footer>
        <div class="flex w-full justify-end">
          <button type="button" class="btn btn-secondary" @click="preview = null">{{ t('pelicanTests.history.close') }}</button>
        </div>
      </template>
    </BaseDialog>

    <PelicanShowcaseApiDialog
      :show="showApiInfo"
      :enabled="!!settings?.enabled && settings?.api_enabled === true"
      @close="showApiInfo = false"
    />

    <ConfirmDialog
      :show="deleting !== null"
      :title="t('pelicanTests.plans.deleteTitle')"
      :message="t('pelicanTests.plans.deleteConfirm')"
      :confirm-text="t('pelicanTests.plans.delete')"
      danger
      @confirm="removePlan"
      @cancel="deleting = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import PelicanPromptMode from '@/components/admin/account/PelicanPromptMode.vue'
import PelicanQuestionSnapshot from '@/components/admin/account/PelicanQuestionSnapshot.vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import PelicanArtworkPreview from '@/components/user/pelican/PelicanArtworkPreview.vue'
import PelicanShowcaseApiDialog from '@/components/user/pelican/PelicanShowcaseApiDialog.vue'
import { pelicanDurationLabel } from '@/components/user/pelican/pelicanShowcaseFormat'
import { adminAPI } from '@/api'
import {
  pelicanTestsAPI,
  type PelicanGroupTestAttempt,
  type PelicanGroupTestPlan,
  type PelicanGroupTestPlanInput,
  type PelicanGroupTestResult,
  type PelicanShowcaseSettings,
} from '@/api/admin/pelicanTests'
import { useAppStore } from '@/stores/app'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import type { AdminGroup, GroupPlatform } from '@/types'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTimeToMinute } from '@/utils/format'
import { PELICAN_PROMPT } from '@/utils/intelligenceTest'
import { extractPelicanHtml } from '@/utils/pelicanHtml'
import { platformBadgeLightClass, platformLabel } from '@/utils/platformColors'

const SCHEDULES = [
  { key: 'every15m', cron: '*/15 * * * *' },
  { key: 'every30m', cron: '*/30 * * * *' },
  { key: 'hourly', cron: '0 * * * *' },
  { key: 'every2h', cron: '0 */2 * * *' },
  { key: 'every6h', cron: '0 */6 * * *' },
  { key: 'daily', cron: '0 9 * * *' },
] as const
const EFFORTS = ['minimal', 'low', 'medium', 'high', 'xhigh']
const POLL_MS = 15000

const { t } = useI18n()
const appStore = useAppStore()

const plans = ref<PelicanGroupTestPlan[]>([])
const plansLoaded = ref(false)
const settings = ref<PelicanShowcaseSettings | null>(null)
const draft = ref<PelicanShowcaseSettings | null>(null)
const results = ref<PelicanGroupTestResult[]>([])
const resultsLoaded = ref(false)
const resultsPage = ref(1)
const resultsPageSize = ref(getPersistedPageSize())
const resultsTotal = ref(0)
const resultsLoading = ref(false)
const groups = ref<AdminGroup[]>([])
const groupsLoadFailed = ref(false)
const loading = ref(false)
const savingSettings = ref(false)
const showApiInfo = ref(false)
const savingPlan = ref(false)
const error = ref('')
const notice = ref('')
const pending = reactive<Record<number, string>>({})
const editor = ref<{ id: number | null; groupName: string; form: PelicanGroupTestPlanInput; schedule: string } | null>(null)
const deleting = ref<PelicanGroupTestPlan | null>(null)
const preview = ref<{ result: PelicanGroupTestResult; html: string; status: 'loading' | 'ready' | 'invalid' | 'error' } | null>(null)
let alive = true
let timer: ReturnType<typeof setInterval> | undefined
let resultsController: AbortController | undefined
let resultsRequest = 0
let polling = false

const settingsDirty = computed(() => !!draft.value && JSON.stringify(draft.value) !== JSON.stringify(settings.value))
const groupOptions = computed(() => {
  const options = groups.value.map((group) => ({ value: group.id, label: `${group.name} · ${platformLabel(group.platform)}` }))
  // An edited plan's group may be disabled or deleted; still show its name.
  const current = editor.value
  if (current?.id && !options.some((option) => option.value === current.form.group_id)) {
    options.unshift({ value: current.form.group_id, label: current.groupName })
  }
  return options
})
const effortOptions = computed(() => EFFORTS.map((value) => ({ value, label: effortLabel(value) })))
const scheduleOptions = computed(() => [
  ...SCHEDULES.map((schedule) => ({ value: schedule.cron, label: t(`pelicanTests.schedules.${schedule.key}`) })),
  { value: 'custom', label: t('pelicanTests.schedules.custom') },
])
const previewTitle = computed(() => (preview.value
  ? t('pelicanTests.history.previewTitle', { group: preview.value.result.group_name || '—', model: preview.value.result.pelican_config?.model_id || '—' })
  : ''))

// A preset keeps the cron in step; "custom" lets the admin type one.
watch(() => editor.value?.schedule, (schedule) => {
  if (editor.value && schedule && schedule !== 'custom') editor.value.form.cron_expression = schedule
})

function effortLabel(effort?: string) {
  return effort && EFFORTS.includes(effort) ? t(`pelicanShowcase.efforts.${effort}`) : '—'
}

function scheduleLabel(cron: string) {
  const preset = SCHEDULES.find((schedule) => schedule.cron === cron)
  return preset ? t(`pelicanTests.schedules.${preset.key}`) : cron
}

function accountName(result: PelicanGroupTestResult) {
  return result.account_name || `#${result.account_id}`
}

function costLabel(value: number | null | undefined, incomplete = false) {
  if (value == null) return t('pelicanTests.cost.unknown')
  const amount = value > 0 && value < 0.000001 ? '<$0.000001' : `$${value.toFixed(6)}`
  return incomplete ? t('pelicanTests.cost.partial', { amount }) : amount
}

function attemptLabel(attempt: PelicanGroupTestAttempt) {
  return attempt.account_name || `#${attempt.account_id}`
}

function isRunning(plan: PelicanGroupTestPlan) {
  return !!plan.running_until && new Date(plan.running_until).getTime() > Date.now()
}

function fail(err: unknown, fallback: string) {
  error.value = extractApiErrorMessage(err, fallback)
  notice.value = ''
}

function succeed(message: string) {
  notice.value = message
  error.value = ''
}

async function load() {
  if (loading.value) return
  loading.value = true
  try {
    const [planList, showcase] = await Promise.all([
      pelicanTestsAPI.listPlans(),
      pelicanTestsAPI.getShowcaseSettings(),
      loadResults(),
    ])
    if (!alive) return
    plans.value = planList
    plansLoaded.value = true
    settings.value = showcase
    // Keep an admin's unsaved edits across a refresh.
    if (!draft.value || !settingsDirty.value) draft.value = { ...showcase }
  } catch (err: unknown) {
    fail(err, t('pelicanTests.loadError'))
  } finally {
    loading.value = false
  }
}

// Refresh the current page in place so polling cannot grow the table indefinitely.
async function poll() {
  if (loading.value || polling || document.hidden) return
  polling = true
  try {
    const [planList] = await Promise.all([pelicanTestsAPI.listPlans(), loadResults(true)])
    if (!alive) return
    plans.value = planList
  } catch {
    // The next tick or a manual refresh tries again.
  } finally {
    polling = false
  }
}

async function loadResults(silent = false) {
  if (silent && resultsLoading.value) return
  const request = ++resultsRequest
  resultsController?.abort()
  const controller = new AbortController()
  resultsController = controller
  const pageNumber = resultsPage.value
  const pageSize = resultsPageSize.value
  resultsLoading.value = true
  if (!silent) results.value = []
  try {
    let page = await pelicanTestsAPI.listResults(pageNumber, pageSize, 0, controller.signal)
    if (!alive || request !== resultsRequest) return
    // Deleting a plan or retention cleanup can remove the last page.
    const lastPage = Math.max(1, Math.ceil(page.total / pageSize))
    if (pageNumber > lastPage) {
      resultsPage.value = lastPage
      page = await pelicanTestsAPI.listResults(lastPage, pageSize, 0, controller.signal)
    }
    if (!alive || request !== resultsRequest) return
    results.value = page.items
    resultsTotal.value = page.total
    resultsLoaded.value = true
  } catch (err: unknown) {
    if (alive && request === resultsRequest && !controller.signal.aborted && !silent) {
      fail(err, t('pelicanTests.loadError'))
    }
  } finally {
    if (alive && request === resultsRequest) resultsLoading.value = false
  }
}

function changeResultsPage(page: number) {
  if (page === resultsPage.value) return
  resultsPage.value = page
  void loadResults()
}

function changeResultsPageSize(pageSize: number) {
  if (pageSize === resultsPageSize.value) return
  resultsPageSize.value = pageSize
  resultsPage.value = 1
  void loadResults()
}

async function saveSettings() {
  if (!draft.value || savingSettings.value) return
  savingSettings.value = true
  try {
    const saved = await pelicanTestsAPI.updateShowcaseSettings({ ...draft.value })
    settings.value = saved
    draft.value = { ...saved }
    succeed(t('pelicanTests.showcase.saved'))
    // The sidebar entry for users follows the public flag.
    await appStore.fetchPublicSettings(true)
  } catch (err: unknown) {
    fail(err, t('pelicanTests.showcase.saveFailed'))
  } finally {
    savingSettings.value = false
  }
}

async function loadGroups() {
  if (groups.value.length) return
  try {
    const all = await adminAPI.groups.getAll()
    groups.value = all.filter((group) => group.status === 'active')
    groupsLoadFailed.value = false
  } catch {
    groupsLoadFailed.value = true
  }
}

function inputFrom(plan: PelicanGroupTestPlan): PelicanGroupTestPlanInput {
  return {
    group_id: plan.group_id,
    model_id: plan.model_id,
    cron_expression: plan.cron_expression,
    enabled: plan.enabled,
    prompt: plan.pelican_config?.prompt || PELICAN_PROMPT,
    prompt_mode: plan.pelican_config?.prompt_mode || 'custom',
    reasoning_effort: plan.pelican_config?.reasoning_effort || 'medium',
    parallel_count: plan.pelican_config?.parallel_count || 1,
  }
}

function openEditor(plan?: PelicanGroupTestPlan) {
  const form: PelicanGroupTestPlanInput = plan
    ? inputFrom(plan)
    : { group_id: 0, model_id: '', cron_expression: SCHEDULES[1].cron, enabled: true, prompt: PELICAN_PROMPT, reasoning_effort: 'medium', parallel_count: 1 }
  const schedule = SCHEDULES.some((preset) => preset.cron === form.cron_expression) ? form.cron_expression : 'custom'
  editor.value = { id: plan?.id ?? null, groupName: plan?.group_name ?? '', form, schedule }
  void loadGroups()
}

async function savePlan() {
  const current = editor.value
  if (!current || savingPlan.value) return
  const form = current.form
  if (!form.group_id || !form.model_id.trim() || (form.prompt_mode !== 'random' && !form.prompt.trim()) || !form.cron_expression.trim()) {
    fail(null, t('pelicanTests.editor.required'))
    return
  }
  savingPlan.value = true
  try {
    const input = { ...form, parallel_count: Number(form.parallel_count) || 1 }
    if (current.id) {
      await pelicanTestsAPI.updatePlan(current.id, input)
      succeed(t('pelicanTests.editor.updated'))
    } else {
      await pelicanTestsAPI.createPlan(input)
      succeed(t('pelicanTests.editor.created'))
    }
    editor.value = null
    plans.value = await pelicanTestsAPI.listPlans()
  } catch (err: unknown) {
    fail(err, t('pelicanTests.editor.saveFailed'))
  } finally {
    savingPlan.value = false
  }
}

async function toggle(plan: PelicanGroupTestPlan) {
  // A plan carried over without a model needs one before it can run.
  if (!plan.enabled && !plan.model_id) {
    openEditor(plan)
    if (editor.value) editor.value.form.enabled = true
    return
  }
  pending[plan.id] = 'toggle'
  try {
    await pelicanTestsAPI.updatePlan(plan.id, { ...inputFrom(plan), enabled: !plan.enabled })
    plans.value = await pelicanTestsAPI.listPlans()
  } catch (err: unknown) {
    fail(err, t('pelicanTests.plans.actionFailed'))
  } finally {
    delete pending[plan.id]
  }
}

async function runNow(plan: PelicanGroupTestPlan) {
  pending[plan.id] = 'run'
  try {
    await pelicanTestsAPI.runPlan(plan.id)
    succeed(t('pelicanTests.plans.runStarted'))
    plans.value = await pelicanTestsAPI.listPlans()
  } catch (err: unknown) {
    if (extractApiErrorCode(err) === 'PELICAN_GROUP_TEST_PLAN_RUNNING') fail(null, t('pelicanTests.plans.alreadyRunning'))
    else fail(err, t('pelicanTests.plans.actionFailed'))
  } finally {
    delete pending[plan.id]
  }
}

async function removePlan() {
  const plan = deleting.value
  deleting.value = null
  if (!plan) return
  pending[plan.id] = 'delete'
  try {
    await pelicanTestsAPI.deletePlan(plan.id)
    plans.value = plans.value.filter((item) => item.id !== plan.id)
    succeed(t('pelicanTests.plans.deleted'))
    await loadResults()
  } catch (err: unknown) {
    fail(err, t('pelicanTests.plans.actionFailed'))
  } finally {
    delete pending[plan.id]
  }
}

async function openPreview(result: PelicanGroupTestResult) {
  preview.value = { result, html: '', status: 'loading' }
  try {
    const full = await pelicanTestsAPI.getResult(result.id)
    if (preview.value?.result.id !== result.id) return
    const html = extractPelicanHtml(full.response_text || '')
    preview.value = { result: full, html, status: html ? 'ready' : 'invalid' }
  } catch {
    if (preview.value?.result.id === result.id) preview.value = { result, html: '', status: 'error' }
  }
}

onMounted(() => {
  void load()
  timer = setInterval(poll, POLL_MS)
})
onBeforeUnmount(() => {
  alive = false
  resultsController?.abort()
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.pelican-tests { @apply w-full min-w-0 text-gray-900 dark:text-gray-100; }
.ops-heading { @apply mb-6 flex flex-wrap items-center justify-between gap-4; }
.eyebrow { @apply mb-1 text-[11px] font-semibold tracking-widest text-primary-600; }
.ops-heading h2 { @apply text-2xl font-semibold tracking-tight; }
.subtitle { @apply mt-2 max-w-3xl text-sm leading-6 text-gray-500 dark:text-gray-400; }
.ops-columns { display: grid; grid-template-columns: minmax(300px, .32fr) minmax(0, .68fr); gap: 20px; align-items: start; }
.settings-card, .plans-card, .history-card { @apply min-w-0 overflow-hidden rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.history-card { @apply mt-5; }
.section-title { @apply flex items-center gap-3 border-b border-gray-100 p-5 dark:border-dark-700; }
.icon-tile { @apply flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary-50 text-primary-600 dark:bg-primary-950/30; }
.section-title h3, .panel-heading h3 { @apply text-base font-semibold; }
.section-title p, .panel-heading p { @apply mt-1 text-xs leading-relaxed text-gray-400; }
.settings-form { @apply p-5; }
.enable-row { @apply mb-2 flex items-center justify-between gap-4; }
.enable-row strong { @apply block text-sm font-medium; }
.enable-row small { @apply mt-1 block text-xs leading-relaxed text-gray-400; }
.field-label { @apply mb-2 mt-5 block text-xs font-medium text-gray-600 dark:text-gray-300; }
.field-hint { @apply mt-2 text-xs leading-relaxed text-gray-400; }
.unit { @apply pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-sm text-gray-400; }
.settings-actions { @apply mt-5 flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700; }
.settings-actions span { @apply text-xs text-gray-400; }
.panel-heading { @apply border-b border-gray-100 p-5 dark:border-dark-700; }
.panel-heading .count { @apply ml-2 rounded-md bg-gray-100 px-2 py-0.5 text-xs font-normal tabular-nums text-gray-500 dark:bg-dark-800; }
.plan-list { @apply divide-y divide-gray-100 dark:divide-dark-800; }
.plan-row { @apply flex flex-wrap items-start justify-between gap-4 px-5 py-4; }
.plan-main { @apply flex min-w-0 items-start gap-3; flex: 1 1 20rem; }
.group-icon { @apply grid h-9 w-9 shrink-0 place-items-center rounded-xl ring-1 ring-black/5 dark:ring-white/10; }
.plan-title { @apply flex flex-wrap items-center gap-2; }
.plan-title strong { @apply truncate text-sm font-semibold; max-width: 260px; }
.plan-meta { @apply mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-gray-500 dark:text-gray-400; }
.plan-meta code, td code { @apply rounded bg-gray-100 px-1.5 py-0.5 font-mono text-[11px] text-gray-700 dark:bg-dark-800 dark:text-gray-300; }
.plan-warning { @apply mt-2 text-xs text-amber-600 dark:text-amber-400; }
.plan-last { @apply mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-600 dark:text-gray-300; }
.plan-last .truncate { max-width: 340px; }
.plan-next { @apply mt-1 text-xs text-gray-400; }
.state-chip { @apply rounded-full px-2 py-0.5 text-[11px] font-medium; }
.state-on { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300; }
.state-off { @apply bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.state-running { @apply bg-primary-50 text-primary-700 dark:bg-primary-950/30 dark:text-primary-300; }
.result-dot { @apply inline-block h-2 w-2 rounded-full; }
.dot-ok { @apply bg-emerald-500; }
.dot-bad { @apply bg-red-500; }
.plan-actions { @apply ml-auto flex shrink-0 flex-wrap items-center gap-1; }
.plan-actions button { @apply inline-flex items-center gap-1 rounded-lg px-2.5 py-1.5 text-xs text-gray-600 transition-colors hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-800; }
.plan-actions button.danger { @apply text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-950/30; }
.history-scroll { @apply overflow-x-auto; }
table { @apply w-full text-left text-xs; min-width: 760px; }
thead { @apply bg-gray-50/60 text-gray-400 dark:bg-dark-800/50; }
th { @apply whitespace-nowrap px-4 py-3 font-medium; }
td { @apply border-b border-gray-100 px-4 py-3 align-top dark:border-dark-800; }
td strong { @apply block truncate text-[13px] font-medium; max-width: 200px; }
td small { @apply mt-1 block text-[11px]; }
.error-text { @apply truncate text-red-500; max-width: 280px; }
.result-badge { @apply inline-block rounded-md px-2 py-0.5 text-[11px] font-medium; }
.badge-ok { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300; }
.badge-bad { @apply bg-red-50 text-red-600 dark:bg-red-950/30 dark:text-red-300; }
.link-button { @apply text-primary-600 hover:underline dark:text-primary-400; }
.empty-state { @apply flex min-h-48 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-gray-400; }
.scope-note { @apply mt-5 flex items-start gap-2 text-xs leading-6 text-gray-400; }
.scope-note svg { @apply mt-1 shrink-0; }
.error-banner { @apply mb-4 rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; }
.success-banner { @apply mb-4 rounded-xl bg-emerald-50 p-3 text-sm text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300; }
button:disabled { @apply cursor-not-allowed opacity-40; }
@media (max-width: 1100px) { .ops-columns { grid-template-columns: 1fr; } }
</style>
