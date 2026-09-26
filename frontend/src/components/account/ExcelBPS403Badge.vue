<template>
  <span
    v-if="actions.length"
    data-test="excel-bps-403-badge"
    class="mt-1 inline-flex items-center self-start rounded bg-amber-400 px-1.5 py-0.5 text-[11px] font-semibold leading-4 text-amber-950 ring-1 ring-amber-500"
    :title="title"
  >
    {{ t('admin.accounts.openai.excelBPS403Badge') }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountListItem } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{
  account: Pick<AccountListItem, 'platform' | 'type' | 'extra' | 'group_ids'>
  // The account's current groups, used to name the destination group.
  groups?: { id: number; name: string }[]
}>()

const { t } = useI18n()

function parseTime(value: unknown): Date | null {
  if (typeof value !== 'string') return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

// The backend records each automatic action taken after a BPS 403. A shutdown
// counts until an admin turns the protocol back on; a group action counts while
// the account's groups still match it.
const actions = computed(() => {
  const { platform, type, extra, group_ids: groupIDs = [] } = props.account
  if (platform !== 'openai' || type !== 'oauth' || !extra) return []
  const lines: string[] = []
  const disabledAt = extra.openai_excel_bps === true ? null : parseTime(extra.openai_excel_bps_403_disabled_at)
  if (disabledAt) {
    lines.push(t('admin.accounts.openai.excelBPS403BadgeDisabled', { time: formatDateTime(disabledAt) }))
  }
  const movedAt = parseTime(extra.openai_excel_bps_403_moved_at)
  const target = extra.openai_excel_bps_403_moved_group_id
  if (movedAt && typeof target === 'number') {
    const time = formatDateTime(movedAt)
    if (target === 0 && groupIDs.length === 0) {
      lines.push(t('admin.accounts.openai.excelBPS403BadgeLeftGroups', { time }))
    } else if (target > 0 && groupIDs.length === 1 && groupIDs[0] === target) {
      const group = props.groups?.find(item => item.id === target)?.name ?? `#${target}`
      lines.push(t('admin.accounts.openai.excelBPS403BadgeMoved', { time, group }))
    }
  }
  return lines
})

const title = computed(() =>
  [t('admin.accounts.openai.excelBPS403BadgeNote'), ...actions.value.map(line => `· ${line}`)].join('\n')
)
</script>
