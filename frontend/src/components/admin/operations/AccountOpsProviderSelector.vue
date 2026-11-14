<template>
  <details class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <summary class="cursor-pointer text-sm">{{ t('accountOps.linkProvider') }}<span v-if="modelValue"> #{{ modelValue }}</span></summary>
    <p class="my-2 text-xs text-gray-500">{{ t('accountOps.providerBalanceHint') }}</p>
    <div class="flex gap-2"><input v-model="search" class="input min-w-0 flex-1" :aria-label="t('accountOps.search')" /><button type="button" class="btn btn-secondary" :disabled="busy || disabled" @click="load(1)">{{ t('accountOps.loadProviders') }}</button></div>
    <select :value="modelValue" :disabled="busy || disabled" class="input mt-2 w-full" :aria-label="t('accountOps.linkProvider')" data-testid="ops-provider-select" @focus="ensureLoaded" @change="emit('update:modelValue', Number(($event.target as HTMLSelectElement).value))">
      <option :value="0">{{ t('accountOps.memberBalanceSource') }}</option>
      <option v-if="modelValue && !items.some(p => p.id === modelValue)" :value="modelValue">#{{ modelValue }}</option>
      <option v-for="provider in items" :key="provider.id" :value="provider.id">{{ provider.name }} · #{{ provider.id }}</option>
    </select>
    <div v-if="loaded" class="mt-2 flex gap-3 text-xs"><button type="button" :disabled="busy || disabled || page === 1" @click="load(page - 1)">{{ t('profitOps.previous') }}</button><span>{{ page }}</span><button type="button" :disabled="busy || disabled || page * 20 >= total" @click="load(page + 1)">{{ t('profitOps.next') }}</button></div>
    <p v-if="error" role="alert" class="mt-2 text-xs text-red-600">{{ t('accountOps.providersUnavailable') }}</p>
  </details>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { listProviders, type MatureProvider } from '@/api/admin/matureProviders'
const props = defineProps<{ modelValue: number; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: number] }>()
const { t } = useI18n()
const items = ref<MatureProvider[]>([]), search = ref(''), page = ref(1), total = ref(0), busy = ref(false), loaded = ref(false), error = ref(false)
let alive = true
onBeforeUnmount(() => { alive = false })
function ensureLoaded() { if (!loaded.value) void load(1) }
async function load(next: number) {
  if (busy.value || props.disabled) return
  busy.value = true; error.value = false
  try { const result = await listProviders({ search: search.value, status: '', page: next, page_size: 20 }); if (alive) { items.value = result.items; total.value = result.total; page.value = next; loaded.value = true } }
  catch { if (alive) error.value = true }
  finally { if (alive) busy.value = false }
}
</script>
