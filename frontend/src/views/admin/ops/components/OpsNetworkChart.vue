<template>
  <section class="card space-y-3 p-4" data-testid="ops-network-chart">
    <h3 class="font-semibold">{{ t('latencyObservability.networkTitle') }}</h3>
    <p class="text-xs text-gray-500">{{ t('latencyObservability.networkScope') }}</p>
    <p v-if="error" role="alert">{{ t('latencyObservability.error') }}</p>
    <p v-else-if="!data?.points.length">{{ t('latencyObservability.empty') }}</p>
    <label v-if="instances.length" class="text-sm">{{ t('latencyObservability.instance') }}
      <select v-model="selected" class="input ml-2"><option v-for="(id, i) in instances" :key="id" :value="id">{{ i + 1 }} · {{ id.slice(0, 8) }}</option></select>
    </label>
    <div class="grid gap-4 md:grid-cols-2">
      <div v-for="series in curves" :key="series.key" :data-testid="`network-${series.key}`">
        <h4 class="text-sm">{{ t(`latencyObservability.${series.key}`) }}</h4>
        <svg viewBox="0 0 600 100" class="h-28 w-full" role="img" :aria-label="t(`latencyObservability.${series.key}`)">
          <path :d="path(series.values)" fill="none" stroke="currentColor" stroke-width="2" class="text-sky-600" />
          <circle v-for="(v, i) in series.values" :key="i" :cx="x(i, series.values)" :cy="y(v.value, series.values)" :r="v.value == null ? 0 : 3" class="fill-sky-600"><title>{{ v.at }} · {{ v.value ?? t('latencyObservability.notCollected') }} · {{ v.status }}</title></circle>
        </svg>
        <p class="text-xs text-gray-500">{{ series.values.at(-1)?.value ?? t('latencyObservability.notCollected') }} · {{ series.values.at(-1)?.status ?? t('latencyObservability.notCollected') }}</p>
        <p v-if="series.values.length" class="flex justify-between text-xs text-gray-400"><span>{{ series.values[0]?.at.slice(11, 16) }} UTC</span><span>{{ series.values.at(-1)?.at.slice(11, 16) }} UTC</span></p>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getNetworkSeries, type NetworkSeries } from '@/api/admin/opsNetwork'
const props = defineProps<{ refreshToken?: number }>()
const { t } = useI18n()
const data = ref<NetworkSeries | null>(null), error = ref(false), selected = ref('')
const instances = computed(() => [...new Set(data.value?.points.map(p => p.instance) ?? [])])
const points = computed(() => data.value?.points.filter(p => p.instance === selected.value) ?? [])
interface Value { at: string; value: number | null; status: string }
const curves = computed(() => [
  { key: 'egress', values: points.value.map(p => ({ at: p.at, value: p.egress_mbps, status: p.egress_status })) },
  { key: 'retrans', values: points.value.map(p => ({ at: p.at, value: p.tcp_retrans_percent, status: p.tcp_status })) },
  { key: 'qdisc', values: points.value.map(p => ({ at: p.at, value: p.qdisc_drops, status: p.qdisc_status })) },
])
const x = (i: number, values: Value[]) => {
  const start = Date.parse(values[0]?.at ?? '')
  const end = Date.parse(values.at(-1)?.at ?? '')
  const value = Date.parse(values[i]?.at ?? '')
  return Number.isFinite(start + end + value) ? 10 + (value - start) * 580 / Math.max(60000, end - start) : 10
}
const y = (value: number | null, values: Value[]) => 90 - (value ?? 0) * 80 / Math.max(1, ...values.map(v => v.value ?? 0))
const path = (values: Value[]) => {
  let result = '', connected = false
  values.forEach((v, i) => {
    if (v.value == null || (i > 0 && Date.parse(v.at) - Date.parse(values[i - 1]!.at) > 90000)) connected = false
    if (v.value != null) { result += `${connected ? 'L' : 'M'}${x(i, values)},${y(v.value, values)} `; connected = true }
  })
  return result
}
let controller: AbortController | undefined
watch(() => props.refreshToken, async () => {
  controller?.abort(); const current = new AbortController(); controller = current; error.value = false
  try {
    const result = await getNetworkSeries(current.signal)
    if (current === controller && !current.signal.aborted) {
      data.value = result
      if (!instances.value.includes(selected.value)) selected.value = instances.value[0] ?? ''
    }
  } catch { if (current === controller && !current.signal.aborted) error.value = true }
}, { immediate: true })
onUnmounted(() => { controller?.abort(); controller = undefined })
</script>
