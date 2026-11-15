<template>
  <section class="card mt-6 space-y-4 p-6" :aria-label="t('qqbot.chatPreview.title')">
    <h2 class="text-lg font-semibold">{{ t('qqbot.chatPreview.title') }}</h2>
    <p class="text-sm text-gray-500">{{ t('qqbot.chatPreview.help') }}</p>
    <p class="rounded bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-950 dark:text-amber-200">{{ t('qqbot.chatPreview.savedOnly') }}</p>
    <div class="grid gap-4 md:grid-cols-2">
      <label class="space-y-1 text-sm">{{ t('qqbot.group') }}
        <select v-model="group" class="input" data-testid="qq-preview-group">
          <option value="">{{ t('qqbot.workspace.chooseGroup') }}</option>
          <option v-for="id in config.allowed_groups" :key="id" :value="id">{{ config.group_policies?.find(policy => policy.group_id === id)?.label || id }}</option>
        </select>
      </label>
      <div>
        <QQMemberPicker v-if="group" v-model="member" :app-id="config.app_id" :group-id="group" :disabled="busy" @select="selectMember" />
        <p class="mt-1 text-xs text-gray-500">{{ t('qqbot.chatPreview.identityHelp') }}</p>
      </div>
    </div>
    <label class="block space-y-1 text-sm">{{ t('qqbot.chatPreview.question') }}
      <textarea v-model="question" rows="3" class="input" maxlength="6000" data-testid="qq-preview-question" :placeholder="t('qqbot.chatPreview.placeholder')" @keydown.ctrl.enter.prevent="preview" @keydown.meta.enter.prevent="preview" />
    </label>
    <button type="button" class="btn btn-secondary" data-testid="qq-preview-run" :disabled="busy || !group || !question.trim()" @click="preview">{{ t(busy ? 'qqbot.chatPreview.running' : 'qqbot.chatPreview.run') }}</button>
    <p v-if="busy" role="status" class="text-sm text-gray-500">{{ t('qqbot.chatPreview.wait') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <article v-if="result" class="space-y-3 rounded-lg border p-4 dark:border-dark-700" data-testid="qq-preview-result">
      <p class="text-xs text-gray-500">{{ t('qqbot.chatPreview.action') }}: {{ actionLabel(result.action) }} · {{ t(result.chat_execution?.usage_complete === false ? 'qqbot.chatExecution.knownTokens' : 'qqbot.chatExecution.tokens') }}: {{ result.input_tokens }} / {{ result.output_tokens }} · {{ t('qqbot.chatPreview.duration', { ms: result.duration_ms }) }}</p>
      <div v-if="result.chat_execution?.attempts" class="space-y-1 text-xs" data-testid="qq-preview-chat-execution">
        <p>{{ t('qqbot.chatExecution.attempts', { attempts: result.chat_execution.attempts, retries: result.chat_execution.retries }) }}</p>
        <p v-if="result.chat_execution.last_error_code">{{ t('qqbot.chatExecution.lastError') }}: {{ result.chat_execution.last_error_code }}</p>
      </div>
      <p v-if="result.chat_execution?.usage_complete === false" class="text-xs text-amber-700 dark:text-amber-300" data-testid="qq-preview-usage-incomplete">{{ t('qqbot.chatExecution.incompleteUsage') }}</p>
      <p v-if="result.query" class="text-xs">{{ t(`qqbot.siteQuery.kinds.${result.query.kind}`) }} · {{ t(`qqbot.siteQuery.statuses.${result.query.status}`) }}<span v-if="result.query.error_code"> · {{ result.query.error_code }}</span></p>
      <p class="whitespace-pre-wrap break-words text-sm [overflow-wrap:anywhere]">{{ result.text }}</p>
      <div v-if="result.knowledge_sources?.length" class="space-y-1 border-t pt-3 text-xs dark:border-dark-700">
        <p class="font-medium">{{ t('qqbot.chatPreview.sources') }}</p>
        <p v-for="source in result.knowledge_sources" :key="source.id" class="break-words">
          <a v-if="safeURL(source.url)" :href="safeURL(source.url)" target="_blank" rel="noopener noreferrer" class="underline">{{ source.title || source.id }}</a>
          <span v-else>{{ source.title || source.id }}</span>
          <span v-if="source.updated_at" class="text-gray-500"> · {{ t('qqbot.chatPreview.updated', { time: source.updated_at }) }}</span>
        </p>
      </div>
    </article>
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQBotConfig, type QQChatPreviewResult, type QQMemberOption } from '@/api/qqbot'
import QQMemberPicker from './QQMemberPicker.vue'
const props = defineProps<{ config: QQBotConfig }>()
const { t, te } = useI18n()
const group = ref(''), member = ref(''), question = ref(''), busy = ref(false), error = ref('')
const result = ref<QQChatPreviewResult>()
let request = 0
function invalidate() { ++request; result.value = undefined; error.value = '' }
watch([group, member, question, () => JSON.stringify(props.config)], invalidate, { flush: 'sync' })
watch(group, () => { member.value = '' }, { flush: 'sync' })
watch(() => props.config.app_id, () => { group.value = ''; member.value = '' }, { flush: 'sync' })
watch(() => props.config.allowed_groups, groups => { if (!groups.includes(group.value)) group.value = '' }, { deep: true, flush: 'sync' })
function selectMember(selected: QQMemberOption) { if (selected.group_id === group.value) member.value = selected.member_id }
function safeURL(value: string) {
  try { const url = new URL(value); return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : undefined } catch { return undefined }
}
function actionLabel(action: string) { const key = `qqbot.chatPreview.actions.${action}`; return te(key) ? t(key) : action }
async function preview() {
  if (busy.value || !group.value || !question.value.trim()) return
  const seq = ++request
  busy.value = true; error.value = ''; result.value = undefined
  try {
    const value = await qqbotAPI.chatPreview({ group_id: group.value, member_id: member.value || undefined, question: question.value.trim() })
    if (seq === request) result.value = value
  } catch (e) { if (seq === request) error.value = (e as Error).message || t('common.error') }
  finally { busy.value = false }
}
onBeforeUnmount(invalidate)
</script>
