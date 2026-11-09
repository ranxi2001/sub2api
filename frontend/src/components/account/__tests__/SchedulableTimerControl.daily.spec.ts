import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import Control from '../SchedulableTimerControl.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import type { SchedulableTimer, TimerSnapshot } from '@/api/admin/schedulableTimers'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

const { api, toast } = vi.hoisted(() => ({
  api: { list: vi.fn(), preview: vi.fn(), save: vi.fn(), saveDailyWindow: vi.fn(), cancel: vi.fn() },
  toast: { showSuccess: vi.fn(), showError: vi.fn() }
}))
vi.mock('@/api/admin/schedulableTimers', () => ({ schedulableTimersAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))

const dailyOpen: SchedulableTimer = {
  account_id: 7, action: 'open', version: 3, execute_at: '2026-11-07T00:00:00Z',
  timezone: 'Asia/Shanghai', local_date: '2026-11-07', local_time: '08:00', offset: '+08:00',
  requested_by: 9, status: 'pending', attempts: 0, actual_at: null, reason: '',
  recurrence: 'daily', daily_time: '08:00'
}
const dailyClose: SchedulableTimer = {
  ...dailyOpen, action: 'close', version: 5, execute_at: '2026-11-06T17:00:00Z',
  local_time: '01:00', daily_time: '01:00'
}
const snapshot = (tasks: SchedulableTimer[] = []): TimerSnapshot => ({
  tasks, history: [], server_now: '2026-11-06T04:00:00Z',
  poll_seconds: 30, open_grace_seconds: 900, max_delay_minutes: 527040
})
const preview = {
  execute_at: dailyOpen.execute_at, timezone: 'Asia/Shanghai',
  local_date: '2026-11-07', local_time: '08:00', offset: '+08:00',
  server_now: '2026-11-06T04:00:00Z'
}
let wrappers: VueWrapper[] = []

async function render(locale = 'en', noticeUserId?: number, pinia = createPinia()) {
  const wrapper = mount(Control, {
    props: { accountId: 7, refreshKey: 0, noticeUserId, schedulable: true },
    global: { plugins: [pinia, createI18n({ legacy: false, locale, messages: { en, zh } })], stubs: { BaseDialog: { props: ['show'], template: '<section v-if="show" role="dialog"><slot /></section>' } } }
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  localStorage.clear()
  vi.resetAllMocks()
  api.list.mockResolvedValue(snapshot())
  api.preview.mockResolvedValue(preview)
  api.save.mockResolvedValue(dailyOpen)
  api.saveDailyWindow.mockResolvedValue(snapshot([dailyOpen, dailyClose]))
  api.cancel.mockResolvedValue(undefined)
})
afterEach(() => {
  wrappers.forEach(wrapper => wrapper.unmount())
  wrappers = []
})

describe('daily schedulable timers', () => {
  it.each([
    ['zh', '一次性', '打开调度', '当前待执行任务', '剩余'],
    ['en', 'One-shot', 'Enable scheduling', 'Pending tasks', 'in ']
  ])('shows a one-shot task in %s before opening the task dialog', async (locale, once, action, status, remaining) => {
    api.list.mockResolvedValue(snapshot([{ ...dailyOpen, recurrence: 'once', daily_time: '' }]))
    const wrapper = await render(locale)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="timer-pending-summary"]').text()).toContain(status)
    const summary = wrapper.get('[data-test="timer-summary-open"]').text()
    for (const text of [once, action, '2026-11-07 08:00', 'Asia/Shanghai', 'UTC+08:00', remaining]) {
      expect(summary).toContain(text)
    }
    expect(wrapper.find('[data-test="timer-edit-open"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="timer-cancel-open"]').exists()).toBe(false)
    expect(api.save).not.toHaveBeenCalled()
  })

  it.each([dailyOpen, dailyClose])('shows a single daily $action task without requiring a paired window', async task => {
    api.list.mockResolvedValue(snapshot([task]))
    const wrapper = await render('zh')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="timer-daily-window"]').exists()).toBe(false)
    const summary = wrapper.get(`[data-test="timer-summary-${task.action}"]`).text()
    expect(summary).toContain(`每天 ${task.daily_time}`)
    expect(summary).toContain(`${task.local_date} ${task.local_time}`)
    expect(summary).toContain('Asia/Shanghai')
  })

  it('removes completed tasks from the row summary on refresh and hides cancelled tasks', async () => {
    api.list.mockResolvedValue(snapshot([dailyOpen, { ...dailyClose, status: 'cancelled' }]))
    const wrapper = await render()
    expect(wrapper.find('[data-test="timer-summary-open"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="timer-summary-close"]').exists()).toBe(false)
    api.list.mockResolvedValue(snapshot([{ ...dailyOpen, status: 'executed', actual_at: dailyOpen.execute_at }]))
    await wrapper.setProps({ refreshKey: 1 })
    await flushPromises()
    expect(wrapper.find('[data-test="timer-pending-summary"]').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('previews and saves a repeating wall clock without a one-shot date or duration', async () => {
    const wrapper = await render()
    await wrapper.get('[data-test="timer-manage"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="timer-open-time"]').setValue('08:00')
    expect(wrapper.find('input[type="date"]').exists()).toBe(false)
    expect(wrapper.find('input[type="number"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="timer-mode-hint"]').text()).toContain('08:00–01:00 next day')
    expect(wrapper.text()).toContain('without immediately changing the current switch')
    await wrapper.get('[data-test="timer-preview"]').trigger('click')
    await flushPromises()
    expect(api.preview).toHaveBeenCalledWith(7, { mode: 'daily', time: '08:00', timezone: 'Asia/Shanghai' })
    expect(api.preview).toHaveBeenCalledWith(7, { mode: 'daily', time: '01:00', timezone: 'Asia/Shanghai' })
    expect(wrapper.get('[data-test="timer-preview-open"]').text()).toContain('2026-11-07 08:00')
    expect(wrapper.find('[data-test="timer-confirm"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="timer-save"]').text()).toBe('Save daily plan')
    await wrapper.get('[data-test="timer-save"]').trigger('click')
    await flushPromises()
    expect(api.saveDailyWindow).toHaveBeenCalledWith(7, {
      open_time: '08:00', close_time: '01:00', timezone: 'Asia/Shanghai',
      expected_open_version: 0, expected_close_version: 0
    })
    expect(api.save).not.toHaveBeenCalled()
  })

  it.each([
    ['zh', '01:00', '每天允许调度：08:00–次日 01:00'],
    ['en', '01:00', 'Daily scheduling window: 08:00–01:00 next day'],
    ['zh', '23:00', '每天允许调度：08:00–23:00'],
    ['en', '23:00', 'Daily scheduling window: 08:00–23:00']
  ])('explains the daily interval in %s when closing at %s', async (locale, close, expected) => {
    api.list.mockResolvedValue(snapshot([dailyOpen, { ...dailyClose, daily_time: close }]))
    const wrapper = await render(locale)
    expect(wrapper.get('[data-test="timer-daily-window"]').text()).toContain(expected)
    expect(wrapper.get('[data-test="timer-daily-window"]').text()).toContain('Asia/Shanghai')
    await wrapper.get('[data-test="timer-manage"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="timer-daily-open"]').text()).toContain('08:00')
    expect(wrapper.get('[data-test="timer-daily-close"]').text()).toContain(close)
  })

  it('restores a saved daily close rule for editing and cancels its latest version', async () => {
    api.list.mockResolvedValue(snapshot([dailyOpen, dailyClose]))
    const wrapper = await render('zh')
    await wrapper.get('[data-test="timer-manage"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="timer-plan-type"]').element).toHaveProperty('value', 'daily')
    expect(wrapper.get('[data-test="timer-close-time"]').element).toHaveProperty('value', '01:00')
    expect(wrapper.text()).toContain('包括批量操作')
    expect(wrapper.text()).toContain('全部待执行的一次性与每天循环计划')
    await wrapper.get('[data-test="timer-close-time"]').setValue('01:30')
    await wrapper.get('[data-test="timer-preview"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="timer-save"]').trigger('click')
    await flushPromises()
    expect(api.saveDailyWindow).toHaveBeenCalledWith(7, {
      open_time: '08:00', close_time: '01:30', timezone: 'Asia/Shanghai',
      expected_open_version: 3, expected_close_version: 5
    })
    await wrapper.get('[data-test="timer-manage"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="timer-cancel-close"]').trigger('click')
    await flushPromises()
    expect(api.cancel).toHaveBeenCalledWith(7, 'close', 5)
  })

  it('refreshes the account switch when a daily occurrence advances while remaining pending', async () => {
    api.list.mockResolvedValueOnce(snapshot([dailyOpen]))
    const wrapper = await render()
    api.list.mockResolvedValue(snapshot([{
      ...dailyOpen, version: 4, execute_at: '2026-11-08T00:00:00Z',
      local_date: '2026-11-08', actual_at: dailyOpen.execute_at
    }]))
    await wrapper.setProps({ refreshKey: 1 })
    await flushPromises()
    expect(wrapper.emitted('executed')).toHaveLength(1)
    await wrapper.setProps({ refreshKey: 2 })
    await flushPromises()
    expect(wrapper.emitted('executed')).toHaveLength(1)
  })

  it('treats older task responses with no recurrence fields as one-shot plans', async () => {
    const { recurrence: _recurrence, daily_time: _clock, ...legacyTask } = dailyOpen
    api.list.mockResolvedValue(snapshot([legacyTask]))
    const wrapper = await render()
    expect(wrapper.find('[data-test="timer-daily-window"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="timer-daily-open"]').exists()).toBe(false)
    await wrapper.get('[data-test="timer-manage"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="timer-mode"]').element).toHaveProperty('value', 'absolute')
    expect(wrapper.find('input[type="date"]').exists()).toBe(true)
  })

  it('keeps old notices dismissed, shares the daily notice across entries and persists dismissal for the same admin', async () => {
    const A = NEW_FEATURE_NOTICES.protocolFeatureSwitches
    const B = NEW_FEATURE_NOTICES.accountDailyScheduling
    const C = 'future-distinct-scheduling-feature-c'
    expect(A).not.toBe(B)
    expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
    const pinia = createPinia()
    const notices = useNewFeatureNoticeStore(pinia)
    notices.dismiss(41, A)
    const first = await render('en', 41, pinia)
    const second = await render('en', 41, pinia)
    expect(first.findAll('[data-testid="new-feature-badge"]')).toHaveLength(1)
    await first.get('[data-test="timer-manage"]').trigger('click')
    await flushPromises()
    expect(first.findAll('[data-testid="new-feature-badge"]')).toHaveLength(2)
    await first.get('[data-testid="new-feature-badge"]').trigger('click')
    expect(first.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    expect(second.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    expect(notices.isDismissed(41, A)).toBe(true)
    first.unmount()
    second.unmount()
    wrappers = []
    const reloadedPinia = createPinia()
    const reopened = await render('en', 41, reloadedPinia)
    expect(reopened.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    await reopened.setProps({ noticeUserId: 42 })
    expect(reopened.findAll('[data-testid="new-feature-badge"]')).toHaveLength(1)
    await reopened.setProps({ noticeUserId: 41 })
    expect(reopened.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    const future = mount(NewFeatureBadge, {
      props: { noticeId: C, userId: 41 },
      global: { plugins: [reloadedPinia, createI18n({ legacy: false, locale: 'en', messages: { en } })] }
    })
    wrappers.push(future)
    expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    const reloaded = useNewFeatureNoticeStore(reloadedPinia)
    expect(reloaded.isDismissed(41, A)).toBe(true)
    expect(reloaded.isDismissed(41, B)).toBe(true)
  })
})
