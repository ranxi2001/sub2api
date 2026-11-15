import { computed, inject, onBeforeUnmount, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { routerKey, type Router } from 'vue-router'
import { useI18n } from 'vue-i18n'

const registries = new WeakMap<Router, Set<() => boolean>>()

export function useQQDraft<T>(source: MaybeRefOrGetter<T>) {
  const { t } = useI18n()
  const router = inject(routerKey, undefined)
  const snapshot = () => JSON.stringify(toValue(source), (key, value) => key.startsWith('_') ? undefined : value)
  const baseline = ref<string>()
  watch(() => toValue(source), value => { if (value != null && baseline.value === undefined) baseline.value = snapshot() }, { deep: true, immediate: true, flush: 'sync' })
  const dirty = computed(() => baseline.value !== undefined && snapshot() !== baseline.value)
  const markSaved = () => { baseline.value = snapshot() }
  // KeepAlive drafts remain registered when their tab is not visible.
  let registry = router && registries.get(router)
  if (router && !registry) {
    registry = new Set()
    registries.set(router, registry)
    router.beforeEach((to, from) => {
      const preservesDrafts = to.path === from.path && to.path === '/admin/qqbot/activities'
      return preservesDrafts || ![...registries.get(router)!].some(check => check()) || window.confirm(t('qqbot.workspace.leaveConfirm'))
    })
  }
  const check = () => dirty.value
  registry?.add(check)
  const beforeUnload = (event: BeforeUnloadEvent) => { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
  window.addEventListener('beforeunload', beforeUnload)
  onBeforeUnmount(() => { registry?.delete(check); window.removeEventListener('beforeunload', beforeUnload) })
  return { dirty, markSaved }
}

export function hasQQDrafts(router: Router) { return [...(registries.get(router) || [])].some(check => check()) }
