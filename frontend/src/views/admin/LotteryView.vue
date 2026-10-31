<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-5 text-gray-900 dark:text-gray-100">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-2xl font-semibold">{{ t('lottery.adminTitle') }}</h1>
        <div class="flex gap-2">
          <button
            class="btn btn-secondary h-10 w-10 !p-2"
            :title="t('lottery.refresh')"
            :aria-label="t('lottery.refresh')"
            :disabled="loading"
            @click="load"
          >
            <Icon name="refresh" /></button
          ><button class="btn btn-primary gap-2" @click="editPool()">
            <Icon name="plus" size="sm" />{{ t('lottery.addPool') }}
          </button>
        </div>
      </header>
      <section class="flex items-center justify-between gap-4 border-y border-gray-200 py-4 dark:border-dark-700">
        <div class="min-w-0">
          <h2 id="lottery-enabled-label" class="text-sm font-semibold">{{ t('lottery.siteEnabled') }}</h2>
          <p class="mt-1 text-sm text-gray-500">{{ t(enabled ? 'lottery.siteEnabledNote' : 'lottery.siteDisabledNote') }}</p>
        </div>
        <Toggle :model-value="enabled" :disabled="saving || loading || !settingsLoaded" aria-labelledby="lottery-enabled-label" @update:model-value="saveEnabled" />
      </section>
      <p
        v-if="error && !poolDialog && !prizeDialog && !deleteTarget"
        role="alert"
        class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950 dark:text-red-300"
      >
        {{ error }}
      </p>
      <nav
        class="flex gap-6 border-b border-gray-200 dark:border-dark-700"
        role="tablist"
      >
        <button
          v-for="key in tabs"
          :key="key"
          role="tab"
          :aria-selected="tab === key"
          class="border-b-2 py-3 text-sm font-medium"
          :class="
            tab === key
              ? 'border-primary-600 text-primary-600'
              : 'border-transparent text-gray-500'
          "
          @click="tab = key"
        >
          {{ t(`lottery.${key}`) }}
        </button>
      </nav>
      <template v-if="tab === 'pools'">
        <p
          v-if="loading && !pools.length"
          class="py-12 text-center text-gray-500"
        >
          {{ t('lottery.loading') }}
        </p>
        <p v-else-if="!pools.length" class="py-12 text-center text-gray-500">
          {{ t('lottery.empty') }}
        </p>
        <section
          v-for="pool in pools"
          :key="pool.id"
          class="border-b border-gray-200 pb-6 dark:border-dark-700"
        >
          <header
            class="flex flex-wrap items-center justify-between gap-3 py-3"
          >
            <div class="min-w-0">
              <h2 class="break-words text-lg font-semibold">{{ pool.name }}</h2>
              <p class="mt-1 text-sm text-gray-500">
                {{
                  t(
                    pool.mode === 'single'
                      ? 'lottery.singleRule'
                      : 'lottery.cumulativeRule',
                    { amount: pool.threshold },
                  )
                }}
              </p>
              <div class="mt-2 flex flex-wrap gap-3 text-xs">
                <span
                  :class="
                    pool.grant_enabled
                      ? 'text-emerald-700 dark:text-emerald-400'
                      : 'text-gray-500'
                  "
                  >{{
                    t(
                      pool.grant_enabled
                        ? 'lottery.grantEnabled'
                        : 'lottery.grantStopped',
                    )
                  }}</span
                ><span
                  :class="
                    pool.draw_enabled
                      ? 'text-emerald-700 dark:text-emerald-400'
                      : 'text-gray-500'
                  "
                  >{{
                    t(
                      pool.draw_enabled
                        ? 'lottery.drawEnabled'
                        : 'lottery.paused',
                    )
                  }}</span
                >
              </div>
            </div>
            <div class="flex shrink-0 gap-2">
              <button class="btn btn-secondary h-10 w-10 !p-2 text-rose-600" :title="t('lottery.deletePool')" :aria-label="`${t('lottery.deletePool')}: ${pool.name}`" @click="requestDelete(pool)"><Icon name="trash" /></button>
              <button
                class="btn btn-secondary h-10 w-10 !p-2"
                :title="t('lottery.editPool')"
                :aria-label="t('lottery.editPool')"
                @click="editPool(pool)"
              >
                <Icon name="edit" /></button
              ><button class="btn btn-secondary gap-2" @click="editPrize(pool)">
                <Icon name="plus" size="sm" />{{ t('lottery.addPrize') }}
              </button>
            </div>
          </header>
          <div class="overflow-x-auto">
            <table class="w-full min-w-[720px] text-left text-sm">
              <thead class="bg-gray-50 text-gray-500 dark:bg-dark-800">
                <tr>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.prizes') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.reward') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.weight') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.probability') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.stock') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.enabled') }}
                  </th>
                  <th class="w-24"></th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="prize in pool.prizes" :key="prize.id">
                  <td class="max-w-xs break-words px-3 py-3 font-medium">
                    {{ prize.name }}
                  </td>
                  <td class="max-w-xs px-3 py-3">
                    <span v-if="prize.kind === 'balance'"
                      >{{ money(prize.amount) }} USD</span
                    ><span v-else-if="prize.kind === 'subscription'">{{
                      t('lottery.snapshot', {
                        plan: prize.plan_name,
                        days: prize.validity_days,
                      })
                    }}</span
                    ><span v-else class="line-clamp-2 break-words">{{
                      prize.text
                    }}</span>
                  </td>
                  <td class="px-3 py-3 tabular-nums">{{ prize.weight }}</td>
                  <td class="px-3 py-3 tabular-nums">
                    {{ percent(prize.probability) }}
                  </td>
                  <td class="whitespace-nowrap px-3 py-3">
                    {{
                      prize.stock === -1 ? t('lottery.unlimited') : prize.stock
                    }}
                  </td>
                  <td class="px-3 py-3">
                    <Icon
                      :name="prize.enabled ? 'checkCircle' : 'xCircle'"
                      :class="
                        prize.available ? 'text-emerald-600' : 'text-gray-400'
                      "
                      :title="
                        prize.available
                          ? t('lottery.enabled')
                          : t('lottery.unavailable')
                      "
                    />
                  </td>
                  <td class="px-3 py-3">
                    <div class="flex gap-1">
                    <button
                      class="btn btn-secondary h-9 w-9 !p-2"
                      :title="t('lottery.editPrize')"
                      :aria-label="`${t('lottery.editPrize')}: ${prize.name}`"
                      @click="editPrize(pool, prize)"
                    >
                      <Icon name="edit" size="sm" />
                    </button>
                    <button class="btn btn-secondary h-9 w-9 !p-2 text-rose-600" :title="t('lottery.deletePrize')" :aria-label="`${t('lottery.deletePrize')}: ${prize.name}`" @click="requestDelete(pool, prize)"><Icon name="trash" size="sm" /></button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p
            v-if="!pool.prizes.length"
            class="py-6 text-center text-sm text-gray-500"
          >
            {{ t('lottery.noPrizes') }}
          </p>
        </section>
      </template>
      <template v-else>
        <form
          class="flex flex-wrap items-end gap-3"
          @submit.prevent="searchHistory"
        >
          <label class="min-w-0 sm:w-64"
            ><span class="input-label">{{ t('lottery.pools') }}</span
            ><select v-model="filterPool" class="input mt-1">
              <option :value="0">{{ t('lottery.allPools') }}</option>
              <option v-for="pool in pools" :key="pool.id" :value="pool.id">
                {{ pool.name }}
              </option>
            </select></label
          ><label class="w-36"
            ><span class="input-label">{{ t('lottery.userID') }}</span
            ><input
              v-model.number="filterUser"
              type="number"
              min="1"
              class="input mt-1" /></label
          ><button
            class="btn btn-secondary h-10 w-10 !p-2"
            :title="t('lottery.refresh')"
            :aria-label="t('lottery.refresh')"
          >
            <Icon name="search" />
          </button>
        </form>
        <div class="overflow-x-auto">
          <table class="w-full min-w-[720px] text-left text-sm">
            <thead class="bg-gray-50 text-gray-500 dark:bg-dark-800">
              <tr>
                <th class="px-3 py-3 font-medium">{{ t('lottery.time') }}</th>
                <th class="px-3 py-3 font-medium">{{ t('lottery.user') }}</th>
                <th class="px-3 py-3 font-medium">{{ t('lottery.pools') }}</th>
                <template v-if="tab === 'records'"
                  ><th class="px-3 py-3 font-medium">
                    {{ t('lottery.reward') }}
                  </th></template
                ><template v-else
                  ><th class="px-3 py-3 font-medium">
                    {{ t('lottery.source') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.amount') }}
                  </th>
                  <th class="px-3 py-3 font-medium">
                    {{ t('lottery.delta') }}
                  </th></template
                >
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <template v-if="tab === 'records'"
                ><tr v-for="record in records" :key="record.id">
                  <td class="whitespace-nowrap px-3 py-3">
                    {{ date(record.created_at) }}
                  </td>
                  <td class="px-3 py-3">#{{ record.user_id }}</td>
                  <td class="px-3 py-3">{{ record.prize.pool_name }}</td>
                  <td class="max-w-md break-words px-3 py-3">
                    <span class="font-medium">{{ record.prize.name }}</span>
                    <p class="mt-1 whitespace-pre-wrap text-xs text-gray-500">
                      {{
                        record.prize.kind === 'text'
                          ? record.prize.text
                          : record.prize.kind === 'balance'
                            ? `${money(record.prize.amount)} USD`
                            : t('lottery.snapshot', {
                                plan: record.prize.plan_name,
                                days: record.prize.validity_days,
                              })
                      }}
                    </p>
                  </td>
                </tr></template
              ><template v-else
                ><tr v-for="entry in ledger" :key="entry.id">
                  <td class="whitespace-nowrap px-3 py-3">
                    {{ date(entry.created_at) }}
                  </td>
                  <td class="px-3 py-3">#{{ entry.user_id }}</td>
                  <td class="px-3 py-3">{{ poolName(entry.pool_id) }}</td>
                  <td class="px-3 py-3">
                    <p>
                      {{
                        t(
                          `lottery.${entry.action === 'draw' ? 'drawAction' : entry.action}`,
                        )
                      }}
                    </p>
                    <p class="mt-1 text-xs text-gray-500">
                      {{ t(`lottery.${entry.source_type}Source`) }} #{{
                        entry.source_id
                      }}
                    </p>
                  </td>
                  <td class="px-3 py-3 tabular-nums">
                    {{ money(entry.amount) }}
                  </td>
                  <td
                    class="px-3 py-3 tabular-nums"
                    :class="
                      entry.ticket_delta > 0
                        ? 'text-emerald-600'
                        : entry.ticket_delta < 0
                          ? 'text-rose-600'
                          : ''
                    "
                  >
                    {{ entry.ticket_delta > 0 ? '+' : ''
                    }}{{ entry.ticket_delta }}
                  </td>
                </tr></template
              >
            </tbody>
          </table>
        </div>
        <p v-if="!total" class="py-8 text-center text-sm text-gray-500">
          {{ t('lottery.noRecords') }}
        </p>
        <div
          v-if="total"
          class="flex items-center justify-between gap-2 text-sm"
        >
          <button
            class="btn btn-secondary"
            :disabled="page <= 1"
            @click="changeHistoryPage(-1)"
          >
            {{ t('lottery.previous') }}</button
          ><span>{{ t('lottery.page', { page, total }) }}</span
          ><button
            class="btn btn-secondary"
            :disabled="page * 20 >= total"
            @click="changeHistoryPage(1)"
          >
            {{ t('lottery.next') }}
          </button>
        </div>
      </template>
      <BaseDialog
        :show="poolDialog"
        :title="t(poolID ? 'lottery.editPool' : 'lottery.addPool')"
        :show-close-button="!saving"
        @close="poolDialog = false"
      >
        <form
          id="lottery-pool-form"
          class="space-y-4"
          @submit.prevent="savePool"
        >
          <fieldset :disabled="saving" class="space-y-4">
            <label class="block"
              ><span class="input-label">{{ t('lottery.name') }}</span
              ><input
                v-model="poolForm.name"
                maxlength="100"
                required
                class="input mt-1" /></label
            ><label class="block"
              ><span class="input-label">{{ t('lottery.mode') }}</span
              ><select
                v-model="poolForm.mode"
                class="input mt-1"
                :disabled="locked"
              >
                <option value="single">{{ t('lottery.single') }}</option>
                <option value="cumulative">
                  {{ t('lottery.cumulative') }}
                </option>
              </select></label
            ><label class="block"
              ><span class="input-label">{{ t('lottery.threshold') }}</span
              ><input
                v-model.number="poolForm.threshold"
                type="number"
                min="0.01"
                max="1000000000"
                step="0.00000001"
                required
                class="input mt-1"
                :disabled="locked"
            /></label>
            <p v-if="locked" class="text-xs text-amber-700 dark:text-amber-400">
              {{ t('lottery.ruleLocked') }}
            </p>
            <label class="block"
              ><span class="input-label">{{ t('lottery.sort') }}</span
              ><input
                v-model.number="poolForm.sort_order"
                type="number"
                step="1"
                class="input mt-1"
            /></label>
            <div v-if="poolID" class="flex flex-wrap gap-5">
              <label class="flex items-center gap-2 text-sm"
                ><input v-model="poolForm.grant_enabled" type="checkbox" />{{
                  t('lottery.grantEnabled')
                }}</label
              ><label class="flex items-center gap-2 text-sm"
                ><input v-model="poolForm.draw_enabled" type="checkbox" />{{
                  t('lottery.drawEnabled')
                }}</label
              >
            </div>
          </fieldset>
          <p v-if="error" role="alert" class="text-sm text-red-600">
            {{ error }}
          </p>
        </form>
        <template #footer
          ><button
            class="btn btn-secondary"
            :disabled="saving"
            @click="poolDialog = false"
          >
            {{ t('lottery.cancel') }}</button
          ><button
            class="btn btn-primary"
            form="lottery-pool-form"
            type="submit"
            :disabled="saving"
          >
            {{ t('lottery.save') }}
          </button></template
        >
      </BaseDialog>
      <BaseDialog
        :show="prizeDialog"
        :title="t(prizeID ? 'lottery.editPrize' : 'lottery.addPrize')"
        :show-close-button="!saving"
        @close="prizeDialog = false"
      >
        <form
          id="lottery-prize-form"
          class="space-y-4"
          @submit.prevent="savePrize"
        >
          <fieldset :disabled="saving" class="space-y-4">
            <label class="block"
              ><span class="input-label">{{ t('lottery.name') }}</span
              ><input
                v-model="prizeForm.name"
                required
                maxlength="100"
                class="input mt-1" /></label
            ><label class="block"
              ><span class="input-label">{{ t('lottery.reward') }}</span
              ><select v-model="prizeForm.kind" class="input mt-1">
                <option value="balance">{{ t('lottery.balance') }}</option>
                <option value="subscription">
                  {{ t('lottery.subscription') }}
                </option>
                <option value="text">{{ t('lottery.text') }}</option>
              </select></label
            ><label v-if="prizeForm.kind === 'balance'" class="block"
              ><span class="input-label">{{ t('lottery.amount') }}</span
              ><input
                v-model.number="prizeForm.amount"
                type="number"
                min="0.00000001"
                max="1000000000"
                step="0.00000001"
                required
                class="input mt-1" /></label
            ><label v-else-if="prizeForm.kind === 'subscription'" class="block"
              ><span class="input-label">{{ t('lottery.plan') }}</span
              ><select v-model="prizeForm.plan_id" required class="input mt-1">
                <option :value="0" disabled>{{ t('lottery.plan') }}</option>
                <option
                  v-if="
                    oldPrize?.plan_id &&
                    !plans.some((p) => p.id === oldPrize?.plan_id)
                  "
                  :value="oldPrize.plan_id"
                >
                  {{ oldPrize.plan_name }}
                </option>
                <option v-for="plan in plans" :key="plan.id" :value="plan.id">
                  {{ plan.name }}
                </option></select
              ><span
                v-if="
                  oldPrize?.kind === 'subscription' &&
                  oldPrize.plan_id === prizeForm.plan_id
                "
                class="mt-1 block text-xs text-gray-500"
                >{{
                  t('lottery.snapshot', {
                    plan: oldPrize.plan_name,
                    days: oldPrize.validity_days,
                  })
                }}</span
              ></label
            ><label v-else class="block"
              ><span class="input-label">{{ t('lottery.textContent') }}</span
              ><textarea
                v-model="prizeForm.text"
                required
                maxlength="4000"
                rows="4"
                class="input mt-1"
              ></textarea>
            </label>
            <div class="grid grid-cols-2 gap-4">
              <label
                ><span class="input-label">{{ t('lottery.weight') }}</span
                ><input
                  v-model.number="prizeForm.weight"
                  type="number"
                  min="1"
                  max="1000000"
                  step="1"
                  required
                  class="input mt-1" /></label
              ><label
                ><span class="input-label">{{ t('lottery.sort') }}</span
                ><input
                  v-model.number="prizeForm.sort_order"
                  type="number"
                  step="1"
                  class="input mt-1"
              /></label>
            </div>
            <label class="flex items-center gap-2 text-sm"
              ><input v-model="unlimited" type="checkbox" />{{
                t('lottery.unlimited')
              }}</label
            ><label v-if="!unlimited" class="block"
              ><span class="input-label">{{ t('lottery.stock') }}</span
              ><input
                v-model.number="prizeForm.stock"
                type="number"
                min="0"
                max="1000000000"
                step="1"
                required
                class="input mt-1" /></label
            ><label class="flex items-center gap-2 text-sm"
              ><input v-model="prizeForm.enabled" type="checkbox" />{{
                t('lottery.enabled')
              }}</label
            >
          </fieldset>
          <p v-if="error" role="alert" class="text-sm text-red-600">
            {{ error }}
          </p>
        </form>
        <template #footer
          ><button
            class="btn btn-secondary"
            :disabled="saving"
            @click="prizeDialog = false"
          >
            {{ t('lottery.cancel') }}</button
          ><button
            class="btn btn-primary"
            form="lottery-prize-form"
            type="submit"
            :disabled="saving"
          >
            {{ t('lottery.save') }}
          </button></template
        >
      </BaseDialog>
      <BaseDialog :show="!!deleteTarget" :title="t(deleteTarget?.prize ? 'lottery.deletePrize' : 'lottery.deletePool')" width="narrow" :show-close-button="!saving" @close="!saving && (deleteTarget = null)">
        <p class="break-words font-semibold">{{ deleteTarget?.prize?.name || deleteTarget?.pool.name }}</p>
        <p class="mt-3 text-sm leading-relaxed text-gray-600 dark:text-gray-300">{{ t(deleteTarget?.prize ? 'lottery.deletePrizeNote' : 'lottery.deletePoolNote') }}</p>
        <p v-if="error" role="alert" class="mt-3 text-sm text-red-600">{{ error }}</p>
        <template #footer>
          <button class="btn btn-secondary" :disabled="saving" @click="deleteTarget = null">{{ t('lottery.cancel') }}</button>
          <button class="btn bg-rose-600 text-white hover:bg-rose-700" data-testid="confirm-lottery-delete" :disabled="saving" @click="confirmDelete"><Icon name="trash" size="sm" class="mr-2" />{{ t('lottery.confirmDelete') }}</button>
        </template>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores/app'
import { useLotteryStore } from '@/stores/lottery'
import {
  adminLotteryAPI,
  type LotteryPool,
  type LotteryPrize,
  type LotteryDraw,
  type LotteryLedger,
  type PoolInput,
  type PrizeInput,
} from '@/api/lottery'
import { adminPaymentAPI } from '@/api/admin/payment'
import type { SubscriptionPlan } from '@/types/payment'

const { t, te, locale } = useI18n()
const app = useAppStore()
const lottery = useLotteryStore()
const enabled = ref(false)
const settingsLoaded = ref(false)
const deleteTarget = ref<{ pool: LotteryPool; prize?: LotteryPrize } | null>(null)
const tabs = ['pools', 'records', 'ledger'] as const
const tab = ref<(typeof tabs)[number]>('pools')
const pools = ref<LotteryPool[]>([]),
  plans = ref<SubscriptionPlan[]>([])
const loading = ref(false),
  saving = ref(false),
  error = ref('')
const poolDialog = ref(false),
  prizeDialog = ref(false),
  locked = ref(false)
const poolID = ref<number | null>(null),
  prizeID = ref<number | null>(null),
  prizePoolID = ref(0)
const oldPrize = ref<LotteryPrize | null>(null)
const poolForm = reactive<PoolInput>({
  name: '',
  mode: 'single',
  threshold: 100,
  grant_enabled: false,
  draw_enabled: false,
  sort_order: 0,
})
const prizeForm = reactive<PrizeInput>({
  name: '',
  kind: 'balance',
  weight: 1,
  stock: -1,
  enabled: true,
  sort_order: 0,
  amount: 1,
  plan_id: 0,
  text: '',
})
const unlimited = computed({
  get: () => prizeForm.stock === -1,
  set: (value) => {
    prizeForm.stock = value ? -1 : 10
  },
})
const records = ref<LotteryDraw[]>([]),
  ledger = ref<LotteryLedger[]>([])
const page = ref(1),
  total = ref(0),
  filterPool = ref(0),
  filterUser = ref<number | ''>('')
let writeKey = '',
  writePayload = ''
let historyGeneration = 0
const money = (n: number) =>
  new Intl.NumberFormat(locale.value, { maximumFractionDigits: 8 }).format(n)
const percent = (n: number) =>
  new Intl.NumberFormat(locale.value, {
    style: 'percent',
    maximumFractionDigits: 4,
  }).format(n)
const date = (s: string) => new Date(s).toLocaleString(locale.value)
const poolName = (id: number) =>
  pools.value.find((p) => p.id === id)?.name || `#${id}`
function showError(e: unknown) {
  const err = e as { reason?: string; message?: string }
  const key = `lottery.errors.${err.reason}`
  error.value = te(key) ? t(key) : err.message || t('lottery.error')
}
function keyFor(input: unknown) {
  const serialized = JSON.stringify(input)
  if (!writeKey || writePayload !== serialized) {
    writeKey = crypto.randomUUID()
    writePayload = serialized
  }
  return writeKey
}
async function load() {
  loading.value = true
  error.value = ''
  try {
    const config = await adminLotteryAPI.settings()
    enabled.value = config.enabled
    settingsLoaded.value = true
    pools.value = await adminLotteryAPI.pools()
    if (tab.value !== 'pools') await loadHistory()
  } catch (e) {
    showError(e)
  } finally {
    loading.value = false
  }
}
async function saveEnabled(value: boolean) {
  if (saving.value) return
  saving.value = true
  error.value = ''
  try {
    const config = await adminLotteryAPI.saveSettings(value, keyFor({ enabled: value }))
    enabled.value = config.enabled
    writeKey = ''
    if (app.cachedPublicSettings) app.cachedPublicSettings.lottery_enabled = config.enabled
    await app.fetchPublicSettings(true)
    await lottery.refresh(true)
  } catch (e) {
    showError(e)
  } finally {
    saving.value = false
  }
}
function requestDelete(pool: LotteryPool, prize?: LotteryPrize) {
  error.value = ''
  writeKey = ''
  deleteTarget.value = { pool, prize }
}
async function confirmDelete() {
  if (!deleteTarget.value || saving.value) return
  const { pool, prize } = deleteTarget.value
  saving.value = true
  error.value = ''
  try {
    const key = keyFor({ deletePool: pool.id, prize: prize?.id })
    if (prize) await adminLotteryAPI.deletePrize(pool.id, prize.id, key)
    else await adminLotteryAPI.deletePool(pool.id, key)
    deleteTarget.value = null
    await load()
    await lottery.refresh(true)
  } catch (e) {
    showError(e)
  } finally {
    saving.value = false
  }
}
async function loadHistory() {
  const generation = ++historyGeneration
  error.value = ''
  try {
    if (tab.value === 'records') {
      const r = await adminLotteryAPI.records(
        page.value,
        filterPool.value || undefined,
        filterUser.value || undefined,
      )
      if (generation === historyGeneration) {
        records.value = r.items
        total.value = r.total
      }
    } else if (tab.value === 'ledger') {
      const r = await adminLotteryAPI.ledger(
        page.value,
        filterPool.value || undefined,
        filterUser.value || undefined,
      )
      if (generation === historyGeneration) {
        ledger.value = r.items
        total.value = r.total
      }
    }
  } catch (e) {
    if (generation === historyGeneration) showError(e)
  }
}
function searchHistory() {
  page.value = 1
  void loadHistory()
}
function changeHistoryPage(delta: number) {
  page.value += delta
  void loadHistory()
}
function editPool(pool?: LotteryPool) {
  error.value = ''
  writeKey = ''
  poolID.value = pool?.id ?? null
  locked.value = !!pool?.rule_locked
  Object.assign(poolForm, {
    name: pool?.name ?? '',
    mode: pool?.mode ?? 'single',
    threshold: pool?.threshold ?? 100,
    grant_enabled: pool?.grant_enabled ?? false,
    draw_enabled: pool?.draw_enabled ?? false,
    sort_order: pool?.sort_order ?? 0,
  })
  poolDialog.value = true
}
async function editPrize(pool: LotteryPool, prize?: LotteryPrize) {
  error.value = ''
  writeKey = ''
  prizePoolID.value = pool.id
  prizeID.value = prize?.id ?? null
  oldPrize.value = prize ?? null
  Object.assign(prizeForm, {
    name: prize?.name ?? '',
    kind: prize?.kind ?? 'balance',
    weight: prize?.weight ?? 1,
    stock: prize?.stock ?? -1,
    enabled: prize?.enabled ?? true,
    sort_order: prize?.sort_order ?? 0,
    amount: prize?.amount ?? 1,
    plan_id: prize?.plan_id ?? 0,
    text: prize?.text ?? '',
    updated_at: prize?.updated_at,
  })
  prizeDialog.value = true
  try {
    plans.value = (await adminPaymentAPI.getPlans()).data.filter(
      (p) => p.for_sale,
    )
  } catch (e) {
    showError(e)
  }
}
async function savePool() {
  saving.value = true
  error.value = ''
  try {
    await adminLotteryAPI.savePool(
      poolID.value,
      { ...poolForm },
      keyFor({ id: poolID.value, ...poolForm }),
    )
    poolDialog.value = false
    await load()
  } catch (e) {
    showError(e)
  } finally {
    saving.value = false
  }
}
async function savePrize() {
  saving.value = true
  error.value = ''
  try {
    await adminLotteryAPI.savePrize(
      prizePoolID.value,
      prizeID.value,
      { ...prizeForm },
      keyFor({ pool: prizePoolID.value, id: prizeID.value, ...prizeForm }),
    )
    prizeDialog.value = false
    await load()
  } catch (e) {
    showError(e)
  } finally {
    saving.value = false
  }
}
watch(tab, () => {
  page.value = 1
  total.value = 0
  void loadHistory()
})
onMounted(load)
</script>
