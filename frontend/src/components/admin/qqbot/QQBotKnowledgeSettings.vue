<template>
  <section class="card space-y-4 p-6" :aria-label="t('qqbot.knowledge.title')">
    <h2 class="text-lg font-semibold">{{ t('qqbot.knowledge.title') }}</h2>
    <p class="text-sm text-gray-500">{{ t('qqbot.knowledge.help') }}</p>
    <label class="flex items-center gap-2">
      <input v-model="value.enabled" data-testid="knowledge-enabled" type="checkbox" />
      {{ t('qqbot.knowledge.enabled') }}
    </label>
    <label class="block space-y-2">
      <span>{{ t('qqbot.knowledge.content') }}</span>
      <textarea
        ref="textarea"
        v-model="value.content"
        data-testid="knowledge-content"
        class="input min-h-80"
        rows="16"
        :placeholder="t('qqbot.knowledge.placeholder')"
        :required="false"
        :aria-invalid="!!validationError"
        aria-describedby="qq-knowledge-scope qq-knowledge-count qq-knowledge-validation"
      />
    </label>
    <div class="flex items-center justify-between gap-3">
      <h3 class="font-semibold">{{ t('qqbot.knowledge.entries') }}</h3>
      <button type="button" class="btn btn-secondary" data-testid="knowledge-add" :disabled="entries.length >= 40" @click="addEntry">{{ t('qqbot.knowledge.add') }}</button>
    </div>
    <p class="text-xs text-gray-500">{{ t('qqbot.knowledge.entriesHelp') }}</p>
    <article v-for="(entry, index) in entries" :key="entry.id" class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <label class="flex items-center gap-2 text-sm"><input v-model="entry.enabled" type="checkbox" :data-testid="`knowledge-entry-enabled-${index}`" />{{ t('qqbot.knowledge.entryEnabled') }}</label>
        <button type="button" class="btn btn-secondary" :data-testid="`knowledge-remove-${index}`" @click="removeEntry(index)">{{ t('qqbot.knowledge.remove') }}</button>
      </div>
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block space-y-1 text-sm"><span>{{ t('qqbot.knowledge.entryTitle') }}</span><input v-model="entry.title" class="input" maxlength="120" :required="entry.enabled" :data-testid="`knowledge-entry-title-${index}`" /></label>
        <label class="block space-y-1 text-sm"><span>{{ t('qqbot.knowledge.category') }}</span><input v-model="entry.category" class="input" maxlength="60" :data-testid="`knowledge-entry-category-${index}`" /></label>
      </div>
      <label class="block space-y-1 text-sm"><span>{{ t('qqbot.knowledge.keywords') }}</span><input :value="keywordDrafts[entry.id] ?? entry.keywords.join(', ')" class="input" :placeholder="t('qqbot.knowledge.keywordsPlaceholder')" :data-testid="`knowledge-entry-keywords-${index}`" @input="setKeywords(index, ($event.target as HTMLInputElement).value)" /></label>
      <label class="block space-y-1 text-sm"><span>{{ t('qqbot.knowledge.entryContent') }}</span><textarea v-model="entry.content" class="input min-h-32" rows="5" :required="entry.enabled" :data-testid="`knowledge-entry-content-${index}`" /></label>
      <label class="block space-y-1 text-sm"><span>{{ t('qqbot.knowledge.url') }}</span><input v-model="entry.url" class="input" type="url" maxlength="2048" :data-testid="`knowledge-entry-url-${index}`" /></label>
      <p v-if="entry.updated_at" class="text-xs text-gray-500">{{ t('qqbot.knowledge.updatedAt') }}: {{ entry.updated_at }}</p>
    </article>
    <p id="qq-knowledge-count" data-testid="knowledge-count" class="text-xs text-gray-500">{{ t('qqbot.knowledge.count', { count: characterCount, max: maxCharacters }) }}</p>
    <p id="qq-knowledge-scope" class="text-xs text-gray-500">{{ t('qqbot.knowledge.scope') }}</p>
    <p id="qq-knowledge-validation" class="text-sm text-red-600 dark:text-red-400" :role="validationError ? 'alert' : undefined">{{ validationError }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QQSiteKnowledgeConfig } from '@/api/qqbot'

const value = defineModel<QQSiteKnowledgeConfig>({ required: true })
const { t } = useI18n()
const maxCharacters = 12000
const textarea = ref<HTMLTextAreaElement | null>(null)
const keywordDrafts = ref<Record<string, string>>({})
watch(() => value.value, () => { keywordDrafts.value = {} }, { flush: 'sync' })
const entries = computed(() => value.value.entries ?? [])
const characterCount = computed(() => Array.from(value.value.content).length + entries.value.reduce((total, entry) => total + Array.from(entry.title + entry.category + entry.content + entry.url + entry.keywords.join('')).length, 0))
function addEntry() {
  if (entries.value.length >= 40) return
  const id = `knowledge_${crypto.randomUUID().replace(/-/g, '')}`
  value.value.entries = [...entries.value, { id, title: '', category: '', content: '', url: '', keywords: [], enabled: false, updated_at: '' }]
}
function removeEntry(index: number) {
  const entry = entries.value[index]
  if (entry) delete keywordDrafts.value[entry.id]
  value.value.entries = entries.value.filter((_, i) => i !== index)
}
function setKeywords(index: number, text: string) {
  const entry = entries.value[index]
  if (entry) {
    keywordDrafts.value[entry.id] = text
    entry.keywords = text.split(/[,，]/).map(word => word.trim()).filter(Boolean)
  }
}
function hasControl(text: string) {
  return Array.from(text).some(char => { const code = char.codePointAt(0)!; return code < 32 || (code >= 127 && code <= 159) })
}
function validSourceURL(text: string) {
  try { const url = new URL(text); return ['http:', 'https:'].includes(url.protocol) && !!url.hostname && !url.username && !url.password && !/\s/u.test(text) && !hasControl(text) } catch { return false }
}
const validationError = computed(() => {
  if (characterCount.value > maxCharacters) return t('qqbot.knowledge.tooLong', { max: maxCharacters })
  if (entries.value.length > 40) return t('qqbot.knowledge.tooMany')
  if (entries.value.some(entry => hasControl(entry.title + entry.category) || entry.keywords.some(hasControl))) return t('qqbot.knowledge.entryControl')
  if (entries.value.some(entry => entry.enabled && (!entry.title.trim() || !entry.content.trim()))) return t('qqbot.knowledge.entryRequired')
  if (entries.value.some(entry => Array.from(entry.title).length > 120 || Array.from(entry.category).length > 60 || Array.from(entry.content).length > 8000 || entry.keywords.length > 12 || entry.keywords.some(word => !word.trim() || Array.from(word).length > 60))) return t('qqbot.knowledge.entryTooLong')
  if (entries.value.some(entry => entry.url && (entry.url.length > 2048 || !validSourceURL(entry.url)))) return t('qqbot.knowledge.invalidURL')
  if (value.value.enabled && !value.value.content.trim() && !entries.value.some(entry => entry.enabled && entry.title.trim() && entry.content.trim())) return t('qqbot.knowledge.required')
  return ''
})
watch([textarea, validationError], ([field, error]) => field?.setCustomValidity(error), { flush: 'post', immediate: true })
</script>
