import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ExcelBPS403Badge from '../ExcelBPS403Badge.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}(${Object.entries(params).map(([name, value]) => `${name}=${value}`).join(',')})` : key
    })
  }
})

vi.mock('@/utils/format', async () => {
  const actual = await vi.importActual<typeof import('@/utils/format')>('@/utils/format')
  return {
    ...actual,
    formatDateTime: (date: Date) => date.toISOString()
  }
})

const disabledAt = '2026-09-26T15:04:05Z'
const movedAt = '2026-09-27T01:02:03Z'
const note = 'admin.accounts.openai.excelBPS403BadgeNote'
const disabledLine = '· admin.accounts.openai.excelBPS403BadgeDisabled(time=2026-09-26T15:04:05.000Z)'
const badge = '[data-test="excel-bps-403-badge"]'

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'openai',
    type: 'oauth',
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides
  }
}

function movedAccount(groupID: number, groupIDs: number[] | undefined, extra: Record<string, unknown> = {}): Account {
  return makeAccount({
    group_ids: groupIDs,
    extra: {
      openai_excel_bps: true,
      openai_excel_bps_403_moved_at: movedAt,
      openai_excel_bps_403_moved_group_id: groupID,
      ...extra
    }
  })
}

describe('ExcelBPS403Badge', () => {
  it('BPS 403 自动关闭协议后显示标签和触发时间', () => {
    const wrapper = mount(ExcelBPS403Badge, {
      props: {
        account: makeAccount({
          extra: { openai_excel_bps: false, openai_excel_bps_403_disabled_at: disabledAt }
        })
      }
    })

    const tag = wrapper.get(badge)
    expect(tag.text()).toBe('admin.accounts.openai.excelBPS403Badge')
    expect(tag.attributes('title')).toBe(`${note}\n${disabledLine}`)
    expect(tag.classes()).toContain('bg-amber-400')
  })

  it('协议开关键被删除后仍显示标签', () => {
    const wrapper = mount(ExcelBPS403Badge, {
      props: { account: makeAccount({ extra: { openai_excel_bps_403_disabled_at: disabledAt } }) }
    })

    expect(wrapper.find(badge).exists()).toBe(true)
  })

  it('BPS 403 自动移入分组后显示标签和目标分组', () => {
    const wrapper = mount(ExcelBPS403Badge, {
      props: { account: movedAccount(7, [7]), groups: [{ id: 7, name: 'BPS 隔离' }] }
    })

    expect(wrapper.get(badge).attributes('title')).toBe(
      `${note}\n· admin.accounts.openai.excelBPS403BadgeMoved(time=2026-09-27T01:02:03.000Z,group=BPS 隔离)`
    )
  })

  it('分组名称未加载时显示分组 ID', () => {
    const wrapper = mount(ExcelBPS403Badge, { props: { account: movedAccount(7, [7]) } })

    expect(wrapper.get(badge).attributes('title')).toContain('group=#7')
  })

  it('BPS 403 自动退出所有分组后显示标签', () => {
    const wrapper = mount(ExcelBPS403Badge, { props: { account: movedAccount(0, undefined) } })

    expect(wrapper.get(badge).attributes('title')).toBe(
      `${note}\n· admin.accounts.openai.excelBPS403BadgeLeftGroups(time=2026-09-27T01:02:03.000Z)`
    )
  })

  it('同时关闭协议和调整分组时只显示一个标签并列出两项', () => {
    const wrapper = mount(ExcelBPS403Badge, {
      props: {
        account: movedAccount(0, [], { openai_excel_bps: false, openai_excel_bps_403_disabled_at: disabledAt })
      }
    })

    expect(wrapper.findAll(badge)).toHaveLength(1)
    expect(wrapper.get(badge).attributes('title')).toBe(
      `${note}\n${disabledLine}\n· admin.accounts.openai.excelBPS403BadgeLeftGroups(time=2026-09-27T01:02:03.000Z)`
    )
  })

  it('重新开启协议但仍在目标分组时保留分组调整标记', () => {
    const wrapper = mount(ExcelBPS403Badge, {
      props: { account: movedAccount(7, [7], { openai_excel_bps_403_disabled_at: disabledAt }) }
    })

    const title = wrapper.get(badge).attributes('title')
    expect(title).toContain('excelBPS403BadgeMoved')
    expect(title).not.toContain('excelBPS403BadgeDisabled')
  })

  it.each([
    ['重新开启协议', makeAccount({ extra: { openai_excel_bps: true, openai_excel_bps_403_disabled_at: disabledAt } })],
    ['没有自动处理记录', makeAccount({ extra: { openai_excel_bps: false } })],
    ['关闭记录时间无效', makeAccount({ extra: { openai_excel_bps_403_disabled_at: 'not-a-time' } })],
    ['关闭记录不是字符串', makeAccount({ extra: { openai_excel_bps_403_disabled_at: 1 } })],
    ['没有 extra', makeAccount({ extra: undefined })],
    ['非 OAuth 账号', makeAccount({ type: 'apikey', extra: { openai_excel_bps_403_disabled_at: disabledAt } })],
    ['非 OpenAI 账号', makeAccount({ platform: 'anthropic', extra: { openai_excel_bps_403_disabled_at: disabledAt } })],
    ['已调整回其他分组', movedAccount(7, [3])],
    ['目标分组外又加入其他分组', movedAccount(7, [7, 3])],
    ['退出所有分组后又加入分组', movedAccount(0, [3])],
    ['分组调整时间无效', movedAccount(7, [7], { openai_excel_bps_403_moved_at: 'not-a-time' })],
    ['分组调整缺少目标', movedAccount(7, [7], { openai_excel_bps_403_moved_group_id: undefined })]
  ])('%s时不显示标签', (_, account) => {
    const wrapper = mount(ExcelBPS403Badge, { props: { account } })

    expect(wrapper.find(badge).exists()).toBe(false)
  })
})
