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
        <details v-for="panel in panels" :key="panel" :open="opened[panel]" class="card min-w-0" :data-testid="`config-panel-${panel}`" @toggle="toggle(panel, $event)">
          <summary class="cursor-pointer p-5 text-lg font-semibold">{{ t(labels[panel]) }}</summary>
          <div v-if="visited[panel]" v-show="opened[panel]" class="min-w-0 space-y-5 border-t p-4 dark:border-dark-700">
            <template v-if="panel === 'channels'">
              <p class="text-sm text-gray-500">{{ t('channelConfig.identityHelp') }}</p>
              <UpstreamChannelManager expanded @changed="changed" @selected="selectChannel($event.id)" />
              <RouterLink class="btn btn-secondary inline-block" to="/admin/channels/pricing">{{ t('channelOps.pricing') }}</RouterLink>
              <details class="rounded-lg border p-4 dark:border-dark-700" @toggle="walletOpen = ($event.target as HTMLDetailsElement).open">
                <summary class="cursor-pointer font-semibold">{{ t('channelConfig.connections') }}</summary>
                <div v-if="walletOpen" class="mt-4 space-y-5"><MatureProvidersPanel passive @changed="changed" /><ProfitProviders @changed="changed" /></div>
              </details>
              <details class="rounded-lg border p-4 dark:border-dark-700" @toggle="analyticsOpen = ($event.target as HTMLDetailsElement).open"><summary class="cursor-pointer font-semibold">{{ t('channelConfig.observations') }}</summary><UpstreamAnalytics v-if="analyticsOpen" embedded class="mt-4" /></details>
            </template>
            <ProfitAnalytics v-else-if="panel === 'profit'" embedded :active="opened.profit" :scope="scope" @scope="acceptScope" @refreshed="stale = false" />
            <AccountOpsView v-else embedded :active="opened.accounts" :channel-id="scope.channel_id" :account-id="scope.account_id" />
          </div>
        </details>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { defineAsyncComponent, reactive, ref, watch } from 'vue'
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
const panels=['channels','profit','accounts'] as const
type Panel=typeof panels[number]
const labels={channels:'channelConfig.channels',profit:'profitOps.title',accounts:'accountOps.title'}
const initial=panels.includes(route.query.panel as Panel)?route.query.panel as Panel:'channels'
const opened=reactive({channels:initial==='channels',profit:initial==='profit',accounts:initial==='accounts'})
const visited=reactive({...opened}), walletOpen=ref(false), analyticsOpen=ref(false), stale=ref(false)
const scope=ref<Partial<OperationsQuery>>(profitScopeFromQuery(route.query))
const channelDraft=ref(scope.value.channel_id || 0)
function selectChannel(id:number){if(!Number.isSafeInteger(id)||id<0)return;channelDraft.value=id;acceptScope({...scope.value,channel_id:id,upstream:'',account_id:0,dimension:id?'account':'upstream',page:1})}
function acceptScope(value:Partial<OperationsQuery>){
 if(JSON.stringify(scope.value)!==JSON.stringify(value))scope.value={...value}
 channelDraft.value=value.channel_id||0
 void router.replace({query:{...route.query,...Object.fromEntries(Object.entries(value).map(([key,v])=>[key,String(v)]))},hash:route.hash})
}
function toggle(panel:Panel,event:Event){const open=(event.target as HTMLDetailsElement).open;opened[panel]=open;if(open){visited[panel]=true;if(route.query.panel!==panel)void router.replace({query:{...route.query,panel},hash:route.hash})}}
function changed(){stale.value=true}
watch(()=>route.query,query=>{
 const next=profitScopeFromQuery(query)
 if(JSON.stringify(next)!==JSON.stringify(scope.value))scope.value=next
 const panel=query.panel as Panel;if(panels.includes(panel)){opened[panel]=true;visited[panel]=true}
})
</script>
