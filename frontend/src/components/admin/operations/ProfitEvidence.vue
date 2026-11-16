<template>
  <table class="w-full text-left text-sm tabular-nums">
    <caption class="sr-only">{{ t('profitOps.evidence') }}</caption>
    <tbody>
      <tr v-for="field in fields" :key="field" class="border-t dark:border-dark-700">
        <th scope="row" class="py-1 pr-4 font-normal">{{ t(`profitOps.${field}`) }}</th>
        <td class="py-1 text-right" :data-field="field">{{ format(field) }}</td>
      </tr>
    </tbody>
  </table>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProfitMetrics } from '@/api/admin/profitOperations'

const props = defineProps<{ metrics: ProfitMetrics }>()
const { t, locale } = useI18n()
const fields = ['reported_usage_requests', 'reported_usage_cost', 'reconciled_requests', 'covered_reconciled_cost', 'reconciled_cost', 'reconciled_profit'] as const
function format(field: typeof fields[number]) {
  const m = props.metrics
  const value = m[field]
  if (value == null || !Number.isFinite(value)) return t('profitOps.unknown')
  if (field.endsWith('_requests')) return `${value} / ${m.requests}`
  if (field === 'reported_usage_cost' && !(Number(m.reported_usage_requests) > 0)) return t('profitOps.unknown')
  if (field === 'covered_reconciled_cost' && !(Number(m.reconciled_requests) > 0)) return t('profitOps.unknown')
  if ((field === 'reconciled_cost' || field === 'reconciled_profit') && !(m.requests > 0 && m.requests === m.reconciled_requests)) return t('profitOps.unknown')
  return value.toLocaleString(locale.value, { maximumSignificantDigits: 12 })
}
</script>
