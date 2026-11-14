<template>
  <fieldset :disabled="disabled" class="space-y-3 rounded-lg border border-gray-100 p-4 dark:border-dark-700" data-testid="moderation-upstream-selector">
    <h3 class="flex items-center gap-2 text-base font-semibold">{{ t('admin.riskControl.upstreamAccounts') }}<slot name="badge" /></h3>
    <p class="text-sm text-gray-500">{{ t('admin.riskControl.upstreamAccountsHint') }}</p>
    <div class="flex flex-wrap gap-4 text-sm">
      <label class="flex items-center gap-2"><input v-model="scope" type="radio" value="all" data-testid="moderation-upstream-all" />{{ t('admin.riskControl.allUpstreamAccounts') }}</label>
      <label class="flex items-center gap-2"><input v-model="scope" type="radio" value="selected" data-testid="moderation-upstream-selected" />{{ t('admin.riskControl.selectedUpstreamAccounts') }}</label>
    </div>
    <template v-if="scope === 'selected'">
      <details v-if="selected.length" class="rounded border border-gray-200 p-2 dark:border-dark-600" data-testid="moderation-selected-ids">
        <summary class="cursor-pointer text-sm">{{ t('admin.riskControl.manageSelectedUpstream', { count: selected.length }) }}</summary>
        <label class="mt-2 block text-sm">{{ t('admin.riskControl.findSelectedUpstream') }}<input v-model="selectedSearch" type="search" class="input mt-1" data-testid="moderation-selected-search" /></label>
        <div class="mt-2 flex max-h-32 flex-wrap gap-2 overflow-auto">
          <button v-for="id in visibleSelected" :key="id" type="button" class="btn btn-secondary" :data-testid="`moderation-remove-${id}`" @click="remove(id)">#{{ id }} · {{ t('common.remove') }}</button>
        </div>
        <p class="mt-1 text-xs text-gray-500">{{ t('admin.riskControl.selectedUpstreamRecoveryHint') }}</p>
      </details>
      <p v-if="!selected.length" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.riskControl.emptyUpstreamAccounts') }}</p>
      <QualityAccountSelector
        v-model="selected" v-model:search="search" v-model:group="group" v-model:type="type"
        v-model:statuses="statuses" v-model:scope="manualScope"
        :accounts="accounts" :groups="groups" :accounts-loading="loading" :selecting-accounts="selecting"
        :accounts-error="error" :account-page="page" :account-pages="pages" :accounts-total="total"
        :bulk="false" hide-scope hide-status-filter :type-options="typeOptions" :disabled-reason="disabledReason"
        :label="t('admin.riskControl.findUpstreamAccounts')" search-id="moderation-upstream-search"
        :selection-hint="t('admin.riskControl.upstreamSelectionHint')"
        @search="loadPage" @select-page="selectPage" @select-all="selectAll" @clear="clear"
      >
        <template #account-details="{ account }"><span class="ml-1 text-xs text-gray-500">{{ account.platform }} · {{ account.type }}</span></template>
      </QualityAccountSelector>
    </template>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AccountListItem } from '@/types'
import QualityAccountSelector from '@/components/admin/operations/QualityAccountSelector.vue'
import { qualityAccountTypeOptions } from '@/utils/qualityAccountFilter'

const { t } = useI18n()
const emit = defineEmits<{ busy: [value: boolean] }>()
const props = defineProps<{ groups: { id: number; name: string }[]; disabled?: boolean }>()
const selected = defineModel<number[]>({ required: true })
const scope = defineModel<'all' | 'selected'>('scope', { required: true })
const search = ref(''), group = ref(''), type = ref(''), statuses = ref<string[]>([]), manualScope = ref<'all' | 'manual'>('manual')
const accounts = ref<AccountListItem[]>([]), page = ref(1), pages = ref(1), total = ref(0)
const loading = ref(false), selecting = ref(false), error = ref('')
const typeOptions = [...qualityAccountTypeOptions,
  { value: 'upstream', label: 'admin.riskControl.upstreamType' },
  { value: 'bedrock', label: 'admin.riskControl.bedrockType' },
  { value: 'service_account', label: 'admin.riskControl.serviceAccountType' },
]
const disabledReason = (account: AccountListItem) => account.moderation_scope_eligible === true ? '' : t('admin.riskControl.ineligibleUpstreamAccount')
const selectedSearch = ref('')
const visibleSelected = computed(() => selected.value.filter(id => !selectedSearch.value.trim() || String(id) === selectedSearch.value.trim().replace(/^#/, '')).slice(0, 50))
function remove(id: number) { if (props.disabled) return; invalidate(); selected.value = selected.value.filter(value => value !== id) }
let revision = 0, controller: AbortController | undefined
function invalidate() { revision++; controller?.abort(); loading.value = false; selecting.value = false }
function filters() { return { search: search.value.trim(), group: group.value, type: type.value, lite: 'true' } }
async function loadPage(target = 1) {
  invalidate()
  accounts.value = []; page.value = 1; pages.value = 1; total.value = 0
  if (scope.value !== 'selected') return
  const ticket = revision, params = filters()
  controller = new AbortController()
  loading.value = true; error.value = ''
  try {
    const result = await adminAPI.accounts.list(target, 50, params, { signal: controller.signal })
    if (ticket !== revision) return
    accounts.value = result.items; page.value = result.page; pages.value = Math.max(1, result.pages); total.value = result.total
  } catch {
    if (ticket === revision) error.value = t('admin.riskControl.upstreamAccountsLoadFailed')
  } finally { if (ticket === revision) loading.value = false }
}
function selectPage() { if (props.disabled || loading.value || error.value) return; selected.value = [...new Set([...selected.value, ...accounts.value.filter(a => !disabledReason(a)).map(a => a.id)])] }
function clear() { if (props.disabled) return; invalidate(); selected.value = [] }
async function selectAll() {
  if (props.disabled) return
  invalidate()
  const ticket = revision, params = filters(), ids = new Set(selected.value)
  controller = new AbortController(); selecting.value = true; error.value = ''
  try {
    for (let target = 1; ; target++) {
      const result = await adminAPI.accounts.list(target, 100, params, { signal: controller.signal })
      if (ticket !== revision) return
      for (const account of result.items) if (!disabledReason(account)) ids.add(account.id)
      if (ids.size > 10000) throw Error('selection limit')
      if (target >= result.pages) break
    }
    selected.value = [...ids]
  } catch { if (ticket === revision) error.value = t('admin.riskControl.upstreamAccountsLoadFailed') }
  finally { if (ticket === revision) selecting.value = false }
}
// Filters only discover accounts; selection is an independent persistent ID set.
watch([search, group, type, scope], () => { void loadPage(1) }, { immediate: true, flush: 'sync' })
watch(selecting, value => emit('busy', value), { flush: 'sync' })
onBeforeUnmount(() => { invalidate(); emit('busy', false) })
</script>
