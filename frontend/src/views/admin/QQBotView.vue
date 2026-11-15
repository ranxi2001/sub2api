<template>
  <AppLayout>
    <QQCommunityNav />
    <div class="mx-auto max-w-5xl space-y-6">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div><h1 class="text-2xl font-semibold">{{ t(groupMode ? 'qqbot.workspace.groups' : 'qqbot.basicConfig') }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('qqbot.description') }}</p></div>
        <button class="btn btn-secondary" :disabled="busy || refreshingRuntime" @click="refreshStatus">{{ t('common.refresh') }}</button>
      </div>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
      <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
      <p v-if="!form" class="text-gray-500">{{ t('common.loading') }}</p>
      <RouterLink v-if="!groupMode" to="/admin/qqbot/setup" class="btn btn-primary">{{ t('qqbot.simple.setup') }}</RouterLink>
      <form v-if="form" ref="settingsForm" novalidate @submit.prevent="save"><fieldset :disabled="busy" class="min-w-0 space-y-6">
        <p class="rounded-xl bg-blue-50 p-3 text-sm text-blue-800 dark:bg-blue-950 dark:text-blue-200">{{ t('qqbot.workspace.scopedSaveHelp') }}</p>
        <div v-if="!groupMode" class="flex flex-wrap gap-2"><button v-for="category in categories" :key="category.key" type="button" class="btn" :class="activeCategory === category.key ? 'btn-primary' : 'btn-secondary'" @click="selectCategory(category.key)">{{ t(category.label) }}</button></div>
        <nav
          role="tablist"
          :aria-label="t('qqbot.basicConfig')"
          class="flex flex-wrap gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-800"
        >
          <button
            v-for="tab in visibleTabs"
            :id="`qqbot-settings-tab-${tab.key}`"
            :key="tab.key"
            type="button"
            role="tab"
            :aria-selected="activeTab === tab.key"
            :aria-controls="`qqbot-settings-panel-${tab.key}`"
            :tabindex="activeTab === tab.key ? 0 : -1"
            class="min-h-[44px] flex-auto whitespace-nowrap rounded-lg px-3 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
            :class="activeTab === tab.key
              ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300'
              : 'text-gray-600 hover:bg-white/60 dark:text-gray-300 dark:hover:bg-dark-700/60'"
            @click="activeTab = tab.key"
            @keydown="handleTabKeydown($event, tab.key)"
          >
            {{ t(tab.label) }}{{ isTabDirty(tab.key) ? ' •' : '' }}
          </button>
        </nav>
        <section v-show="activeTab === 'connection'" id="qqbot-settings-panel-connection" role="tabpanel" aria-labelledby="qqbot-settings-tab-connection" class="card space-y-4 p-6">
          <div class="flex items-center justify-between"><h2 class="text-lg font-semibold">{{ t('qqbot.connection') }}</h2><label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" />{{ t('common.enabled') }}</label></div>
          <p v-if="runtime && !runtime.encryption_ready" class="text-sm text-amber-700">{{ t('qqbot.encryptionRequired') }}</p>
          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1">AppID<input v-model.trim="form.app_id" class="input" autocomplete="off" /></label>
            <label class="space-y-1">AppSecret<input v-model="form.app_secret" type="password" class="input" autocomplete="new-password" :placeholder="secretHint(form.app_secret_set)" /><span class="flex gap-2 text-xs"><input v-model="form.clear_app_secret" type="checkbox" />{{ t('qqbot.clearSecret') }}</span></label>
          </div>
          <label class="flex items-center gap-2"><input v-model="form.sandbox" type="checkbox" />{{ t('qqbot.sandbox') }}</label>
          <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800"><span>{{ t('qqbot.webhook') }}</span><code class="mt-1 block break-all select-all">{{ webhookURL }}</code><p class="mt-2 text-gray-500">{{ t('qqbot.eventsHelp') }}</p></div>
        </section>
        <section v-show="activeTab === 'groups'" id="qqbot-settings-panel-groups" role="tabpanel" aria-labelledby="qqbot-settings-tab-groups" class="card space-y-4 p-6">
          <QQGroupPicker v-model="selectedGroups" :groups="knownGroups" :policies="groupPolicies" :names="groupNames" :label="t('qqbot.allowedGroups')" />
          <details><summary class="cursor-pointer text-sm">{{ t('qqbot.workspace.manualGroups') }}</summary><textarea v-model="groupsText" rows="3" class="input mt-2 font-mono" :aria-label="t('qqbot.allowedGroups')" /></details>
          <p class="text-xs text-gray-500">{{ t('qqbot.workspace.groupNamesHelp') }}</p>
          <p v-if="groupNamesLoading" role="status" class="text-sm text-gray-500">{{ t('qqbot.workspace.groupNamesLoading') }}</p>
          <p v-if="groupNamesStatus" role="status" class="text-sm text-amber-700 dark:text-amber-300">{{ t(`qqbot.workspace.groupNamesStatus.${groupNamesStatus}`) }}</p>
          <QQGroupPolicies v-model="groupPolicies" :groups="allowedGroups" :names="groupNames" />
          <p class="text-sm text-gray-500">{{ t('qqbot.groupsHelp') }}</p>
          <div v-if="runtime?.groups.length" class="space-y-2">
            <p class="text-sm font-medium">{{ t('qqbot.discoveredGroups') }}</p>
            <p class="text-xs text-gray-500">{{ t('qqbot.discoveredGroupsHelp') }}</p>
            <div v-for="group in runtime.groups" :key="group.id" class="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-2 dark:border-dark-700">
              <div class="min-w-0 break-all"><p v-if="groupLabels[group.id]" class="text-sm font-medium">{{ groupLabels[group.id] }}</p><code class="text-xs text-gray-500">{{ group.id }}</code></div>
              <div class="flex shrink-0 gap-2">
                <button type="button" class="btn btn-secondary text-xs" @click="addGroup(group.id)">{{ t('common.add') }}</button>
                <button type="button" class="btn btn-secondary text-xs" :disabled="busy || refreshingRuntime" :aria-label="t('qqbot.deleteDiscoveredGroup', { id: group.id })" @click="deleteDiscoveredGroup(group.id)">{{ t('common.delete') }}</button>
              </div>
            </div>
          </div>
        </section>
        <section v-show="activeTab === 'notifications'" id="qqbot-settings-panel-notifications" role="tabpanel" aria-labelledby="qqbot-settings-tab-notifications" class="card space-y-4 p-6" :aria-label="t('qqbot.priceNotifications.title')">
          <QQBotAnnouncement v-if="baseline" :config="baseline" :names="groupNames" :disabled="busy" @queued="refreshRuntime" />
          <fieldset v-if="form.notification_quiet_hours" class="space-y-3 rounded-lg border p-4 dark:border-dark-700"><legend>{{ t('qqbot.workspace.quietHours') }}</legend><label class="flex items-center gap-2"><input v-model="form.notification_quiet_hours.enabled" type="checkbox" />{{ t('common.enabled') }}</label><div class="grid gap-3 sm:grid-cols-2"><label>{{ t('qqbot.workspace.startHour') }}<input v-model.number="form.notification_quiet_hours.start_hour" class="input" type="number" min="0" max="23" required /></label><label>{{ t('qqbot.workspace.endHour') }}<input v-model.number="form.notification_quiet_hours.end_hour" class="input" type="number" min="0" max="23" required /></label></div><p class="text-xs text-gray-500">{{ t('qqbot.workspace.quietHoursHelp') }}</p></fieldset>
          <h2 class="text-lg font-semibold">{{ t('qqbot.priceNotifications.title') }}</h2>
          <p class="text-sm text-gray-500">{{ t('qqbot.priceNotifications.help') }}</p>
          <label class="flex items-center gap-2"><input v-model="priceNotifications.enabled" data-testid="price-notifications-enabled" type="checkbox" />{{ t('qqbot.priceNotifications.enabled') }}</label>
          <label class="flex items-center gap-2"><input v-model="priceNotifications.scheduled" data-testid="price-notifications-scheduled" type="checkbox" />{{ t('qqbot.priceNotifications.scheduled') }}</label>
          <label class="flex items-center gap-2"><input v-model="priceNotifications.mention_all" data-testid="price-notifications-mention-all" type="checkbox" />{{ t('qqbot.priceNotifications.mentionAll') }}</label>
          <p class="text-xs text-gray-500">{{ t('qqbot.announcement.mentionHelp') }}</p>
          <QQGroupPicker v-model="notificationGroups" :groups="allowedGroups" :policies="groupPolicies" :names="groupNames" :label="t('qqbot.priceNotifications.groups')" allow-all />
          <p class="text-xs text-gray-500">{{ t('qqbot.priceNotifications.permission') }}</p>
          <fieldset class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
            <legend class="font-medium">{{ t('qqbot.health.title') }}</legend>
            <p class="text-sm text-gray-500">{{ t('qqbot.health.help') }}</p>
            <label class="flex items-center gap-2"><input v-model="healthNotifications.enabled" data-testid="health-enabled" type="checkbox" />{{ t('qqbot.health.enabled') }}</label>
            <label class="flex items-center gap-2"><input v-model="healthNotifications.mention_all" data-testid="health-mention-all" type="checkbox" />{{ t('qqbot.health.mentionAll') }}</label>
            <p class="text-xs text-gray-500">{{ t('qqbot.announcement.mentionHelp') }}</p>
            <QQGroupPicker v-model="healthGroups" :groups="allowedGroups" :policies="groupPolicies" :names="groupNames" :label="t('qqbot.health.groups')" allow-all />
            <div class="grid gap-4 md:grid-cols-2">
              <label v-for="field in healthFields" :key="field.key" class="space-y-1">{{ t(`qqbot.health.${field.key}`) }}<input v-model.number="healthNotifications[field.key]" :data-testid="`health-${field.key}`" type="number" :min="field.min" :max="field.max" :step="field.step" required class="input" /></label>
            </div>
            <p class="text-xs text-gray-500">{{ t('qqbot.health.recoveryHelp') }}</p>
          </fieldset>
          <details>
            <summary class="cursor-pointer text-sm font-medium">{{ t('qqbot.priceNotifications.records') }}</summary>
            <p v-if="!notificationJobs.length" class="mt-3 text-sm text-gray-500">{{ t('qqbot.priceNotifications.empty') }}</p>
            <div v-for="job in notificationJobs" :key="job.id" class="mt-3 space-y-1 rounded-lg border p-3 text-sm dark:border-dark-700">
              <p class="break-all">{{ job.group_id }}</p>
              <p>{{ new Date(job.created_at).toLocaleString() }} · {{ t(`qqbot.priceNotifications.status.${job.status}`, job.status) }}</p>
              <p v-if="job.error" class="text-red-600 dark:text-red-400">{{ job.error }}</p>
            </div>
          </details>
        </section>
        <section v-show="activeTab === 'commands'" id="qqbot-settings-panel-commands" role="tabpanel" aria-labelledby="qqbot-settings-tab-commands" class="card space-y-4 p-6" :aria-label="t('qqbot.customCommands')">
          <QQCommandInventory v-if="activeTab === 'commands'" />
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h2 class="text-lg font-semibold">{{ t('qqbot.customCommands') }}</h2>
            <button type="button" class="btn btn-secondary" :disabled="busy || form.custom_commands.length >= 20" @click="addCustomCommand">{{ t('qqbot.addCustomCommand') }}</button>
          </div>
          <p class="text-sm text-gray-500">{{ t('qqbot.customCommandsHelp') }}</p>
          <p v-if="!form.custom_commands.length" class="text-sm text-gray-500">{{ t('qqbot.noCustomCommands') }}</p>
          <fieldset v-for="(custom, index) in form.custom_commands" :key="index" :disabled="busy" class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
            <legend class="px-1 text-sm font-medium">{{ t('qqbot.customCommandEntry', { index: index + 1 }) }}</legend>
            <div class="flex flex-wrap items-center justify-between gap-3">
              <label class="flex items-center gap-2"><input v-model="custom.enabled" type="checkbox" />{{ t('common.enabled') }}</label>
              <button type="button" class="btn btn-secondary text-sm" @click="form.custom_commands.splice(index, 1)">{{ t('common.delete') }}</button>
            </div>
            <label class="block space-y-1">{{ t('qqbot.customCommandName') }}<input v-model.trim="custom.command" class="input font-mono" placeholder="a" required maxlength="33" /></label>
            <label class="block space-y-1">{{ t('qqbot.customCommandReply') }}<textarea v-model="custom.reply" rows="4" class="input" :placeholder="t('qqbot.customReplyPlaceholder')" required /></label>
          </fieldset>
        </section>
        <section v-show="activeTab === 'chat'" id="qqbot-settings-panel-chat" role="tabpanel" aria-labelledby="qqbot-settings-tab-chat" class="card space-y-4 p-6">
          <h2 class="text-lg font-semibold">{{ t('qqbot.ai') }}</h2>
          <label class="flex items-center gap-2"><input v-model="form.chat_disabled" type="checkbox" />{{ t('qqbot.workspace.chatDisabled') }}</label>
          <p class="text-xs text-gray-500">{{ t('qqbot.workspace.chatDisabledHelp') }}</p>
          <QQBotSiteAISettings v-if="activeTab === 'chat'" ref="siteAISettings" v-model="form" v-model:quota="siteQuota" v-model:regenerate="regenerateSiteKey" />
          <details><summary class="cursor-pointer text-sm">{{ t('qqbot.workspace.advanced') }}</summary><label class="mt-3 block space-y-1">{{ t('qqbot.systemPrompt') }}<textarea v-model="form.system_prompt" rows="3" class="input" /></label>
          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1">{{ t('qqbot.maxTokens') }}<input v-model.number="form.max_output_tokens" class="input" type="number" min="64" max="8192" /></label>
            <label class="space-y-1">{{ t('qqbot.history') }}<input v-model.number="form.history_messages" data-testid="history-messages" aria-describedby="qq-history-help" class="input" type="number" min="0" max="100" step="1" required /></label>
          </div>
          <p id="qq-history-help" class="text-xs text-gray-500">{{ t('qqbot.historyHelp') }}</p></details>
        </section>
        <div v-show="activeTab === 'autonomous'" id="qqbot-settings-panel-autonomous" role="tabpanel" aria-labelledby="qqbot-settings-tab-autonomous" class="space-y-6">
          <QQBotAutonomousSettings v-model="autonomous" :runtime="runtime" :groups="allowedGroups" :policies="groupPolicies" :names="groupNames" :total-daily-chat-limit="form.total_daily_chat_limit" :chat-disabled="form.chat_disabled" />

          <QQBotKnowledgeSettings v-model="siteKnowledge" />
          <QQBotChatPreview v-if="baseline && activeTab === 'autonomous'" :config="baseline" />
        </div>
        <div v-show="activeTab === 'moderation'" id="qqbot-settings-panel-moderation" role="tabpanel" aria-labelledby="qqbot-settings-tab-moderation">
          <QQBotModerationSettings v-model="moderation" :app-id="form.app_id" :group-labels="groupLabels" :groups="allowedGroups" :runtime="form.app_id === savedAppId ? runtime : null" :disabled="busy" />
        </div>
        <div v-show="activeTab === 'group-admins'" id="qqbot-settings-panel-group-admins" role="tabpanel" aria-labelledby="qqbot-settings-tab-group-admins">
          <QQBotGroupAdminSettings v-model="groupAdmins" :app-id="form.app_id" :group-labels="groupLabels" :groups="allowedGroups" :runtime="form.app_id === savedAppId ? runtime : null" :disabled="busy" />
        </div>
        <section v-show="activeTab === 'images'" id="qqbot-settings-panel-images" role="tabpanel" aria-labelledby="qqbot-settings-tab-images" class="card space-y-4 p-6">
          <div class="flex items-center justify-between"><h2 class="text-lg font-semibold">{{ t('qqbot.images') }}</h2><label class="flex items-center gap-2"><input v-model="form.image_enabled" type="checkbox" />{{ t('common.enabled') }}</label></div>
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('qqbot.chatImageHelp') }}</p>
          <QQBotImageAISettings v-if="activeTab === 'images'" ref="imageAISettings" v-model="form" v-model:quota="imageSiteQuota" v-model:regenerate="regenerateImageSiteKey" :saved-group-id="baseline?.image_site_group_id" />
          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1">{{ t('qqbot.imageSize') }}<select v-model="form.image_size" class="input"><option>1024x1024</option><option>1536x1024</option><option>1024x1536</option><option>auto</option></select></label>
            <label class="space-y-1">{{ t('qqbot.imageOutputFormat') }}<select v-model="form.image_output_format" data-testid="image-output-format" class="input"><option value="">{{ t('qqbot.imageOutputDefault') }}</option><option value="png">PNG</option><option value="jpeg">JPEG</option><option value="webp">WebP</option><option value="gif">GIF</option></select></label>
          </div>
          <p class="text-sm text-gray-500">{{ t('qqbot.imageHelp') }}</p>
          <p class="text-sm text-gray-500">{{ t('qqbot.imageGIFHelp') }}</p>
        </section>
        <section v-show="activeTab === 'limits'" id="qqbot-settings-panel-limits" role="tabpanel" aria-labelledby="qqbot-settings-tab-limits" class="card space-y-4 p-6">
          <label class="block space-y-1">{{ t('qqbot.workspace.rewardAlert') }}<input v-model.number="form.reward_alert_daily" class="input" type="number" min="0" max="1000000" step="0.01" /></label><p class="text-xs text-gray-500">{{ t('qqbot.workspace.rewardAlertHelp') }}</p>
          <h2 class="text-lg font-semibold">{{ t('qqbot.limits') }}</h2><p class="text-sm text-gray-500">{{ t('qqbot.limitsHelp') }}</p>
          <div class="grid gap-4 md:grid-cols-2">
            <label v-for="field in limitFields" :key="field.key" class="space-y-1">{{ t(`qqbot.${field.label}`) }}<input v-model.number="form[field.key]" type="number" min="0" :max="field.max" class="input" /></label>
          </div>
        </section>
        <div class="sm:sticky sm:bottom-3 z-10 flex flex-wrap items-center gap-3 rounded-xl border bg-white p-4 shadow-lg dark:border-dark-700 dark:bg-dark-900">
          <span class="text-sm">{{ t(isTabDirty(activeTab) ? 'qqbot.workspace.unsaved' : 'qqbot.workspace.saved') }}</span>
          <button type="submit" :disabled="busy || !isTabDirty(activeTab)" class="btn btn-primary">{{ t('qqbot.workspace.saveModule', { name: t(tabs.find(tab => tab.key === activeTab)!.label) }) }}</button>
          <button type="button" :disabled="busy" class="btn btn-secondary" @click="reloadModule">{{ t('qqbot.workspace.reloadModule') }}</button>
        </div>
        <details v-if="visibleProbeKinds.length" :key="activeTab" class="card p-4">
          <summary class="cursor-pointer">{{ t('qqbot.workspace.capabilityTests') }}</summary>
          <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ dirty ? t('qqbot.saveBeforeTest') : t(activeTab === 'connection' ? 'qqbot.workspace.credentialsTestHelp' : 'qqbot.testCost') }}</p>
          <div class="mt-3 flex flex-wrap gap-2"><button v-for="kind in visibleProbeKinds" :key="kind" type="button" class="btn btn-secondary" :disabled="busy || dirty" @click="probe(kind)">{{ t(probeLabels[kind]) }}</button></div>
        </details>
        <details class="card p-4"><summary class="cursor-pointer">{{ t('qqbot.workspace.changes') }}</summary><ul class="mt-3 space-y-2 break-words text-sm [overflow-wrap:anywhere]"><li v-for="change in changes" :key="change">{{ change }}</li></ul></details>
        <details class="card p-4" @toggle="loadHistory"><summary class="cursor-pointer">{{ t('qqbot.workspace.history') }}</summary><article v-for="(item, index) in history" :key="index" class="mt-3 flex flex-wrap items-center justify-between gap-2 border-t pt-3 text-sm"><span>{{ new Date(item.at).toLocaleString() }} · {{ t(item.section === 'knowledge' ? 'qqbot.knowledge.title' : tabs.find(tab => tab.key === item.section)?.label || item.section) }}</span><button type="button" class="btn btn-secondary" :disabled="busy" @click="restoreDraft(item)">{{ t('qqbot.workspace.restoreDraft') }}</button></article><p class="mt-2 text-xs text-gray-500">{{ t('qqbot.workspace.restoreHelp') }}</p></details>
      </fieldset></form>

    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import QQCommunityNav from '@/components/admin/qqbot/QQCommunityNav.vue'
import { computed, nextTick, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useQQGroupNames } from '@/composables/useQQGroupNames'
import { useRoute, useRouter, onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import QQBotSiteAISettings from '@/components/admin/qqbot/QQBotSiteAISettings.vue'
import QQBotImageAISettings from '@/components/admin/qqbot/QQBotImageAISettings.vue'
import QQCommandInventory from '@/components/admin/qqbot/QQCommandInventory.vue'
import QQGroupPicker from '@/components/admin/qqbot/QQGroupPicker.vue'
import QQGroupPolicies from '@/components/admin/qqbot/QQGroupPolicies.vue'
import { configSections, pickSection, cloneConfig, type ConfigSection } from '@/components/admin/qqbot/configSections'
import type { QQConfigChange } from '@/api/qqbot'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import QQBotAutonomousSettings from '@/components/admin/qqbot/QQBotAutonomousSettings.vue'
import QQBotKnowledgeSettings from '@/components/admin/qqbot/QQBotKnowledgeSettings.vue'
import QQBotChatPreview from '@/components/admin/qqbot/QQBotChatPreview.vue'
import QQBotAnnouncement from '@/components/admin/qqbot/QQBotAnnouncement.vue'
import QQBotGroupAdminSettings from '@/components/admin/qqbot/QQBotGroupAdminSettings.vue'
import QQBotModerationSettings from '@/components/admin/qqbot/QQBotModerationSettings.vue'
import { defaultModerationConfig } from '@/api/qqbot'
import { defaultAutonomousConfig } from '@/api/qqbot'
import { defaultHealthNotificationConfig, defaultSiteKnowledgeConfig, qqbotAPI, type QQBotConfig, type QQBotRuntime } from '@/api/qqbot'
const { t } = useI18n()
const tabs = [
  { key: 'connection', label: 'qqbot.connection' },
  { key: 'groups', label: 'qqbot.workspace.groups' },
  { key: 'chat', label: 'qqbot.ai' },
  { key: 'group-admins', label: 'qqbot.groupAdmins.title' },
  { key: 'moderation', label: 'qqbot.moderation.title' },
  { key: 'autonomous', label: 'qqbot.autonomous.workspaceTitle' },
  { key: 'images', label: 'qqbot.images' },
  { key: 'commands', label: 'qqbot.customCommands' },
  { key: 'notifications', label: 'qqbot.priceNotifications.title' },
  { key: 'limits', label: 'qqbot.limits' }
] as const
type SettingsTab = (typeof tabs)[number]['key']
const route = useRoute()
const router = useRouter()
const groupMode = computed(() => route?.path === '/admin/qqbot/groups')
function resolveTab(value: unknown): SettingsTab {
  return value === 'knowledge' ? 'autonomous' : tabs.find(tab => tab.key === value)?.key || (groupMode.value ? 'groups' : 'connection')
}
const activeTab = ref<SettingsTab>(resolveTab(route?.query.tab))
function tabSections(tab: SettingsTab): ConfigSection[] { return tab === 'autonomous' ? ['autonomous', 'knowledge'] : [tab] }
function isTabDirty(tab: SettingsTab) { return (tab === 'chat' && regenerateSiteKey.value) || (tab === 'images' && regenerateImageSiteKey.value) || tabSections(tab).some(section => dirtySections.value.includes(section)) }
const categories = computed(() => groupMode.value ? [{ key: 'groups', label: 'qqbot.workspace.groups', tabs: ['groups', 'group-admins', 'moderation'] }] : [
  { key: 'connect', label: 'qqbot.workspace.connectCategory', tabs: ['connection', 'chat', 'images'] },
  { key: 'behavior', label: 'qqbot.workspace.behaviorCategory', tabs: ['autonomous', 'commands'] },
  { key: 'operations', label: 'qqbot.workspace.operationsCategory', tabs: ['notifications', 'limits'] }
])
const activeCategory = computed(() => categories.value.find(c => c.tabs.includes(activeTab.value))?.key || categories.value[0]!.key)
const visibleTabs = computed(() => tabs.filter(tab => categories.value.find(c => c.key === activeCategory.value)?.tabs.includes(tab.key)))
function selectCategory(key: string) { activeTab.value = categories.value.find(c => c.key === key)!.tabs[0] as SettingsTab }
watch(activeTab, tab => { if (route && router && route.query.tab !== tab) void router.replace({ query: { ...route.query, tab } }) })
watch(() => route?.path, async () => { activeTab.value = resolveTab(route?.query.tab); busy.value = true; try { apply(await qqbotAPI.config()) } catch (e) { error.value = message(e) } finally { busy.value = false } })
watch(() => route?.query.tab, value => { if (value) activeTab.value = resolveTab(value) })
const settingsForm = ref<HTMLFormElement | null>(null)
const siteAISettings = ref<InstanceType<typeof QQBotSiteAISettings>>()
const imageAISettings = ref<InstanceType<typeof QQBotImageAISettings>>()
const form = ref<QQBotConfig | null>(null)
const siteKnowledge = computed({
  get: () => form.value?.site_knowledge ?? defaultSiteKnowledgeConfig(),
  set: value => { if (form.value) form.value.site_knowledge = value }
})
const groupAdmins = computed({
  get: () => form.value?.group_admins ?? [],
  set: value => { if (form.value) form.value.group_admins = value }
})
const runtime = ref<QQBotRuntime | null>(null)
const savedAppId = ref('')
const savedGroupNameSource = ref<{ appID: string; sandbox: boolean; revision: string; groups: string[] } | null>(null)
const runtimeIdentity = ref('')
const { names: groupNames, status: groupNamesStatus, loading: groupNamesLoading, refresh: refreshGroupNames } = useQQGroupNames(() => {
  const saved = savedGroupNameSource.value
  if (!saved || form.value?.app_id !== saved.appID || form.value?.sandbox !== saved.sandbox) return null
  const discovered = runtimeIdentity.value === JSON.stringify([saved.appID, saved.sandbox]) ? runtime.value?.groups.map(g => g.id) ?? [] : []
  return { ...saved, groups: [...new Set([...saved.groups, ...discovered])] }
})
const groupsText = ref('')
const allowedGroups = computed(() => [...new Set(groupsText.value.split(/[\s,，]+/).filter(Boolean))])
const groupLabels = computed(() => ({ ...groupNames.value, ...Object.fromEntries((form.value?.group_policies ?? []).filter(policy => policy.label).map(policy => [policy.group_id, policy.label])) }))
const autonomous = ref(defaultAutonomousConfig())
const moderation = ref(defaultModerationConfig())
const healthNotifications = ref(defaultHealthNotificationConfig())
const healthGroupsText = ref('')
const healthFields = [
  { key: 'minimum_samples', min: 1, max: 1000000, step: 1 },
  { key: 'error_percent', min: 0.01, max: 100, step: 0.01 },
  { key: 'recovery_percent', min: 0, max: 99.99, step: 0.01 },
  { key: 'ttft_seconds', min: 0, max: 3600, step: 1 },
  { key: 'failure_minutes', min: 1, max: 60, step: 1 },
  { key: 'recovery_minutes', min: 1, max: 60, step: 1 },
  { key: 'cooldown_minutes', min: 0, max: 1440, step: 1 }
] as const
const notificationGroupsText = ref('')
const priceNotifications = ref({ enabled: false, scheduled: true, mention_all: false, groups: [] as string[] })
const notificationJobs = computed(() => runtime.value?.jobs.filter(job => ['price_notification', 'health_notification', 'ranking_notification', 'announcement'].includes(job.kind)) ?? [])
const baseline = ref<QQBotConfig | null>(null)
const history = ref<QQConfigChange[]>([])
const parseGroups = (text: string) => [...new Set(text.split(/[\s,，]+/).filter(Boolean))]
const selectedGroups = computed({ get: () => allowedGroups.value, set: value => { groupsText.value = value.join('\n') } })
const knownGroups = computed(() => [...new Set([...allowedGroups.value, ...(runtime.value?.groups.map(g => g.id) || [])])])
const groupPolicies = computed({ get: () => form.value?.group_policies || [], set: value => { if (form.value) form.value.group_policies = value } })
const notificationGroups = computed({ get: () => parseGroups(notificationGroupsText.value), set: value => { notificationGroupsText.value = value.join('\n') } })
const healthGroups = computed({ get: () => parseGroups(healthGroupsText.value), set: value => { healthGroupsText.value = value.join('\n') } })
const siteQuota = ref(0), regenerateSiteKey = ref(false)
const imageSiteQuota = ref(0), regenerateImageSiteKey = ref(false)
const busy = ref(false)
const refreshingRuntime = ref(false)
const error = ref('')
const notice = ref('')
type ProbeKind = 'qq' | 'chat' | 'vision' | 'tools' | 'image' | 'image_edit'
const probeLabels: Record<ProbeKind, string> = { qq: 'qqbot.test_qq', chat: 'qqbot.test_chat', vision: 'qqbot.test_vision', tools: 'qqbot.test_tools', image: 'qqbot.test_image', image_edit: 'qqbot.test_image_edit' }
const visibleProbeKinds = computed<ProbeKind[]>(() => activeTab.value === 'connection' ? ['qq'] : activeTab.value === 'chat' ? ['chat', 'vision', 'tools'] : activeTab.value === 'images' ? ['image', 'image_edit'] : [])
const limitFields = [
  { key: 'daily_chat_limit', label: 'dailyChat', max: 100000 },
  { key: 'daily_image_limit', label: 'dailyImage', max: 100000 },
  { key: 'total_daily_chat_limit', label: 'totalChat', max: 100000 },
  { key: 'total_daily_image_limit', label: 'totalImage', max: 100000 },
  { key: 'cooldown_seconds', label: 'cooldown', max: 3600 }
] as const
function draft(): QQBotConfig {
 return { ...form.value!, custom_commands: form.value!.custom_commands.map(command => ({ ...command, command: command.command && !command.command.startsWith('/') ? `/${command.command}` : command.command })), allowed_groups: allowedGroups.value, group_policies: groupPolicies.value.filter(p => allowedGroups.value.includes(p.group_id)), autonomous: autonomous.value, moderation: moderation.value, price_notifications: { ...priceNotifications.value, groups: notificationGroups.value }, health_notifications: { ...healthNotifications.value, groups: healthGroups.value } }
}
const dirtySections = computed(() => !baseline.value || !form.value ? [] : (Object.keys(configSections) as ConfigSection[]).filter(section => JSON.stringify(pickSection(draft(), section)) !== JSON.stringify(pickSection(baseline.value!, section))))
const dirty = computed(() => dirtySections.value.length > 0 || regenerateSiteKey.value || regenerateImageSiteKey.value)
const changes = computed(() => {
 if (!baseline.value || !form.value) return []
 const current = draft()
 return tabSections(activeTab.value).flatMap(section => [...configSections[section]]).filter(key => JSON.stringify(current[key]) !== JSON.stringify(baseline.value![key])).map(key => `${key}: ${/secret|api_key/.test(key) ? t('qqbot.workspace.secretChanged') : JSON.stringify(baseline.value![key] ?? null) + ' → ' + JSON.stringify(current[key] ?? null)}`)
})
const webhookURL = computed(() => `${window.location.origin}/api/v1/qqbot/webhook`)
async function handleTabKeydown(event: KeyboardEvent, tab: SettingsTab) {
  const navTabs = visibleTabs.value
  const index = navTabs.findIndex(item => item.key === tab)
  let next: number
  switch (event.key) {
    case 'ArrowRight': next = (index + 1) % navTabs.length; break
    case 'ArrowLeft': next = (index + navTabs.length - 1) % navTabs.length; break
    case 'Home': next = 0; break
    case 'End': next = navTabs.length - 1; break
    default: return
  }
  event.preventDefault()
  activeTab.value = navTabs[next]!.key
  await nextTick()
  document.getElementById(`qqbot-settings-tab-${activeTab.value}`)?.focus()
}
function secretHint(set: boolean) { return t(set ? 'qqbot.secretStored' : 'qqbot.secretEmpty') }
function message(e: unknown) { return (e as {message?: string})?.message || t('common.error') }
function apply(c: QQBotConfig, updateBaseline = true) {
  if (updateBaseline) {
    savedAppId.value = c.app_id
    savedGroupNameSource.value = { appID: c.app_id, sandbox: c.sandbox, revision: c.section_versions?.connection || '', groups: [...c.allowed_groups] }
  }
  form.value = cloneConfig({ ...c, image_output_format: c.image_output_format ?? '', notification_quiet_hours: c.notification_quiet_hours ?? { enabled: false, start_hour: 23, end_hour: 8 }, chat_disabled: c.chat_disabled ?? false, group_policies: c.group_policies ?? [], reward_alert_daily: c.reward_alert_daily ?? 0, custom_commands: (c.custom_commands ?? []).map(command => ({ ...command, command: command.command.replace(/^\//, '') })), site_knowledge: { ...defaultSiteKnowledgeConfig(), ...c.site_knowledge } })
  autonomous.value = { ...defaultAutonomousConfig(), ...c.autonomous }
  moderation.value = { ...defaultModerationConfig(), ...c.moderation }
  healthNotifications.value = { ...defaultHealthNotificationConfig(), ...c.health_notifications }
  healthGroupsText.value = healthNotifications.value.groups.join('\n')
  groupsText.value = (c.allowed_groups || []).join('\n')
  priceNotifications.value = { enabled: false, scheduled: true, mention_all: false, ...c.price_notifications, groups: c.price_notifications?.groups ?? [] }
  notificationGroupsText.value = priceNotifications.value.groups.join('\n')
  if (updateBaseline) baseline.value = cloneConfig(draft())
}
function addCustomCommand() {
  if (!form.value || busy.value || form.value.custom_commands.length >= 20) return
  form.value.custom_commands.push({ command: '', reply: '', enabled: true })
}
async function refreshRuntime() {
  if (refreshingRuntime.value) return
  refreshingRuntime.value = true
  const identity = JSON.stringify([savedGroupNameSource.value?.appID, savedGroupNameSource.value?.sandbox])
  try {
    const data = await qqbotAPI.runtime()
    if (identity === JSON.stringify([savedGroupNameSource.value?.appID, savedGroupNameSource.value?.sandbox])) { runtime.value = data; runtimeIdentity.value = identity }
  } catch (e) { error.value = message(e) } finally { refreshingRuntime.value = false }
}
async function refreshStatus() { await refreshRuntime(); refreshGroupNames() }
async function deleteDiscoveredGroup(id: string) {
  if (busy.value || refreshingRuntime.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await qqbotAPI.deleteDiscoveredGroup(id)
    if (runtime.value) runtime.value.groups = runtime.value.groups.filter(group => group.id !== id)
    notice.value = t('qqbot.discoveredGroupDeleted')
  } catch (e) { error.value = message(e) } finally { busy.value = false }
}
async function save() {
  if (!form.value || busy.value) return
  if (activeTab.value === 'chat' && siteAISettings.value && !siteAISettings.value.validate()) return
  if (activeTab.value === 'images' && imageAISettings.value && !imageAISettings.value.validate()) return
  const invalidField = settingsForm.value?.querySelector<HTMLInputElement>(`#qqbot-settings-panel-${activeTab.value} input:invalid, #qqbot-settings-panel-${activeTab.value} textarea:invalid, #qqbot-settings-panel-${activeTab.value} select:invalid`)
  if (invalidField) { invalidField.closest('details')?.setAttribute('open', ''); invalidField.reportValidity(); return }
  const section = activeTab.value
  const pending = cloneConfig(draft())
  const previousBaseline = cloneConfig(baseline.value!)
  const sections = section === 'autonomous' ? tabSections(section).filter(key => dirtySections.value.includes(key)) : [section]
  if (!sections.length) return
  const otherDrafts = dirtySections.value.filter(key => !sections.includes(key))
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const saved = sections.length > 1
      ? await qqbotAPI.saveSetup({ config: pending, sections, versions: baseline.value?.section_versions || {}, site_quota: 0 })
      : section === 'chat' && (pending.ai_connection === 'site' || baseline.value?.ai_connection === 'site' || pending.ai_connection !== baseline.value?.ai_connection)
      ? await qqbotAPI.saveSetup({ config: pending, sections: [section], versions: baseline.value?.section_versions || {}, site_quota: siteQuota.value, regenerate_site_key: regenerateSiteKey.value })
      : section === 'images' && (pending.image_ai_connection === 'site' || baseline.value?.image_ai_connection === 'site' || pending.image_ai_connection !== baseline.value?.image_ai_connection)
      ? await qqbotAPI.saveSetup({ config: pending, sections: [section], versions: baseline.value?.section_versions || {}, site_quota: 0, image_site_quota: imageSiteQuota.value, regenerate_image_site_key: regenerateImageSiteKey.value })
      : await qqbotAPI.saveSection(sections[0]!, baseline.value?.section_versions?.[sections[0]!] || '', pending)
    if (section === 'chat') regenerateSiteKey.value = false
    if (section === 'images') regenerateImageSiteKey.value = false
    apply(saved)
    const merged = cloneConfig(draft())
    for (const key of otherDrafts) {
      Object.assign(merged, pickSection(pending, key))
      Object.assign(baseline.value!, pickSection(previousBaseline, key))
      if (baseline.value!.section_versions && previousBaseline.section_versions) baseline.value!.section_versions[key] = previousBaseline.section_versions[key]!
    }
    apply(merged, false)
    notice.value = saved.setup_warning || t('common.saved')
    await refreshRuntime()
  } catch (e) { error.value = message(e) } finally { busy.value = false }
}
async function reloadModule() {
 if (isTabDirty(activeTab.value) && !window.confirm(t('qqbot.workspace.discardModule'))) return
 busy.value = true
 try {
  const fresh = await qqbotAPI.config()
  const merged = draft()
  const sections = tabSections(activeTab.value)
  for (const section of sections) Object.assign(merged, pickSection(fresh, section))
  const oldBaseline = cloneConfig(baseline.value!)
  apply(fresh)
  for (const section of sections) Object.assign(oldBaseline, pickSection(baseline.value!, section), { section_versions: { ...oldBaseline.section_versions, [section]: fresh.section_versions?.[section] || '' } })
  baseline.value = oldBaseline
  apply(merged, false)
  if (activeTab.value === 'chat') regenerateSiteKey.value = false
  if (activeTab.value === 'images') { regenerateImageSiteKey.value = false; await imageAISettings.value?.reload() }
  error.value = ''
 } catch (e) { error.value = message(e) } finally { busy.value = false }
}
async function loadHistory(event: Event) { if (!(event.target as HTMLDetailsElement).open) return; try { history.value = (await qqbotAPI.configHistory()).filter(item => ['groups', 'group-admins', 'moderation'].includes(item.section) === groupMode.value) } catch (e) { error.value = message(e) } }
function restoreDraft(item: QQConfigChange) {
 const section = item.section as ConfigSection
 if (!(section in configSections)) return
 if (dirtySections.value.includes(section) && !window.confirm(t('qqbot.workspace.discardModule'))) return
 const merged = draft()
 Object.assign(merged, item.before)
 apply(merged, false)
 activeTab.value = resolveTab(section)
 notice.value = t('qqbot.workspace.restoreHelp')
}
function confirmLeave() { return !dirty.value || window.confirm(t('qqbot.workspace.leaveConfirm')) }
onBeforeRouteLeave(confirmLeave)
onBeforeRouteUpdate((to, from) => to.path === from.path || confirmLeave())
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
window.addEventListener('beforeunload', beforeUnload)
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))
function addGroup(id: string) { groupsText.value = [...new Set([...groupsText.value.split(/[\s,，]+/).filter(Boolean), id])].join('\n') }
async function probe(kind: ProbeKind) {
  if (kind !== 'qq' && !window.confirm(t('qqbot.workspace.probeConfirm'))) return
  busy.value = true; error.value = ''; notice.value = ''
  try { notice.value = (await qqbotAPI.probe(kind)).message } catch (e) { error.value = message(e) } finally { busy.value = false }
}
onMounted(async () => { if (route?.path === '/admin/qqbot/config' && ['groups', 'group-admins', 'moderation'].includes(activeTab.value)) { await router.replace({ path: '/admin/qqbot/groups', query: { tab: activeTab.value } }) } busy.value = true; try { apply(await qqbotAPI.config()); await refreshRuntime() } catch (e) { error.value = message(e) } finally { busy.value = false } })
</script>
