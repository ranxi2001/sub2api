import { computed, onScopeDispose, ref, watch } from 'vue'
import { qqbotAPI, type QQGroupInfo } from '@/api/qqbot'

interface GroupNameSource { appID: string; sandbox: boolean; revision: string; groups: string[] }

// Display metadata stays separate from editable group policies and module drafts.
export function useQQGroupNames(source: () => GroupNameSource | null) {
  const names = ref<Record<string, string>>({})
  const status = ref<QQGroupInfo['status'] | ''>('')
  const loading = ref(false)
  let identity = ''
  let completed = new Set<string>()
  let generation = 0
  let controller: AbortController | undefined
  let timer: ReturnType<typeof setTimeout> | undefined

  function stop() {
    generation++
    controller?.abort()
    clearTimeout(timer)
    loading.value = false
  }

  function load(retry = false) {
    stop()
    const current = source()
    const key = current ? JSON.stringify([current.appID, current.sandbox, current.revision]) : ''
    if (key !== identity) {
      identity = key
      names.value = {}
      completed = new Set()
      status.value = ''
    }
    if (!current?.appID) return
    if (retry) { completed.clear(); status.value = '' }
    if (status.value === 'permission_required' || status.value === 'unconfigured') return
    const requestGeneration = generation
    controller = new AbortController()
    const { signal } = controller
    const queue = [...new Set(current.groups)].filter(group => !completed.has(group))
    const run = async () => {
      loading.value = queue.length > 0
      while (queue.length && requestGeneration === generation) {
        const group = queue[0]!
        try {
          const info = await qqbotAPI.groupInfo(current.appID, group, signal)
          if (requestGeneration !== generation) return
          if (info.app_id !== current.appID || info.sandbox !== current.sandbox || info.group_id !== group) {
            status.value = 'unavailable'
          } else if (info.status === 'rate_limited') {
            status.value = info.status
            loading.value = false
            timer = setTimeout(() => { void run() }, Math.max(1, Math.min(info.retry_after_seconds || 60, 300)) * 1000)
            return
          } else {
            if (info.name && info.status === 'available') names.value = { ...names.value, [group]: info.name }
            if (info.status !== 'available') status.value = info.status
            else if (status.value === 'rate_limited') status.value = ''
            if (info.status === 'permission_required' || info.status === 'unconfigured') break
          }
        } catch {
          if (requestGeneration !== generation) return
          status.value = 'unavailable'
        }
        completed.add(group)
        queue.shift()
      }
      if (requestGeneration === generation) loading.value = false
    }
    void run()
  }

  watch(() => JSON.stringify(source()), () => load(), { immediate: true })
  onScopeDispose(stop)
  return { names: computed(() => names.value), status, loading, refresh: () => load(true) }
}
