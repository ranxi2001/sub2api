<template>
  <AppLayout>
    <div class="lottery-page mx-auto min-w-0 max-w-6xl text-gray-900 dark:text-gray-100">
      <header class="page-heading">
        <div class="flex min-w-0 items-center gap-3">
          <span class="page-symbol"><Icon name="gift" /></span>
          <h1 class="text-2xl font-semibold">{{ t('lottery.title') }}</h1>
        </div>
        <button
          class="btn btn-secondary !h-11 !w-11 shrink-0 !p-2"
          :title="t('lottery.refresh')" :aria-label="t('lottery.refresh')"
          :disabled="busy || loading" @click="load"
        ><Icon name="refresh" :class="{ 'animate-spin': loading }" /></button>
      </header>

      <p v-if="error && !pool" role="alert" class="lottery-error">{{ error }}</p>
      <p v-if="loading && !pools.length" class="py-12 text-center text-gray-500">{{ t('lottery.loading') }}</p>
      <div v-else-if="!pools.length" class="py-16 text-center text-sm text-gray-500 dark:text-gray-400">
        <Icon name="gift" class="mx-auto mb-4 h-10 w-10" />
        {{ t(lottery.enabled ? 'lottery.empty' : 'lottery.siteUnavailable') }}
      </div>

      <template v-if="pool">
        <nav v-if="pools.length > 1" class="pool-selector" :aria-label="t('lottery.choosePool')">
          <div class="pool-tabs" role="tablist" :aria-label="t('lottery.pools')">
            <button
              v-for="p in pools" :id="`pool-tab-${p.id}`" :key="p.id"
              role="tab" :aria-selected="selectedID === p.id" aria-controls="lottery-pool-panel"
              :tabindex="selectedID === p.id ? 0 : -1" :disabled="busy"
              class="pool-option" :class="{ 'is-selected': selectedID === p.id }"
              @click="selectedID = p.id" @keydown="navigatePools($event, p.id)"
            >
              <span class="pool-option-copy">
                <span class="pool-option-name" :title="p.name">{{ p.name }}</span>
                <span class="pool-option-status">{{ t(p.draw_enabled ? 'lottery.available' : 'lottery.paused') }}</span>
              </span>
              <span class="pool-option-count">{{ p.available }}</span>
              <Icon v-if="selectedID === p.id" name="check" size="sm" class="shrink-0 text-primary-600 dark:text-primary-400" />
            </button>
          </div>
        </nav>

        <div
          id="lottery-pool-panel" :role="pools.length > 1 ? 'tabpanel' : 'region'"
          :aria-labelledby="pools.length > 1 ? `pool-tab-${pool.id}` : 'current-pool-title'"
        >
          <section class="draw-section" :aria-label="t('lottery.title')">
            <div class="draw-toolbar">
              <div class="draw-heading">
                <h2 id="current-pool-title" class="break-words text-lg font-semibold">{{ pool.name }}</h2>
                <p class="ticket-count">
                  <span>{{ t('lottery.available') }}</span>
                  <strong data-testid="available-count">{{ pool.available }}</strong>
                </p>
              </div>
              <div class="draw-actions">
                <fieldset class="quantity-control" :disabled="busy || !!pendingKeys.length">
                  <legend class="sr-only">{{ t('lottery.drawQuantity') }}</legend>
                  <label v-for="count in DRAW_COUNTS" :key="count" :title="t('lottery.drawMany', { count })">
                    <input v-model="drawCount" type="radio" name="draw-count" :value="count" :disabled="pool.available < count" class="peer sr-only" />
                    <span>X{{ count }}</span>
                  </label>
                </fieldset>
                <button
                  data-testid="lottery-draw" class="btn btn-primary draw-button"
                  :disabled="busy || !canDraw" @click="draw"
                >
                  <Icon :name="busy || pendingKeys.length ? 'refresh' : 'gift'" class="shrink-0" :class="{ 'animate-spin': busy }" />
                  <span>{{ drawLabel }}</span>
                </button>
              </div>
            </div>

            <div v-if="error || batchNotice || pool.debt || pendingKeys.length || !pool.draw_enabled || pool.available < drawCount" class="draw-notices">
              <p v-if="error" role="alert" class="lottery-error">{{ error }}</p>
              <p v-if="batchNotice" role="status">{{ batchNotice }}</p>
              <p v-if="pendingKeys.length && !busy">{{ t('lottery.retryNotice') }}</p>
              <p v-if="!pool.draw_enabled">{{ t('lottery.paused') }}</p>
              <p v-else-if="!pendingKeys.length && pool.available < drawCount">{{ t(pool.available ? 'lottery.insufficientTickets' : 'lottery.noTickets', { count: drawCount }) }}</p>
              <p v-if="pool.debt">{{ t('lottery.debt', { count: pool.debt }) }}</p>
            </div>

            <LotteryReel :key="pool.id" ref="reel" :prizes="pool.prizes" :count="drawCount" />
            <div class="progress-row">
              <div v-if="pool.mode === 'cumulative'" class="progress-group">
                <div class="progress-label">
                  <span>{{ t('lottery.progress') }}</span>
                  <span class="tabular-nums">{{ money(pool.progress) }} / {{ money(pool.threshold) }} USD</span>
                </div>
                <progress :value="pool.progress" :max="pool.threshold" :aria-label="t('lottery.progress')" class="lottery-progress"></progress>
              </div>
              <p v-else-if="!isInvitationPool(pool)" class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('lottery.singleRule', { amount: money(pool.threshold) }) }}</p>
              <p v-if="!pool.grant_enabled" class="text-xs text-gray-500 dark:text-gray-400">{{ t('lottery.grantStopped') }}</p>
              <router-link v-if="!isInvitationPool(pool) || pool.mode === 'invite_recharge'" to="/purchase" class="recharge-link">
                {{ t('lottery.rechargeLink') }}<Icon name="arrowRight" size="sm" />
              </router-link>
            </div>
            <p class="reel-disclosure">{{ t('lottery.reelNote') }}</p>
            <LotteryInvitePanel v-if="isInvitationPool(pool)" :pool="pool" />
          </section>

          <section class="prizes-section" aria-labelledby="prizes-title">
            <div class="section-heading">
              <h2 id="prizes-title">{{ t('lottery.prizes') }}</h2>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('lottery.probability') }}</span>
            </div>
            <div class="prize-grid">
              <article
                v-for="prize in pool.prizes" :key="prize.id" class="prize-item"
                :class="{ 'is-unavailable': !prize.available }"
                :data-kind="prize.kind"
                :style="{ '--prize-color': prizeColor(prize, pool.prizes) }"
              >
                <div class="prize-card-heading">
                  <span>{{ t(`lottery.${prize.kind === 'text' ? 'textPrize' : prize.kind}`) }}</span>
                  <span class="prize-card-accent" aria-hidden="true"></span>
                </div>
                <div class="prize-content flex min-w-0 items-start gap-3">
                  <PrizeIcon :kind="prize.kind" :color="prizeColor(prize, pool.prizes)" />
                  <div class="min-w-0 flex-1">
                    <p class="prize-value" :class="{ 'has-long-value': prizeValue(prize).length > 10 }">
                      {{ prizeValue(prize) }}<span v-if="prize.kind === 'balance'" class="prize-unit">USD</span>
                    </p>
                    <h3 class="prize-name">{{ prize.name }}</h3>
                    <p v-if="prize.kind === 'subscription'" class="mt-1 break-words text-xs text-gray-500 dark:text-gray-400">{{ prize.plan_name }}</p>
                  </div>
                </div>
                <div class="prize-meta">
                  <span class="prize-stock">{{ !prize.available ? t(prize.stock === 0 ? 'lottery.soldOut' : 'lottery.unavailable') : prize.stock === -1 ? t('lottery.unlimited') : t('lottery.stockCount', { count: prize.stock }) }}</span>
                  <span class="prize-odds"><span class="prize-odds-label">{{ t('lottery.probability') }}</span><strong class="tabular-nums">{{ percent(prize.probability) }}</strong></span>
                </div>
              </article>
            </div>
            <p v-if="!available.length" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('lottery.noPrizes') }}</p>
            <p class="odds-note">{{ t('lottery.oddsNote') }}</p>
            <details :key="pool.id" class="lottery-rules">
              <summary>
                <Icon name="infoCircle" size="sm" />
                <span>{{ t('lottery.rules') }}</span>
                <Icon name="chevronDown" size="sm" class="rules-chevron" />
              </summary>
              <div class="space-y-2 pb-4 text-xs leading-5 text-gray-500 dark:text-gray-400">
                <p>{{ t(lotteryRuleKey(pool), lotteryRuleParams(pool)) }}</p>
                <p v-if="isInvitationPool(pool)">{{ t('lottery.inviteOnce') }}</p>
                <p v-if="!isInvitationPool(pool) || pool.mode === 'invite_recharge'">{{ t('lottery.creditRule') }}</p>
                <p>{{ t('lottery.refundRule') }}</p>
              </div>
            </details>
          </section>
        </div>
      </template>

      <section class="history-section" aria-labelledby="history-title">
        <div class="section-heading">
          <h2 id="history-title" class="flex items-center gap-2"><Icon name="clock" size="sm" class="text-gray-400" />{{ t('lottery.records') }}</h2>
          <span v-if="total" class="text-xs tabular-nums text-gray-500 dark:text-gray-400">{{ t('lottery.recordCount', { count: total }) }}</span>
        </div>
        <table v-if="records.length" class="history-table">
          <thead>
            <tr><th>{{ t('lottery.time') }}</th><th>{{ t('lottery.pools') }}</th><th>{{ t('lottery.reward') }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="record in records" :key="record.id">
              <td class="text-xs text-gray-500 dark:text-gray-400">{{ date(record.created_at) }}</td>
              <td>{{ record.prize.pool_name }}</td>
              <td><button class="history-detail" @click="results = [record]">{{ record.prize.name }}<Icon name="chevronRight" size="sm" class="shrink-0" /></button></td>
            </tr>
          </tbody>
        </table>
        <div v-if="records.length" class="history-mobile">
          <button v-for="record in records" :key="record.id" class="history-record" @click="results = [record]">
            <PrizeIcon :kind="record.prize.kind" />
            <span class="min-w-0 flex-1">
              <span class="block break-words text-sm font-medium">{{ record.prize.name }}</span>
              <span class="mt-1 block break-words text-xs text-gray-500 dark:text-gray-400">{{ record.prize.pool_name }}</span>
              <time class="mt-1 block text-xs text-gray-500 dark:text-gray-400" :datetime="record.created_at">{{ date(record.created_at) }}</time>
            </span>
            <Icon name="chevronRight" size="sm" class="shrink-0 text-gray-400" />
          </button>
        </div>
        <p v-if="!records.length" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('lottery.noRecords') }}</p>
        <div v-if="total > 10" class="history-pagination">
          <button class="btn btn-secondary !h-11 !w-11 !p-2" :disabled="page <= 1" :aria-label="t('lottery.previous')" :title="t('lottery.previous')" @click="changePage(-1)"><Icon name="chevronLeft" /></button>
          <span>{{ t('lottery.page', { page, total }) }}</span>
          <button class="btn btn-secondary !h-11 !w-11 !p-2" :disabled="page * 10 >= total" :aria-label="t('lottery.next')" :title="t('lottery.next')" @click="changePage(1)"><Icon name="chevronRight" /></button>
        </div>
      </section>

      <BaseDialog
        :show="results.length > 0" :title="t('lottery.result')" width="normal"
        content-class="lottery-result-dialog" body-class="lottery-result-body"
        @close="results = []"
      >
        <div v-if="results.length > 1" class="result-summary">
          <div>
            <p class="text-sm font-medium">{{ t('lottery.confirmedResults', { count: results.length }) }}</p>
            <p v-if="hasBalanceResult" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('lottery.balanceTotal') }}</p>
          </div>
          <strong v-if="hasBalanceResult" data-testid="result-balance-total" class="result-total">+{{ money(resultBalance) }}<span>USD</span></strong>
        </div>
        <div class="result-list" :class="{ 'is-single': results.length === 1 }">
          <article v-for="result in results" :key="result.id" class="result-item">
            <PrizeIcon :kind="result.prize.kind" :color="prizeColor(result.prize, pool?.prizes || [])" />
            <div class="result-copy">
              <p v-if="result.prize.kind !== 'text'" class="result-value">
                {{ result.prize.kind === 'balance' ? '+' + money(result.prize.amount) : t('lottery.days', { days: result.prize.validity_days }) }}
                <span v-if="result.prize.kind === 'balance'" class="prize-unit">USD</span>
              </p>
              <h3 class="break-words font-semibold">{{ result.prize.name }}</h3>
              <p v-if="result.prize.kind === 'subscription'" class="mt-1 break-words text-sm text-gray-500 dark:text-gray-400">{{ result.prize.plan_name }}</p>
              <p v-if="result.prize.kind === 'text'" class="result-text">{{ result.prize.text }}</p>
            </div>
            <Icon v-if="results.length > 1" name="checkCircle" size="sm" class="mt-2 shrink-0 text-primary-600 dark:text-primary-400" />
          </article>
        </div>
        <template #footer>
          <button class="btn btn-primary min-h-11 min-w-24" @click="results = []">{{ t('lottery.close') }}</button>
        </template>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import LotteryReel from '@/components/lottery/LotteryReel.vue'
import LotteryInvitePanel from '@/components/lottery/LotteryInvitePanel.vue'
import PrizeIcon from '@/components/lottery/PrizeIcon.vue'
import { availablePrizes, prizeColor, type ReelPrize } from '@/components/lottery/reel'
import { DRAW_COUNTS, isDefinitiveDrawError, readDrawKeys, serializeDrawKeys, type DrawCount } from '@/components/lottery/batch'
import { lotteryAPI, isInvitationPool, lotteryRuleKey, lotteryRuleParams, type LotteryPool, type LotteryDraw } from '@/api/lottery'
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
const available = computed(() => availablePrizes(pool.value?.prizes || []))
const reel = ref<InstanceType<typeof LotteryReel>>()
const drawCount = ref<DrawCount>(1)
const batchNotice = ref('')
let disposed = false
const loading = ref(false),
  busy = ref(false),
  error = ref('')
const records = ref<LotteryDraw[]>([]),
  results = ref<LotteryDraw[]>([])
const page = ref(1),
  total = ref(0),
  pendingKeys = ref<string[]>([])
const storageKey = () => `sub2api.lottery.${auth.user?.id}.${selectedID.value}`
const money = (n: number) =>
  new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }).format(n)
const prizeValue = (prize: ReelPrize) => prize.kind === 'balance' ? money(prize.amount)
  : prize.kind === 'subscription' ? t('lottery.days', { days: prize.validity_days }) : t('lottery.textPrize')
const hasBalanceResult = computed(() => results.value.some(result => result.prize.kind === 'balance'))
const resultBalance = computed(() => results.value.reduce((sum, result) => sum + (result.prize.kind === 'balance' ? result.prize.amount : 0), 0))
const percent = (n: number) =>
  new Intl.NumberFormat(locale.value, {
    style: 'percent',
    maximumFractionDigits: 4,
  }).format(n)
const date = (s: string) => new Date(s).toLocaleString(locale.value)
const canDraw = computed(
  () =>
    !!pendingKeys.value.length ||
    (!!pool.value?.draw_enabled &&
      pool.value.available >= drawCount.value &&
      available.value.length > 0),
)
const drawLabel = computed(() =>
  busy.value
    ? t('lottery.drawing')
    : pendingKeys.value.length
      ? t('lottery.retryDraw')
      : !pool.value?.draw_enabled
        ? t('lottery.paused')
        : pool.value.available <= 0
          ? t('lottery.noTickets')
          : !available.value.length
            ? t('lottery.noPrizes')
            : pool.value.available < drawCount.value
              ? t('lottery.insufficientTickets', { count: drawCount.value })
              : drawCount.value === 1 ? t('lottery.draw') : t('lottery.drawMany', { count: drawCount.value }),
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
  try { pendingKeys.value = readDrawKeys(localStorage.getItem(storageKey())) } catch { pendingKeys.value = [] }
  drawCount.value = (pendingKeys.value.length || 1) as DrawCount
  batchNotice.value = ''
  if (selectedID.value && String(route.query.pool) !== String(selectedID.value))
    void router.replace({ query: { ...route.query, pool: String(selectedID.value) } })
  void nextTick(() => {
    document.getElementById(`pool-tab-${selectedID.value}`)?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  })
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
  batchNotice.value = ''
  const poolID = selectedID.value
  const keyName = storageKey()
  const keys = pendingKeys.value.length ? [...pendingKeys.value] : Array.from({ length: drawCount.value }, () => crypto.randomUUID())
  const completed: LotteryDraw[] = []
  let failure: unknown
  let needsRetry = false
  try {
    // Keep the entire batch until its results are displayed. Replaying completed
    // keys returns the original awards, including after a reload mid-animation.
    localStorage.setItem(keyName, serializeDrawKeys(keys))
    pendingKeys.value = keys
    for (const key of keys) {
      if (disposed) return
      try { completed.push(await lotteryAPI.draw(poolID, key)) }
      catch (e) { failure = e; needsRetry = !isDefinitiveDrawError(e); break }
    }
    if (disposed) return
    if (completed.length) {
      await reel.value?.spinTo(completed.map(record => record.prize))
      if (disposed) return
      results.value = completed
    }
    if (!needsRetry) {
      localStorage.removeItem(keyName)
      pendingKeys.value = []
    }
    if (failure && completed.length) batchNotice.value = t('lottery.batchPartial', { count: completed.length, total: keys.length })
    if (completed.length || isDefinitiveDrawError(failure)) {
      await load()
      void lottery.refresh(true)
    }
    try {
      if (completed.length) {
        await auth.refreshUser()
        if (completed.some(record => record.prize.kind === 'subscription'))
          await subscriptions.fetchActiveSubscriptions(true)
      }
    } catch {
      error.value = t('lottery.refreshWarning')
    }
    if (failure) showError(failure)
  } catch (e) {
    showError(e)
  } finally {
    busy.value = false
  }
}
onMounted(load)
onBeforeUnmount(() => { disposed = true })
</script>

<style scoped>
.lottery-page { overflow-wrap: anywhere; letter-spacing: 0; }
.page-heading { @apply mb-5 flex items-center justify-between gap-3; }
.page-symbol { @apply flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-400; }
.pool-selector { @apply mb-5 min-w-0 border-b border-gray-200 dark:border-dark-700; }
.pool-tabs { display: flex; gap: 8px; overflow-x: auto; padding: 2px 2px 12px; scroll-snap-type: x proximity; scrollbar-width: thin; scrollbar-color: #cbd5e1 transparent; }
.pool-option { @apply flex shrink-0 items-center gap-3 rounded-lg border border-gray-200 bg-white px-3 py-2.5 text-left transition-colors dark:border-dark-700 dark:bg-dark-900; width: 218px; min-height: 64px; scroll-snap-align: start; }
.pool-option:hover { @apply border-primary-300 dark:border-primary-700; }
.pool-option.is-selected { @apply border-primary-500 bg-primary-50/70 dark:border-primary-500 dark:bg-primary-950/40; }
.pool-option:focus-visible { @apply outline outline-2 outline-offset-2 outline-primary-500; }
.pool-option:disabled { cursor: wait; }
.pool-option-copy { @apply min-w-0 flex-1; }
.pool-option-name { @apply block truncate text-sm font-medium; }
.pool-option-status { @apply mt-1 block text-xs text-gray-500 dark:text-gray-400; }
.pool-option-count { @apply text-xl font-semibold tabular-nums text-gray-500 dark:text-gray-400; }
.is-selected .pool-option-count { @apply text-primary-700 dark:text-primary-400; }
.draw-section { @apply min-w-0; }
.draw-toolbar { @apply mb-4 flex min-w-0 flex-wrap items-center justify-between gap-4; }
.draw-heading { flex: 1 1 240px; min-width: 0; }
.ticket-count { @apply mt-1 flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400; }
.ticket-count strong { @apply text-lg font-semibold tabular-nums text-primary-700 dark:text-primary-400; }
.draw-actions { @apply flex max-w-full flex-wrap items-stretch gap-2; }
.quantity-control { @apply flex shrink-0 items-center rounded-lg border border-gray-200 bg-gray-100 p-0.5 dark:border-dark-700 dark:bg-dark-900; }
.quantity-control label { @apply relative; }
.quantity-control span { @apply flex h-11 w-11 cursor-pointer items-center justify-center rounded-md text-sm font-semibold tabular-nums text-gray-500 transition-colors dark:text-gray-400; }
.quantity-control input:checked + span { @apply bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300; }
.quantity-control input:focus-visible + span { @apply outline outline-2 outline-primary-500; outline-offset: -2px; }
.quantity-control input:disabled + span { @apply cursor-not-allowed opacity-40; }
.draw-button { @apply flex min-h-11 min-w-36 items-center justify-center gap-2 !rounded-lg; flex: 1 1 144px; padding: 10px 16px; }
.draw-notices { @apply mb-3 space-y-1 text-xs leading-5 text-amber-700 dark:text-amber-400; }
.lottery-error { @apply rounded-md bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300; }
.progress-row { @apply mt-3 flex flex-wrap items-center gap-x-5 gap-y-1; }
.progress-group { @apply flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2; flex: 1 1 260px; }
.progress-label { @apply flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400; }
.lottery-progress { appearance: none; border: 0; border-radius: 4px; overflow: hidden; width: 96px; height: 4px; background: #e5e7eb; }
.lottery-progress::-webkit-progress-bar { background: #e5e7eb; }
.lottery-progress::-webkit-progress-value { background: #0d9488; border-radius: 4px; }
.lottery-progress::-moz-progress-bar { background: #0d9488; }
.dark .lottery-progress, .dark .lottery-progress::-webkit-progress-bar { background: #374151; }
.recharge-link { @apply ml-auto flex min-h-11 shrink-0 items-center gap-1 text-xs font-medium text-primary-700 hover:text-primary-600 dark:text-primary-400; }
.reel-disclosure { @apply mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400; }
.prizes-section { @apply mt-6; }
.section-heading { @apply mb-3 flex items-center justify-between gap-3; }
.section-heading h2 { @apply text-base font-semibold; }
.prize-grid { @apply grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3; }
.prize-item { @apply flex min-w-0 flex-col justify-between gap-3 rounded-lg border border-gray-200 bg-white p-3.5 dark:border-dark-700 dark:bg-dark-900/80; }
.prize-item.is-unavailable { @apply opacity-60; }
.prize-value { @apply text-xl font-semibold tabular-nums; line-height: 1.3; }
.prize-unit { @apply ml-1.5 text-xs font-medium text-gray-500 dark:text-gray-400; }
.prize-name { @apply mt-1 break-words text-sm text-gray-600 dark:text-gray-300; }
.prize-meta { @apply flex flex-wrap items-center justify-between gap-2 text-xs text-gray-500 dark:text-gray-400; }
.prize-meta strong { @apply font-semibold text-gray-700 dark:text-gray-200; }
.prize-card-heading, .prize-odds-label { display: none; }
.prize-odds { display: contents; }
/* Light-mode prize cards use their own surfaces; the dark presentation stays unchanged. */
html:not(.dark) .prizes-section > .section-heading { margin-bottom: 14px; }
html:not(.dark) .prize-item {
  position: relative;
  gap: 0;
  padding: 0;
  overflow: hidden;
  border-color: #e0e7e6;
  background: #fff;
  box-shadow: 0 2px 4px #182c2705, 0 8px 20px #182c2704;
  transition: border-color 160ms ease, box-shadow 160ms ease;
}
html:not(.dark) .prize-card-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 13px 16px 0;
  font-size: 12px;
  font-weight: 500;
  line-height: 18px;
  color: #66736f;
}
html:not(.dark) .prize-card-accent {
  width: 24px;
  height: 3px;
  flex-shrink: 0;
  border-radius: 2px;
  background: var(--prize-color);
  opacity: 0.65;
}
html:not(.dark) .prize-content { align-items: center; gap: 16px; padding: 13px 16px 17px; flex: 1; }
html:not(.dark) .prize-content :deep(.prize-icon) {
  width: 56px;
  height: 56px;
  border-color: color-mix(in srgb, var(--prize-color) 18%, white);
  background: color-mix(in srgb, var(--prize-color) 8%, white);
  box-shadow: inset 0 1px 0 #ffffff, 0 2px 3px #172c2605;
  color: color-mix(in srgb, var(--prize-color) 85%, #182c27);
}
html:not(.dark) .prize-content :deep(.prize-icon svg) { width: 30px; height: 30px; }
html:not(.dark) .prize-content :deep(.prize-icon-dot) { display: none; }
html:not(.dark) .prize-value { font-size: 27px; line-height: 32px; font-weight: 650; color: #1d2b28; }
html:not(.dark) .prize-value.has-long-value { font-size: 19px; line-height: 26px; }
html:not(.dark) .prize-item[data-kind='text'] .prize-value { font-size: 21px; line-height: 28px; }
html:not(.dark) .prize-unit { margin-left: 6px; font-size: 12px; font-weight: 500; color: #7c8985; }
html:not(.dark) .prize-name { margin-top: 3px; color: #6a7773; font-size: 13px; line-height: 20px; }
html:not(.dark) .prize-meta {
  min-height: 40px;
  padding: 9px 16px;
  border-top: 1px solid #eef1f0;
  background: #f8faf9;
  color: #75817d;
  line-height: 20px;
}
html:not(.dark) .prize-odds { display: inline-flex; align-items: baseline; gap: 8px; margin-left: auto; }
html:not(.dark) .prize-odds-label { display: inline; font-size: 12px; color: #7b8783; }
html:not(.dark) .prize-meta strong { font-size: 13px; font-weight: 650; color: color-mix(in srgb, var(--prize-color) 75%, #172c26); }
html:not(.dark) .prize-item.is-unavailable { opacity: 1; background: #fafbfb; box-shadow: none; }
html:not(.dark) .prize-item.is-unavailable .prize-content,
html:not(.dark) .prize-item.is-unavailable .prize-card-heading { opacity: 0.5; }
html:not(.dark) .prize-item.is-unavailable .prize-stock { color: #8d6555; }
html:not(.dark) .prize-item.is-unavailable .prize-meta strong { color: #7b8783; }
@media (hover: hover) {
  html:not(.dark) .prize-item:not(.is-unavailable):hover {
    border-color: color-mix(in srgb, var(--prize-color) 35%, #dce4e1);
    box-shadow: 0 3px 7px #182c2706, 0 10px 24px #182c2708;
  }
}
.odds-note { @apply mt-3 text-xs leading-5 text-gray-500 dark:text-gray-400; }
.lottery-rules { @apply mt-2 border-b border-gray-200 dark:border-dark-700; }
.lottery-rules summary { @apply flex min-h-11 w-fit cursor-pointer list-none items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-300; }
.lottery-rules summary::-webkit-details-marker { display: none; }
.lottery-rules summary:focus-visible { @apply rounded outline outline-2 outline-primary-500; }
.rules-chevron { transition: transform 150ms ease; }
.lottery-rules[open] .rules-chevron { transform: rotate(180deg); }
.history-section { @apply mt-6; }
.history-table { @apply hidden w-full table-fixed text-left text-sm md:table; }
.history-table th { @apply border-b border-gray-200 pb-2 text-xs font-medium text-gray-500 dark:border-dark-700 dark:text-gray-400; }
.history-table th:nth-child(1) { width: 26%; }
.history-table th:nth-child(2) { width: 34%; }
.history-table th, .history-table td { @apply pr-4 last:pr-0; }
.history-table td { @apply border-b border-gray-100 py-1.5 dark:border-dark-800; }
.history-detail { @apply flex min-h-11 items-center gap-2 text-left text-sm font-medium text-primary-700 dark:text-primary-400; }
.history-mobile { @apply divide-y divide-gray-200 dark:divide-dark-700 md:hidden; }
.history-record { @apply flex w-full items-center gap-3 py-3 text-left; }
.history-record :deep(.prize-icon) { width: 36px; height: 36px; border-radius: 6px; }
.history-record :deep(.prize-icon svg) { width: 20px; height: 20px; }
.history-pagination { @apply mt-3 flex items-center justify-between gap-3 text-center text-xs text-gray-500 dark:text-gray-400; }
.result-summary { @apply mb-2 flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-700; }
.result-total { @apply break-words text-2xl font-semibold tabular-nums text-primary-700 dark:text-primary-400; }
.result-total span { @apply ml-1.5 text-xs font-medium; }
.result-item { @apply flex min-w-0 items-start gap-3 border-b border-gray-100 py-3 last:border-0 dark:border-dark-700; }
.result-item :deep(.prize-icon) { width: 40px; height: 40px; }
.result-copy { @apply min-w-0 flex-1 text-sm; }
.result-value { @apply mb-1 text-lg font-semibold tabular-nums text-primary-700 dark:text-primary-400; }
.result-text { @apply mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-gray-600 dark:text-gray-300; overflow-wrap: anywhere; }
.is-single .result-item { @apply flex-col items-center py-4 text-center; }
.is-single .result-item :deep(.prize-icon) { width: 64px; height: 64px; }
.is-single .result-item :deep(.prize-icon svg) { width: 32px; height: 32px; }
.is-single .result-copy { width: 100%; }
.is-single .result-value { @apply mb-2 text-3xl; }
.is-single h3 { @apply text-lg; }
:global(.lottery-result-dialog) { border-radius: 8px; max-height: calc(100dvh - 32px); overflow-wrap: anywhere; }
:global(.lottery-result-body) { min-height: 0; overscroll-behavior: contain; }
:global(.lottery-result-dialog .modal-header button) { min-width: 44px; min-height: 44px; }
@media (max-width: 639px) {
  .page-heading { margin-bottom: 16px; }
  .pool-option { width: 218px; }
  .draw-toolbar { gap: 12px; }
  .draw-heading { flex-basis: 100%; }
  .draw-actions { width: 100%; }
  .draw-button { min-width: 0; flex-basis: 120px; padding: 10px 12px; }
  .progress-group { flex-basis: 180px; }
  .progress-label { font-size: 12px; }
  .lottery-progress { width: 100%; max-width: 260px; }
  .prizes-section, .history-section { margin-top: 24px; }
}
@media (prefers-reduced-motion: reduce) {
  .rules-chevron { transition: none; }
  html:not(.dark) .prize-item { transition: none; }
}
</style>
