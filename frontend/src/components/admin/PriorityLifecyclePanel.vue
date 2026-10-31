<template>
  <section v-if="auth.user?.role === 'admin'" class="min-w-0 rounded-2xl border border-gray-200 p-5 dark:border-dark-700" data-testid="priority-lifecycle">
    <h2 class="text-lg font-semibold">{{ t('priorityScheduling.lifecycle.title') }}</h2>
    <p class="mt-2 text-sm text-amber-700 dark:text-amber-300">{{ t('priorityScheduling.lifecycle.shadow') }}</p>
    <form class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-5" @submit.prevent="selectScope">
      <label class="text-sm">{{ t('priorityScheduling.lifecycle.group') }}<input v-model="group" data-testid="lifecycle-group" class="input mt-1 w-full" inputmode="numeric" /></label>
      <label class="text-sm">{{ t('priorityScheduling.model') }}<input v-model="model" data-testid="lifecycle-model" required maxlength="200" class="input mt-1 w-full" /></label>
      <label class="text-sm">{{ t('priorityScheduling.lifecycle.endpoint') }}<select v-model="endpoint" data-testid="lifecycle-endpoint" class="input mt-1 w-full"><option>/v1/responses</option><option>/v1/chat/completions</option><option>/v1/messages</option><option value="unknown">{{ t('priorityScheduling.unknown') }}</option></select></label>
      <label class="text-sm">{{ t('priorityScheduling.lifecycle.requestType') }}<select v-model.number="requestType" data-testid="lifecycle-type" class="input mt-1 w-full"><option :value="2">HTTP stream</option><option :value="1">HTTP sync</option><option :value="3">WebSocket</option><option :value="0">{{ t('priorityScheduling.unknown') }}</option></select></label>
      <button class="btn btn-primary self-end" type="submit" data-testid="lifecycle-select">{{ t('priorityScheduling.lifecycle.select') }}</button>
    </form>
    <div v-if="chosen" class="mt-4">
      <p data-testid="lifecycle-scope" class="break-all text-sm">{{ chosen.group_id ?? 'ungrouped' }} · {{ chosen.model }} · {{ chosen.endpoint }} · {{ chosen.request_type }}</p>
      <div class="my-3 flex flex-wrap gap-2">
        <button v-for="value in tabs" :key="value" type="button" class="btn btn-secondary" :aria-pressed="tab === value" :data-testid="'lifecycle-tab-' + value" @click="changeTab(value)">{{ t('priorityScheduling.lifecycle.' + value) }}</button>
        <button type="button" class="btn btn-secondary" data-testid="lifecycle-refresh" @click="refresh()">{{ t('priorityScheduling.refresh') }}</button>
      </div>
      <p v-if="failure || offline" role="alert" class="mb-3 text-amber-700" data-testid="lifecycle-error">{{ t('priorityScheduling.lifecycle.lastGood') }} {{ failure }}</p>
      <p v-if="loading" role="status">{{ t('priorityScheduling.loading') }}</p>
      <template v-if="tab === 'current' && state">
        <p class="break-all text-xs text-gray-500">{{ state.scope.measurement_schema }} · {{ state.scope.statistics_spec }} · {{ t('priorityScheduling.configRevision') }}: {{ state.policy_revision }} · {{ t('priorityScheduling.lifecycle.generation') }}: {{ state.statistics?.generation ?? '—' }}</p>
        <p class="my-2 text-xs">{{ t('priorityScheduling.historyObservedAt') }}: {{ state.statistics?.observed_at ?? '—' }} · {{ t('priorityScheduling.windowEnd') }}: {{ state.statistics?.window_end ?? '—' }} · {{ t('priorityScheduling.lifecycle.watermark') }}: {{ state.statistics?.watermark ?? '—' }}</p>
        <p v-if="state.last_error" role="alert" class="text-amber-700">{{ state.last_error }}</p>
        <div class="overflow-x-auto"><table class="w-full whitespace-nowrap text-left text-sm" data-testid="lifecycle-accounts"><thead><tr><th v-for="key in ['account','configuration','data','policy','admission']" :key="key" class="p-3">{{ t('priorityScheduling.lifecycle.' + key) }}</th></tr></thead>
          <tbody><tr v-for="row in state.accounts" :key="row.account_id" :data-testid="'lifecycle-account-' + row.account_id" class="border-t border-gray-100 dark:border-dark-700">
            <td class="p-3">{{ row.account_name }}<small class="block">#{{ row.account_id }}</small></td>
            <td class="p-3">{{ row.configuration }}</td>
            <td class="p-3"><span>{{ row.content }} / {{ failed ? 'STALE' : row.freshness }} / {{ failed ? 'ERROR' : row.refresh }}</span><small class="block">P{{ row.signal?.latency_percentile ?? '—' }}: {{ row.signal?.selected_ttft_ms == null ? '—' : Math.round(row.signal.selected_ttft_ms) + ' ms' }}</small><small class="block">{{ row.signal?.samples ?? '—' }} / {{ row.signal?.latency_unknown_samples ?? '—' }}</small></td>
            <td class="p-3"><template v-if="row.policy"><span>{{ row.policy.state }}</span><small class="block">{{ row.policy.observed_value ?? '—' }} / {{ row.policy.threshold }} ms · P{{ row.policy.percentile }}</small><small class="block">{{ row.policy.evidence_at }}</small><small class="block">{{ row.policy.expires_at }} · {{ remaining(row.policy.expires_at) }}</small><small class="block max-w-xs break-all whitespace-normal">{{ row.policy.evidence_id }} · {{ row.policy.policy_revision }}</small></template><span v-else>{{ t('priorityScheduling.unknown') }}</span></td>
            <td class="p-3">{{ row.admission }}<small v-for="reason in row.reasons" :key="reason" class="block max-w-xs whitespace-normal">{{ reason }}</small></td>
          </tr></tbody></table></div>
      </template>
      <div v-if="tab === 'decisions'" class="space-y-3">
        <article v-for="decision in decisions" :key="decision.decision_id" class="rounded border border-gray-200 p-3 text-sm" data-testid="lifecycle-decision">
          <p class="break-all">{{ decision.decision_id }} · {{ decision.at }} · {{ decision.phase }}</p><p>{{ decision.selected_account_id }} · {{ decision.allowed }} · {{ decision.reason }}</p>
          <p class="break-all text-xs">{{ decision.policy_revision }} · {{ decision.statistics_generation ?? '—' }} · {{ decision.trace_id ?? '—' }}</p>
        </article>
        <button v-if="decisions.length" type="button" class="btn btn-secondary" @click="more">{{ t('priorityScheduling.lifecycle.more') }}</button>
      </div>
      <div v-if="tab === 'events'" class="space-y-3">
        <article v-for="event in events" :key="event.sequence" class="rounded border border-gray-200 p-3 text-sm" data-testid="lifecycle-event">
          <p>{{ event.kind }} · #{{ event.policy.account_id }} · {{ event.event_at }}</p><p class="break-all">{{ event.policy.evidence_id }} · {{ event.policy.policy_revision }}</p><p>{{ event.recorded_at }} · {{ event.policy.expires_at }}</p>
        </article>
        <button v-if="events.length" type="button" class="btn btn-secondary" @click="more">{{ t('priorityScheduling.lifecycle.more') }}</button>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { getPriorityLifecycleState, getPriorityLifecycleDecisions, getPriorityLifecycleEvents, type PriorityLifecycleScope, type PriorityLifecycleState, type PriorityLifecycleDecision, type PriorityLifecycleEvent } from '@/api/admin/priorityScheduling'
const { t } = useI18n(), auth = useAuthStore()
const group = ref(''), model = ref(''), endpoint = ref('/v1/responses'), requestType = ref(2)
const chosen = ref<PriorityLifecycleScope | null>(null), state = ref<PriorityLifecycleState | null>(null)
const tabs = ['current', 'decisions', 'events'] as const
type Tab = typeof tabs[number]
const tab = ref<Tab>('current'), decisions = ref<PriorityLifecycleDecision[]>([]), events = ref<PriorityLifecycleEvent[]>([])
const failure = ref(''), loading = ref(false), offline = ref(false), now = ref(Date.now())
const failed = computed(() => !!failure.value || offline.value)
let generation = 0, alive = true, timer: ReturnType<typeof setTimeout> | undefined, clock: ReturnType<typeof setInterval> | undefined, serverOffset = 0
function stop() { if (timer) clearTimeout(timer); timer = undefined }
function same(a: PriorityLifecycleScope, b: PriorityLifecycleScope) { return a.group_id === b.group_id && a.model === b.model && a.endpoint === b.endpoint && a.request_type === b.request_type }
function selectScope() {
  const id = group.value.trim() ? Number(group.value) : null
  if ((id !== null && (!Number.isSafeInteger(id) || id <= 0)) || !model.value.trim()) { failure.value = t('priorityScheduling.invalid'); return }
  generation++; stop(); state.value = null; decisions.value = []; events.value = []
  chosen.value = { group_id: id, model: model.value.trim(), endpoint: endpoint.value, request_type: requestType.value }
  void refresh()
}
async function refresh(after = 0) {
  if (!chosen.value || !alive || document.visibilityState === 'hidden') return
  stop()
  const scope = { ...chosen.value }, request = ++generation, target = tab.value
  loading.value = true
  try {
    if (target === 'current') {
      const value = await getPriorityLifecycleState(scope)
      if (!alive || request !== generation) return
      if (!same(scope, value.scope) || (state.value?.statistics && value.statistics && value.statistics.generation < state.value.statistics.generation)) throw new Error('scope_or_revision_mismatch')
      state.value = value; serverOffset = Date.parse(value.server_now) - Date.now()
    } else if (target === 'decisions') {
      const result = await getPriorityLifecycleDecisions(scope, after)
      if (!alive || request !== generation) return
      if (result.items.some(item => !same(scope, item.scope))) throw new Error('scope_mismatch')
      decisions.value = after ? [...decisions.value, ...result.items.filter(item => !decisions.value.some(prior => prior.decision_id === item.decision_id))] : result.items
    } else {
      const result = await getPriorityLifecycleEvents(scope, after)
      if (!alive || request !== generation) return
      events.value = after ? [...events.value, ...result.items.filter(item => !events.value.some(prior => prior.sequence === item.sequence))] : result.items
    }
    failure.value = ''; offline.value = false
  } catch { if (alive && request === generation) failure.value = t('priorityScheduling.error') }
  finally {
    if (alive && request === generation) { loading.value = false; timer = setTimeout(() => { void refresh() }, failed.value ? 15000 : 5000) }
  }
}
function changeTab(value: Tab) { tab.value = value; void refresh() }
function more() { void refresh(Math.max(0, ...(tab.value === 'decisions' ? decisions.value : events.value).map(item => item.sequence))) }
function remaining(expires: string) {
  const seconds = Math.max(0, Math.ceil((Date.parse(expires) - now.value - serverOffset) / 1000))
  return seconds > 0 ? seconds + ' s' : t('priorityScheduling.lifecycle.awaitingServer')
}
function visibility() { if (document.visibilityState === 'hidden') { generation++; loading.value = false; stop() } else void refresh() }
function disconnected() { offline.value = true; generation++; loading.value = false; stop() }
function connected() { void refresh() }
watch(() => auth.user ? auth.user.id + ':' + auth.user.role : '', () => { generation++; stop(); chosen.value = null; state.value = null; decisions.value = []; events.value = []; model.value = ''; group.value = ''; failure.value = ''; loading.value = false }, { flush: 'sync' })
onMounted(() => {
  clock = setInterval(() => { now.value = Date.now() }, 1000)
  document.addEventListener('visibilitychange', visibility); window.addEventListener('offline', disconnected); window.addEventListener('online', connected)
})
onBeforeUnmount(() => {
  alive = false; generation++; stop(); if (clock) clearInterval(clock)
  document.removeEventListener('visibilitychange', visibility); window.removeEventListener('offline', disconnected); window.removeEventListener('online', connected)
})
</script>
