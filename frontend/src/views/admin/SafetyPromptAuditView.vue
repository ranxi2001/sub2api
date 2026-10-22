<template>
  <AppLayout>
    <div class="w-full min-w-0 pb-8" data-testid="safety-prompt-audit">
      <SecurityAuditNav active="/admin/safety-prompt-audit" />
      <header class="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <p class="text-xs font-semibold uppercase tracking-[0.16em] text-primary-600 dark:text-primary-400">{{ t('nav.securityAudit') }}</p>
          <h1 class="mt-1 text-2xl font-semibold tracking-tight text-gray-950 dark:text-white">{{ t('admin.safetyPromptAudit.title') }}</h1>
          <p class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">{{ t('admin.safetyPromptAudit.description') }}</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">{{ t('admin.safetyPromptAudit.refresh') }}</button>
          <RouterLink to="/admin/plugins" class="btn btn-secondary btn-sm">{{ t('admin.safetyPromptAudit.openPlugins') }}</RouterLink>
        </div>
      </header>

      <p v-if="loading" role="status" class="card px-5 py-8 text-sm text-gray-500">{{ t('admin.safetyPromptAudit.loading') }}</p>
      <div v-else-if="error" role="alert" class="card px-5 py-8">
        <p class="text-sm text-red-600 dark:text-red-300">{{ t('admin.safetyPromptAudit.loadFailed') }}</p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="load">{{ t('admin.safetyPromptAudit.refresh') }}</button>
      </div>
      <div v-else-if="!plugin" class="card px-5 py-8" data-testid="safety-audit-missing">
        <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.safetyPromptAudit.pluginMissing') }}</p>
        <p class="mt-1 text-sm text-gray-500">{{ t('admin.safetyPromptAudit.pluginMissingHint') }}</p>
        <RouterLink to="/admin/plugins" class="btn btn-secondary btn-sm mt-3">{{ t('admin.safetyPromptAudit.openPlugins') }}</RouterLink>
      </div>
      <template v-else>
        <section class="card px-5 py-4">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div>
              <p class="font-medium text-gray-900 dark:text-white">{{ plugin.name }} <span class="text-xs font-normal text-gray-400">v{{ plugin.version }}</span></p>
              <p class="mt-1 text-xs text-gray-500">{{ t('admin.safetyPromptAudit.pluginState') }}: {{ plugin.state }}<template v-if="status?.message"> · {{ status.message }}</template></p>
            </div>
            <span class="rounded-md px-2 py-1 text-xs font-medium" :class="plugin.state === 'enabled' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'">{{ plugin.state }}</span>
          </div>
          <p v-if="audit" class="mt-3 text-sm" :class="audit.persistent ? 'text-gray-500' : 'text-amber-700 dark:text-amber-300'" data-testid="safety-audit-storage">{{ storageText }}</p>
          <p v-else class="mt-3 text-sm text-amber-700 dark:text-amber-300">{{ t('admin.safetyPromptAudit.noAudit') }}</p>
        </section>

        <section v-if="audit" class="card mt-4 px-4 py-4 sm:px-6">
          <div class="grid gap-3 sm:grid-cols-3">
            <label class="text-xs text-gray-500">{{ t('admin.safetyPromptAudit.filters.from') }}<input v-model="from" type="date" class="input mt-1 w-full" /></label>
            <label class="text-xs text-gray-500">{{ t('admin.safetyPromptAudit.filters.to') }}<input v-model="to" type="date" class="input mt-1 w-full" /></label>
            <label class="text-xs text-gray-500">{{ t('admin.safetyPromptAudit.filters.search') }}<input v-model="search" type="search" class="input mt-1 w-full" /></label>
          </div>
          <div class="mt-3 flex flex-wrap items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm" @click="setToday">{{ t('admin.safetyPromptAudit.filters.today') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" @click="clearFilters">{{ t('admin.safetyPromptAudit.filters.all') }}</button>
            <span class="text-xs text-gray-500" data-testid="safety-audit-count">{{ countText }}</span>
          </div>
          <div v-if="badRange" role="alert" class="mt-3 text-sm text-amber-700 dark:text-amber-300">{{ t('admin.safetyPromptAudit.badRange') }}</div>
          <div v-else-if="filtered.length" class="mt-4 overflow-x-auto" data-testid="safety-audit-table">
            <table class="w-full whitespace-nowrap text-left text-sm">
              <thead><tr>
                <th class="px-3 py-2 text-xs font-medium text-gray-500">{{ t('admin.safetyPromptAudit.table.time') }}</th>
                <th class="px-3 py-2 text-xs font-medium text-gray-500">{{ t('admin.safetyPromptAudit.table.model') }}</th>
                <th class="px-3 py-2 text-xs font-medium text-gray-500">{{ t('admin.safetyPromptAudit.table.user') }}</th>
                <th class="px-3 py-2 text-xs font-medium text-gray-500">{{ t('admin.safetyPromptAudit.table.summary') }}</th>
              </tr></thead>
              <tbody>
                <tr v-for="event in filtered" :key="event.id || `${event.time}-${event.request_id}`" class="border-t border-gray-100 align-top dark:border-dark-700">
                  <td class="px-3 py-3"><span class="block">{{ formatTime(event.time) }}</span><span class="block text-xs text-gray-400">{{ event.id || '—' }}</span><span v-if="event.request_id" class="block text-xs text-gray-400">{{ t('admin.safetyPromptAudit.requestId') }}: {{ event.request_id }}</span></td>
                  <td class="px-3 py-3"><span class="block">{{ event.model || '—' }}</span><span class="block text-xs text-gray-400">{{ t('admin.safetyPromptAudit.upstreamAccount') }}: {{ event.upstream_account_id ?? '—' }}</span></td>
                  <td class="px-3 py-3 text-xs text-gray-500">{{ t('admin.safetyPromptAudit.unknownUser') }}</td>
                  <td class="max-w-xl whitespace-pre-wrap break-words px-3 py-3 text-xs">{{ event.summary?.trim() || t('admin.safetyPromptAudit.noSummary') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="mt-4 text-sm text-gray-500" data-testid="safety-audit-empty">{{ t('admin.safetyPromptAudit.empty') }}</p>
          <div class="mt-4 flex flex-wrap gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!filtered.length" @click="exportJson">{{ t('admin.safetyPromptAudit.exportJson') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!filtered.length" @click="exportCsv">{{ t('admin.safetyPromptAudit.exportCsv') }}</button>
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SecurityAuditNav from '@/components/admin/security/SecurityAuditNav.vue'
import { adminAPI } from '@/api/admin'
import type { PluginInstallation } from '@/api/admin'
import type { PluginStatusResult } from '@/api/admin/plugins'

interface AuditEvent {
  time: string
  id?: string
  request_id?: string
  upstream_account_id?: number
  user_id?: number | null
  identity?: string
  model?: string
  action?: string
  summary?: string
  request_sha256?: string
}
interface AuditSnapshot {
  events?: AuditEvent[]
  retention_days?: number
  limit?: number
  persistent?: boolean
  warning?: string
  capacity_evictions?: number
}

const PLUGIN_ID = 'local.sub2api.safety-prompt'
const { t } = useI18n()
const plugin = ref<PluginInstallation | null>(null)
const status = ref<PluginStatusResult | null>(null)
const audit = ref<AuditSnapshot | null>(null)
const loading = ref(true)
const error = ref('')
const from = ref('')
const to = ref('')
const search = ref('')

const badRange = computed(() => Boolean(from.value && to.value && from.value > to.value))
const validEvents = computed(() => {
  const events = Array.isArray(audit.value?.events) ? audit.value!.events! : []
  const days = Number.isFinite(audit.value?.retention_days) ? Number(audit.value!.retention_days) : 30
  const cutoff = Date.now() - days * 86_400_000
  return events.filter(event => event && typeof event.time === 'string' && Number.isFinite(Date.parse(event.time)) && Date.parse(event.time) > cutoff)
})
const filtered = computed(() => {
  if (badRange.value) return []
  const query = search.value.trim().toLowerCase()
  return validEvents.value.filter(event => {
    const date = dayOf(event.time)
    if (from.value && date < from.value) return false
    if (to.value && date > to.value) return false
    if (!query) return true
    return [event.id, event.request_id, event.model, event.upstream_account_id, event.summary]
      .filter(value => value != null)
      .join(' ')
      .toLowerCase()
      .includes(query)
  })
})
const countText = computed(() => badRange.value ? t('admin.safetyPromptAudit.badRange') : t('admin.safetyPromptAudit.count', { shown: filtered.value.length, total: validEvents.value.length }))
const storageText = computed(() => {
  if (!audit.value) return ''
  if (audit.value.persistent) {
    return t('admin.safetyPromptAudit.storageReady', {
      days: audit.value.retention_days ?? 30,
      limit: audit.value.limit ?? 500,
      evictions: audit.value.capacity_evictions ?? 0
    })
  }
  return t('admin.safetyPromptAudit.storageWarning', { warning: audit.value.warning || '—' })
})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const installations = await adminAPI.plugins.list()
    const found = installations.find(item => item.plugin_key === PLUGIN_ID || item.manifest?.id === PLUGIN_ID || item.plugin_key?.startsWith(PLUGIN_ID))
    plugin.value = found || null
    status.value = null
    audit.value = null
    if (!found) return
    const result = await adminAPI.plugins.status(found.id)
    status.value = result
    if (!result.status_json) return
    const parsed = JSON.parse(result.status_json) as { audit?: AuditSnapshot }
    audit.value = parsed && typeof parsed === 'object' && parsed.audit ? parsed.audit : null
  } catch {
    error.value = t('admin.safetyPromptAudit.loadFailed')
  } finally {
    loading.value = false
  }
}
function dayOf(value: string): string {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return ''
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}
function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : '—'
}
function setToday(): void {
  from.value = to.value = dayOf(new Date().toISOString())
  search.value = ''
}
function clearFilters(): void {
  from.value = to.value = search.value = ''
}
function download(filename: string, content: string, type: string): void {
  const url = URL.createObjectURL(new Blob([content], { type }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
function exportRows() {
  return filtered.value.map(event => ({
    time: event.time,
    id: event.id ?? '',
    request_id: event.request_id ?? '',
    upstream_account_id: event.upstream_account_id ?? '',
    user_id: null,
    identity: 'unknown',
    model: event.model ?? '',
    action: event.action ?? '',
    summary: event.summary ?? '',
    request_sha256: event.request_sha256 ?? ''
  }))
}
function exportJson(): void {
  if (!filtered.value.length) return
  download(`safety-prompt-audit-${dayOf(new Date().toISOString())}.json`, JSON.stringify({ exported_at: new Date().toISOString(), identity_available: false, records: exportRows() }, null, 2), 'application/json')
}
function csvCell(value: unknown): string {
  let text = value == null ? '' : String(value)
  if (/^\s*[=+@-]/.test(text)) text = `'${text}`
  return `"${text.replace(/"/g, '""')}"`
}
function exportCsv(): void {
  if (!filtered.value.length) return
  const rows = exportRows()
  const columns = Object.keys(rows[0]) as (keyof ReturnType<typeof exportRows>[number])[]
  const content = columns.map(csvCell).join(',') + '\r\n' + rows.map(row => columns.map(column => csvCell(row[column])).join(',')).join('\r\n')
  download(`safety-prompt-audit-${dayOf(new Date().toISOString())}.csv`, content, 'text/csv;charset=utf-8')
}

onMounted(load)
</script>
