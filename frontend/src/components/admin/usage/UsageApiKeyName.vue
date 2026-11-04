<template>
  <!-- A definite width also bounds this cell's min-content contribution. -->
  <div class="w-40 min-w-0 max-w-full">
    <button
      v-if="name"
      ref="trigger"
      type="button"
      class="block w-full truncate text-left text-sm text-gray-900 focus-visible:outline focus-visible:outline-primary-500 dark:text-white"
      :aria-expanded="show"
      @mouseenter="!isMobile && open()"
      @mouseleave="!isMobile && scheduleClose()"
      @focus="!isMobile && open()"
      @blur="!isMobile && scheduleClose()"
      @click="open()"
    >{{ name }}</button>
    <span v-else class="text-sm text-gray-900 dark:text-white">-</span>
    <UsageDetailPopover :show="show" :anchor="anchor" :title="t('usage.apiKeyFilter')"
      @close="close" @mouseenter="cancelClose" @mouseleave="scheduleClose">
      <div ref="content" class="w-80 max-w-full whitespace-normal break-all rounded-lg bg-gray-900 p-3 text-sm text-white">{{ name }}</div>
    </UsageDetailPopover>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { onClickOutside, onKeyStroke, useEventListener, useTimeoutFn, useWindowSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import UsageDetailPopover from './UsageDetailPopover.vue'

const props = defineProps<{ name?: string | null }>()
const { t } = useI18n()
const { width } = useWindowSize()
const isMobile = computed(() => width.value < 768)
const show = ref(false)
const anchor = ref<DOMRect | null>(null)
const trigger = ref<HTMLElement | null>(null)
const content = ref<HTMLElement | null>(null)
const { start: scheduleClose, stop: cancelClose } = useTimeoutFn(close, 120, { immediate: false })
function updateAnchor() {
  if (show.value && trigger.value) anchor.value = trigger.value.getBoundingClientRect()
}
function open() {
  cancelClose()
  show.value = true
  updateAnchor()
}
function close() {
  cancelClose()
  show.value = false
}
onClickOutside(trigger, close, { ignore: [content] })
onKeyStroke('Escape', close)
useEventListener(window, 'scroll', updateAnchor, { capture: true, passive: true })
useEventListener(window, 'resize', updateAnchor)
watch(() => props.name, close)
</script>
