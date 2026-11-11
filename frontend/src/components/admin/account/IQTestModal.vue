<template>
  <PelicanRecordsDashboard v-if="recordsView" :key="recordsView" :accounts="props.accounts || []" :account="props.account" :manual-record="records[0] || null"
    :account-only="recordsView === 'account'" @show-all="recordsView = 'all'" @close="recordsView = null" />
  <BaseDialog :show="show" :title="t('admin.accounts.pelicanTest.title')" width="full" :fullscreen="viewingScheduled && testMode === 'question'" @close="handleClose">
    <div class="space-y-5">
      <div v-if="account" class="flex flex-col items-start gap-3 rounded-xl border border-amber-200 bg-amber-50/70 p-3 dark:border-amber-800/60 dark:bg-amber-950/20 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex items-center gap-3">
          <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-amber-500 text-white">
            <Icon name="brain" size="md" :stroke-width="2" />
          </div>
          <div>
            <div class="font-semibold text-gray-900 dark:text-gray-100">{{ account.name }}</div>
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ account.platform }} · {{ t('admin.accounts.pelicanTest.subtitle') }}</div>
          </div>
        </div>
        <span v-if="testMode === 'question'" class="whitespace-nowrap rounded-full bg-white px-2.5 py-1 text-xs font-medium text-amber-700 shadow-sm dark:bg-dark-800 dark:text-amber-300">
          {{ t('admin.accounts.pelicanTest.noScoring') }}
        </span>
      </div>

      <div class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-1 dark:border-dark-600 dark:bg-dark-800" role="group">
        <button
          v-for="mode in testModes"
          :key="mode"
          type="button"
          :data-testid="`mode-${mode}`"
          :aria-pressed="testMode === mode"
          :disabled="running"
          class="rounded-md px-4 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          :class="testMode === mode ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300' : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          @click="selectMode(mode)"
        >
          {{ t(mode === 'modeltrace' ? 'modelTrace.tab' : mode === 'probe' ? 'admin.accounts.pelicanTest.modeProbe' : 'admin.accounts.pelicanTest.modeQuestion') }}
        </button>
      </div>

      <div v-if="testMode === 'probe'" class="space-y-4" data-testid="probe-panel">
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.pelicanTest.probe.intro') }}</p>
        <p class="text-xs text-gray-500 dark:text-gray-400" data-testid="probe-schedule-hint">{{ t('admin.accounts.pelicanTest.probe.scheduleHint') }}</p>
        <div v-if="!probeSupported" class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-800/60 dark:bg-amber-950/20 dark:text-amber-200" data-testid="probe-unsupported">
          {{ t('admin.accounts.pelicanTest.probe.unsupported') }}
        </div>
        <div class="max-w-sm">
          <TestModelSelect
            id="probe-model"
            v-model="modelId"
            :label="t('admin.accounts.pelicanTest.probe.model')"
            :options="modelOptions"
            :disabled="running"
            :hint="t('admin.accounts.pelicanTest.probe.modelHint')"
          />
        </div>

        <div v-if="probeError" class="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800/60 dark:bg-red-950/20 dark:text-red-300" data-testid="probe-error">
          {{ t('admin.accounts.pelicanTest.probe.requestFailed') }}：{{ probeError }}
        </div>
        <div v-if="running" class="flex items-center gap-2 rounded-lg border border-primary-200 bg-primary-50/60 px-3 py-3 text-sm text-primary-700 dark:border-primary-800/60 dark:bg-primary-900/20 dark:text-primary-300">
          <Icon name="refresh" size="sm" class="animate-spin" />
          {{ t('admin.accounts.pelicanTest.probe.running') }}
        </div>
        <div v-else-if="probeResults.length === 0 && !probeError" class="rounded-lg border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
          {{ t('admin.accounts.pelicanTest.probe.empty') }}
        </div>

        <article
          v-for="(result, index) in probeResults"
          :key="`${result.started_at}-${index}`"
          data-testid="probe-result"
          class="rounded-xl border bg-white p-4 dark:bg-dark-800"
          :class="index === 0 ? verdictStyle(result.verdict).border : 'border-gray-200 dark:border-dark-600'"
        >
          <header class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex flex-wrap items-center gap-2">
              <span data-testid="probe-verdict" class="rounded-full px-3 py-1 text-sm font-semibold" :class="verdictStyle(result.verdict).badge">
                {{ verdictLabel(result.verdict) }}
              </span>
              <span v-if="result.failure" data-testid="probe-failure" class="text-xs text-gray-500 dark:text-gray-400">{{ failureLabel(result.failure) }}</span>
            </div>
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.pelicanTest.probe.finishedAt') }}：{{ formatDate(result.finished_at) }} · {{ t('admin.accounts.pelicanTest.probe.latency') }} {{ (result.latency_ms / 1000).toFixed(1) }} s
            </span>
          </header>
          <p class="mt-3 text-sm text-gray-800 dark:text-gray-100" data-testid="probe-reason">{{ result.reason }}</p>
          <pre v-if="result.detail" class="mt-2 max-h-32 overflow-auto whitespace-pre-wrap break-words rounded-md bg-gray-50 p-2 text-xs text-gray-600 dark:bg-dark-900 dark:text-gray-300" data-testid="probe-detail">{{ t('admin.accounts.pelicanTest.probe.detail') }}：{{ result.detail }}</pre>
          <dl class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-3 lg:grid-cols-6">
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.probe.mintStatus') }}</dt>
              <dd class="font-medium text-gray-800 dark:text-gray-100">{{ httpStatusLabel(result.mint_status) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.probe.continueStatus') }}</dt>
              <dd class="font-medium text-gray-800 dark:text-gray-100">{{ httpStatusLabel(result.continue_status) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.probe.ticketLength') }}</dt>
              <dd class="font-medium text-gray-800 dark:text-gray-100">{{ ticketLengthLabel(result) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.probe.newTicket') }}</dt>
              <dd class="font-medium text-gray-800 dark:text-gray-100" data-testid="probe-new-ticket">{{ newTicketLabel(result) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.probe.model') }}</dt>
              <dd class="break-all font-medium text-gray-800 dark:text-gray-100">{{ result.model || '—' }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.pelicanTest.probe.reportedModel') }}</dt>
              <dd class="break-all font-medium text-gray-800 dark:text-gray-100">{{ result.reported_model || '—' }}</dd>
            </div>
          </dl>
        </article>

        <div class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-800/70 dark:text-gray-300">
          <div class="flex items-start gap-2">
            <Icon name="shield" size="sm" class="mt-0.5 shrink-0 text-emerald-500" />
            <span>{{ t('admin.accounts.pelicanTest.probe.limitation') }}</span>
          </div>
        </div>
      </div>

      <template v-else-if="testMode === 'modeltrace'">
        <ModelTracePanel v-if="show && account" :key="account.id" v-model:model-id="modelId" :account="account"
          :model-options="modelOptions" :reasoning-levels="reasoningLevels" @running="running = $event" />
      </template>
      <template v-else>
      <div>
        <label class="input-label mb-1.5 block">{{ t('admin.accounts.pelicanTest.question') }}</label>
        <Select data-testid="question-select" :model-value="questionKind" :options="questionOptions" :disabled="running" @update:model-value="selectQuestion" />
        <p v-if="questionKind === 'candy'" class="mt-2 text-xs text-gray-500">{{ t('admin.accounts.pelicanTest.candyHint') }}</p>
      </div>
      <template v-if="questionKind === 'pelican'">
        <PelicanPromptMode :model-value="promptMode" @update:model-value="selectPromptMode" :disabled="running || preparingQuestion" />
        <template v-if="promptMode === 'random'">
          <button type="button" class="btn btn-secondary" data-testid="pelican-next-round" :disabled="running || preparingQuestion" @click="prepareQuestion">{{ t('pelicanTests.questionBank.next') }}</button>
          <p v-if="questionError" role="alert">{{ questionError }}</p>
          <PelicanQuestionSnapshot v-if="questionSnapshot" :question="questionSnapshot.pelican_question" :actual-prompt="questionSnapshot.actual_prompt" />
        </template>
      </template>
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_280px]">
        <TextArea
          v-if="promptMode !== 'random'"
          v-model="prompt"
          :label="t('admin.accounts.pelicanTest.promptLabel')"
          :disabled="running"
          :rows="5"
          :hint="t('admin.accounts.pelicanTest.promptHint')"
        />
        <div class="space-y-3">
          <TestModelSelect
            id="question-model"
            v-model="modelId"
            :label="t('admin.accounts.pelicanTest.model')"
            :options="modelOptions"
            :disabled="running"
            :hint="t('admin.accounts.pelicanTest.modelHint', { model: defaultModel })"
          />
          <div>
            <label class="input-label mb-1.5 block">{{ t('admin.accounts.pelicanTest.reasoning') }}</label>
            <Select v-model="reasoningEffort" :options="reasoningOptions" :disabled="running" />
          </div>
          <Input
            v-model="parallelCount"
            type="number"
            :label="t('admin.accounts.pelicanTest.parallel')"
            :disabled="running"
            :hint="t('admin.accounts.pelicanTest.parallelHint')"
          />
        </div>
      </div>

      <div class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-800/70 dark:text-gray-300">
        <div class="flex items-start gap-2">
          <Icon name="shield" size="sm" class="mt-0.5 shrink-0 text-emerald-500" />
          <span>{{ deliveryContract }}</span>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-200 pb-2 dark:border-dark-600">
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors"
            :class="activeTab === 'results' ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300' : 'text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700'"
            @click="openManualResults"
          >
            {{ viewingScheduled ? t('admin.accounts.pelicanTest.scheduledPreview') : t('admin.accounts.pelicanTest.results') }}
          </button>
          <button
            type="button"
            class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors"
            :class="activeTab === 'history' ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300' : 'text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700'"
            data-testid="open-records"
            @click="recordsView = 'account'"
          >
            {{ t('admin.accounts.pelicanTest.history') }}<span v-if="records.length" class="ml-1">({{ records.length }})</span>
          </button>
          <button type="button" class="rounded-md px-3 py-1.5 text-sm font-medium"
            :class="activeTab === 'schedule' ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300' : 'text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700'"
            @click="activeTab = 'schedule'">{{ t('admin.accounts.pelicanTest.schedule') }}</button>
        </div>
        <span v-if="running" class="flex items-center gap-1.5 text-xs text-primary-600 dark:text-primary-300">
          <Icon name="refresh" size="sm" class="animate-spin" />
          {{ t('admin.accounts.pelicanTest.running', { count: runs.length }) }}
        </span>
      </div>

      <ScheduledTestsPanel v-if="show && account && activeTab === 'schedule'" :key="account.id" :show="true" embedded
        :account-id="account.id" :default-model="modelId" :model-options="[{ value: modelId, label: modelId }]"
        :pelican-config="{ question_kind: questionKind, prompt_mode: promptMode, prompt, reasoning_effort: reasoningEffort, parallel_count: Number(parallelCount) }"
        :disabled="running" @preview="previewScheduled" @history="scheduledRecords = $event" />
      <div v-else-if="activeTab === 'history'" class="space-y-2">
        <button v-for="result in scheduledRecords" :key="`scheduled-${result.id}`" type="button"
          class="flex w-full items-center justify-between rounded-lg border border-gray-200 px-3 py-2 text-left transition-colors hover:border-primary-300 hover:bg-primary-50/50 dark:border-dark-600 dark:hover:border-primary-700 dark:hover:bg-primary-900/10"
          @click="previewScheduled(result)">
          <span class="min-w-0">
            <span class="block truncate text-sm font-medium text-gray-800 dark:text-gray-100">
              {{ t('admin.accounts.pelicanTest.sourceScheduled') }} · {{ result.pelican_config?.model_id || modelId }} / {{ result.pelican_config?.reasoning_effort || reasoningEffort }}
            </span>
            <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">
              {{ formatDate(result.started_at) }} · {{ t('admin.accounts.pelicanTest.duration') }} {{ (result.latency_ms / 1000).toFixed(1) }} s
            </span>
          </span>
          <span class="text-xs" :class="scheduledStatus(result).class">{{ scheduledStatus(result).label }}</span>
        </button>
        <div v-for="record in records" :key="record.id" class="flex w-full items-center justify-between rounded-lg border border-gray-200 px-3 py-2 text-left dark:border-dark-600">
          <button type="button" class="min-w-0 text-left" @click="loadRecord(record)">
            <span class="block truncate text-sm font-medium text-gray-800 dark:text-gray-100">{{ t('admin.accounts.pelicanTest.sourceManual') }} · {{ record.modelId }} / {{ record.reasoningEffort }}</span>
            <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">{{ formatDate(record.createdAt) }} · {{ record.runs.length }} {{ t('admin.accounts.pelicanTest.outputs') }}</span>
          </button>
        </div>
        <div v-if="scheduledRecords.length === 0 && records.length === 0" class="rounded-lg border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
          {{ t('admin.accounts.pelicanTest.noHistory') }}
        </div>
      </div>

      <div v-else>
        <div v-if="runs.length === 0" class="rounded-lg border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
          {{ t('admin.accounts.pelicanTest.emptyResults') }}
        </div>
        <div v-else class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <article v-for="(run, index) in runs" :key="run.id" class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800">
            <header class="flex items-center justify-between gap-2 border-b border-gray-200 px-3 py-2 dark:border-dark-600">
              <div class="flex items-center gap-2">
                <span class="flex h-6 w-6 items-center justify-center rounded-full bg-primary-100 text-xs font-semibold text-primary-700 dark:bg-primary-900/50 dark:text-primary-300">{{ index + 1 }}</span>
                <span class="text-sm font-medium text-gray-800 dark:text-gray-100">{{ t('admin.accounts.pelicanTest.output') }} {{ index + 1 }}</span>
              </div>
              <div class="flex items-center gap-1">
                <span v-if="run.verdict" data-testid="run-verdict" class="rounded-full px-2 py-0.5 text-xs font-medium" :class="verdictStyle(run.verdict).badge">{{ verdictLabel(run.verdict) }}</span>
                <span v-else-if="run.status === 'running'" class="text-xs text-amber-600 dark:text-amber-300">{{ t('admin.accounts.pelicanTest.runningShort') }}</span>
                <span v-else-if="run.status === 'success'" class="text-xs text-emerald-600 dark:text-emerald-300">{{ t('admin.accounts.pelicanTest.success') }}</span>
                <span v-else class="text-xs text-red-600 dark:text-red-300">{{ t('admin.accounts.pelicanTest.failed') }}</span>
                <button v-if="run.output" type="button" class="rounded-md p-1.5 text-gray-500 hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-300" :title="t('admin.accounts.pelicanTest.download')" @click="downloadHtml(run)">
                  <Icon name="download" size="sm" />
                </button>
              </div>
            </header>
            <div class="space-y-1 px-3 py-2 text-xs text-gray-500 dark:text-gray-400" data-testid="run-metadata">
              <div>{{ t(run.source === 'scheduled' ? 'admin.accounts.pelicanTest.sourceScheduled' : 'admin.accounts.pelicanTest.sourceManual') }} · {{ run.modelId || '—' }} / {{ run.reasoningEffort || '—' }}</div>
              <div>{{ t('admin.accounts.pelicanTest.generatedAt') }}：{{ run.startedAt ? formatDate(run.startedAt) : '—' }}</div>
              <div>{{ t('admin.accounts.pelicanTest.duration') }}：{{ run.durationMs == null ? '—' : `${(run.durationMs / 1000).toFixed(1)} s` }}</div>
            </div>
            <div v-if="run.html" class="aspect-[4/3] bg-white dark:bg-white">
              <iframe :srcdoc="run.html" class="h-full w-full border-0" sandbox="allow-scripts" referrerpolicy="no-referrer" :title="`${t('admin.accounts.pelicanTest.output')} ${index + 1}`"></iframe>
            </div>
            <pre class="max-h-48 overflow-auto whitespace-pre-wrap break-words border-t border-gray-200 bg-gray-950 p-3 text-xs leading-relaxed text-gray-200 dark:border-dark-600">{{ run.output || run.error || t('admin.accounts.pelicanTest.waiting') }}</pre>
          </article>
        </div>
      </div>
      </template>
    </div>

    <template #footer>
      <div class="flex w-full items-center justify-between gap-3">
        <button v-if="testMode === 'question'" type="button" class="btn btn-secondary" :disabled="running || !hasDownloadable" @click="downloadAll">
          <Icon name="download" size="sm" />
          {{ t('admin.accounts.pelicanTest.downloadAll') }}
        </button>
        <span v-else />
        <div class="flex gap-3">
          <button type="button" class="btn btn-secondary" :disabled="running" @click="handleClose">{{ t('common.close') }}</button>
          <button v-if="testMode === 'probe'" type="button" data-testid="probe-start" class="btn btn-primary flex items-center gap-2" :disabled="running || !canStartProbe" @click="startProbe">
            <Icon v-if="running" name="refresh" size="sm" class="animate-spin" />
            <Icon v-else name="play" size="sm" />
            {{ running ? t('admin.accounts.pelicanTest.probe.probing') : t('admin.accounts.pelicanTest.probe.start') }}
          </button>
          <button v-else-if="testMode === 'question' && activeTab !== 'schedule'" type="button" class="btn btn-primary flex items-center gap-2" :disabled="running || !canStart" @click="startTest">
            <Icon v-if="running" name="refresh" size="sm" class="animate-spin" />
            <Icon v-else name="play" size="sm" />
            {{ running ? t('admin.accounts.pelicanTest.generating') : t('admin.accounts.pelicanTest.start') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { questionPrompt, questionContract, isTextAnswerKind, stateProbeVerdict, STATE_PROBE_QUESTION, type IntelligenceQuestion, type StateProbeVerdict } from '@/utils/intelligenceTest'
import { useI18n } from 'vue-i18n'
import { extractPelicanHtml as extractHtml } from '@/utils/pelicanHtml'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Input from '@/components/common/Input.vue'
import TextArea from '@/components/common/TextArea.vue'
import Select from '@/components/common/Select.vue'
import { Icon } from '@/components/icons'
import PelicanPromptMode from './PelicanPromptMode.vue'
import PelicanQuestionSnapshot from './PelicanQuestionSnapshot.vue'
import { useAuthStore } from '@/stores/auth'
import { currentPelicanQuestionRound, preparePelicanQuestionRound } from '@/utils/pelicanQuestionRound'
import { buildApiUrl } from '@/api/client'
import { ADMIN_UI_REQUEST_HEADER } from '@/api/adminUIRequest'
import { getAvailableModels, getModelReasoning, probeOpenAICodexState, type OpenAICodexStateProbeResult, type OpenAICodexStateVerdict } from '@/api/admin/accounts'
import type { Account, AccountListItem, PelicanQuestion, PelicanTestConfig, ScheduledTestResult } from '@/types'
import ScheduledTestsPanel from './ScheduledTestsPanel.vue'
import PelicanRecordsDashboard from './PelicanRecordsDashboard.vue'
import TestModelSelect from './TestModelSelect.vue'
import ModelTracePanel from './ModelTracePanel.vue'

const { t } = useI18n()
const auth = useAuthStore()

const STORAGE_PREFIX = 'sub2api-pelican-test:'
const PROBE_CONTROLLER_KEY = 'state-probe'
const PROBE_RESULT_LIMIT = 5
const PROBE_FAILURE_KINDS = new Set(['unsupported', 'account_error', 'rate_limited', 'model_unsupported', 'upstream_error', 'network_error', 'stream_error', 'no_ticket', 'cancelled'])

type TestMode = 'question' | 'probe' | 'modeltrace'
const testModes: TestMode[] = ['question', 'probe', 'modeltrace']

type RunStatus = 'running' | 'success' | 'error'
interface TestRun {
  questionKind?: PelicanTestConfig['question_kind']
  // 定时探针结果的判定；手动题目测试没有。
  verdict?: StateProbeVerdict
  id: string
  status: RunStatus
  output: string
  html: string
  error: string
  source?: 'manual' | 'scheduled'
  startedAt?: string
  finishedAt?: string
  durationMs?: number
  modelId?: string
  reasoningEffort?: string
}
interface TestRecord {
  pelicanQuestion?: PelicanQuestion
  actualPrompt?: string
  questionKind?: IntelligenceQuestion
  id: string
  createdAt: string
  prompt: string
  modelId: string
  reasoningEffort: string
  runs: TestRun[]
}

const props = defineProps<{ show: boolean; account: Account | null; accounts?: AccountListItem[] }>()
const emit = defineEmits<{ (event: 'close'): void }>()

const questionKind = ref<IntelligenceQuestion>('candy')
const prompt = ref(questionPrompt('candy'))
const promptMode = ref<'custom' | 'random'>('custom')
const questionSnapshot = ref<PelicanTestConfig | null>(null)
const questionError = ref('')
const preparingQuestion = ref(false)
async function prepareQuestion() {
  if (running.value || preparingQuestion.value) return
  preparingQuestion.value = true
  questionError.value = ''
  const controller = new AbortController()
  controllers.set('question', controller)
  try {
    questionSnapshot.value = await preparePelicanQuestionRound(auth.user?.id ?? null, controller.signal)
  } catch {
    if (!controller.signal.aborted) questionError.value = t('pelicanTests.questionBank.error')
  } finally {
    preparingQuestion.value = false
    controllers.delete('question')
  }
}
function selectPromptMode(mode: 'custom' | 'random') {
  promptMode.value = mode
  if (mode !== 'random') return
  questionSnapshot.value = currentPelicanQuestionRound(auth.user?.id ?? null)
  if (!questionSnapshot.value) void prepareQuestion()
}
// Claude accounts cannot serve the OpenAI default.
const defaultModel = computed(() => props.account?.platform === 'anthropic' ? 'claude-opus-5-5' : 'gpt-6-astra')
const modelId = ref(defaultModel.value)
const reasoningEffort = ref('medium')
const reasoningLevels = ref<string[]>(['low', 'medium', 'high'])
const availableModels = ref<Array<{ id: string; display_name?: string }>>([])
const modelOptions = computed(() => {
  const models = new Map(availableModels.value.map((model) => [model.id, model.display_name?.trim()]))
  // A diagnostic test must also allow explicitly configured public model names,
  // even when the upstream discovery catalog does not advertise their targets.
  const mapping = props.account?.credentials?.model_mapping
  if (mapping && typeof mapping === 'object' && !Array.isArray(mapping)) {
    for (const id of Object.keys(mapping)) {
      if (id.trim() && !id.includes('*') && !models.has(id)) models.set(id, undefined)
    }
  }
  return Array.from(models, ([id, name]) => ({ value: id, label: name && name !== id ? `${id} (${name})` : id }))
})
const parallelCount = ref<string | number>(1)
const activeTab = ref<'results' | 'history' | 'schedule'>('results')
const running = ref(false)
const viewingScheduled = ref(false)
// Test records open as this account's window over the dialog; every account's page is one step further.
const recordsView = ref<'account' | 'all' | null>(null)
const runs = ref<TestRun[]>([])
const records = ref<TestRecord[]>([])
const scheduledRecords = ref<ScheduledTestResult[]>([])
const controllers = new Map<string, AbortController>()
const testMode = ref<TestMode>('question')
const probeResults = ref<OpenAICodexStateProbeResult[]>([])
const probeError = ref('')

const deliveryContract = computed(() => questionContract(questionKind.value))
const questionOptions = computed(() => ['candy', 'pelican'].map(value => ({ value, label: t(`admin.accounts.pelicanTest.${value}Question`) })))
function selectQuestion(value: string | number | boolean | null) {
  if (running.value || (value !== 'candy' && value !== 'pelican')) return
  questionKind.value = value
  promptMode.value = 'custom'
  prompt.value = questionPrompt(value)
}
const reasoningOptions = computed(() => reasoningLevels.value.map((value) => ({
  value,
  label: ({ low: t('admin.accounts.pelicanTest.reasoningLow'), medium: t('admin.accounts.pelicanTest.reasoningMedium'), high: t('admin.accounts.pelicanTest.reasoningHigh'), none: t('admin.accounts.pelicanTest.reasoningNone'), minimal: t('admin.accounts.pelicanTest.reasoningMinimal'), xhigh: t('admin.accounts.pelicanTest.reasoningXHigh'), max: t('admin.accounts.pelicanTest.reasoningMax'), ultra: t('admin.accounts.pelicanTest.reasoningUltra') } as Record<string, string>)[value] || value
})))
const canStart = computed(() => Boolean(props.account && !preparingQuestion.value && (promptMode.value === 'random' ? (questionSnapshot.value?.actual_prompt && questionSnapshot.value.pelican_question?.round_id.startsWith('manual:')) : prompt.value.trim()) && modelId.value.trim() && normalizeCount() > 0))
const hasDownloadable = computed(() => runs.value.some((run) => Boolean(run.output)))
const probeSupported = computed(() => props.account?.platform === 'openai' && (props.account.type === 'oauth' || props.account.type === 'setup-token'))
const canStartProbe = computed(() => Boolean(props.account && probeSupported.value))

let modelLoadToken = 0
async function loadModels() {
  if (!props.account) return
  const token = ++modelLoadToken
  const accountId = props.account.id
  const initialModel = modelId.value
  availableModels.value = []
  try {
    const models = await getAvailableModels(accountId)
    if (token !== modelLoadToken || props.account?.id !== accountId || !props.show) return
    availableModels.value = models.map((model: any) => ({ id: model.id, display_name: model.display_name }))
    if (modelId.value === initialModel && !modelOptions.value.some((model) => model.value === modelId.value) && modelOptions.value.length > 0) {
      modelId.value = modelOptions.value[0].value
    }
  } catch {
    if (token === modelLoadToken && props.account?.id === accountId) availableModels.value = []
  }
}
let reasoningLoadToken = 0
async function loadReasoning() {
  if (!props.account || !modelId.value.trim()) return
  const token = ++reasoningLoadToken
  try {
    const result = await getModelReasoning(props.account.id, modelId.value.trim())
    if (token !== reasoningLoadToken) return
    reasoningLevels.value = result.supported_reasoning_levels.length > 0 ? result.supported_reasoning_levels : ['none']
    reasoningEffort.value = reasoningLevels.value.includes(result.default_reasoning_level) ? result.default_reasoning_level : reasoningLevels.value[0]
  } catch {
    reasoningLevels.value = ['low', 'medium', 'high']
    if (!reasoningLevels.value.includes(reasoningEffort.value)) reasoningEffort.value = 'medium'
  }
}

function selectMode(mode: TestMode) {
  if (running.value) return
  testMode.value = mode
}

function verdictLabel(verdict: OpenAICodexStateVerdict) {
  if (verdict === 'healthy') return t('admin.accounts.pelicanTest.probe.verdictHealthy')
  if (verdict === 'degraded') return t('admin.accounts.pelicanTest.probe.verdictDegraded')
  return t('admin.accounts.pelicanTest.probe.verdictInconclusive')
}

function verdictStyle(verdict: OpenAICodexStateVerdict) {
  if (verdict === 'healthy') return { badge: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300', border: 'border-emerald-300 dark:border-emerald-700' }
  if (verdict === 'degraded') return { badge: 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300', border: 'border-red-300 dark:border-red-700' }
  return { badge: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300', border: 'border-gray-300 dark:border-dark-500' }
}

function scheduledStatus(result: ScheduledTestResult) {
  const verdict = stateProbeVerdict(result)
  if (verdict === 'healthy') return { label: verdictLabel(verdict), class: 'text-emerald-600' }
  if (verdict === 'degraded') return { label: verdictLabel(verdict), class: 'text-red-500' }
  if (verdict) return { label: verdictLabel(verdict), class: 'text-gray-500' }
  return result.status === 'success'
    ? { label: t('admin.accounts.pelicanTest.success'), class: 'text-emerald-600' }
    : { label: t('admin.accounts.pelicanTest.failed'), class: 'text-red-500' }
}

function failureLabel(failure: string) {
  return PROBE_FAILURE_KINDS.has(failure) ? t(`admin.accounts.pelicanTest.probe.failures.${failure}`) : failure
}

function httpStatusLabel(status: number) {
  return status > 0 ? `HTTP ${status}` : '—'
}

function ticketLengthLabel(result: OpenAICodexStateProbeResult) {
  if (!result.ticket_length) return '—'
  return result.continue_ticket_length ? `${result.ticket_length} → ${result.continue_ticket_length}` : `${result.ticket_length}`
}

function newTicketLabel(result: OpenAICodexStateProbeResult) {
  if (result.verdict === 'inconclusive') return '—'
  return t(result.new_ticket ? 'admin.accounts.pelicanTest.probe.yes' : 'admin.accounts.pelicanTest.probe.no')
}

const storageKey = computed(() => `${STORAGE_PREFIX}${props.account?.id ?? 'unknown'}`)

function normalizeCount(): number {
  const value = Number(parallelCount.value)
  if (!Number.isFinite(value)) return 1
  return Math.min(8, Math.max(1, Math.floor(value)))
}

function readRecords() {
  try {
    const parsed = JSON.parse(localStorage.getItem(storageKey.value) || '[]')
    records.value = Array.isArray(parsed) ? parsed : []
  } catch {
    records.value = []
  }
}

function saveRecords() {
  try {
    localStorage.setItem(storageKey.value, JSON.stringify(records.value.slice(0, 8)))
  } catch {
    // A large model response must not prevent the current result from being shown.
  }
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value))
}


function editSchedule(config: PelicanTestConfig, model: string) {
  if (running.value) return
  modelId.value = model
  // Server-generated questions belong to quality rules, not manual test inputs.
  if (config.question_kind === STATE_PROBE_QUESTION || config.question_kind === 'random_reasoning') return
  questionKind.value = config.question_kind || 'pelican'
  prompt.value = config.prompt
  promptMode.value = config.prompt_mode || 'custom'
  questionSnapshot.value = config.pelican_question ? JSON.parse(JSON.stringify(config)) : null
  modelId.value = model
  reasoningEffort.value = config.reasoning_effort
  parallelCount.value = config.parallel_count
}

function openManualResults() {
  if (running.value) return
  viewingScheduled.value = false
  runs.value = []
  activeTab.value = 'results'
}

function previewScheduled(result: ScheduledTestResult) {
  if (running.value) return
  const config = result.pelican_config
  if (config) editSchedule(config, config.model_id || modelId.value)
  const html = isTextAnswerKind(config?.question_kind) ? '' : extractHtml(result.response_text)
  runs.value = [{ id: `scheduled-${result.id}`, questionKind: config?.question_kind || 'pelican', verdict: stateProbeVerdict(result) ?? undefined, status: result.status === 'success' ? 'success' : 'error', output: result.response_text, html, error: result.error_message,
    source: 'scheduled', startedAt: result.started_at, finishedAt: result.finished_at,
    durationMs: result.latency_ms, modelId: config?.model_id, reasoningEffort: config?.reasoning_effort
  }]
  viewingScheduled.value = true
  activeTab.value = 'results'
}

function loadRecord(record: TestRecord) {
  if (running.value) return
  questionKind.value = record.questionKind || 'pelican'
  prompt.value = record.prompt
  promptMode.value = record.pelicanQuestion ? 'random' : 'custom'
  questionSnapshot.value = record.pelicanQuestion ? { prompt: record.prompt, reasoning_effort: record.reasoningEffort, parallel_count: record.runs.length, pelican_question: record.pelicanQuestion, actual_prompt: record.actualPrompt } : null
  modelId.value = record.modelId
  reasoningEffort.value = record.reasoningEffort || 'medium'
  runs.value = record.runs.map((run) => ({ ...run, questionKind: run.questionKind || record.questionKind || 'pelican', modelId: run.modelId || record.modelId, reasoningEffort: run.reasoningEffort || record.reasoningEffort }))
  viewingScheduled.value = false
  activeTab.value = 'results'
}

function handleClose() {
  for (const controller of controllers.values()) controller.abort()
  controllers.clear()
  running.value = false
  emit('close')
}

interface ManualRound {
  accountId: number
  questionKind: IntelligenceQuestion
  modelId: string
  reasoningEffort: string
  prompt: string
  actualPrompt: string
  question?: PelicanQuestion
}
async function consumeRun(run: TestRun, signal: AbortSignal, round: ManualRound) {
  const response = await fetch(buildApiUrl(`/admin/accounts/${round.accountId}/pelican-test`), {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${localStorage.getItem('auth_token')}`,
      'Content-Type': 'application/json',
      [ADMIN_UI_REQUEST_HEADER]: '1'
    },
    body: JSON.stringify({
      model_id: round.modelId,
      prompt: round.actualPrompt,
      prompt_mode: round.question ? 'random' : 'custom',
      pelican_question: round.question,
      mode: 'default',
      reasoning_effort: round.reasoningEffort
    }),
    signal
  })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  const reader = response.body?.getReader()
  if (!reader) throw new Error(t('admin.accounts.pelicanTest.noResponseBody'))
  const decoder = new TextDecoder()
  let buffer = ''
  let completed = false
  const consumeLine = (line: string) => {
    if (!line.startsWith('data:')) return
    const json = line.replace(/^data:\s*/, '').trim()
    if (!json) return
    let event: { type?: string; text?: string; success?: boolean; error?: string }
    try {
      event = JSON.parse(json) as { type?: string; text?: string; success?: boolean; error?: string }
    } catch {
      return
    }
    if (event.type === 'content' && event.text) run.output += event.text
    if (event.type === 'test_complete') {
      completed = true
      if (!event.success) throw new Error(event.error || t('admin.accounts.pelicanTest.failed'))
    }
    if (event.type === 'error') throw new Error(event.error || t('admin.accounts.pelicanTest.failed'))
  }
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() || ''
    for (const line of lines) consumeLine(line.trim())
  }
  if (buffer.trim()) consumeLine(buffer.trim())
  if (!completed || !run.output.trim()) throw new Error(t('admin.accounts.pelicanTest.emptyResponse'))
  run.html = run.questionKind === 'candy' ? '' : extractHtml(run.output)
  if (run.questionKind !== 'candy' && !run.html) throw new Error(t('admin.accounts.pelicanTest.invalidHtml'))
  run.status = 'success'
}

async function startOne(run: TestRun, round: ManualRound) {
  const started = performance.now()
  run.startedAt = new Date().toISOString()
  const controller = new AbortController()
  controllers.set(run.id, controller)
  try {
    await consumeRun(run, controller.signal, round)
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return
    run.status = 'error'
    run.error = error instanceof Error ? error.message : t('admin.accounts.pelicanTest.failed')
  } finally {
    run.durationMs = Math.max(0, Math.round(performance.now() - started))
    run.finishedAt = new Date().toISOString()
    controllers.delete(run.id)
  }
}

async function startTest() {
  if (running.value || !props.account || !canStart.value) return
  viewingScheduled.value = false
  const question = promptMode.value === 'random' ? questionSnapshot.value?.pelican_question : undefined
  const round: ManualRound = {
    accountId: props.account.id, questionKind: questionKind.value,
    modelId: modelId.value.trim(), reasoningEffort: reasoningEffort.value,
    prompt: question?.prompt || prompt.value.trim(),
    actualPrompt: question ? questionSnapshot.value!.actual_prompt! : `${prompt.value.trim()}\n\n${deliveryContract.value}`,
    question: question ? JSON.parse(JSON.stringify(question)) : undefined,
  }
  const count = normalizeCount()
  parallelCount.value = count
  runs.value = Array.from({ length: count }, (_, index) => ({
    id: `${Date.now()}-${index}`,
    questionKind: questionKind.value,
    status: 'running',
    output: '',
    html: '',
    error: '',
    source: 'manual',
    modelId: modelId.value.trim(),
    reasoningEffort: reasoningEffort.value
  }))
  activeTab.value = 'results'
  running.value = true
  await Promise.all(runs.value.map((run) => startOne(run, round)))
  running.value = false
  if (props.account?.id !== round.accountId || !props.show) return
  const record: TestRecord = {
    id: `${Date.now()}`,
    createdAt: new Date().toISOString(),
    questionKind: round.questionKind,
    prompt: round.prompt,
    actualPrompt: round.actualPrompt,
    pelicanQuestion: round.question,
    modelId: round.modelId,
    reasoningEffort: round.reasoningEffort,
    runs: runs.value.map((run) => ({ ...run }))
  }
  records.value = [record, ...records.value.filter((item) => item.id !== record.id)]
  saveRecords()
}

async function startProbe() {
  if (running.value || !props.account || !canStartProbe.value) return
  const accountId = props.account.id
  const controller = new AbortController()
  controllers.set(PROBE_CONTROLLER_KEY, controller)
  probeError.value = ''
  running.value = true
  try {
    const result = await probeOpenAICodexState(accountId, modelId.value, { signal: controller.signal })
    if (controller.signal.aborted || props.account?.id !== accountId) return
    probeResults.value = [result, ...probeResults.value].slice(0, PROBE_RESULT_LIMIT)
  } catch (error) {
    if (controller.signal.aborted) return
    const message = (error as { message?: unknown } | null)?.message
    probeError.value = typeof message === 'string' && message ? message : t('admin.accounts.pelicanTest.failed')
  } finally {
    if (controllers.get(PROBE_CONTROLLER_KEY) === controller) controllers.delete(PROBE_CONTROLLER_KEY)
    if (!controllers.has(PROBE_CONTROLLER_KEY)) running.value = false
  }
}

function downloadHtml(run: TestRun) {
  const text = isTextAnswerKind(run.questionKind)
  const content = text ? run.output : run.html || extractHtml(run.output)
  if (!content) return
  const url = URL.createObjectURL(new Blob([content], { type: text ? 'text/plain;charset=utf-8' : 'text/html;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = `intelligence-test-${new Date().toISOString().replace(/[:.]/g, '-')}.${text ? 'txt' : 'html'}`
  link.click()
  URL.revokeObjectURL(url)
}

function downloadAll() {
  runs.value.filter((run) => run.output).forEach((run) => downloadHtml(run))
}

onBeforeUnmount(() => { for (const controller of controllers.values()) controller.abort() })

watch(modelId, () => { if (props.show) loadReasoning() })

watch(() => [props.show, props.account?.id] as const, ([show]) => {
  // The records window never outlives the dialog or carries over to another account.
  recordsView.value = null
  if (show) {
    readRecords()
    activeTab.value = 'results'
    viewingScheduled.value = false
    scheduledRecords.value = []
    questionKind.value = 'candy'
    promptMode.value = 'custom'
    questionSnapshot.value = null
    questionError.value = ''
    prompt.value = questionPrompt('candy')
    modelId.value = defaultModel.value
    reasoningEffort.value = 'medium'
    parallelCount.value = 1
    runs.value = []
    controllers.get(PROBE_CONTROLLER_KEY)?.abort()
    testMode.value = 'question'
    probeResults.value = []
    probeError.value = ''
    void loadModels()
    void loadReasoning()
  } else {
    for (const controller of controllers.values()) controller.abort()
    controllers.clear()
  }
}, { immediate: true })
</script>
