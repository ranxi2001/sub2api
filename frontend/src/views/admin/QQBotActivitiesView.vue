<template>
  <AppLayout>
    <QQCommunityNav />
    <div class="mx-auto max-w-5xl space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="text-2xl font-semibold">{{ t('qqbot.activitiesTitle') }}</h1>
        <p class="mt-2 text-sm text-gray-500">{{ t('qqbot.activitiesHelp') }}</p></div>
        <RouterLink v-if="!['templates', 'rewards', 'growth', 'rankings', 'preview'].includes(activeTab.key)" class="btn btn-secondary" :to="{ path: '/admin/qqbot/records', query: { tab: activeTab.key } }">{{ t('qqbot.recordsTitle') }}</RouterLink>
      </div>
      <nav
        role="tablist"
        :aria-label="t('qqbot.activitiesTitle')"
        class="flex flex-wrap gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-800"
      >
        <button
          v-for="tab in tabs"
          :id="`qqbot-tab-${tab.key}`"
          :key="tab.key"
          type="button"
          role="tab"
          :aria-selected="activeTab.key === tab.key"
          :aria-controls="`qqbot-panel-${tab.key}`"
          :tabindex="activeTab.key === tab.key ? 0 : -1"
          class="min-h-[44px] min-w-[100px] flex-1 rounded-lg px-2 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
          :class="activeTab.key === tab.key
            ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300'
            : 'text-gray-600 hover:bg-white/60 dark:text-gray-300 dark:hover:bg-dark-700/60'"
          @click="selectTab(tab.key)"
          @keydown="handleKeydown($event, tab.key)"
        >
          {{ t(tab.label) }}
        </button>
      </nav>
      <div
        :id="`qqbot-panel-${activeTab.key}`"
        role="tabpanel"
        :aria-labelledby="`qqbot-tab-${activeTab.key}`"
      >
        <KeepAlive :key="activityRevision">
          <component :is="activeTab.component" :key="activeTab.key" :before-apply="beforeApply" @applied="activityRevision++" />
        </KeepAlive>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import QQCommunityNav from '@/components/admin/qqbot/QQCommunityNav.vue'
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import QQBotActivityTemplates from '@/components/admin/qqbot/QQBotActivityTemplates.vue'
import { hasQQDrafts } from '@/components/admin/qqbot/useQQDraft'
import QQBotCheckinSettings from '@/components/admin/qqbot/QQBotCheckinSettings.vue'
import QQBotGrowthSettings from '@/components/admin/qqbot/QQBotGrowthSettings.vue'
import QQBotLotterySettings from '@/components/admin/qqbot/QQBotLotterySettings.vue'
import QQBotInviteSettings from '@/components/admin/qqbot/QQBotInviteSettings.vue'
import QQBotRankingSettings from '@/components/admin/qqbot/QQBotRankingSettings.vue'
import QQActivityPreview from '@/components/admin/qqbot/QQActivityPreview.vue'
import QQBotRewardCodes from '@/components/admin/qqbot/QQBotRewardCodes.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
onMounted(() => { if (route.query.tab === 'transfer') void router.replace({ path: '/admin/qqbot/bindings', query: { tab: 'transfer' } }) })
const activityRevision = ref(0)
function beforeApply() { return !hasQQDrafts(router) || window.confirm(t('qqbot.simple.discardActivities')) }
const tabs = [
  { key: 'checkin', label: 'qqbot.checkinTitle', component: QQBotCheckinSettings },
  { key: 'growth', label: 'qqbot.growth.tab', component: QQBotGrowthSettings },
  { key: 'rankings', label: 'qqbot.rankings.tab', component: QQBotRankingSettings },
  { key: 'lottery', label: 'qqbot.lotteryTitle', component: QQBotLotterySettings },
  { key: 'invite', label: 'qqbot.inviteTitle', component: QQBotInviteSettings },
  { key: 'preview', label: 'qqbot.workspace.preview', component: QQActivityPreview },
  { key: 'templates', label: 'qqbot.simple.templates', component: QQBotActivityTemplates },
  { key: 'rewards', label: 'qqbot.redemption.title', component: QQBotRewardCodes }
] as const
type ActivityTab = (typeof tabs)[number]['key']
const activeTab = computed(() => tabs.find(tab => tab.key === (route.query.tab === 'redemption' ? 'rewards' : route.query.tab)) ?? tabs[0])

async function selectTab(tab: ActivityTab) {
  if (activeTab.value.key !== tab) {
    await router.push({ query: { ...route.query, tab } })
  }
}
async function handleKeydown(event: KeyboardEvent, tab: ActivityTab) {
  const index = tabs.findIndex(item => item.key === tab)
  let next: number
  switch (event.key) {
    case 'ArrowRight': next = (index + 1) % tabs.length; break
    case 'ArrowLeft': next = (index + tabs.length - 1) % tabs.length; break
    case 'Home': next = 0; break
    case 'End': next = tabs.length - 1; break
    default: return
  }
  event.preventDefault()
  const target = tabs[next]!
  await selectTab(target.key)
  await nextTick()
  document.getElementById(`qqbot-tab-${target.key}`)?.focus()
}
</script>
