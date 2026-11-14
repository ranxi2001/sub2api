<template>
  <nav :aria-label="t('qqbot.title')" class="mb-5 flex flex-wrap items-center gap-2">
    <div v-for="item in items" :key="item.path" class="flex items-center gap-1">
      <RouterLink :to="`/admin/qqbot/${item.path}`" class="btn btn-secondary" active-class="!border-primary-500 !text-primary-600">{{ t(item.label) }}</RouterLink>
      <NewFeatureBadge v-if="item.path === 'overview'" :notice-id="NEW_FEATURE_NOTICES.qqCommunity" :user-id="auth.user?.id" />
    </div>
    <div class="flex items-center gap-1"><RouterLink to="/admin/orders/coupons" class="btn btn-secondary">{{ t('coupon.adminTitle') }}</RouterLink><NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.qqRewardCoupons" :user-id="auth.user?.id" /></div>
  </nav>
</template>
<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
const { t } = useI18n(), auth = useAuthStore()
const items = [
  { path: 'overview', label: 'qqbot.workspace.overview' },
  { path: 'setup', label: 'qqbot.simple.setup' },
  { path: 'groups', label: 'qqbot.workspace.groups' },
  { path: 'config', label: 'qqbot.basicConfig' },
  { path: 'activities', label: 'qqbot.activitiesTitle' },
  { path: 'bindings', label: 'qqbot.bindingsTitle' },
  { path: 'records', label: 'qqbot.recordsTitle' },
]
</script>
