<template>
  <section class="mb-4 space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <button type="button" class="btn btn-secondary" data-testid="batch-upstream-start"
      :disabled="disabled || busy || !valid" @click="run">
      {{ t('admin.accounts.batchUpstream.start') }}
    </button>
    <p class="text-xs text-gray-600 dark:text-gray-400">{{ t('admin.accounts.batchUpstream.hint') }}</p>
    <ul v-if="rows.length" class="max-h-40 overflow-auto text-sm" aria-live="polite">
      <li v-for="row in rows" :key="row.id" :data-sync-account="row.id">
        #{{ row.id }} · {{ t('admin.accounts.batchUpstream.' + row.state) }}
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'

const props = defineProps<{ accountIds: number[]; disabled: boolean }>()
const emit = defineEmits<{ started: [] }>()
const { t } = useI18n()
const ids = [...props.accountIds]
const valid = ids.length > 0 && ids.every(id => Number.isSafeInteger(id) && id > 0) && new Set(ids).size === ids.length
const busy = ref(false)
const rows = ref<{ id: number; state: string }[]>([])

async function run() {
  if (!valid || busy.value || props.disabled) return
  busy.value = true
  emit('started')
  rows.value = ids.map(id => ({ id, state: 'pending' }))
  try {
    for (const row of rows.value) {
      try {
        const result = await adminAPI.accounts.syncUpstreamModels(row.id, { apply: true, whitelist_only: true })
        row.state = result.applied ? 'saved' : 'failed'
      } catch {
        row.state = 'failed'
      }
    }
  } finally {
    busy.value = false
  }
}
</script>
