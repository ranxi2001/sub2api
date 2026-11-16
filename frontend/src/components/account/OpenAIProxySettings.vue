<template>
  <div class="space-y-3" data-testid="openai-proxy-settings">
    <div class="flex flex-wrap gap-4">
      <label class="flex items-center gap-1.5 text-sm">
        <input :checked="mode === 'single'" :name="modeName" type="radio" value="single"
          data-testid="openai-proxy-mode-single" class="h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
          @change="emit('update:mode', 'single')" />
        {{ t('admin.accounts.openai.proxyModeSingle') }}
      </label>
      <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.openaiAccountMultiProxy" :user-id="userId" />
      <label class="flex items-center gap-1.5 text-sm">
        <input :checked="mode === 'request'" :name="modeName" type="radio" value="request"
          data-testid="openai-proxy-mode-request" class="h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
          @change="emit('update:mode', 'request')" />
        {{ t('admin.accounts.openai.proxyModeRequest') }}
      </label>
    </div>
    <ProxySelector v-if="mode === 'single'" :model-value="singleProxyId" :proxies="proxies" :disabled="loading"
      @update:model-value="emit('update:singleProxyId', $event)" />
    <p v-if="mode === 'request'" class="text-sm text-amber-700 dark:text-amber-400" role="note" data-testid="openai-proxy-risk">
      {{ t('admin.accounts.openai.proxyRequestRisk') }}
    </p>
    <BPSProxyRoutingSettings v-if="mode === 'request'" mode="request" embedded :proxy-ids="proxyIds" :proxies="proxies"
      :loading="loading" :load-error="loadError" :current-account-id="currentAccountId" :user-id="userId"
      @update:proxy-ids="emit('update:proxyIds', $event)" @retry="emit('retry')" />
  </div>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Proxy } from '@/types'
import ProxySelector from '@/components/common/ProxySelector.vue'
import BPSProxyRoutingSettings from './BPSProxyRoutingSettings.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

defineProps<{
  mode: 'single' | 'request'
  singleProxyId: number | null
  proxyIds: number[]
  proxies: Proxy[]
  currentAccountId?: number | null
  userId?: number | null
  loading?: boolean
  loadError?: boolean
}>()
const emit = defineEmits<{
  'update:mode': [value: 'single' | 'request']
  'update:singleProxyId': [value: number | null]
  'update:proxyIds': [value: number[]]
  retry: []
}>()
const { t } = useI18n()
const modeName = useId()
</script>
