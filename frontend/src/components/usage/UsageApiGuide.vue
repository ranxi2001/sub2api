<template>
  <article class="card min-w-0 space-y-6 p-4 text-sm text-gray-700 dark:text-gray-300 sm:p-6">
    <div class="space-y-2">
      <p>{{ t('usageApi.intro') }}</p>
      <code class="block break-all rounded-lg bg-gray-100 p-3 text-gray-900 dark:bg-dark-800 dark:text-gray-100">GET {{ endpoint }}</code>
      <p>{{ t('usageApi.auth') }}</p>
    </div>

    <section>
      <h2 class="mb-2 font-semibold">{{ t('usageApi.parameters') }}</h2>
      <table class="w-full table-fixed text-left">
        <thead class="text-gray-500 dark:text-gray-400">
          <tr><th scope="col" class="w-28 py-2">{{ t('usageApi.name') }}</th><th scope="col">{{ t('usageApi.description') }}</th></tr>
        </thead>
        <tbody>
          <tr v-for="name in parameters" :key="name" class="border-t border-gray-200 dark:border-dark-700">
            <th scope="row" class="py-2 align-top font-mono font-normal">{{ name }}</th>
            <td class="break-words py-2">{{ t('usageApi.params.' + name) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <section v-for="example in examples" :key="example.id" class="min-w-0 space-y-2">
      <div class="flex items-center justify-between gap-2">
        <h2 class="font-semibold">{{ t('usageApi.examples.' + example.id) }}</h2>
        <button type="button" class="btn btn-secondary btn-sm" @click="copyToClipboard(example.code)">{{ t('common.copy') }}</button>
      </div>
      <p v-if="example.id === 'csv'">{{ t('usageApi.csvHelp') }}</p>
      <p v-if="example.id === 'python'">{{ t('usageApi.pythonHelp') }}</p>
      <pre class="max-w-full overflow-x-auto rounded-lg bg-gray-100 p-3 text-xs leading-relaxed text-gray-900 dark:bg-dark-800 dark:text-gray-100"><code>{{ example.code }}</code></pre>
    </section>

    <section class="space-y-2">
      <h2 class="font-semibold">{{ t('usageApi.matchTitle') }}</h2>
      <p>{{ t('usageApi.match') }}</p>
      <p>{{ t('usageApi.exceptions') }}</p>
    </section>
    <section class="space-y-2">
      <h2 class="font-semibold">{{ t('usageApi.reconcileTitle') }}</h2>
      <ul class="list-disc space-y-1 pl-5">
        <li v-for="note in notes" :key="note">{{ t('usageApi.' + note) }}</li>
      </ul>
    </section>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { normalizeCodexBaseUrl } from '@/utils/codexQuickConfig'
import pythonExample from './usage_records.py?raw'

const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()
const endpoint = computed(() => normalizeCodexBaseUrl(appStore.cachedPublicSettings?.api_base_url?.trim() || window.location.origin) + '/usage/records')
const parameters = ['start_time', 'end_time', 'limit', 'cursor', 'format'] as const
const notes = ['availability', 'regional', 'delays', 'overlap', 'settlement', 'retention'] as const
const end = new Date()
end.setUTCMinutes(0, 0, 0)
const start = new Date(end.getTime() - 3600_000)
const response = {
  object: 'list',
  data: [{
    id: 42, request_id: 'client:example', created_at: start.toISOString(),
    model: 'example-model', request_type: 'stream', stream: true, input_tokens: 120, output_tokens: 30,
    cache_read_tokens: 0, cache_creation_tokens: 0, image_count: 0,
    total_cost: 0.002, actual_cost: 0.001, rate_multiplier: 0.5, duration_ms: 730, first_token_ms: 95,
  }],
  has_more: false, next_cursor: null, start_time: start.toISOString(), end_time: end.toISOString(),
}

const examples = computed(() => {
  const url = "'" + endpoint.value.replace(/'/g, "'\\''") + "'"
  const curl = [
    'curl --fail-with-body -sS -G ' + url,
    '  -H "Authorization: Bearer $SUB2API_KEY"',
    "  --data-urlencode 'start_time=" + start.toISOString() + "'",
    "  --data-urlencode 'end_time=" + end.toISOString() + "'",
  ].join(' \\\n')
  return [
    { id: 'curl', code: curl },
    { id: 'json', code: JSON.stringify(response, null, 2) },
    { id: 'csv', code: curl + " \\\n  --data-urlencode 'format=csv' -D usage.headers -o usage.csv" },
    { id: 'python', code: pythonExample.replace('os.environ["SUB2API_USAGE_URL"]', () => `os.environ.get("SUB2API_USAGE_URL", ${JSON.stringify(endpoint.value)})`).trimEnd() },
  ]
})
</script>
