<template>
  <div class="space-y-6">
    <p class="text-sm text-gray-500">{{ t('qqbot.rankings.help') }}</p>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
    <p v-if="dirty" class="text-sm text-amber-700">{{ t('qqbot.workspace.unsaved') }}</p>
    <form v-if="form" data-testid="ranking-settings-form" class="card space-y-6 p-6" @submit.prevent="save">
      <fieldset class="space-y-4" :disabled="busy">
        <legend class="mb-2 font-medium">{{ t('qqbot.rankings.title') }}</legend>
        <label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" data-testid="rankings-enabled" />{{ t('qqbot.rankings.enabled') }}</label>
        <label class="flex items-center gap-2"><input v-model="form.rewards_enabled" type="checkbox" data-testid="rankings-rewards-enabled" />{{ t('qqbot.rankings.rewardsEnabled') }}</label>
        <p class="rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ t('qqbot.rankings.settlement') }}</p>
        <p v-if="form.effective_day" class="text-sm text-gray-500">{{ t('qqbot.rankings.effective', { day: form.effective_day }) }}</p>
        <p class="text-sm text-gray-500">{{ t('qqbot.rankings.tieHelp') }}</p>
        <section v-for="board in boards" :key="board.key" class="space-y-4">
          <h3 class="font-medium">{{ t(board.label) }}</h3>
          <p class="text-sm text-gray-500">{{ t(board.help) }}</p>
          <article v-for="(prize, index) in form[board.key]" :key="index" class="space-y-4 rounded-lg border p-4 dark:border-dark-700">
            <div class="flex flex-wrap items-end justify-between gap-3">
              <label class="space-y-1">{{ t('qqbot.rankings.rank') }}
                <select v-model.number="prize.rank" class="input" :data-testid="board.key + '-rank-' + index">
                  <option v-for="rank in 10" :key="rank" :value="rank">{{ t('qqbot.rankings.place', { rank }) }}</option>
                </select>
              </label>
              <button type="button" class="text-sm text-red-600 dark:text-red-400" :data-testid="board.key + '-remove-' + index" @click="form[board.key].splice(index, 1)">{{ t('common.delete') }}</button>
            </div>
            <QQBotGrowthRewardFields v-model="prize.reward" :prefix="board.key" :index="index" />
          </article>
          <p v-if="!form[board.key].length" class="text-sm text-gray-500">{{ t('qqbot.rankings.empty') }}</p>
          <button type="button" class="btn btn-secondary" :data-testid="board.key + '-add'" :disabled="form[board.key].length >= 10" @click="addPrize(board.key)">{{ t('qqbot.rankings.add') }}</button>
        </section>
      </fieldset>
      <button type="submit" class="btn btn-primary" :disabled="busy" data-testid="rankings-save">{{ t('common.save') }}</button>
    </form>
    <p v-else-if="!error">{{ t('common.loading') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useQQDraft } from './useQQDraft'
import { onMounted, ref, toRaw } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQRankingConfig, type QQRankingPrize } from '@/api/qqbot'
import QQBotGrowthRewardFields from './QQBotGrowthRewardFields.vue'
import { normalizeGrowthReward, rewardEnabled, validGrowthReward } from './growthReward'

const { t } = useI18n()
const boards = [
  { key: 'token_rewards', label: 'qqbot.rankings.tokens', help: 'qqbot.rankings.tokenHelp' },
  { key: 'amount_rewards', label: 'qqbot.rankings.amount', help: 'qqbot.rankings.amountHelp' }
] as const
type Board = typeof boards[number]['key']
const form = ref<QQRankingConfig | null>(null)
const { dirty, markSaved } = useQQDraft(form)
const busy = ref(false), error = ref(''), notice = ref('')

function addPrize(board: Board) {
  if (!form.value || busy.value || form.value[board].length >= 10) return
  const used = new Set(form.value[board].map(prize => prize.rank))
  const rank = Array.from({ length: 10 }, (_, i) => i + 1).find(rank => !used.has(rank))!
  form.value[board].push({ rank, reward: normalizeGrowthReward() })
}
function validPrizes(prizes: QQRankingPrize[]) {
  return prizes.length <= 10 && new Set(prizes.map(prize => prize.rank)).size === prizes.length
    && prizes.every(prize => Number.isInteger(prize.rank) && prize.rank >= 1 && prize.rank <= 10 && validGrowthReward(prize.reward))
}
async function save() {
  if (!form.value || busy.value) return
  error.value = ''; notice.value = ''
  if (!boards.every(board => validPrizes(form.value![board.key]))
    || (form.value.rewards_enabled && !boards.some(board => form.value![board.key].some(prize => rewardEnabled(prize.reward))))) {
    error.value = t('qqbot.rankings.invalid'); return
  }
  busy.value = true
  try { form.value = await qqbotAPI.saveRankings(structuredClone(toRaw(form.value))); markSaved(); notice.value = t('common.saved') }
  catch (e) { error.value = (e as Error).message || t('common.error') }
  finally { busy.value = false }
}
onMounted(async () => {
  try { form.value = await qqbotAPI.rankingConfig() }
  catch (e) { error.value = (e as Error).message || t('common.error') }
})
</script>
