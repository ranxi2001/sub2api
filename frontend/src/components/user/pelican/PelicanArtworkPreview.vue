<template>
  <div
    ref="containerRef"
    class="relative h-full min-h-0 w-full min-w-0"
    :style="{ overflow: mode === 'actual' ? 'auto' : 'clip' }"
    data-testid="pelican-artwork-preview"
  >
    <div class="flex min-h-full min-w-full items-center justify-center" :style="surfaceStyle">
      <!-- Clipping must not create a scroll container: browser focus/anchoring
           can otherwise pan the larger native iframe and crop the artwork. -->
      <div class="relative shrink-0 overflow-clip" :style="{ width: `${fitted.width}px`, height: `${fitted.height}px` }">
        <iframe
          ref="frameRef"
          :srcdoc="srcdoc"
          :title="title"
          class="absolute left-0 top-0 block border-0"
          :style="frameStyle"
          :tabindex="interactive ? undefined : -1"
          :sandbox="interactive ? 'allow-scripts allow-pointer-lock' : 'allow-scripts'"
          referrerpolicy="no-referrer"
          scrolling="no"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { readPelicanPreviewNonce } from '@/utils/pelicanHtml'
import { createPelicanPreviewChannel, createPelicanPreviewDocument, fitPelicanArtwork, getPelicanViewport, readPelicanSizeMessage, type PelicanPreviewMode } from '@/utils/pelicanPreview'

const props = withDefaults(defineProps<{ html: string; title: string; mode?: PelicanPreviewMode; interactive?: boolean }>(), { mode: 'fit', interactive: true })
const containerRef = ref<HTMLElement | null>(null)
const frameRef = ref<HTMLIFrameElement | null>(null)
const available = ref({ width: 0, height: 0 })
const viewport = ref({ width: 1024, height: 768 })
const artwork = ref({ width: 1024, height: 768 })
const srcdoc = ref('')
let channel = ''
let updates = 0
let observer: ResizeObserver | undefined

watch(() => props.html, (html) => {
  channel = createPelicanPreviewChannel()
  updates = 0
  viewport.value = getPelicanViewport(html)
  artwork.value = { ...viewport.value }
  srcdoc.value = createPelicanPreviewDocument(html, channel, readPelicanPreviewNonce())
}, { immediate: true })

const fitted = computed(() => fitPelicanArtwork(artwork.value, available.value, props.mode))
const innerScale = computed(() => fitPelicanArtwork(artwork.value, viewport.value, 'fit').scale)
const surfaceStyle = computed(() => ({ width: `${Math.max(available.value.width, fitted.value.width)}px`, height: `${Math.max(available.value.height, fitted.value.height)}px` }))
const frameStyle = computed(() => ({
  // Native viewport dimensions never change: @media, matchMedia, and authored
  // window.innerWidth/Height reads must describe the same canvas in every mode.
  width: `${viewport.value.width}px`,
  height: `${viewport.value.height}px`,
  transform: `scale(${fitted.value.scale / innerScale.value})`,
  transformOrigin: 'top left',
  pointerEvents: props.interactive ? 'auto' as const : 'none' as const,
}))

function resize() {
  const container = containerRef.value
  // Dialog entrance transforms affect painted bounds, not its layout viewport.
  // Client dimensions remain correct after the transition without a resize event.
  if (container) available.value = { width: container.clientWidth, height: container.clientHeight }
}

function receiveSize(event: MessageEvent) {
  if (updates >= 120) return
  const size = readPelicanSizeMessage(event, frameRef.value?.contentWindow ?? null, channel)
  if (!size || (size.width === artwork.value.width && size.height === artwork.value.height)) return
  updates++
  artwork.value = size
}

onMounted(() => {
  resize()
  if (typeof ResizeObserver !== 'undefined' && containerRef.value) { observer = new ResizeObserver(resize); observer.observe(containerRef.value) }
  window.addEventListener('resize', resize)
  window.addEventListener('message', receiveSize)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  window.removeEventListener('resize', resize)
  window.removeEventListener('message', receiveSize)
})
</script>
