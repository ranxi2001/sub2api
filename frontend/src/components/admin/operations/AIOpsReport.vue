<template>
  <div :key="run.id" class="space-y-5" data-testid="ai-ops-report">
    <template v-if="run.result">
      <section class="card space-y-3 p-5">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <h2 class="font-semibold text-gray-900 dark:text-gray-100">{{ t('aiOps.summary') }}</h2>
          <time class="text-xs text-gray-500" :datetime="run.finished_at || run.created_at">{{ formatTime(run.finished_at || run.created_at) }}</time>
        </div>
        <p class="whitespace-pre-wrap break-words text-sm leading-7" data-testid="ai-ops-summary">{{ run.result.summary }}</p>
      </section>

      <aside
        v-if="run.result.limitations.length || incompleteEvidenceCount || !run.evidence.length"
        class="space-y-2 rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-900/15 dark:text-amber-200"
        data-testid="ai-ops-report-limitations"
      >
        <p v-if="run.result.limitations.length" class="font-medium">{{ t('aiOps.limitationsCount', { count: run.result.limitations.length }) }}</p>
        <p v-if="incompleteEvidenceCount" class="font-medium" data-testid="ai-ops-incomplete-evidence">{{ t('aiOps.evidenceIncomplete', { count: incompleteEvidenceCount }) }}</p>
        <p v-if="!run.evidence.length" class="font-medium">{{ t('aiOps.evidenceEmpty') }}</p>
        <p class="leading-6">{{ t('aiOps.limitationsHint') }}</p>
        <details v-if="run.result.limitations.length" data-testid="ai-ops-limitations-details">
          <summary class="cursor-pointer font-medium">{{ t('aiOps.limitations') }}</summary>
          <ul class="mt-3 list-disc space-y-2 pl-5 leading-6">
            <li v-for="(limitation, index) in run.result.limitations" :key="index" class="whitespace-pre-wrap break-words">{{ limitation }}</li>
          </ul>
        </details>
      </aside>

      <section class="card space-y-4 p-5">
        <h2 class="font-semibold text-gray-900 dark:text-gray-100">{{ t('aiOps.proposals') }}</h2>
        <p v-if="!run.result.proposals.length" class="text-sm text-gray-500">{{ t('aiOps.noProposals') }}</p>
        <article
          v-for="(proposal, index) in run.result.proposals"
          :key="index"
          class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700"
          data-testid="ai-ops-proposal"
        >
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-semibold">{{ t(`aiOps.action.${proposal.action}`) }}</h3>
            <span class="rounded-full bg-gray-100 px-2 py-1 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ t('aiOps.notExecuted') }}</span>
          </div>
          <p v-if="proposal.account_id != null" class="text-sm text-gray-600 dark:text-gray-300">
            {{ t('aiOps.account', { id: proposal.account_id }) }}<span v-if="proposal.proposed_value != null"> · {{ t('aiOps.proposedValue', { value: proposal.proposed_value }) }}</span>
          </p>
          <p class="whitespace-pre-wrap break-words text-sm leading-6">{{ proposal.reason }}</p>
          <details class="text-xs text-gray-500">
            <summary class="cursor-pointer">{{ t('aiOps.proposalEvidence') }}</summary>
            <p class="mt-2 break-words">{{ t('aiOps.evidenceRefs') }}: {{ proposal.evidence_ids.join(', ') || '—' }}</p>
          </details>
        </article>
      </section>

      <section class="card space-y-4 p-5">
        <div class="flex items-center gap-2">
          <h2 class="font-semibold text-gray-900 dark:text-gray-100">{{ t('aiOps.findings') }}</h2>
          <span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ run.result.findings.length }}</span>
        </div>
        <p v-if="!run.result.findings.length" class="text-sm leading-6 text-gray-500">{{ t('aiOps.noFindings') }}</p>
        <article
          v-for="(finding, index) in run.result.findings"
          :key="index"
          class="space-y-3 rounded-lg border p-4"
          :class="findingClass(finding.severity)"
          data-testid="ai-ops-finding"
        >
          <div class="flex flex-wrap items-center gap-2 text-xs">
            <span class="rounded-full px-2 py-1" :class="severityClass(finding.severity)">{{ t(`aiOps.severity.${finding.severity}`) }}</span>
            <span class="text-gray-600 dark:text-gray-300">{{ t(`aiOps.category.${finding.category}`) }}</span>
          </div>
          <p class="whitespace-pre-wrap break-words text-sm leading-6">{{ finding.description }}</p>
          <p class="whitespace-pre-wrap break-words text-sm leading-6"><strong>{{ t('aiOps.recommendation') }}: </strong>{{ finding.recommendation }}</p>
          <details class="text-xs text-gray-500">
            <summary class="cursor-pointer">{{ t('aiOps.findingDetails') }}</summary>
            <p v-if="finding.account_ids.length" class="mt-2 break-words">{{ t('aiOps.accounts') }}: {{ finding.account_ids.map(id => `#${id}`).join(', ') }}</p>
            <p class="mt-2 break-words">{{ t('aiOps.evidenceRefs') }}: {{ finding.evidence_ids.join(', ') || '—' }}</p>
          </details>
        </article>
      </section>
    </template>

    <details class="card p-5" data-testid="ai-ops-report-details">
      <summary class="cursor-pointer font-semibold text-gray-700 dark:text-gray-200">{{ t('aiOps.reportDetails') }}</summary>
      <div class="mt-5 space-y-5">
        <dl class="grid gap-4 text-sm sm:grid-cols-2">
          <div class="min-w-0"><dt class="text-xs text-gray-500">{{ t('aiOps.runId') }}</dt><dd class="mt-1 break-all font-mono text-xs">{{ run.id }}</dd></div>
          <div><dt class="text-xs text-gray-500">{{ t('aiOps.startedAt') }}</dt><dd class="mt-1">{{ formatTime(run.created_at) }}</dd></div>
          <div v-if="run.finished_at"><dt class="text-xs text-gray-500">{{ t('aiOps.finishedAt') }}</dt><dd class="mt-1">{{ formatTime(run.finished_at) }}</dd></div>
          <div class="sm:col-span-2"><dt class="text-xs text-gray-500">{{ t('aiOps.observedWindow') }}</dt><dd class="mt-1 break-words">{{ formatTime(run.window.start) }} → {{ formatTime(run.window.end) }}</dd></div>
        </dl>
        <div class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
          <h3 class="text-sm font-semibold">{{ t('aiOps.evidence') }}</h3>
          <p v-if="!run.evidence.length" class="text-sm text-gray-500">{{ t('aiOps.evidenceEmpty') }}</p>
          <details
            v-for="evidence in run.evidence"
            :key="evidence.id"
            class="rounded-lg border border-gray-200 p-3 dark:border-dark-700"
            data-testid="ai-ops-evidence"
          >
            <summary class="cursor-pointer break-words text-sm">
              <span class="mr-2 rounded px-1.5 py-0.5 text-xs" :class="evidence.status === 'ok' ? 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-gray-300' : 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-200'">{{ t(`aiOps.evidenceStatus.${evidence.status}`) }}</span>
              <span class="font-mono">{{ evidence.id }}</span> · {{ evidence.source }}
            </summary>
            <p class="mt-3 text-xs text-gray-500">{{ t('aiOps.collectedAt', { time: formatTime(evidence.collected_at) }) }}</p>
            <pre class="mt-3 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-800">{{ JSON.stringify(evidence.metrics, null, 2) }}</pre>
          </details>
        </div>
      </div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AIOpsRun } from '@/api/admin/aiOps'

const props = defineProps<{ run: AIOpsRun }>()
const { t, locale } = useI18n()
const incompleteEvidenceCount = computed(() => props.run.evidence.filter(evidence => evidence.status !== 'ok').length)

function formatTime(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString(locale.value, { timeZoneName: 'short' })
}

function findingClass(severity: string): string {
  if (severity === 'critical') return 'border-red-200 dark:border-red-900'
  if (severity === 'warning') return 'border-amber-200 dark:border-amber-900'
  return 'border-gray-200 dark:border-dark-700'
}

function severityClass(severity: string): string {
  if (severity === 'critical') return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
  if (severity === 'warning') return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
  return 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-300'
}
</script>
