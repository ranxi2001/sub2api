<template>
  <AppLayout>
    <div class="min-w-0 space-y-5">
      <header><div class="flex items-center gap-2"><h1 class="text-2xl font-semibold">{{ t('channelConfig.title') }}</h1><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.channelConfig" :user-id="auth.user?.id" /></div><p class="mt-2 text-sm text-gray-500">{{ t('channelConfig.description') }}</p></header>
      <SmartOpsNav />
      <form class="card flex flex-wrap items-end gap-3 p-4" @submit.prevent="selectChannel(channelDraft)">
        <label class="text-sm">{{ t('channelConfig.currentChannel') }}<input v-model.number="channelDraft" class="input mt-1 block" type="number" min="0" step="1" /></label>
        <button class="btn btn-secondary">{{ t('profitOps.apply') }}</button><button type="button" class="btn btn-secondary" @click="selectChannel(0)">{{ t('profitOps.overview') }}</button>
        <span v-if="scope.account_id" class="text-sm">{{ t('profitOps.account') }} #{{ scope.account_id }}</span>
      </form>
      <p v-if="stale" role="status" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950">{{ t('channelConfig.stale') }}</p>
      <div class="grid min-w-0 gap-5" data-testid="channel-config-panels">
        <section aria-labelledby="profit-heading" data-testid="config-panel-profit" class="card min-w-0 p-4">
          <ProfitAnalytics embedded heading-id="profit-heading" :scope="scope" @scope="acceptScope" @refreshed="stale = false" />
        </section>
        <section ref="channelPanel" aria-labelledby="channel-heading" class="card min-w-0 scroll-mt-4 space-y-4 p-4" data-testid="config-panel-channels">
          <h2 id="channel-heading" class="text-lg font-semibold">{{ t('channelConfig.channels') }}</h2>
          <AccountOpsView embedded :channel-id="scope.channel_id" :account-id="scope.account_id" />
          <details data-testid="channel-directory" class="rounded-lg border p-4 dark:border-dark-700" @toggle="directoryOpen = ($event.target as HTMLDetailsElement).open">
            <summary class="cursor-pointer font-semibold">{{ t('channelConfig.directory') }}</summary>
            <div v-if="directoryOpen" class="mt-4 space-y-4">
              <p class="text-sm text-gray-500">{{ t('channelConfig.identityHelp') }}</p>
              <UpstreamChannelManager expanded @changed="changed" @selected="selectChannel($event.id)" />
              <RouterLink class="btn btn-secondary inline-block" to="/admin/channels/pricing">{{ t('channelOps.pricing') }}</RouterLink>
            </div>
          </details>
          <details class="rounded-lg border p-4 dark:border-dark-700" @toggle="walletOpen = ($event.target as HTMLDetailsElement).open">
            <summary class="cursor-pointer font-semibold">{{ t('channelConfig.connections') }}</summary>
            <div v-if="walletOpen" class="mt-4 space-y-5"><MatureProvidersPanel passive @changed="changed" /><ProfitProviders @changed="changed" /></div>
          </details>
          <details class="rounded-lg border p-4 dark:border-dark-700" @toggle="analyticsOpen = ($event.target as HTMLDetailsElement).open"><summary class="cursor-pointer font-semibold">{{ t('channelConfig.observations') }}</summary><UpstreamAnalytics v-if="analyticsOpen" embedded class="mt-4" /></details>
        </section>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { defineAsyncComponent, onMounted, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import type { OperationsQuery } from '@/api/admin/profitOperations'
import { profitScopeFromQuery } from './channelConfigScope'
const UpstreamChannelManager=defineAsyncComponent(()=>import('@/components/admin/operations/UpstreamChannelManager.vue').then(module=>module.default))
const ProfitProviders=defineAsyncComponent(()=>import('@/components/admin/operations/ProfitProviders.vue').then(module=>module.default))
const MatureProvidersPanel=defineAsyncComponent(()=>import('@/components/admin/operations/MatureProvidersPanel.vue').then(module=>module.default))
const UpstreamAnalytics=defineAsyncComponent(()=>import('@/components/admin/operations/UpstreamAnalytics.vue').then(module=>module.default))
const ProfitAnalytics=defineAsyncComponent(()=>import('@/components/admin/operations/ProfitAnalytics.vue').then(module=>module.default))
const AccountOpsView=defineAsyncComponent(()=>import('./AccountOpsView.vue').then(module=>module.default))
const { t }=useI18n(), route=useRoute(), router=useRouter(), auth=useAuthStore()
const channelPanel = ref<HTMLElement | null>(null)
const directoryOpen = ref(false), walletOpen = ref(false), analyticsOpen = ref(false), stale = ref(false)
function locatePanel(panel: unknown) {
  if (panel === 'channels' || panel === 'accounts') {
    void nextTick(() => channelPanel.value?.scrollIntoView?.({ block: 'start' }))
  }
}
onMounted(() => locatePanel(route.query.panel))
watch(() => route.query.panel, locatePanel)
const scope=ref<Partial<OperationsQuery>>(profitScopeFromQuery(route.query))
const channelDraft=ref(scope.value.channel_id || 0)
function selectChannel(id:number){if(!Number.isSafeInteger(id)||id<0)return;channelDraft.value=id;acceptScope({...scope.value,channel_id:id,upstream:'',account_id:0,dimension:id?'account':'upstream',page:1})}
function acceptScope(value:Partial<OperationsQuery>){
 if(JSON.stringify(scope.value)!==JSON.stringify(value))scope.value={...value}
 channelDraft.value=value.channel_id||0
 void router.replace({query:{...route.query,...Object.fromEntries(Object.entries(value).map(([key,v])=>[key,String(v)]))},hash:route.hash})
}
function changed(){stale.value=true}
watch(()=>route.query,query=>{
 const next=profitScopeFromQuery(query)
 if(JSON.stringify(next)!==JSON.stringify(scope.value))scope.value=next
})
</script>
