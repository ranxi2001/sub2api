<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6 text-gray-900 dark:text-gray-100">
      <header class="flex items-center justify-between gap-4">
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
          {{ t('lottery.title') }}
        </h1>
        <button
          class="btn btn-secondary h-10 w-10 !p-2"
          :title="t('lottery.refresh')"
          :aria-label="t('lottery.refresh')"
          :disabled="busy || loading"
          @click="load"
        >
          <Icon name="refresh" />
        </button>
      </header>
      <p
        v-if="error"
        role="alert"
        class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950 dark:text-red-300"
      >
        {{ error }}
      </p>
      <p
        v-if="loading && !pools.length"
        class="py-12 text-center text-gray-500"
      >
        {{ t('lottery.loading') }}
      </p>
      <div v-else-if="!pools.length" class="py-16 text-center text-gray-500">
        <Icon name="gift" class="mx-auto mb-4 h-10 w-10" />{{
          t(lottery.enabled ? 'lottery.empty' : 'lottery.siteUnavailable')
        }}
      </div>
      <template v-if="pool">
        <section class="border-b border-gray-200 pb-5 dark:border-dark-700" aria-labelledby="pool-selector-title">
          <div class="mb-3 flex items-center gap-2">
            <h2 id="pool-selector-title" class="text-base font-semibold">{{ t('lottery.choosePool') }}</h2>
            <span class="text-sm tabular-nums text-gray-400">{{ pools.length }}</span>
          </div>
          <div class="grid grid-cols-2 gap-3 xl:grid-cols-4" role="tablist" :aria-label="t('lottery.pools')">
            <button
              v-for="p in pools" :id="`pool-tab-${p.id}`" :key="p.id"
              role="tab" :aria-selected="selectedID === p.id" aria-controls="lottery-pool-panel"
              :tabindex="selectedID === p.id ? 0 : -1" :disabled="busy"
              class="pool-option min-w-0 rounded-lg border p-3 text-left transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-600 sm:p-4"
              :class="selectedID === p.id ? 'border-emerald-600 bg-emerald-50 dark:border-emerald-400 dark:bg-emerald-950/40' : 'border-gray-200 bg-white hover:border-gray-400 dark:border-dark-600 dark:bg-dark-800 dark:hover:border-dark-400'"
              @click="selectedID = p.id" @keydown="navigatePools($event, p.id)"
            >
              <span class="mb-3 flex min-h-10 items-start justify-between gap-2">
                <span class="line-clamp-2 break-words text-sm font-semibold leading-5" :title="p.name">{{ p.name }}</span>
                <Icon :name="selectedID === p.id ? 'checkCircle' : 'gift'" size="sm" class="mt-0.5 shrink-0" :class="selectedID === p.id ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-400'" />
              </span>
              <span class="flex flex-wrap items-baseline gap-x-2">
                <strong class="text-2xl font-semibold tabular-nums" :class="p.available ? 'text-emerald-700 dark:text-emerald-400' : 'text-gray-400'">{{ p.available }}</strong>
                <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('lottery.available') }}</span>
              </span>
              <span v-if="!p.draw_enabled" class="mt-1 block text-xs text-amber-700 dark:text-amber-400">{{ t('lottery.paused') }}</span>
            </button>
          </div>
        </section>
        <div id="lottery-pool-panel" role="tabpanel" :aria-labelledby="`pool-tab-${pool.id}`">
        <header class="mb-5 flex flex-wrap items-baseline justify-between gap-2">
          <h2 class="min-w-0 break-words text-lg font-semibold">{{ pool.name }}</h2>
          <p class="text-sm text-gray-500">{{ t(pool.mode === 'single' ? 'lottery.singleRule' : 'lottery.cumulativeRule', { amount: money(pool.threshold) }) }}</p>
        </header>
        <div class="grid min-w-0 gap-8 lg:grid-cols-2">
          <section class="min-w-0">
            <LotteryWheel :key="pool.id" ref="wheel" :prizes="pool.prizes" />
            <button
              class="btn btn-primary mx-auto flex min-h-12 w-full max-w-sm items-center justify-center gap-2"
              :disabled="busy || !canDraw"
              @click="draw"
            >
              <Icon name="gift" /><span>{{ drawLabel }}</span>
            </button>
            <p
              v-if="pool.debt"
              class="mt-3 text-center text-sm text-amber-700 dark:text-amber-400"
            >
              {{ t('lottery.debt', { count: pool.debt }) }}
            </p>
            <div
              v-if="pool.mode === 'cumulative'"
              class="mx-auto mt-5 max-w-sm"
            >
              <div
                class="mb-2 flex justify-between gap-3 text-sm text-gray-500"
              >
                <span>{{ t('lottery.progress') }}</span
                ><span class="tabular-nums"
                  >{{ money(pool.progress) }} /
                  {{ money(pool.threshold) }}</span
                >
              </div>
              <progress
                :value="pool.progress"
                :max="pool.threshold"
                class="lottery-progress h-2 w-full"
              ></progress>
            </div>
            <p
              v-if="!pool.grant_enabled"
              class="mt-3 text-center text-sm text-gray-500"
            >
              {{ t('lottery.grantStopped') }}
            </p>
            <router-link
              to="/purchase"
              class="mt-5 flex items-center justify-center gap-1 text-sm text-primary-600"
              >{{ t('lottery.rechargeLink') }}<Icon name="arrowRight" size="sm"
            /></router-link>
          </section>
          <section class="min-w-0">
            <h2 class="mb-3 text-lg font-semibold">
              {{ t('lottery.prizes') }}
            </h2>
            <div class="divide-y divide-gray-100 dark:divide-dark-700">
              <div
                v-for="prize in pool.prizes"
                :key="prize.id"
                class="flex items-center gap-3 py-3"
                :class="{ 'opacity-50': !prize.available }"
              >
                <PrizeIcon :kind="prize.kind" :color="segmentFor(prize.id)?.color" />
                <div class="min-w-0 flex-1">
                  <p class="break-words text-sm font-semibold">
                    {{ prize.name }}
                  </p>
                  <p class="text-xs text-gray-500">
                    {{
                      prize.kind === 'balance'
                        ? `${money(prize.amount)} USD`
                        : prize.kind === 'subscription'
                          ? t('lottery.snapshot', {
                              plan: prize.plan_name,
                              days: prize.validity_days,
                            })
                          : t('lottery.text')
                    }}
                  </p>
                </div>
                <div class="shrink-0 text-right">
                  <p class="text-sm font-medium tabular-nums">
                    {{ percent(prize.probability) }}
                  </p>
                  <p class="text-xs text-gray-500">
                    {{
                      prize.stock === -1
                        ? t('lottery.unlimited')
                        : `${t('lottery.stock')}: ${prize.stock}`
                    }}
                  </p>
                </div>
              </div>
            </div>
            <p v-if="!segments.length" class="py-8 text-sm text-gray-500">
              {{ t('lottery.noPrizes') }}
            </p>
            <div
              class="mt-5 space-y-2 border-t border-gray-200 pt-4 text-xs leading-relaxed text-gray-500 dark:border-dark-700"
            >
              <p>{{ t('lottery.oddsNote') }}</p>
              <p>{{ t('lottery.creditRule') }}</p>
              <p>{{ t('lottery.refundRule') }}</p>
            </div>
          </section>
        </div>
        </div>
      </template>
      <section class="border-t border-gray-200 pt-6 dark:border-dark-700">
        <h2 class="mb-3 text-lg font-semibold">{{ t('lottery.records') }}</h2>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="text-gray-500">
              <tr>
                <th class="py-3 font-medium">{{ t('lottery.time') }}</th>
                <th class="px-3 py-3 font-medium">{{ t('lottery.pools') }}</th>
                <th class="py-3 font-medium">{{ t('lottery.reward') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="record in records" :key="record.id">
                <td class="whitespace-nowrap py-3 text-gray-500">
                  {{ date(record.created_at) }}
                </td>
                <td class="px-3 py-3">{{ record.prize.pool_name }}</td>
                <td class="max-w-sm py-3">
                  <button
                    class="break-words text-left font-medium text-primary-600"
                    @click="result = record"
                  >
                    {{ record.prize.name }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p
          v-if="!records.length"
          class="py-6 text-center text-sm text-gray-500"
        >
          {{ t('lottery.noRecords') }}
        </p>
        <div
          v-if="total > 10"
          class="mt-3 flex items-center justify-between gap-2 text-sm"
        >
          <button
            class="btn btn-secondary"
            :disabled="page <= 1"
            @click="changePage(-1)"
          >
            {{ t('lottery.previous') }}</button
          ><span>{{ t('lottery.page', { page, total }) }}</span
          ><button
            class="btn btn-secondary"
            :disabled="page * 10 >= total"
            @click="changePage(1)"
          >
            {{ t('lottery.next') }}
          </button>
        </div>
      </section>
      <BaseDialog
        :show="!!result"
        :title="t('lottery.result')"
        width="narrow"
        @close="result = null"
      >
        <div v-if="result" class="space-y-4 text-center">
          <Icon name="gift" class="mx-auto h-12 w-12 text-emerald-600" />
          <h3 class="break-words text-xl font-semibold">
            {{ result.prize.name }}
          </h3>
          <p
            v-if="result.prize.kind === 'balance'"
            class="text-2xl font-semibold text-emerald-700"
          >
            +{{ money(result.prize.amount) }} USD
          </p>
          <p v-else-if="result.prize.kind === 'subscription'">
            {{
              t('lottery.snapshot', {
                plan: result.prize.plan_name,
                days: result.prize.validity_days,
              })
            }}
          </p>
          <p
            v-else
            class="whitespace-pre-wrap break-words text-sm leading-relaxed"
          >
            {{ result.prize.text }}
          </p>
        </div>
        <template #footer
          ><button class="btn btn-primary" @click="result = null">
            {{ t('lottery.close') }}
          </button></template
        >
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import LotteryWheel from '@/components/lottery/LotteryWheel.vue'
import PrizeIcon from '@/components/lottery/PrizeIcon.vue'
import { wheelSegments } from '@/components/lottery/wheel'
import { lotteryAPI, type LotteryPool, type LotteryDraw } from '@/api/lottery'
import { useAuthStore } from '@/stores/auth'
import { useSubscriptionStore } from '@/stores/subscriptions'
import { useLotteryStore } from '@/stores/lottery'

const { t, te, locale } = useI18n()
const auth = useAuthStore()
const subscriptions = useSubscriptionStore()
const lottery = useLotteryStore()
const route = useRoute()
const router = useRouter()
const pools = ref<LotteryPool[]>([])
const selectedID = ref<number | null>(null)
const pool = computed(() => pools.value.find((p) => p.id === selectedID.value))
const segments = computed(() => wheelSegments(pool.value?.prizes || []))
const segmentFor = (id: number) => segments.value.find((s) => s.prize.id === id)
const wheel = ref<InstanceType<typeof LotteryWheel>>()
const loading = ref(false),
  busy = ref(false),
  error = ref('')
const records = ref<LotteryDraw[]>([]),
  result = ref<LotteryDraw | null>(null)
const page = ref(1),
  total = ref(0),
  pendingKey = ref('')
const storageKey = () => `sub2api.lottery.${auth.user?.id}.${selectedID.value}`
const money = (n: number) =>
  new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }).format(n)
const percent = (n: number) =>
  new Intl.NumberFormat(locale.value, {
    style: 'percent',
    maximumFractionDigits: 4,
  }).format(n)
const date = (s: string) => new Date(s).toLocaleString(locale.value)
const canDraw = computed(
  () =>
    !!pendingKey.value ||
    (!!pool.value?.draw_enabled &&
      pool.value.available > 0 &&
      segments.value.length > 0),
)
const drawLabel = computed(() =>
  busy.value
    ? t('lottery.drawing')
    : pendingKey.value
      ? t('lottery.retryDraw')
      : !pool.value?.draw_enabled
        ? t('lottery.paused')
        : pool.value.available <= 0
          ? t('lottery.noTickets')
          : !segments.value.length
            ? t('lottery.noPrizes')
            : t('lottery.draw'),
)
function showError(e: unknown) {
  const err = e as { reason?: string; message?: string }
  const key = `lottery.errors.${err.reason}`
  error.value = te(key) ? t(key) : err.message || t('lottery.error')
}
async function loadRecords() {
  const data = await lotteryAPI.records(page.value)
  records.value = data.items
  total.value = data.total
}
async function load() {
  loading.value = true
  error.value = ''
  try {
    pools.value = await lotteryAPI.pools()
    lottery.acceptPools(pools.value)
    const requested = Number(route.query.pool)
    if (pools.value.some(p => p.id === requested)) selectedID.value = requested
    else if (!pools.value.some((p) => p.id === selectedID.value))
      selectedID.value = pools.value.find(p => p.available > 0)?.id ?? pools.value[0]?.id ?? null
    await loadRecords()
  } catch (e) {
    showError(e)
  } finally {
    loading.value = false
  }
}
async function changePage(delta: number) {
  page.value += delta
  try {
    await loadRecords()
  } catch (e) {
    showError(e)
  }
}
watch(selectedID, () => {
  pendingKey.value = localStorage.getItem(storageKey()) || ''
  if (selectedID.value && String(route.query.pool) !== String(selectedID.value))
    void router.replace({ query: { ...route.query, pool: String(selectedID.value) } })
})
watch([() => route.query.pool, busy], () => {
  if (!busy.value && pools.value.some(p => p.id === Number(route.query.pool))) selectedID.value = Number(route.query.pool)
})
watch([() => lottery.enabled, busy], ([enabled, drawing]) => {
  if (drawing) return
  if (!enabled) {
    pools.value = []
    selectedID.value = null
  } else if (!pools.value.length && !loading.value) {
    void load()
  }
})
function navigatePools(event: KeyboardEvent, id: number) {
  if (busy.value || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const index = pools.value.findIndex(p => p.id === id)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? pools.value.length - 1
    : (index + (event.key === 'ArrowRight' ? 1 : -1) + pools.value.length) % pools.value.length
  selectedID.value = pools.value[next].id
  document.getElementById(`pool-tab-${selectedID.value}`)?.focus()
}
async function draw() {
  if (!selectedID.value || busy.value || !canDraw.value) return
  busy.value = true
  error.value = ''
  const key = pendingKey.value || crypto.randomUUID()
  pendingKey.value = key
  localStorage.setItem(storageKey(), key)
  try {
    const record = await lotteryAPI.draw(selectedID.value, key)
    void lottery.refresh(true)
    localStorage.removeItem(storageKey())
    pendingKey.value = ''
    await wheel.value?.spinTo(record.prize.id)
    result.value = record
    await load()
    try {
      await auth.refreshUser()
      if (record.prize.kind === 'subscription')
        await subscriptions.fetchActiveSubscriptions(true)
    } catch {
      error.value = t('lottery.refreshWarning')
    }
  } catch (e) {
    const status = (e as { status?: number }).status
    if (status && status >= 400 && status < 500 && status !== 429) {
      localStorage.removeItem(storageKey())
      pendingKey.value = ''
    }
    showError(e)
  } finally {
    busy.value = false
  }
}
onMounted(load)
</script>

<style scoped>
.lottery-progress {
  appearance: none;
  border: 0;
  border-radius: 4px;
  overflow: hidden;
  background: #e5e7eb;
}
.lottery-progress::-webkit-progress-bar {
  background: #e5e7eb;
}
.lottery-progress::-webkit-progress-value {
  background: #059669;
}
.lottery-progress::-moz-progress-bar {
  background: #059669;
}
</style>
