<template>
  <section class="space-y-3" data-testid="gateway-model-settings">
    <div class="flex items-center gap-2">
      <span class="input-label">{{ t('admin.accounts.modelWhitelist') }} / {{ t('admin.accounts.modelMapping') }}</span>
      <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.gatewayModelSync" :user-id="authStore.user?.id" />
    </div>
    <ModelWhitelistSelector v-model="models" v-model:model-mappings="mappings" platform="gateway"
      :sync-credentials="syncCredentials" />
    <div v-for="(mapping, index) in mappings" :key="index" class="flex items-center gap-2">
      <input v-model="mapping.from" class="input min-w-0 flex-1" :aria-label="t('admin.accounts.fromModel')" :placeholder="t('admin.accounts.fromModel')" />
      <span aria-hidden="true">→</span>
      <input v-model="mapping.to" class="input min-w-0 flex-1" :aria-label="t('admin.accounts.toModel')" :placeholder="t('admin.accounts.toModel')" />
      <button type="button" class="text-red-600" :aria-label="t('common.delete')" @click="mappings.splice(index, 1)">×</button>
    </div>
    <button type="button" class="btn btn-secondary text-sm" @click="mappings.push({ from: '', to: '' })">{{ t('admin.accounts.addMapping') }}</button>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores'
import type { SyncUpstreamPreviewParams } from '@/api/admin/accounts'
import type { ModelMappingEntry } from '@/composables/useModelWhitelist'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import ModelWhitelistSelector from './ModelWhitelistSelector.vue'

defineProps<{ syncCredentials?: SyncUpstreamPreviewParams }>()
const models = defineModel<string[]>('models', { required: true })
const mappings = defineModel<ModelMappingEntry[]>('mappings', { required: true })
const authStore = useAuthStore()
const { t } = useI18n()
</script>
