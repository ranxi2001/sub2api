<template>
  <AppLayout>
    <div class="account-ops space-y-5">
      <SmartOpsNav />
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 dark:border-dark-700">
        <div role="tablist" :aria-label="t('accountOps.pageSections')" class="flex gap-6">
          <button v-for="tab in tabs" :id="`ops-tab-${tab}`" :key="tab" type="button" role="tab" :aria-selected="activeTab === tab"
            :aria-controls="`ops-panel-${tab}`" :tabindex="activeTab === tab ? 0 : -1" class="border-b-2 px-1 py-3 text-sm font-medium"
            :class="activeTab === tab ? 'border-primary-600 text-primary-700 dark:text-primary-400' : 'border-transparent text-gray-500 hover:text-gray-800 dark:hover:text-gray-200'"
            :data-testid="`account-ops-tab-${tab}`" @click="selectTab(tab)"
            @keydown="tabKey($event, tab)">{{ t(tab === 'records' ? 'accountOps.recordsTab' : 'accountOps.settingsTab') }}</button>
        </div>
        <div class="flex items-center gap-3 pb-2">
          <div class="flex items-center gap-3 text-sm font-medium"><span id="account-ops-enabled-label">{{ t('accountOps.enabled') }}</span>
            <Toggle :model-value="enabledDraft" aria-labelledby="account-ops-enabled-label"
              :disabled="!remote || loading || mutating || globalSaving" class="disabled:cursor-wait disabled:opacity-50"
              data-testid="notifications-enabled" @update:model-value="toggleNotifications" />
          </div><button class="btn btn-secondary inline-flex items-center gap-2 py-1.5" :disabled="loading || thresholdLoading || mutating || globalSaving" @click="refresh">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />{{ t('qualityOps.refresh') }}
          </button>
        </div>
      </div>
      <section v-if="activeTab === 'records'" id="ops-panel-records" role="tabpanel" aria-labelledby="ops-tab-records"
        class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-5 dark:border-dark-700">
          <div>
            <h2 class="text-base font-semibold">{{ t('accountOps.recordsTab') }}</h2>
            <p class="mt-1 text-xs text-gray-500">{{ t('accountOps.recordsOnceHint') }}</p>
          </div>
          <div class="flex flex-wrap gap-2"><input v-model="query" class="input w-48 text-sm" :placeholder="t('accountOps.search')"
              :aria-label="t('accountOps.search')" /><select v-model="kind" class="input w-auto text-sm"
              :aria-label="t('accountOps.alertTypes')">
              <option value="all">{{ t('accountOps.allTypes') }}</option>
              <option v-for="value in alertKinds" :key="value" :value="value">{{ t(`accountOps.${value}`) }}</option>
            </select><select v-model="phase" class="input w-auto text-sm" :aria-label="t('accountOps.eventPhase')">
              <option value="all">{{ t('accountOps.allPhases') }}</option>
              <option value="alert">{{ t('accountOps.phases.alert') }}</option>
              <option value="recovery">{{ t('accountOps.phases.recovery') }}</option>
            </select></div>
        </div>
        <p v-if="remote?.dropped_signals || remote?.storage_failures" role="alert" class="p-4 text-sm text-amber-700">
          {{ t('accountOps.captureIssue') }}</p>
        <div class="overflow-x-auto" data-testid="account-events-scroll" :aria-busy="loading">
          <table v-if="filteredEvents.length" class="w-full min-w-[760px] text-left text-sm">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800">
              <tr>
                <th class="px-5 py-3">{{ t('qualityOps.accounts') }}</th>
                <th class="px-4 py-3">{{ t('accountOps.eventPhase') }}</th>
                <th class="px-4 py-3">{{ t('accountOps.eventValue') }}</th>
                <th class="px-4 py-3">{{ t('accountOps.occurredAt') }}</th>
                <th class="px-5 py-3">{{ t('accountOps.mailStatus') }}</th>
                <th class="px-5 py-3">{{ t('accountOps.deliveryDetails') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="event in filteredEvents" :key="eventKey(event)" class="border-t border-gray-100 align-top dark:border-dark-700"
                data-testid="notification-record">
                <td class="px-5 py-4">
                  <p class="font-medium">{{ event.group_name || event.account_name }}</p>
                  <p class="mt-1 text-xs text-gray-500">#{{ event.account_id }} · {{ t(`accountOps.${event.kind}`) }}</p>
                </td>
                <td class="px-4 py-4"><span class="rounded px-2 py-1 text-xs"
                    :class="event.phase === 'recovery' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400' : 'bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400'">{{ t(event.phase ? `accountOps.phases.${event.phase}` : 'accountOps.failureSignal') }}</span>
                </td>
                <td class="px-4 py-4 text-xs"><template v-if="event.kind === 'balance_threshold'">{{ event.details?.balance }}
                    {{ event.details?.unit }}<span class="mt-1 block text-gray-500">{{ t('accountOps.thresholdValue') }}
                      {{ event.details?.threshold }} {{ event.details?.unit }}</span></template><template
                    v-else-if="event.kind === 'quota_threshold'">{{ event.details?.used_percent }}% · {{ event.details?.window }}<span
                      class="mt-1 block text-gray-500">{{ t('accountOps.thresholdValue') }}
                      {{ event.details?.threshold_percent }}%</span></template><template v-else>HTTP {{ event.http_status }}<span
                      class="mt-1 block text-gray-500">{{ t(`accountOps.signals.${event.signal}`) }}</span></template></td>
                <td class="whitespace-nowrap px-4 py-4 text-xs text-gray-600 dark:text-gray-400">{{ date(event.first_seen) }}</td>
                <td class="px-5 py-4">
                  <p class="whitespace-nowrap text-xs"
                    :class="event.state === 'failed' ? 'text-red-600' : event.state === 'sent' ? 'text-emerald-600' : 'text-gray-500'">
                    {{ t(event.notification_enabled === false ? 'accountOps.policyRecord' : `accountOps.states.${event.state}`) }}</p>
                  <p v-if="event.state === 'failed'" class="mt-2 text-[11px] text-gray-500">
                    {{ t(event.attempts < 3 ? 'accountOps.retryAt' : 'accountOps.retryStopped', { time: date(event.next_send_at) }) }}</p>
                </td>
                <td class="px-5 py-4"><AccountOpsDeliveryDetails :deliveries="event.deliveries" /></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="!filteredEvents.length" class="flex min-h-72 flex-col items-center justify-center gap-3 p-8 text-center text-gray-500">
          <Icon name="bell" size="xl" />
          <p class="text-sm">{{ t(loading ? 'common.loading' : events.length ? 'accountOps.noMatchingRecords' : 'accountOps.empty') }}</p>
          <p class="max-w-lg text-xs leading-5">{{ t('accountOps.recordsEmptyHint') }}</p><button type="button"
            class="btn btn-secondary mt-1" @click="selectTab('settings')">{{ t('accountOps.settingsTab') }}</button>
        </div>
        <div class="flex items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 text-xs text-gray-500 dark:border-dark-700">
          <span>{{ t('accountOps.noRawErrors') }}</span><button v-if="hasMore" :disabled="loadingMore" class="text-primary-600"
            @click="more">{{ t(loadingMore ? 'common.loading' : 'qualityOps.loadMore') }}</button></div>
      </section>
      <section v-if="activeTab === 'settings' && remote" id="ops-panel-settings" role="tabpanel" aria-labelledby="ops-tab-settings"
        class="space-y-5">
        <section class="rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
          data-testid="account-ops-channels">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div>
              <h2 class="text-sm font-semibold">{{ t('accountOps.notificationChannels') }}</h2>
              <p class="mt-1 text-xs text-gray-500">{{ t('accountOps.channelSummaryHint') }}</p>
            </div>
          </div>
          <div class="grid gap-3 p-5 sm:grid-cols-2 xl:grid-cols-3">
            <div v-if="remote.config.recipient" class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
              data-testid="account-ops-email-channel">
              <div class="flex items-center justify-between"><span
                  class="min-w-0 truncate text-sm font-medium" :title="remote.config.email_name || t('accountOps.providers.email')">{{ remote.config.email_name || t('accountOps.providers.email') }}</span><span
                  class="text-xs text-gray-500">{{ t(remote.config.recipient ? 'accountOps.channelConfigured' : 'accountOps.channelUnconfigured') }}</span>
              </div>
              <p class="mt-2 truncate text-xs text-gray-500">{{ remote.config.email_name ? `${t('accountOps.providers.email')} · ` : '' }}{{ remote.config.recipient }}</p>
              <div class="mt-3 flex gap-4 text-xs"><button type="button" class="text-primary-600" data-testid="edit-email-channel"
                  @click="showEmail = true">{{ t('common.edit') }}</button><button type="button" :disabled="mutating"
                  class="text-gray-500 hover:text-red-600 disabled:opacity-40" data-testid="delete-email-channel"
                  @click="removeEmail">{{ t('common.delete') }}</button></div>
            </div>
            <div v-for="(hook, index) in remote.config.webhooks" :key="hook.id"
              class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
              <div class="flex items-center justify-between gap-2"><span
                  class="min-w-0 truncate text-sm font-medium" :title="hook.name || `${t(`accountOps.providers.${hook.provider}`)} ${index + 1}`">{{ hook.name || `${t(`accountOps.providers.${hook.provider}`)} ${index + 1}` }}</span><span class="text-xs"
                  :class="hook.enabled ? 'text-emerald-600' : 'text-gray-500'">{{ t(hook.enabled ? 'accountOps.channelEnabled' : 'accountOps.channelDisabled') }}</span>
              </div>
              <p class="mt-2 text-xs text-gray-500">
                {{ hook.name ? `${t(`accountOps.providers.${hook.provider}`)} · ` : '' }}{{ t(hook.url_configured ? 'accountOps.credentialSaved' : 'accountOps.channelUnconfigured') }}</p>
              <div class="mt-3 flex gap-4 text-xs"><button type="button" class="text-primary-600" :data-testid="`edit-webhook-${hook.id}`"
                  @click="editWebhook(hook)">{{ t('common.edit') }}</button><button type="button"
                  class="text-primary-600 disabled:opacity-40" :disabled="!hook.url_configured || !!testingId"
                  :data-testid="`account-ops-webhook-test-${hook.id}`"
                  @click="testWebhook(hook.id)">{{ t(testingId === hook.id ? 'common.loading' : 'accountOps.testRobot') }}</button><button
                  type="button" class="text-gray-500 hover:text-red-600" @click="removeWebhook(hook.id)">{{ t('common.delete') }}</button>
              </div>
            </div><button type="button"
              class="min-h-28 rounded-lg border border-dashed border-gray-300 p-4 text-sm text-primary-600 disabled:opacity-40 dark:border-dark-600"
              :disabled="!canAddChannel" data-testid="account-ops-add-channel"
              @click="showAddChannel = true">{{ t('accountOps.addChannel') }}</button>
          </div>
        </section>
        <section class="rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900" data-testid="account-ops-groups">
          <div class="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
            <div>
              <h2 class="text-sm font-semibold">{{ t('accountOps.accountGroups') }}</h2>
              <p class="mt-1 text-xs text-gray-500">{{ t('accountOps.accountGroupsSummary', { count: accountGroups.length }) }}</p>
            </div>
            <button type="button" class="btn btn-secondary text-sm" :disabled="thresholdLoading || mutating" data-testid="edit-account-groups" @click="showGroupDialog = true">{{ t('common.edit') }}</button>
          </div>
        </section>
        <AccountOpsRuleList ref="ruleList" :accounts="thresholdAccounts" :groups="accountGroups" :config="remote.config" :loading="thresholdLoading"
          :ready="thresholdReady" :error="thresholdError" :busy="mutating"
          @batch-edit="batchAccounts = $event" @edit-group="showGroupDialog = true" @toggle="toggleGroup">
          <template #global-settings>
            <AccountOpsGlobalSettings :config="remote.config" :disabled="mutating" @saving="globalSaving = $event" @saved="acceptConfig"
              @error="app.showError" />
          </template>
        </AccountOpsRuleList>
      </section>
      <template v-if="remote && auth.user">
        <AccountOpsBatchRuleDialog :show="batchAccounts.length > 0" :accounts="batchAccounts" :config="remote.config" @close="batchAccounts = []"
          @saved="acceptBatchConfig" @error="app.showError" />
        <AccountOpsAddChannelDialog :show="showAddChannel" :email-configured="!!remote.config.recipient"
          :webhook-count="remote.config.webhooks?.length ?? 0" :encryption-configured="remote.encryption_key_configured === true"
          @close="showAddChannel = false" @select="addChannel" />
        <AccountOpsWebhookDialog :show="showWebhook" :hook="editingHook" :encryption-configured="remote.encryption_key_configured === true"
          :testing-id="testingId" @close="showWebhook = false" @saved="acceptConfig" @error="app.showError" @test="testWebhook" />
        <AccountOpsEmailDialog :show="showEmail" :config="remote.config" :smtp-configured="remote.smtp_configured"
          @close="showEmail = false" @saved="acceptConfig" @error="app.showError" />
        <AccountOpsGroupDialog :show="showGroupDialog" :groups="accountGroups" :accounts="thresholdAccounts"
          @close="showGroupDialog = false" @saved="acceptGroupConfig" @error="app.showError" />
      </template>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useAccountOpsStore } from '@/stores/accountOps'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import AccountOpsRuleList from '@/components/admin/operations/AccountOpsRuleList.vue'
import AccountOpsBatchRuleDialog from '@/components/admin/operations/AccountOpsBatchRuleDialog.vue'
import AccountOpsDeliveryDetails from '@/components/admin/operations/AccountOpsDeliveryDetails.vue'
import AccountOpsWebhookDialog from '@/components/admin/operations/AccountOpsWebhookDialog.vue'
import AccountOpsEmailDialog from '@/components/admin/operations/AccountOpsEmailDialog.vue'
import AccountOpsAddChannelDialog from '@/components/admin/operations/AccountOpsAddChannelDialog.vue'
import AccountOpsGlobalSettings from '@/components/admin/operations/AccountOpsGlobalSettings.vue'
import AccountOpsGroupDialog from '@/components/admin/operations/AccountOpsGroupDialog.vue'
import { getAccountOpsSettings, saveAccountOpsNotificationSettings, getAccountOpsEvents, getAccountOpsThresholdAccounts, getAccountOpsThresholdGroups, testAccountOpsWebhook, saveAccountOpsRuleGroups, deleteAccountOpsWebhook } from '@/api/admin/accountOps'
import type { AccountOpsConfig, AccountOpsEvent, AccountOpsThresholdAccount, AccountOpsWebhook, AccountOpsGroup, AccountOpsRuleBatchGroup } from '@/api/admin/accountOps'
import { extractApiErrorMessage } from '@/utils/apiError'
const { t } = useI18n(), auth = useAuthStore(), app = useAppStore()
const { remote, events, hasMore } = storeToRefs(useAccountOpsStore())
const tabs = ['settings','records'] as const, activeTab = ref<'records'|'settings'>('settings')
const loading = ref(false), loadingMore = ref(false), mutating = ref(false)
const thresholdAccounts = ref<AccountOpsThresholdAccount[]>([]), thresholdLoading = ref(false), thresholdReady = ref(false), thresholdError = ref('')
const accountGroups = ref<AccountOpsGroup[]>([]), showGroupDialog = ref(false)
const enabledDraft = ref(false), globalSaving = ref(false), showAddChannel = ref(false)
const canAddChannel = computed(() => !!remote.value && (!remote.value.config.recipient || ((remote.value.config.webhooks?.length ?? 0) < 5 && remote.value.encryption_key_configured === true)))
watch(() => remote.value?.config.enabled, enabled => { enabledDraft.value = enabled ?? false }, { immediate: true })
const ruleList = ref<InstanceType<typeof AccountOpsRuleList> | null>(null)
const batchAccounts = ref<AccountOpsThresholdAccount[]>([])
watch(thresholdAccounts, accounts => {
  if (!batchAccounts.value.length) return
  const selected = new Set(batchAccounts.value.map(account => account.account_id))
  batchAccounts.value = accounts.filter(account => selected.has(account.account_id))
})
const query = ref(''), kind = ref('all'), phase = ref('all'), alertKinds = ['balance_threshold','quota_threshold','balance_low','weekly_quota']
const editingHook = ref<AccountOpsWebhook|null>(null), showWebhook = ref(false), showEmail = ref(false), testingId = ref<string|null>(null)
let version = 0, accountsVersion = 0, testSequence = 0, mutationSequence = 0, paginationSequence = 0, alive = true, timer: ReturnType<typeof setInterval>|null = null
const normalize = (c: AccountOpsConfig): AccountOpsConfig => ({ enabled:c.enabled, recipient:c.recipient,email_name:c.email_name ?? '', balance_low:c.balance_low, weekly_quota:c.weekly_quota, cooldown_minutes:c.cooldown_minutes, webhooks:(c.webhooks??[]).map(h=>({id:h.id,name:h.name,provider:h.provider,enabled:h.enabled,url_configured:h.url_configured===true,secret_configured:h.secret_configured===true,message_template:h.message_template})), groups:(c.groups??[]).map(g=>({id:g.id,name:g.name,provider:g.provider,site:g.site,account_ids:g.account_ids ? [...g.account_ids] : []})), balance_thresholds:(c.balance_thresholds??[]).map(r=>({...r,notify_alert:r.notify_alert??true,notify_recovery:r.notify_recovery??true})), quota_thresholds:(c.quota_thresholds??[]).map(r=>({...r,notify_alert:r.notify_alert??true,notify_recovery:r.notify_recovery??true})) })
const eventKey = (e: AccountOpsEvent) => e.id ?? `${e.account_id}:${e.kind}:${e.phase??'legacy'}:${e.first_seen}`
const filteredEvents = computed(()=>events.value.filter(e=>(kind.value==='all'||kind.value===e.kind)&&(phase.value==='all'||phase.value===(e.phase??'alert'))&&`${e.account_name} ${e.account_id}`.toLowerCase().includes(query.value.toLowerCase().trim())))
const date = (value: string) => { const d=new Date(value);return Number.isFinite(d.getTime())?d.toLocaleString():'-' }
const message = (e: unknown) => extractApiErrorMessage(e,t('accountOps.saveFailed'))
async function load() {
  if (!auth.user || mutating.value || globalSaving.value) return
  const current=++version;loading.value=true
  await Promise.allSettled([
    getAccountOpsSettings().then(s=>{if(alive&&current===version)remote.value={...s,config:normalize(s.config)}}).catch(e=>{if(alive&&current===version)app.showError(message(e))}),
    getAccountOpsEvents().then(p=>{if(alive&&current===version){events.value=p.items;hasMore.value=p.has_more}}).catch(e=>{if(alive&&current===version)app.showError(message(e))})
  ])
  if(alive&&current===version)loading.value=false
}
async function refresh() {
  if (mutating.value || globalSaving.value) return
  await Promise.all([load(), ...(activeTab.value === 'settings' ? [loadAccounts()] : [])])
}
async function loadAccounts() {
  if(!auth.user)return
  const current=++accountsVersion;thresholdLoading.value=true
  try{const [a, g] = await Promise.all([getAccountOpsThresholdAccounts(), getAccountOpsThresholdGroups()]);if(alive&&current===accountsVersion){thresholdAccounts.value=a;accountGroups.value=g;thresholdReady.value=true;thresholdError.value=''}}
  catch(e){if(alive&&current===accountsVersion){thresholdError.value=message(e);app.showError(thresholdError.value)}}
  finally{if(alive&&current===accountsVersion)thresholdLoading.value=false}
}
function selectTab(tab:'records'|'settings'){activeTab.value=tab;if(tab==='settings'&&!thresholdReady.value&&!thresholdLoading.value)void loadAccounts()}
function tabKey(event:KeyboardEvent,tab:'records'|'settings'){if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;event.preventDefault();const next=event.key==='Home'?'settings':event.key==='End'?'records':tab==='records'?'settings':'records';selectTab(next);document.getElementById(`ops-tab-${next}`)?.focus()}
function acceptConfig(config: AccountOpsConfig){if(!alive||!auth.user||!remote.value)return;version++;loading.value=false;remote.value={...remote.value,config:normalize(config)};app.showSuccess(t('accountOps.settingsSaved'))}
function acceptGroupConfig(config: AccountOpsConfig){showGroupDialog.value=false;acceptConfig(config);void loadGroups()}
async function loadGroups(){try{const groups=await getAccountOpsThresholdGroups();if(alive)accountGroups.value=groups}catch(e){if(alive)app.showError(message(e))}}
function acceptBatchConfig(config: AccountOpsConfig){acceptConfig(config);ruleList.value?.clearSelection()}
function editWebhook(hook:AccountOpsWebhook|null){editingHook.value=hook;showWebhook.value=true}
function addChannel(provider: 'email'|'webhook') {
  if (!remote.value) return
  if (provider === 'email') {
    if (remote.value.config.recipient) return
    showEmail.value = true
  } else {
    if (remote.value.encryption_key_configured !== true || (remote.value.config.webhooks?.length ?? 0) >= 5) return
    editWebhook(null)
  }
  showAddChannel.value = false
}
async function removeEmail(){await mutate(()=>saveAccountOpsNotificationSettings({recipient:'',email_name:''}))}
async function mutate(action:()=>Promise<AccountOpsConfig>){if(mutating.value)return;const current=++mutationSequence;mutating.value=true;try{const c=await action();if(alive&&current===mutationSequence)acceptConfig(c)}catch(e){if(alive&&current===mutationSequence)app.showError(message(e))}finally{if(alive&&current===mutationSequence)mutating.value=false}}
async function toggleNotifications(enabled: boolean) {
  enabledDraft.value = enabled
  await mutate(() => saveAccountOpsNotificationSettings({ enabled: enabledDraft.value }))
  enabledDraft.value = remote.value?.config.enabled ?? false
}
async function toggleGroup(_group:AccountOpsGroup, accounts:AccountOpsThresholdAccount[], enabled:boolean){
  if (!remote.value) return
  const groups: AccountOpsRuleBatchGroup[] = []
  const apiKeys = accounts.filter(account => account.type === 'apikey')
  const oauth = accounts.filter(account => account.type === 'oauth')
  if (apiKeys.length) groups.push({ account_ids: apiKeys.map(account => account.account_id), rule: { metric: 'balance' as const, enabled, enabled_only: true } })
  if (oauth.length) groups.push({ account_ids: oauth.map(account => account.account_id), rule: { metric: 'quota' as const, enabled, enabled_only: true } })
  if (groups.length) await mutate(() => saveAccountOpsRuleGroups(groups))
}
async function removeWebhook(id:string){await mutate(()=>deleteAccountOpsWebhook(id))}
async function testWebhook(id:string){if(testingId.value)return;const current=++testSequence;testingId.value=id;try{await testAccountOpsWebhook(id);if(alive&&current===testSequence)app.showSuccess(t('accountOps.testSuccess'))}catch(e){if(alive&&current===testSequence)app.showError(message(e))}finally{if(alive&&current===testSequence)testingId.value=null}}
async function more(){if(loadingMore.value||loading.value)return;const current=version, sequence=++paginationSequence;loadingMore.value=true;try{const p=await getAccountOpsEvents(events.value.length);if(alive&&current===version){const merged=new Map([...events.value,...p.items].map(e=>[eventKey(e),e]));events.value=[...merged.values()];hasMore.value=p.has_more}}catch(e){if(alive&&current===version)app.showError(message(e))}finally{if(alive&&sequence===paginationSequence)loadingMore.value=false}}
watch(()=>auth.user?`${auth.user.id}:${auth.user.role}`:'',()=>{version++;accountsVersion++;testSequence++;mutationSequence++;paginationSequence++;loading.value=loadingMore.value=mutating.value=thresholdLoading.value=false;thresholdAccounts.value=[];accountGroups.value=[];thresholdReady.value=false;showWebhook.value=showEmail.value=showAddChannel.value=showGroupDialog.value=false;testingId.value=null;enabledDraft.value=globalSaving.value=false;activeTab.value='settings';batchAccounts.value=[]},{flush:'sync'})
onMounted(()=>{void load();void loadAccounts();timer=setInterval(()=>{if(document.visibilityState==='visible'&&!loadingMore.value&&!mutating.value&&!globalSaving.value){void load();if(activeTab.value==='settings')void loadAccounts()}},30000)})
onBeforeUnmount(()=>{alive=false;version++;accountsVersion++;testSequence++;mutationSequence++;paginationSequence++;if(timer)clearInterval(timer)})
</script>
