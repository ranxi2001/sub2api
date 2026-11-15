<template>
  <fieldset class="space-y-4 rounded-lg border p-4 dark:border-dark-700" :disabled="busy || disabled" data-testid="qq-announcement">
    <legend class="font-medium">{{ t('qqbot.announcement.title') }}</legend>
    <p class="text-sm text-gray-500">{{ t('qqbot.announcement.help') }}</p>
    <p class="text-xs text-amber-700 dark:text-amber-300">{{ t('qqbot.announcement.savedOnly') }}</p>
    <p v-if="!config.enabled" class="text-sm text-amber-700 dark:text-amber-300">{{ t('qqbot.announcement.disabled') }}</p>
    <QQGroupPicker v-model="groups" :groups="availableGroups" :policies="config.group_policies" :names="names" :label="t('qqbot.announcement.groups')" />
    <p v-if="!availableGroups.length" class="text-sm text-gray-500">{{ t('qqbot.announcement.noGroups') }}</p>
    <label class="block space-y-1 text-sm">{{ t('qqbot.announcement.text') }}
      <textarea v-model="text" rows="5" class="input" data-testid="qq-announcement-text" :placeholder="t('qqbot.announcement.placeholder')" />
      <span class="text-xs" :class="length > 1600 ? 'text-red-600' : 'text-gray-500'">{{ t('qqbot.announcement.length', { count: length }) }}</span>
    </label>
    <label class="flex items-center gap-2 text-sm"><input v-model="mentionAll" type="checkbox" data-testid="qq-announcement-mention-all" />{{ t('qqbot.announcement.mentionAll') }}</label>
    <p class="text-xs text-gray-500">{{ t('qqbot.announcement.mentionHelp') }}</p>
    <p class="text-xs text-gray-500">{{ t('qqbot.announcement.permission') }}</p>
    <button type="button" class="btn btn-primary" data-testid="qq-announcement-send" :disabled="!canSend" @click="send">{{ t(busy ? 'qqbot.announcement.sending' : 'qqbot.announcement.send') }}</button>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-green-700 dark:text-green-300">{{ notice }}</p>
    <p v-if="notice" class="text-xs text-gray-500">{{ t('qqbot.announcement.recordsHelp') }}</p>
  </fieldset>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQAnnouncementRequest, type QQBotConfig } from '@/api/qqbot'
import QQGroupPicker from './QQGroupPicker.vue'

const props = defineProps<{ config: QQBotConfig; names?: Record<string, string>; disabled?: boolean }>()
const emit = defineEmits<{ queued: [] }>()
const { t } = useI18n()
const groups = ref<string[]>([]), text = ref(''), mentionAll = ref(false)
const busy = ref(false), error = ref(''), notice = ref('')
const availableGroups = computed(() => [...new Set(props.config.allowed_groups)].filter(group => !props.config.group_policies?.some(policy => policy.group_id === group && policy.disable_notifications)))
const length = computed(() => [...text.value.trim()].length)
const payload = computed(() => ({ app_id: props.config.app_id, sandbox: props.config.sandbox, groups: [...groups.value].sort(), text: text.value.trim(), mention_all: mentionAll.value }))
const fingerprint = computed(() => JSON.stringify(payload.value))
const queuedFingerprint = ref('')
const canSend = computed(() => !busy.value && !props.disabled && props.config.enabled && !!props.config.app_id && groups.value.length > 0 && groups.value.every(group => availableGroups.value.includes(group)) && length.value > 0 && length.value <= 1600 && queuedFingerprint.value !== fingerprint.value)
let requestID = '', requestFingerprint = '', revision = 0

watch(fingerprint, () => { error.value = ''; notice.value = ''; queuedFingerprint.value = ''; requestID = ''; requestFingerprint = '' }, { flush: 'sync' })
watch(() => JSON.stringify([props.config.app_id, props.config.sandbox]), () => {
  ++revision
  groups.value = []
  requestID = ''; requestFingerprint = ''; queuedFingerprint.value = ''; error.value = ''; notice.value = ''
}, { flush: 'sync' })
watch(availableGroups, allowed => { groups.value = groups.value.filter(group => allowed.includes(group)) }, { flush: 'sync' })

function newRequestID() {
  if (globalThis.crypto.randomUUID) return globalThis.crypto.randomUUID()
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 15) | 64
  bytes[8] = (bytes[8]! & 63) | 128
  const hex = [...bytes].map(value => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
async function send() {
  if (!canSend.value) return
  const mention = t(mentionAll.value ? 'qqbot.announcement.withMention' : 'qqbot.announcement.withoutMention')
  if (!window.confirm(t('qqbot.announcement.confirm', { count: groups.value.length, mention }))) return
  const sentFingerprint = fingerprint.value, sentRevision = revision
  if (requestFingerprint !== sentFingerprint) { requestID = newRequestID(); requestFingerprint = sentFingerprint }
  const request: QQAnnouncementRequest = { ...payload.value, groups: [...payload.value.groups], request_id: requestID }
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const result = await qqbotAPI.sendAnnouncement(request)
    if (sentRevision === revision && sentFingerprint === fingerprint.value) {
      queuedFingerprint.value = sentFingerprint
      notice.value = t(result.duplicate ? 'qqbot.announcement.alreadyQueued' : 'qqbot.announcement.queued', { count: result.jobs.length })
    }
    emit('queued')
  } catch (e) {
    if (sentRevision === revision && sentFingerprint === fingerprint.value) error.value = (e as Error).message || t('common.error')
  } finally { busy.value = false }
}
onBeforeUnmount(() => { ++revision })
</script>
