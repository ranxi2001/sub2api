<template>
  <AuthLayout>
    <div class="space-y-5">
      <h2 class="text-center text-2xl font-bold text-gray-900 dark:text-white">{{ t('qqbot.emailBindTitle') }}</h2>
      <p v-if="loading" role="status" class="text-center text-sm text-gray-500">{{ t('common.loading') }}</p>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-950 dark:text-red-200">{{ error }}</p>
      <div v-if="success" role="status" class="rounded-lg bg-green-50 p-4 text-green-800 dark:bg-green-950 dark:text-green-200">{{ t('qqbot.emailBindSuccess') }}</div>
      <template v-else-if="preview">
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('qqbot.emailBindHelp') }}</p>
        <dl class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <div><dt class="text-sm text-gray-500">{{ t('qqbot.emailNickname') }}</dt><dd class="break-all font-medium">{{ preview.nickname || 'QQ' }}</dd></div>
          <div><dt class="text-sm text-gray-500">{{ t('qqbot.emailMember') }}</dt><dd class="break-all font-mono text-sm">{{ preview.member_id }}</dd></div>
          <div><dt class="text-sm text-gray-500">{{ t('qqbot.group') }}</dt><dd class="break-all font-mono text-sm">{{ preview.group_id }}</dd></div>
        </dl>
        <p class="text-sm text-amber-700 dark:text-amber-300">{{ t('qqbot.emailBindWarning') }}</p>
        <button class="btn btn-primary w-full" :disabled="busy" @click="confirm">{{ busy ? t('common.loading') : t('qqbot.confirmBinding') }}</button>
      </template>
      <p v-if="!loading && !success && !preview" class="text-sm text-gray-500">{{ t('qqbot.emailBindRetry') }}</p>
    </div>
  </AuthLayout>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthLayout from '@/components/layout/AuthLayout.vue'
import { qqbotAPI, type QQEmailBindingPreview } from '@/api/qqbot'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const preview = ref<QQEmailBindingPreview | null>(null)
const loading = ref(true)
const busy = ref(false)
const success = ref(false)
const error = ref('')
let token = ''
let generation = 0

async function loadLink(hash: string) {
  const current = ++generation
  token = new URLSearchParams(hash.slice(1)).get('token') || ''
  const linkToken = token
  preview.value = null
  success.value = false
  error.value = ''
  loading.value = true
  busy.value = false
  try {
    // Clear both browser and router history while keeping the token in memory.
    if (hash) await router.replace({ path: route.path, query: route.query, hash: '' })
    if (!/^[a-f0-9]{48}$/.test(linkToken)) throw new Error(t('qqbot.emailBindInvalid'))
    const result = await qqbotAPI.previewEmail(linkToken)
    if (current === generation) preview.value = result
  } catch (e) {
    if (current === generation) error.value = (e as { message?: string })?.message || t('qqbot.emailBindInvalid')
  } finally {
    if (current === generation) loading.value = false
  }
}
onMounted(() => loadLink(route.hash))
watch(() => route.hash, (hash) => { if (hash) void loadLink(hash) })

async function confirm() {
  if (!preview.value || busy.value) return
  const current = generation
  busy.value = true
  error.value = ''
  try {
    await qqbotAPI.confirmEmail(token, preview.value.member_id)
    if (current !== generation) return
    token = ''
    preview.value = null
    success.value = true
  } catch (e) {
    if (current === generation) error.value = (e as { message?: string })?.message || t('qqbot.emailBindInvalid')
  } finally {
    if (current === generation) busy.value = false
  }
}
</script>
