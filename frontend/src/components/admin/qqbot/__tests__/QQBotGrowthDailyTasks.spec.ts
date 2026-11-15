import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import type { QQCheckinConfig, QQGrowthTaskKind } from '@/api/qqbot'
import QQBotGrowthSettings from '../QQBotGrowthSettings.vue'

const api = vi.hoisted(() => ({ checkinConfig: vi.fn(), saveCheckin: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
const wrappers: VueWrapper[] = []
const dailyKinds: QQGrowthTaskKind[] = ['usage_amount_days', 'usage_amount_streak', 'recharge_days', 'recharge_streak']
let saved: QQCheckinConfig
async function mountSettings(locale = 'zh') {
  const wrapper = mount(QQBotGrowthSettings, { global: { plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })] } })
  wrappers.push(wrapper); await flushPromises(); return wrapper
}
describe('growth tasks for days meeting a daily amount', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    saved = { enabled: false, min_amount: 0.1, max_amount: 0.5, min_recharge_total: 0, min_recharge_30d: 0, milestones: [], growth: { binding_chances: 0, first_usage_chances: 0, checkin_30_chances: 0, tasks: [], periodic_tasks: [] } } as unknown as QQCheckinConfig
    api.checkinConfig.mockImplementation(async () => structuredClone(saved))
    api.saveCheckin.mockImplementation(async config => { saved = structuredClone(config); return structuredClone(saved) })
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks() })

  it.each(['zh', 'en'])('configures all four conditions independently for one-time and repeating rewards in %s', async locale => {
    const wrapper = await mountSettings(locale)
    for (const [index, kind] of dailyKinds.entries()) {
      await wrapper.get('[data-testid="task-preset"]').setValue(kind)
      await wrapper.get('[data-testid="add-growth-task"]').trigger('click')
      await wrapper.get('[data-testid="periodic-preset"]').setValue(kind)
      await wrapper.get('[data-testid="add-periodic-task"]').trigger('click')
      expect(wrapper.get<HTMLInputElement>(`[data-testid="task-target-${index}"]`).element.value).toBe('7')
      expect(wrapper.get<HTMLInputElement>(`[data-testid="periodic-target-${index}"]`).element.value).toBe('7')
      expect(wrapper.get<HTMLInputElement>(`[data-testid="task-daily-amount-${index}"]`).element.value).toBe('10')
      expect(wrapper.get<HTMLInputElement>(`[data-testid="periodic-daily-amount-${index}"]`).element.value).toBe('10')
      expect(wrapper.get(`[data-testid="task-period-${index}"]`).findAll('option').map(option => option.attributes('value'))).toEqual(['once'])
      await wrapper.get(`[data-testid="task-target-${index}"]`).setValue(index + 2)
      await wrapper.get(`[data-testid="task-daily-amount-${index}"]`).setValue('12.34')
      await wrapper.get(`[data-testid="periodic-target-${index}"]`).setValue(index + 3)
      await wrapper.get(`[data-testid="periodic-daily-amount-${index}"]`).setValue('25.50')
      await wrapper.get(`[data-testid="periodic-chances-${index}"]`).setValue(2)
    }
    await wrapper.get('[data-testid="one-time-growth-form"]').trigger('submit'); await flushPromises()
    expect(saved.growth.tasks).toHaveLength(4)
    for (const [index, kind] of dailyKinds.entries()) expect(saved.growth.tasks[index]).toMatchObject({ kind, target: index + 2, daily_amount: 12.34, period: 'once', enabled: false, reward: { type: 'chances', quantity: 1 } })
    await wrapper.get('[data-testid="periodic-growth-form"]').trigger('submit'); await flushPromises()
    expect(saved.growth.periodic_tasks).toHaveLength(4)
    for (const [index, kind] of dailyKinds.entries()) expect(saved.growth.periodic_tasks[index]).toMatchObject({ kind, target: index + 3, daily_amount: 25.5, period: 'once', reward: { type: 'chances', quantity: 2 } })
    const translation = createI18n({ legacy: false, locale, messages: { zh, en } })
    expect(wrapper.text()).toContain(translation.global.t('qqbot.growth.dailyAmountHelp'))
    expect(wrapper.text()).toContain(translation.global.t('qqbot.growth.periodicDailyAmountHelp'))
    expect(wrapper.text()).not.toContain('qqbot.')
    const reopened = await mountSettings(locale)
    expect(reopened.get<HTMLInputElement>('[data-testid="task-daily-amount-3"]').element.value).toBe('12.34')
    expect(reopened.get<HTMLInputElement>('[data-testid="periodic-target-3"]').element.value).toBe('6')
  })

  it('excludes amount-day tasks from daily and weekly calendars and clears thresholds on kind changes', async () => {
    const wrapper = await mountSettings()
    await wrapper.get('[data-testid="add-growth-task"]').trigger('click')
    for (const period of ['daily', 'weekly']) {
      await wrapper.get('[data-testid="task-period-0"]').setValue(period)
      const options = wrapper.get('[data-testid="task-kind-0"]').findAll('option').map(option => option.attributes('value'))
      expect(options.some(value => dailyKinds.includes(value as QQGrowthTaskKind))).toBe(false)
    }
    await wrapper.get('[data-testid="task-period-0"]').setValue('once')
    await wrapper.get('[data-testid="task-kind-0"]').setValue('usage_amount_streak')
    expect(wrapper.get('[data-testid="task-daily-amount-0"]').exists()).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-testid="task-target-0"]').element.value).toBe('7')
    await wrapper.get('[data-testid="task-kind-0"]').setValue('usage_amount')
    expect(wrapper.find('[data-testid="task-daily-amount-0"]').exists()).toBe(false)
    await wrapper.get('[data-testid="one-time-growth-form"]').trigger('submit'); await flushPromises()
    expect(saved.growth.tasks[0]).not.toHaveProperty('daily_amount')
  })

  it.each(['tasks', 'periodic_tasks'] as const)('validates daily amount and integer day boundaries in %s before saving', async section => {
    const wrapper = await mountSettings()
    const prefix = section === 'tasks' ? 'task' : 'periodic'
    const form = section === 'tasks' ? 'one-time-growth-form' : 'periodic-growth-form'
    await wrapper.get(`[data-testid="${prefix}-preset"]`).setValue('recharge_streak')
    await wrapper.get(`[data-testid="${section === 'tasks' ? 'add-growth-task' : 'add-periodic-task'}"]`).trigger('click')
    for (const value of ['', '0', '-1', '0.001', '10.001', '1000000001']) {
      await wrapper.get(`[data-testid="${prefix}-daily-amount-0"]`).setValue(value)
      await wrapper.get(`[data-testid="${form}"]`).trigger('submit'); await flushPromises()
      expect(api.saveCheckin).not.toHaveBeenCalled()
      expect(wrapper.get('[role="alert"]').exists()).toBe(true)
    }
    await wrapper.get(`[data-testid="${prefix}-daily-amount-0"]`).setValue('0.01')
    for (const value of ['0', '1.5', '3651']) {
      await wrapper.get(`[data-testid="${prefix}-target-0"]`).setValue(value)
      await wrapper.get(`[data-testid="${form}"]`).trigger('submit'); await flushPromises()
      expect(api.saveCheckin).not.toHaveBeenCalled()
    }
    await wrapper.get(`[data-testid="${prefix}-target-0"]`).setValue('3650')
    await wrapper.get(`[data-testid="${form}"]`).trigger('submit'); await flushPromises()
    expect(saved.growth[section][0]).toMatchObject({ kind: 'recharge_streak', target: 3650, daily_amount: 0.01 })
    await wrapper.get(`[data-testid="${prefix}-daily-amount-0"]`).setValue('1000000000')
    await wrapper.get(`[data-testid="${prefix}-target-0"]`).setValue('1')
    await wrapper.get(`[data-testid="${form}"]`).trigger('submit'); await flushPromises()
    expect(saved.growth[section][0]).toMatchObject({ target: 1, daily_amount: 1000000000 })
  })
})
