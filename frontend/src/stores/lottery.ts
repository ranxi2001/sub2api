import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { lotteryAPI, type LotteryPool } from '@/api/lottery'
import { useAuthStore } from './auth'
import { useAppStore } from './app'

export const useLotteryStore = defineStore('lottery', () => {
  const auth = useAuthStore()
  const app = useAppStore()
  const pools = ref<LotteryPool[]>([])
  const enabled = computed(() => app.cachedPublicSettings?.lottery_enabled === true)
  const pendingPools = computed(() => enabled.value ? pools.value.filter(p => p.available > 0) : [])
  const available = computed(() => pendingPools.value.reduce((n, p) => n + p.available, 0))
  let generation = 0
  let lastFetch = 0
  let pending: Promise<void> | null = null

  function reset() {
    generation++
    pools.value = []
    lastFetch = 0
    pending = null
  }

  function refresh(force = false): Promise<void> {
    if (!enabled.value || !auth.user?.id) {
      reset()
      return Promise.resolve()
    }
    if (!force && pending) return pending
    if (!force && lastFetch && Date.now() - lastFetch < 30_000) return Promise.resolve()
    const current = ++generation
    const userID = auth.user.id
    const request = lotteryAPI.pools().then(data => {
      if (current !== generation || auth.user?.id !== userID || !enabled.value) return
      pools.value = data
      lastFetch = Date.now()
    }).catch(() => {
      if (current !== generation) return
      // Avoid presenting stale chances as current after a failed refresh.
      pools.value = []
      lastFetch = 0
    }).finally(() => {
      if (current === generation) pending = null
    })
    pending = request
    return request
  }

  function acceptPools(data: LotteryPool[]) {
    generation++
    pending = null
    pools.value = enabled.value && auth.user?.id ? data : []
    lastFetch = Date.now()
  }

  watch([() => auth.user?.id, enabled], () => {
    reset()
    void refresh()
  }, { immediate: true, flush: 'sync' })

  return { enabled, pools, pendingPools, available, refresh, acceptPools, reset }
})
