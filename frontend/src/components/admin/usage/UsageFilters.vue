<template>
  <div :class="identityOnly ? 'w-full lg:w-auto' : flat ? 'p-4 sm:p-6' : 'card p-6'">
    <!-- Toolbar: left filters (multi-line) + right actions -->
    <div class="flex flex-wrap items-end justify-between gap-4">
      <!-- Left: filters (allowed to wrap to multiple rows) -->
      <div class="flex flex-1 flex-wrap items-end gap-4">
        <!-- User Search -->
        <div v-if="!observerMode" ref="userSearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[240px]">
          <label class="input-label">{{ t('admin.usage.userFilter') }}</label>
          <input
            v-model="userKeyword"
            type="text"
            class="input pr-8"
            :placeholder="t('admin.usage.searchUserPlaceholder')"
            @input="debounceUserSearch"
            @focus="showUserDropdown = true"
          />
          <button
            v-if="filters.user_id"
            type="button"
            @click="clearUser"
            class="absolute right-2 top-9 text-gray-400"
            aria-label="Clear user filter"
          >
            ✕
          </button>
          <div
            v-if="showUserDropdown && (userResults.length > 0 || userKeyword)"
            class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:bg-dark-800"
          >
            <button
              v-for="u in userResults"
              :key="u.id"
              type="button"
              @click="selectUser(u)"
              class="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-dark-700"
            >
              <span>{{ u.email }}<span v-if="u.deleted" class="ml-1 text-xs text-gray-400">（{{ t('admin.usage.userDeletedBadge') }}）</span></span>
              <span class="ml-2 text-xs text-gray-400">#{{ u.id }}</span>
            </button>
          </div>
        </div>

        <!-- API Key Search -->
        <div ref="apiKeySearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[240px]">
          <label class="input-label">{{ t('usage.apiKeyFilter') }}</label>
          <input
            v-model="apiKeyKeyword"
            type="text"
            class="input pr-8"
            :placeholder="t('admin.usage.searchApiKeyPlaceholder')"
            @input="debounceApiKeySearch"
            @focus="onApiKeyFocus"
          />
          <button
            v-if="filters.api_key_id"
            type="button"
            @click="onClearApiKey"
            class="absolute right-2 top-9 text-gray-400"
            aria-label="Clear API key filter"
          >
            ✕
          </button>
          <div
            v-if="showApiKeyDropdown && apiKeyResults.length > 0"
            class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:bg-dark-800"
          >
            <button
              v-for="k in apiKeyResults"
              :key="k.id"
              type="button"
              @click="selectApiKey(k)"
              class="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-dark-700"
            >
              <span class="truncate">{{ k.name || `#${k.id}` }}</span>
              <span v-if="k.deleted" class="ml-1 text-xs text-gray-400">({{ t('traffic.deletedKey') }})</span>
              <span class="ml-2 text-xs text-gray-400">#{{ k.id }}</span>
            </button>
          </div>
          <p v-if="apiKeySearchError" role="alert" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ t('traffic.keysFailed') }}</p>
        </div>

        <template v-if="!identityOnly">
          <div v-if="mode === 'usage' && !observerMode" data-testid="usage-platform-filter" class="w-full sm:w-52">
            <label for="usage-platform" class="input-label flex items-center gap-1.5">
              {{ t('admin.usage.platform') }}
              <NewFeatureBadge v-if="noticeUserId != null" :notice-id="NEW_FEATURE_NOTICES.adminUsagePlatform" :user-id="noticeUserId" />
            </label>
            <Select
              id="usage-platform"
              :model-value="filters.platform ?? null"
              :options="platformOptions"
              :aria-label="t('admin.usage.platform')"
              aria-describedby="usage-platform-hint"
              searchable
              @update:model-value="filters.platform = $event || undefined"
              @change="emitChange"
            />
          </div>

          <!-- Model Filter -->
          <div class="w-full sm:w-auto sm:min-w-[220px]">
            <label class="input-label">{{ t('usage.model') }}</label>
            <Select v-model="filters.model" :options="modelOptions" searchable @change="emitChange" />
          </div>

          <!-- Account Filter -->
          <div ref="accountSearchRef" class="usage-filter-dropdown relative w-full sm:w-auto sm:min-w-[220px]">
            <label class="input-label">{{ t('admin.usage.account') }}</label>
            <input
              v-model="accountKeyword"
              type="text"
              class="input pr-8"
              :placeholder="t('admin.usage.searchAccountPlaceholder')"
              @input="debounceAccountSearch"
              @focus="showAccountDropdown = true"
            />
            <button
              v-if="filters.account_id"
              type="button"
              @click="clearAccount"
              class="absolute right-2 top-9 text-gray-400"
              aria-label="Clear account filter"
            >
              ✕
            </button>
            <div
              v-if="showAccountDropdown && (accountResults.length > 0 || accountKeyword)"
              class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:bg-dark-800"
            >
              <button
                v-for="a in accountResults"
                :key="a.id"
                type="button"
                @click="selectAccount(a)"
                class="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-dark-700"
              >
                <span class="truncate">{{ a.name }}</span>
                <span class="ml-2 text-xs text-gray-400">#{{ a.id }}</span>
              </button>
            </div>
          </div>

          <!-- Request Type Filter (usage only) -->
          <div v-if="mode !== 'errors' && mode !== 'connections' && mode !== 'latency'" class="w-full sm:w-auto sm:min-w-[180px]">
            <label class="input-label">{{ t('usage.type') }}</label>
            <Select v-model="filters.request_type" :options="requestTypeOptions" @change="emitChange" />
          </div>

          <!-- Native compaction is independent of the transport request type. -->
          <div v-if="mode !== 'errors' && mode !== 'connections' && mode !== 'latency'" class="w-full sm:w-auto sm:min-w-[180px]">
            <label class="input-label">{{ t('usage.compactionFilter') }}</label>
            <Select v-model="filters.native_compaction_v2" :options="compactionOptions" @change="emitChange" />
          </div>

          <!-- Billing Type Filter (usage only) -->
          <div v-if="mode !== 'errors' && mode !== 'connections' && mode !== 'latency'" class="w-full sm:w-auto sm:min-w-[200px]">
            <label class="input-label">{{ t('admin.usage.billingType') }}</label>
            <Select v-model="filters.billing_type" :options="billingTypeOptions" @change="emitChange" />
          </div>

          <!-- Billing Mode Filter (usage only；用户排行的 user-breakdown 接口不支持该维度) -->
          <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[200px]">
            <label class="input-label">{{ t('admin.usage.billingMode') }}</label>
            <Select v-model="filters.billing_mode" :options="billingModeOptions" @change="emitChange" />
          </div>

          <div v-if="mode === 'usage'" class="w-full sm:w-auto sm:min-w-[220px]">
            <label class="input-label">{{ t('admin.usage.upstreamModelAudit') }}</label>
            <Select v-model="filters.upstream_model_mismatch" :options="upstreamModelMismatchOptions" @change="emitChange" />
          </div>

          <!-- Error Phase Filter (errors only) -->
          <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
            <label class="input-label">{{ t('admin.ops.errorLog.type') }}</label>
            <Select v-model="filters.error_phase" :options="errorPhaseOptions" @change="emitChange" />
          </div>

          <!-- Error Category Filter (errors only) -->
          <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
            <label class="input-label">{{ t('usage.errors.category') }}</label>
            <Select v-model="filters.error_category" :options="errorCategoryOptions" @change="emitChange" />
          </div>

          <!-- Status Code Filter (errors only) -->
          <div v-if="mode === 'errors'" class="w-full sm:w-auto sm:min-w-[180px]">
            <label class="input-label">{{ t('admin.ops.errorLog.status') }}</label>
            <Select v-model="filters.status_code" :options="statusCodeOptions" @change="emitChange" />
          </div>

          <!-- Group Filter -->
          <div class="w-full sm:w-auto sm:min-w-[200px]">
            <label class="input-label">{{ t('admin.usage.group') }}</label>
            <Select v-model="filters.group_id" :options="groupOptions" searchable @change="emitChange" />
          </div>
        </template>

      </div>

      <!-- Right: actions -->
      <div v-if="showActions" class="flex w-full flex-wrap items-center justify-end gap-3 sm:w-auto">
        <button type="button" @click="$emit('refresh')" class="btn btn-secondary">
          {{ t('common.refresh') }}
        </button>
        <button type="button" @click="$emit('reset')" class="btn btn-secondary">
          {{ t('common.reset') }}
        </button>
        <slot name="after-reset" />
        <template v-if="mode === 'usage'">
          <button v-if="!observerMode" type="button" @click="$emit('cleanup')" :disabled="!!filters.platform" :title="filters.platform ? t('admin.usage.platformCleanupUnavailable') : undefined" class="btn btn-danger">
            {{ t('admin.usage.cleanup.button') }}
          </button>
          <button type="button" @click="$emit('export')" :disabled="exporting" class="btn btn-primary">
            {{ t('usage.exportExcel') }}
          </button>
        </template>
      </div>
    </div>
    <p v-if="!identityOnly && mode === 'usage' && !observerMode" id="usage-platform-hint" class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.usage.platformHistoryHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, toRef, watch, computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { observerUsageAPI, type UsageFilterOption } from '@/api/observerUsage'
import { observerUsageContext } from './observerUsageContext'
import { adminAPI } from '@/api/admin'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import { COMMON_ERROR_STATUS_CODES } from '@/utils/errorBadges'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { SimpleUser } from '@/api/admin/usage'

type ModelValue = Record<string, any>

interface Props {
  modelValue: ModelValue
  noticeUserId?: number
  exporting: boolean
  startDate: string
  endDate: string
  showActions?: boolean
  modelOptions?: string[]
  /**
   * errors 模式:隐藏用量专属字段/按钮,显示错误类型+状态码(错误请求 tab 用)
   * ranking 模式:同 usage 但隐藏计费模式筛选与清理/导出按钮(用户排行 tab 用)
   * latency 模式:仅保留延迟报告接口支持的身份、模型、账号和分组筛选
   */
  mode?: 'usage' | 'errors' | 'ranking' | 'connections' | 'latency'
  /** 嵌入统一卡片内使用：去掉自身卡片外观 */
  flat?: boolean
  /** Reuse user/key search without token, account, model or group filters. */
  identityOnly?: boolean
  apiKeySearch?: (userId: number | undefined, query: string) => Promise<UsageFilterOption[]>
}

const props = withDefaults(defineProps<Props>(), {
  showActions: true,
  mode: 'usage',
  flat: false,
  identityOnly: false
})
const emit = defineEmits([
  'update:modelValue',
  'change',
  'refresh',
  'reset',
  'export',
  'cleanup'
])

const { t } = useI18n()
const observerMode = inject(observerUsageContext, false)
const filters = toRef(props, 'modelValue')

const userSearchRef = ref<HTMLElement | null>(null)
const apiKeySearchRef = ref<HTMLElement | null>(null)
const accountSearchRef = ref<HTMLElement | null>(null)

const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
const showUserDropdown = ref(false)
let userSearchTimeout: ReturnType<typeof setTimeout> | null = null
let userSearchSequence = 0

const apiKeyKeyword = ref('')
const apiKeyResults = ref<UsageFilterOption[]>([])
const showApiKeyDropdown = ref(false)
const apiKeySearchError = ref(false)
let apiKeySearchTimeout: ReturnType<typeof setTimeout> | null = null
let apiKeySearchSequence = 0

interface SimpleAccount {
  id: number
  name: string
}
const accountKeyword = ref('')
const accountResults = ref<SimpleAccount[]>([])
const showAccountDropdown = ref(false)
let accountSearchTimeout: ReturnType<typeof setTimeout> | null = null

const modelOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allModels') },
  ...(props.modelOptions ?? []).map((m) => ({ value: m, label: m })),
])
const platformOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allPlatforms') },
  ...CONCRETE_PLATFORM_OPTIONS,
  { value: 'unknown', label: t('admin.usage.unknownPlatform') },
])
const groupOptions = ref<SelectOption[]>([{ value: null, label: t('admin.usage.allGroups') }])

const requestTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'ws_v2', label: t('usage.ws') },
  { value: 'live', label: t('usage.live') },
  { value: 'stream', label: t('usage.stream') },
  { value: 'sync', label: t('usage.sync') },
  { value: 'cyber', label: t('usage.cyber') }
])

const compactionOptions = ref<SelectOption[]>([
  { value: null, label: t('usage.allCompactionTypes') },
  { value: true, label: t('usage.compactionOnly') }
])

const billingTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingTypes') },
  { value: 0, label: t('admin.usage.billingTypeBalance') },
  { value: 1, label: t('admin.usage.billingTypeSubscription') }
])

// 错误类型对应后端 phase 参数(与错误表"类型"徽章同语义)
const errorPhaseOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'upstream', label: t('admin.ops.errorLog.typeUpstream') },
  { value: 'account_auth', label: t('admin.ops.errorLog.typeAccountAuth') },
  { value: 'request', label: t('admin.ops.errorLog.typeRequest') },
  { value: 'auth', label: t('admin.ops.errorLog.typeAuth') },
  { value: 'routing', label: t('admin.ops.errorLog.typeRouting') },
  { value: 'internal', label: t('admin.ops.errorLog.typeInternal') },
])

// 分类码同用户端 /usage 错误筛选;"other" 无法反查为过滤条件,刻意不列
const errorCategoryCodes = ['auth', 'rate_limit', 'quota', 'invalid_request', 'service_unavailable', 'upstream', 'internal', 'cyber']

const errorCategoryOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allCategories') },
  ...errorCategoryCodes.map((c) => ({ value: c, label: t('usage.errors.categories.' + c) })),
])

const statusCodeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allStatuses') },
  ...COMMON_ERROR_STATUS_CODES.map((c) => ({ value: c, label: String(c) })),
])

const billingModeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingModes') },
  { value: 'token', label: t('admin.usage.billingModeToken') },
  { value: 'per_request', label: t('admin.usage.billingModePerRequest') },
  { value: 'image', label: t('admin.usage.billingModeImage') },
  { value: 'video', label: t('admin.usage.billingModeVideo') }
])

const upstreamModelMismatchOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allUpstreamModelAudit') },
  { value: true, label: t('admin.usage.upstreamModelMismatchOnly') },
  { value: false, label: t('admin.usage.upstreamModelMatchedOnly') }
])

const emitChange = () => emit('change')

const clearPendingUserSearch = () => {
  if (userSearchTimeout) {
    clearTimeout(userSearchTimeout)
    userSearchTimeout = null
  }
  userSearchSequence += 1
}

const debounceUserSearch = () => {
  if (observerMode) return
  clearPendingUserSearch()
  const query = userKeyword.value.trim()
  if (!query) {
    userResults.value = []
    return
  }

  const sequence = userSearchSequence
  userSearchTimeout = setTimeout(async () => {
    userSearchTimeout = null
    try {
      const results = await adminAPI.usage.searchUsers(query)
      if (sequence === userSearchSequence) {
        userResults.value = results.sort((a, b) => Number(a.deleted) - Number(b.deleted))
      }
    } catch {
      if (sequence === userSearchSequence) {
        userResults.value = []
      }
    }
  }, 300)
}

const clearPendingApiKeySearch = () => {
  if (apiKeySearchTimeout) {
    clearTimeout(apiKeySearchTimeout)
    apiKeySearchTimeout = null
  }
  apiKeySearchSequence += 1
}

const searchApiKeys = async (userId: number | undefined, query: string, sequence: number) => {
  try {
    const results = props.apiKeySearch && !observerMode
      ? await props.apiKeySearch(userId, query)
      : observerMode
      ? await observerUsageAPI.filterOptions('api_key', query)
      : await adminAPI.usage.searchApiKeys(userId, query)
    if (sequence === apiKeySearchSequence && userId === filters.value.user_id) { apiKeyResults.value = results; apiKeySearchError.value = false }
  } catch {
    if (sequence === apiKeySearchSequence && userId === filters.value.user_id) { apiKeyResults.value = []; apiKeySearchError.value = Boolean(props.apiKeySearch) }
  }
}

const debounceApiKeySearch = () => {
  clearPendingApiKeySearch()
  apiKeyResults.value = []
  const sequence = apiKeySearchSequence
  const userId = filters.value.user_id
  const query = apiKeyKeyword.value || ''
  apiKeySearchTimeout = setTimeout(() => {
    apiKeySearchTimeout = null
    void searchApiKeys(userId, query, sequence)
  }, 300)
}

const selectUser = async (u: SimpleUser) => {
  if (observerMode) return
  clearPendingUserSearch()
  userKeyword.value = u.email
  showUserDropdown.value = false
  filters.value.user_id = u.id
  clearApiKey()

  emitChange()
  // Auto-load keys in the selected user's scope without delaying filtering.
  await searchApiKeys(u.id, '', apiKeySearchSequence)
}

const clearUser = () => {
  clearPendingUserSearch()
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  filters.value.user_id = undefined
  clearApiKey()
  emitChange()
}

const selectApiKey = (k: UsageFilterOption) => {
  clearPendingApiKeySearch()
  apiKeyKeyword.value = `${k.name || String(k.id)}${k.deleted ? ` (${t('traffic.deletedKey')})` : ''}`
  showApiKeyDropdown.value = false
  filters.value.api_key_id = k.id
  emitChange()
}

const clearApiKey = () => {
  clearPendingApiKeySearch()
  apiKeyKeyword.value = ''
  apiKeySearchError.value = false
  apiKeyResults.value = []
  showApiKeyDropdown.value = false
  filters.value.api_key_id = undefined
}

const onClearApiKey = () => {
  clearApiKey()
  emitChange()
}

const debounceAccountSearch = () => {
  if (accountSearchTimeout) clearTimeout(accountSearchTimeout)
  accountSearchTimeout = setTimeout(async () => {
    if (!accountKeyword.value) {
      accountResults.value = []
      return
    }
    try {
      const res = observerMode
        ? { items: await observerUsageAPI.filterOptions('account', accountKeyword.value) }
        : await adminAPI.accounts.list(1, 20, { search: accountKeyword.value })
      accountResults.value = res.items.map((a) => ({ id: a.id, name: a.name }))
    } catch {
      accountResults.value = []
    }
  }, 300)
}

const selectAccount = (a: SimpleAccount) => {
  accountKeyword.value = a.name
  showAccountDropdown.value = false
  filters.value.account_id = a.id
  emitChange()
}

const clearAccount = () => {
  accountKeyword.value = ''
  accountResults.value = []
  showAccountDropdown.value = false
  filters.value.account_id = undefined
  emitChange()
}

const onApiKeyFocus = () => {
  showApiKeyDropdown.value = true
  // Trigger search if no results yet
  if (apiKeyResults.value.length === 0) {
    debounceApiKeySearch()
  }
}

const onDocumentClick = (e: MouseEvent) => {
  const target = e.target as Node | null
  if (!target) return

  const clickedInsideUser = userSearchRef.value?.contains(target) ?? false
  const clickedInsideApiKey = apiKeySearchRef.value?.contains(target) ?? false
  const clickedInsideAccount = accountSearchRef.value?.contains(target) ?? false

  if (!clickedInsideUser) showUserDropdown.value = false
  if (!clickedInsideApiKey) showApiKeyDropdown.value = false
  if (!clickedInsideAccount) showAccountDropdown.value = false
}

watch(
  () => props.startDate,
  (value) => {
    if (!props.identityOnly) filters.value.start_date = value
  },
  { immediate: true }
)

watch(
  () => props.endDate,
  (value) => {
    if (!props.identityOnly) filters.value.end_date = value
  },
  { immediate: true }
)

watch(
  () => filters.value.user_id,
  (userId) => {
    if (!userId) {
      clearPendingUserSearch()
      userKeyword.value = ''
      userResults.value = []
    }
  }
)

watch(
  () => filters.value.api_key_id,
  (apiKeyId) => {
    if (!apiKeyId) {
      clearPendingApiKeySearch()
      apiKeyKeyword.value = ''
      apiKeyResults.value = []
    }
  },
  { flush: 'sync' }
)

watch(
  () => filters.value.account_id,
  (accountId) => {
    if (!accountId) {
      accountKeyword.value = ''
      accountResults.value = []
    }
  }
)

onMounted(async () => {
  document.addEventListener('click', onDocumentClick)
  if (props.identityOnly) return
  try {
    const gs = observerMode
      ? { items: await observerUsageAPI.filterOptions('group') }
      : await adminAPI.groups.list(1, 1000)
    groupOptions.value.push(...gs.items.map((g: any) => ({ value: g.id, label: g.name })))
  } catch {
    // Ignore filter option loading errors (page still usable)
  }
})

onUnmounted(() => {
  clearPendingUserSearch()
  clearPendingApiKeySearch()
  if (accountSearchTimeout) clearTimeout(accountSearchTimeout)
  document.removeEventListener('click', onDocumentClick)
})

// 供外部(如用户排行下钻)在程序化设置 user_id 后回显选中的用户邮箱
const setUserKeyword = (email: string) => {
  clearPendingUserSearch()
  userKeyword.value = email
  userResults.value = []
  showUserDropdown.value = false
}

const getUserSearchRevision = () => userSearchSequence

defineExpose({ getUserSearchRevision, setUserKeyword })
</script>
