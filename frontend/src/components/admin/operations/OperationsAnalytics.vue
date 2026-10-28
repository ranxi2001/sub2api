<template>
  <AppLayout>
    <div class="min-w-0 space-y-5">
      <header class="flex flex-wrap items-start justify-between gap-3"><div><h1 class="text-2xl font-semibold">{{ t(kind === 'profit' ? 'profitOps.title' : 'profitOps.channelTitle') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('profitOps.description') }}</p></div><button class="btn btn-secondary" data-testid="refresh-operations" :disabled="loading" @click="load">{{ t('profitOps.refresh') }}</button></header>
      <SmartOpsNav />
      <p class="rounded-xl bg-primary-50 p-4 text-sm leading-6 dark:bg-dark-800">{{ t('profitOps.attribution') }}</p>
      <details class="card p-4" open><summary class="font-semibold">{{ t('profitOps.method') }}</summary><p class="mt-3 text-sm leading-6">{{ t('profitOps.formula') }}</p><p class="mt-2 text-sm leading-6">{{ t('profitOps.incomeNote') }}</p><p class="mt-2 text-sm leading-6">{{ t('profitOps.unknownNote') }}</p></details>
      <form data-testid="profit-filters" class="card grid gap-3 p-4 sm:grid-cols-2 lg:grid-cols-4" @submit.prevent="apply">
        <label>{{ t('profitOps.from') }}<input v-model="from" type="datetime-local" class="input w-full" required /></label>
        <label>{{ t('profitOps.until') }}<input v-model="until" type="datetime-local" class="input w-full" required /></label>
        <label>{{ t('profitOps.account') }}<input v-model.number="draft.account_id" type="number" min="0" class="input w-full" /></label>
        <label>{{ t('profitOps.group') }}<input v-model.number="draft.group_id" type="number" min="0" class="input w-full" /></label>
        <label>{{ t('profitOps.upstream') }}<input v-model="draft.upstream" maxlength="2048" class="input w-full" /></label>
        <label>{{ t('profitOps.model') }}<input v-model="draft.model" data-testid="model-filter" maxlength="200" class="input w-full" /></label>
        <label>{{ t('profitOps.modelScope') }}<select v-model="draft.model_scope" data-testid="model-scope" class="input w-full"><option value="public">{{ t('profitOps.public') }}</option><option value="recorded">{{ t('profitOps.recorded') }}</option><option value="upstream">{{ t('profitOps.upstreamModel') }}</option></select></label>
        <label>{{ t('profitOps.dimension') }}<select v-model="draft.dimension" class="input w-full"><option value="upstream">{{ t('profitOps.upstream') }}</option><option value="account">{{ t('profitOps.account') }}</option><option value="model">{{ t('profitOps.model') }}</option></select></label>
        <label>{{ t('profitOps.sort') }}<select v-model="draft.sort" data-testid="sort-filter" class="input w-full"><option v-for="key in sortKeys" :key="key" :value="key">{{ t(`profitOps.${key}`) }}</option></select></label>
        <label>{{ t('profitOps.direction') }}<select v-model="draft.direction" class="input w-full"><option value="desc">{{ t('profitOps.desc') }}</option><option value="asc">{{ t('profitOps.asc') }}</option></select></label>
        <button class="btn btn-primary self-end" :disabled="loading">{{ t('profitOps.apply') }}</button><button type="button" class="btn btn-secondary self-end" :disabled="loading" @click="last24h">{{ t('profitOps.last24h') }}</button>
      </form>
      <p v-if="error" role="alert" class="text-red-600">{{ error }}</p><p v-if="loading" role="status">{{ t('profitOps.loading') }}</p>
      <template v-if="snapshot && !error">
        <p class="break-words text-xs text-gray-500" data-testid="applied-scope">{{ snapshot.scope.from }} ≤ t &lt; {{ snapshot.scope.until }} · {{ snapshot.currency }} · {{ t('profitOps.queried') }} {{ snapshot.queried_at }}</p>
        <section class="card space-y-3 p-4"><h2 class="font-semibold">{{ t('profitOps.discovery') }}</h2><p class="text-xs text-gray-500">{{ t('profitOps.discoveryNote') }}</p><div class="flex flex-wrap gap-2"><button v-for="c in snapshot.usage.catalog" :key="c.key" class="btn btn-secondary max-w-full break-all" @click="selectUpstream(c.key)">{{ c.name }} · {{ c.accounts }}</button></div><p class="text-xs">{{ applied.page }} · {{ snapshot.usage.catalog_total }}</p></section>
        <section class="card space-y-4 p-4" data-testid="profit-summary">
          <h2 class="font-semibold">{{ t('profitOps.summary') }}</h2>
          <dl class="grid gap-4 sm:grid-cols-3 lg:grid-cols-6"><div v-for="key in amountKeys" :key="key"><dt class="text-sm text-gray-500">{{ t(`profitOps.${key}`) }}</dt><dd class="mt-1 break-words text-lg font-semibold">{{ key === 'requests' ? snapshot.usage.summary.requests : formatMetric(snapshot.usage.summary, key) }}</dd></div></dl>
          <p class="text-sm">{{ t('profitOps.coverage') }} {{ snapshot.usage.summary.cost_covered_requests }} / {{ snapshot.usage.summary.requests }} · {{ t('profitOps.unknownCost') }} {{ snapshot.usage.summary.unknown_cost_requests }} · {{ t('profitOps.unknownUpstream') }} {{ snapshot.usage.summary.unknown_upstream_requests }}</p>
          <p class="text-xs leading-6 text-gray-500">{{ t('profitOps.savedMultiplier') }} {{ snapshot.usage.summary.saved_multiplier_requests }} · {{ t('profitOps.defaultMultiplier') }} {{ snapshot.usage.summary.default_multiplier_requests }} · {{ t('profitOps.savedBase') }} {{ snapshot.usage.summary.saved_stats_base_requests }} · {{ t('profitOps.fallbackBase') }} {{ snapshot.usage.summary.standard_base_fallback_requests }} · {{ t('profitOps.subscription') }} {{ snapshot.usage.summary.subscription_requests }} · {{ t('profitOps.deleted') }} {{ snapshot.usage.summary.deleted_account_requests }}</p>
          <p class="text-xs text-gray-500">{{ t('profitOps.unknownIncome') }} {{ snapshot.usage.summary.unknown_income_requests }} · {{ t('profitOps.unknownStandard') }} {{ snapshot.usage.summary.unknown_standard_requests }} · {{ t('profitOps.unmeteredZero') }} {{ snapshot.usage.summary.unmetered_zero_records }}<br />{{ t('profitOps.configUpdated') }} {{ snapshot.usage.summary.oldest_config_updated_at || '—' }} → {{ snapshot.usage.summary.latest_config_updated_at || '—' }}</p>
        </section>
        <section class="card space-y-4 p-4">
          <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ t('profitOps.compare') }} · {{ t(`profitOps.${applied.dimension}`) }}</h2><button class="btn btn-secondary" @click="overview">{{ t('profitOps.overview') }}</button></div>
          <p v-if="!snapshot.usage.rows.length">{{ t('profitOps.empty') }}</p>
          <div v-else class="overflow-x-auto"><table class="w-full min-w-[1000px] text-left text-sm"><thead><tr><th class="p-2">{{ t(`profitOps.${applied.dimension}`) }}</th><th v-for="key in amountKeys" :key="key" class="p-2">{{ t(`profitOps.${key}`) }}</th><th>{{ t('profitOps.coverage') }}</th><th>P50 / P90 / P95 (ms)</th><th>{{ t('profitOps.duration') }}</th></tr></thead><tbody><tr v-for="row in snapshot.usage.rows" :key="row.key" class="border-t dark:border-dark-700"><td class="max-w-80 break-all p-2"><button v-if="applied.dimension !== 'model'" class="text-primary-600 underline" data-testid="drill-row" @click="drill(row)">{{ row.name }}</button><span v-else>{{ row.name }}</span></td><td v-for="key in amountKeys" :key="key" class="p-2">{{ key === 'requests' ? row.requests : formatMetric(row,key) }}</td><td>{{ row.cost_covered_requests }} / {{ row.requests }}</td><td>{{ number(row.p50_ms, 0) }} / {{ number(row.p90_ms, 0) }} / {{ number(row.p95_ms, 0) }}<span class="block text-xs">{{ t('profitOps.samples') }} {{ row.samples }} / {{ t('profitOps.unknown') }} {{ row.unknown_samples }}</span></td><td>{{ number(row.duration_p90_ms,0) }}</td></tr></tbody></table></div>
          <p class="text-xs text-gray-500">{{ t('profitOps.latencyNote') }}</p>
          <div class="flex flex-wrap items-center gap-3"><button class="btn btn-secondary" :disabled="loading || applied.page <= 1" @click="page(-1)">{{ t('profitOps.previous') }}</button><span>{{ applied.page }} · {{ snapshot.usage.total }}</span><button class="btn btn-secondary" data-testid="operations-next" :disabled="loading || applied.page * applied.page_size >= Math.max(snapshot.usage.total, snapshot.usage.catalog_total || 0, snapshot.quality.data?.total || 0)" @click="page(1)">{{ t('profitOps.next') }}</button></div>
        </section>
        <details class="card p-4"><summary>{{ t('profitOps.accountBasis') }} ({{ snapshot.usage.accounts.length }} / {{ snapshot.usage.account_details_limit }})</summary><p class="my-2 text-xs">{{ t('profitOps.basisLimit') }}</p><div class="overflow-x-auto"><table class="min-w-[800px] text-left text-sm"><thead><tr><th>{{ t('profitOps.account') }}</th><th>{{ t('profitOps.upstream') }}</th><th>{{ t('profitOps.costBasis') }}</th><th>{{ t('profitOps.configUpdated') }}</th></tr></thead><tbody><tr v-for="a in snapshot.usage.accounts" :key="a.id"><td class="p-2">{{ a.name }} #{{ a.id }} {{ a.deleted ? t('profitOps.deleted') : '' }}</td><td class="max-w-80 break-all">{{ a.key }} · {{ a.attribution }}</td><td>{{ a.cost_basis }} × {{ number(a.multiplier,4) }}</td><td>{{ a.updated_at }}</td></tr></tbody></table></div></details>
        <section v-if="snapshot.usage.capacity" class="card space-y-2 p-4"><h2 class="font-semibold">{{ t('profitOps.capacity') }}</h2><p>{{ t('profitOps.account') }} {{ snapshot.usage.capacity.accounts }} · {{ t('profitOps.schedulable') }} {{ snapshot.usage.capacity.schedulable }} · {{ t('profitOps.concurrency') }} {{ snapshot.usage.capacity.configured_concurrency }}</p><p class="text-xs text-gray-500">{{ t('profitOps.capacityNote') }}</p></section>
        <section class="card space-y-3 p-4"><h2 class="font-semibold">{{ t('profitOps.quality') }}</h2><p class="text-sm text-gray-500">{{ t('profitOps.qualityNote') }}</p><p v-if="snapshot.quality.status !== 'ready'" role="alert">{{ snapshot.quality.error }}</p><template v-else-if="snapshot.quality.data"><p>{{ t('profitOps.rounds') }} {{ snapshot.quality.data.summary.rounds }} · {{ t('profitOps.passed') }} {{ snapshot.quality.data.summary.passed }} · {{ t('profitOps.degraded') }} {{ snapshot.quality.data.summary.degraded }} · {{ t('profitOps.inconclusive') }} {{ snapshot.quality.data.summary.inconclusive }}</p><div class="overflow-x-auto"><table class="w-full min-w-[650px] text-left text-sm"><tbody><tr v-for="round in snapshot.quality.data.rows" :key="round.id"><td class="p-2">#{{ round.account_id }}</td><td>{{ round.measured_model }}</td><td>{{ round.observed_at }}</td><td>{{ round.outcome }} ({{ round.round_probes }} / {{ round.round_expected ?? '—' }})</td><td><a class="underline text-primary-600" :href="`/admin/account-quality?plan_id=${round.plan_id}`">{{ t('profitOps.plan') }} #{{ round.plan_id }}</a></td></tr></tbody></table></div><p class="text-xs">{{ t('profitOps.pageScope') }} {{ applied.page }} · {{ snapshot.quality.data.total }}</p></template><a class="text-primary-600 underline" href="/admin/account-quality">{{ t('profitOps.quality') }}</a></section>
        <section class="card space-y-3 p-4"><h2 class="font-semibold">{{ t('profitOps.errors') }}</h2><p class="text-sm text-gray-500">{{ t('profitOps.errorNote') }}</p><p v-if="snapshot.errors.status !== 'ready'" role="alert">{{ snapshot.errors.error }}</p><template v-else-if="snapshot.errors.data"><p>{{ t('profitOps.errorRecords') }} {{ snapshot.errors.data.records }} · 5xx {{ snapshot.errors.data.upstream_5xx }}</p><p v-for="(row, i) in snapshot.errors.data.rows" :key="i" class="text-sm">{{ row.error_type }} / {{ row.error_phase }} / {{ row.status_code }}: {{ row.records }}</p></template><a class="text-primary-600 underline" href="/admin/ops">{{ t('profitOps.errors') }}</a></section>
      </template>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from './SmartOpsNav.vue'
import { readOperations, type OperationsQuery, type OperationsSnapshot, type OperationsRow, type ProfitMetrics } from '@/api/admin/upstreamOperations'
const props = defineProps<{ kind: 'profit' | 'upstream' }>()
const { t } = useI18n()
const amountKeys = ['requests','standard_charge','request_income','estimated_cost','estimated_profit','margin'] as const
const sortKeys = [...amountKeys, 'name']
const at = new Date(), start = new Date(at.getTime()-86400000)
const local = (d: Date) => new Date(d.getTime()-d.getTimezoneOffset()*60000).toISOString().slice(0,16)
const from=ref(local(start)), until=ref(local(at))
const initial: OperationsQuery = { from:start.toISOString(),until:at.toISOString(),upstream:'',account_id:0,group_id:0,model:'',model_scope:'public',dimension:'upstream',sort:'requests',direction:'desc',page:1,page_size:20 }
const draft=ref<OperationsQuery>({...initial}), applied=ref<OperationsQuery>({...initial})
const snapshot=ref<OperationsSnapshot|null>(null), loading=ref(false), error=ref('')
let request=0, alive=true, controller: AbortController|undefined
function number(v: number|null|undefined, decimals=6) { return v==null || !Number.isFinite(v) ? t('profitOps.unknown') : v.toLocaleString(undefined,{minimumFractionDigits:decimals,maximumFractionDigits:decimals}) }
function formatMetric(m:ProfitMetrics,key:typeof amountKeys[number]) { return key==='margin' ? (m.margin==null || !Number.isFinite(m.margin) ? t('profitOps.unknown') : `${(m.margin*100).toFixed(2)}%`) : number(m[key]) }
async function load() {
 const id=++request; controller?.abort(); controller=new AbortController(); loading.value=true; error.value=''; snapshot.value=null
 try { const next=await readOperations(props.kind,{...applied.value},controller.signal); if(alive && request===id) snapshot.value=next }
 catch { if(alive && request===id) error.value=t('profitOps.queryFailed') }
 finally { if(alive && request===id) loading.value=false }
}
function apply() { const a=new Date(from.value), b=new Date(until.value); if(!Number.isFinite(a.getTime())||!Number.isFinite(b.getTime())||b<=a||b.getTime()-a.getTime()>31*86400000){error.value=t('profitOps.invalidRange');return} applied.value={...draft.value,from:a.toISOString(),until:b.toISOString(),page:1};void load() }
function last24h(){const end=new Date();applied.value={...draft.value,from:new Date(end.getTime()-86400000).toISOString(),until:end.toISOString(),page:1};from.value=local(new Date(applied.value.from));until.value=local(end);void load()}
function page(delta:number){applied.value={...applied.value,page:applied.value.page+delta};void load()}
function drill(row:OperationsRow){if(applied.value.dimension==='upstream'){applied.value={...applied.value,upstream:row.key,account_id:0,dimension:'account',page:1}}else if(applied.value.dimension==='account'){applied.value={...applied.value,account_id:Number(row.key),dimension:'model',page:1}}draft.value={...applied.value};void load()}
function selectUpstream(key:string){applied.value={...applied.value,upstream:key,account_id:0,dimension:'account',page:1};draft.value={...applied.value};void load()}
function overview(){applied.value={...applied.value,upstream:'',account_id:0,dimension:'upstream',page:1};draft.value={...applied.value};void load()}
onMounted(load)
onBeforeUnmount(()=>{alive=false;request++;controller?.abort()})
</script>
