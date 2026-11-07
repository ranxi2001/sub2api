<template>
  <AppLayout>
    <div class="w-full min-w-0 pb-8">
      <SecurityAuditNav active="/admin/ip-audit" />
      <h1 class="mb-4 text-2xl font-semibold">{{ t('ipAudit.title') }}</h1>
      <div class="mb-4 flex flex-wrap gap-1" role="tablist" :aria-label="t('ipAudit.title')">
        <div v-for="tab in tabs" :key="tab.id" class="flex items-center gap-1">
        <button
          type="button"
          role="tab"
          class="tab"
          :class="{ 'tab-active': activeTab === tab.id }"
          :aria-selected="activeTab === tab.id"
          :data-test="`ip-tab-${tab.id}`"
          @click="selectTab(tab.id)"
        >{{ tab.label }}</button>
        <NewFeatureBadge v-if="tab.id === 'keys'" :notice-id="NEW_FEATURE_NOTICES.keyRisk" :user-id="auth.user?.id" />
        </div>
      </div>
      <KeepAlive>
        <IPRiskSettings v-if="activeTab === 'config'" expanded />
        <KeyRiskPanel v-else-if="activeTab === 'keys'" @configure="selectTab('config')" />
        <IPAuditPanel v-else class="card px-4 sm:px-6 lg:px-8" @configure="selectTab('config')" />
      </KeepAlive>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import SecurityAuditNav from '@/components/admin/security/SecurityAuditNav.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import IPRiskSettings from '@/features/ip-risk/IPRiskSettings.vue'
import IPAuditPanel from './IPAuditPanel.vue'
import KeyRiskPanel from './KeyRiskPanel.vue'

const { t } = useI18n()
const auth = useAuthStore()
const route = useRoute(), router = useRouter()
type IPAuditTab = 'sources' | 'config' | 'keys'
const activeTab = computed<IPAuditTab>(() => route.query.tab === 'keys' ? 'keys' : route.query.tab === 'config' || route.hash === '#ip-risk-settings' ? 'config' : 'sources')
const tabs = computed(() => [
  { id: 'keys' as const, label: t('ipAudit.keyRisk.title') },
  { id: 'sources' as const, label: t('ipAudit.tabs.sources') },
  { id: 'config' as const, label: t('ipAudit.tabs.config') },
])
function selectTab(tab: IPAuditTab) {
  void router.push({ query: { ...route.query, tab }, hash: tab === 'config' ? '#ip-risk-settings' : '' })
}
</script>
