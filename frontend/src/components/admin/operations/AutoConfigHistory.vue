<template>
  <section class="min-w-0 rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900" aria-labelledby="auto-config-history-title" data-testid="auto-config-history">
    <header class="flex flex-wrap items-start justify-between gap-4 p-5">
      <div class="min-w-0">
        <h2 id="auto-config-history-title" class="text-lg font-semibold">{{ t('autoConfig.logs.title') }}</h2>
        <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('autoConfig.logs.description') }}</p>
      </div>
      <div class="flex w-full items-center gap-3 sm:w-auto">
        <select v-model="kind" class="input min-w-0 flex-1 sm:w-40" :aria-label="t('autoConfig.logs.filter')" data-testid="history-kind">
          <option value="">{{ t('autoConfig.logs.all') }}</option>
          <option v-for="value in kinds" :key="value" :value="value">{{ t('autoConfig.logs.kinds.' + value) }}</option>
        </select>
        <button type="button" class="btn btn-secondary shrink-0" :disabled="loading" data-testid="history-refresh" @click="load()">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
          {{ t('autoConfig.logs.refresh') }}
        </button>
      </div>
    </header>
    <div v-if="error" role="alert" class="mx-5 mb-4 flex flex-wrap items-center justify-between gap-3 rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      <span>{{ error }}</span>
      <button type="button" class="font-medium underline" :disabled="loading" data-testid="history-retry" @click="load(retryMore)">{{ t('autoConfig.retry') }}</button>
    </div>
    <div :aria-busy="loading">
      <p v-if="!events.length && loading" role="status" class="px-5 py-12 text-center text-sm text-gray-500">{{ t('common.loading') }}</p>
      <div v-else-if="!events.length && !error" class="border-t border-gray-100 px-5 py-12 text-center dark:border-dark-700" data-testid="history-empty">
        <Icon name="clock" size="lg" class="mx-auto mb-3 text-gray-400" />
        <p class="font-medium text-gray-700 dark:text-gray-200">{{ t(kind ? 'autoConfig.logs.emptyFiltered' : 'autoConfig.logs.empty') }}</p>
        <p class="mt-2 text-sm text-gray-500">{{ t('autoConfig.logs.emptyHint') }}</p>
      </div>
      <div v-if="events.length" class="overflow-x-auto">
        <table class="w-full min-w-[720px] text-left text-sm">
          <thead class="border-y border-gray-100 bg-gray-50 text-xs text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400">
            <tr><th scope="col" class="px-5 py-3 font-medium">{{ t('autoConfig.logs.time') }}</th><th scope="col" class="px-5 py-3 font-medium">{{ t('autoConfig.logs.account') }}</th><th scope="col" class="px-5 py-3 font-medium">{{ t('autoConfig.logs.event') }}</th><th scope="col" class="px-5 py-3 font-medium">{{ t('autoConfig.logs.detail') }}</th></tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="event in events" :key="event.id" class="align-top hover:bg-gray-50/70 dark:hover:bg-dark-800/50" data-testid="history-row">
              <td class="whitespace-nowrap px-5 py-4 text-xs tabular-nums text-gray-500 dark:text-gray-400"><time :datetime="event.created_at">{{ formatDateTime(event.created_at) }}</time></td>
              <td class="px-5 py-4"><p class="max-w-60 break-words font-medium">{{ event.account_id ? event.account_name || '#' + event.account_id : t('autoConfig.logs.global') }}</p><p class="mt-1 text-xs text-gray-400">{{ event.platform }}<span v-if="event.account_id"> · #{{ event.account_id }}</span></p></td>
              <td class="whitespace-nowrap px-5 py-4"><span class="inline-flex rounded-full px-2.5 py-1 text-xs font-medium" :class="badgeClass(event.kind)">{{ t('autoConfig.logs.kinds.' + event.kind) }}</span></td>
              <td class="min-w-64 px-5 py-4 leading-6 text-gray-600 dark:text-gray-300">
                {{ details(event) }}
                <p v-if="event.details.model_mapping && Object.keys(event.details.model_mapping).length" class="mt-1 break-words text-xs">{{ t('autoConfig.mapping.applied') }}：{{ Object.entries(event.details.model_mapping).map(([from, to]) => from + ' → ' + to).join(' / ') }}</p>
                <details v-if="event.kind === 'config_saved' && event.details.config" class="mt-1 text-xs">
                  <summary class="w-fit cursor-pointer text-primary-600 dark:text-primary-400">{{ t('autoConfig.logs.snapshot') }}</summary>
                  <div class="mt-2 space-y-1 rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
                    <template v-if="event.details.config.model_billing">
                      <p>{{ t('autoConfig.modelBilling.title') }}：{{ t(event.details.config.model_billing.enabled ? 'autoConfig.logs.on' : 'autoConfig.logs.off') }}</p>
                      <p v-for="rule in event.details.config.model_billing.rules" :key="rule.model" class="break-all">{{ rule.model }} · {{ rule.multiplier }}× · {{ rule.group_ids?.length ? t('autoConfig.modelBilling.groups') + '：' + groupIDs(rule.group_ids) : t('autoConfig.modelBilling.allGroups') }}</p>
                    </template>
                    <p v-if="event.details.config.model_mappings?.length">{{ t('autoConfig.mapping.title') }}：</p>
                    <p v-for="rule in event.details.config.model_mappings ?? []" :key="rule.from" class="break-words">{{ rule.from }} → {{ rule.to }}</p>
                    <p>{{ t('autoConfig.groups') }}：{{ groupIDs(event.details.config.group_ids) }}</p>
                    <p>{{ t('autoConfig.upgradeGroups') }}：{{ groupIDs(event.details.config.upgrade_group_ids) }}</p>
                    <p>{{ t('autoConfig.logs.savedRule', { count: event.details.config.successes_per_step, step: event.details.config.upgrade_step, max: event.details.config.max_concurrency, seconds: event.details.config.cooldown_seconds }) }}</p>
                  </div>
                </details>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <footer class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-4 text-xs text-gray-400 dark:border-dark-700">
      <span>{{ t('autoConfig.logs.scopeHint') }}</span>
      <button v-if="hasMore" type="button" class="btn btn-secondary" :disabled="loading" data-testid="history-more" @click="load(true)">{{ t(loading ? 'common.loading' : 'autoConfig.logs.more') }}</button>
    </footer>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import { getAutoConfigEvents, type AutoConfigEvent, type AutoConfigEventKind } from '@/api/admin/autoConfig'

const props = defineProps<{ refreshKey: number }>()
const { t } = useI18n()
const auth = useAuthStore()
const kinds: AutoConfigEventKind[] = ['config_saved', 'initial_applied', 'concurrency_upgraded', 'failure_cooldown', 'degradation_applied']
const kind = ref<AutoConfigEventKind | ''>('')
const events = ref<AutoConfigEvent[]>([])
const hasMore = ref(false), loading = ref(false), error = ref(''), retryMore = ref(false)
let generation = 0, alive = true

async function load(more = false) {
  if (!alive || auth.user?.role !== 'admin') return
  const version = ++generation
  loading.value = true
  error.value = ''
  retryMore.value = more
  try {
    const page = await getAutoConfigEvents({ limit: 20, kind: kind.value, before: more ? events.value.at(-1)?.id : undefined })
    if (!alive || generation !== version) return
    events.value = more ? [...events.value, ...page.items] : page.items
    hasMore.value = page.has_more
  } catch {
    if (alive && generation === version) error.value = t('autoConfig.logs.loadFailed')
  } finally {
    if (alive && generation === version) loading.value = false
  }
}
watch(() => auth.user ? auth.user.id + ':' + auth.user.role : '', () => {
  generation++
  events.value = []
  hasMore.value = loading.value = false
  error.value = ''
  void load()
}, { immediate: true, flush: 'sync' })
watch(kind, () => { events.value = []; hasMore.value = false; void load() })
watch(() => props.refreshKey, () => { void load() })
onBeforeUnmount(() => { alive = false; generation++ })

function badgeClass(value: AutoConfigEventKind) {
  if (value === 'failure_cooldown') return 'bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-300'
  if (value === 'concurrency_upgraded') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300'
  if (value === 'initial_applied') return 'bg-blue-50 text-blue-700 dark:bg-blue-950/30 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}
function details(event: AutoConfigEvent) {
  const d = event.details
  if (event.kind === 'degradation_applied' && d.degradation) {
    const e = d.degradation
    const actions = []
    if (e.limit_concurrency !== false) actions.push(t('autoConfig.degradation.concurrencyEvent', { before: d.previous_concurrency, after: d.concurrency }))
    if (e.limit_models !== false) actions.push(t('autoConfig.degradation.modelsEvent', { models: (e.models ?? []).join(', ') || '∅' }))
    if (e.limit_groups) actions.push(t('autoConfig.degradation.groupsEvent', { groups: groupIDs(e.group_ids), removed: groupIDs(e.removed_group_ids) }))
    return t('autoConfig.degradation.event', { percent: e.degradation_percent, threshold: e.threshold_percent, samples: e.quality_samples, actions: actions.join(' · '), start: formatDateTime(e.window_start), end: formatDateTime(e.window_end) })
  }
  if (event.kind === 'config_saved' && d.config) return t('autoConfig.logs.savedDetail', { initial: t(d.config.enabled ? 'autoConfig.logs.on' : 'autoConfig.logs.off'), upgrade: t(d.config.upgrade_enabled ? 'autoConfig.logs.on' : 'autoConfig.logs.off'), priority: d.config.priority, load: d.config.load_factor, concurrency: d.config.concurrency })
  if (event.kind === 'initial_applied') return t('autoConfig.logs.initialDetail', { priority: d.priority, load: d.load_factor, concurrency: d.concurrency, groups: groupIDs(d.group_ids) })
  if (event.kind === 'concurrency_upgraded') return t('autoConfig.logs.upgradeDetail', { before: d.previous_concurrency, after: d.concurrency, seconds: d.cooldown_seconds })
  if (event.kind === 'failure_cooldown') return t('autoConfig.logs.cooldownDetail', { concurrency: d.concurrency, seconds: d.cooldown_seconds })
  return '—'
}
function groupIDs(ids?: number[]) {
  return (ids ?? []).map(id => '#' + id).join(' / ') || '—'
}
</script>
