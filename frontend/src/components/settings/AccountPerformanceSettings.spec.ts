import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountPerformanceSettings from './AccountPerformanceSettings.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

enableAutoUnmount(afterEach)
const api = vi.hoisted(() => ({ getAccountPerformanceSettings: vi.fn(), updateAccountPerformanceSettings: vi.fn() }))
vi.mock('@/api', () => ({ adminAPI: { settings: api } }))

beforeEach(() => {
  vi.resetAllMocks()
  api.getAccountPerformanceSettings.mockResolvedValue({ window_minutes: 60 })
  api.updateAccountPerformanceSettings.mockImplementation(async settings => settings)
})

async function open(locale = 'en') {
  const wrapper = mount(AccountPerformanceSettings, { global: { plugins: [createI18n({ legacy: false, locale, messages: { en, zh } })] } })
  await flushPromises()
  return wrapper
}

describe('Account performance global window', () => {
  it.each(['en', 'zh'])('loads and saves whole minutes in %s', async locale => {
    const w = await open(locale)
    expect(w.get<HTMLInputElement>('input').element.value).toBe('60')
    expect(api.updateAccountPerformanceSettings).not.toHaveBeenCalled()
    await w.get('input').setValue('120')
    await w.get('[data-testid="performance-window-save"]').trigger('click')
    await flushPromises()
    expect(api.updateAccountPerformanceSettings).toHaveBeenCalledWith({ window_minutes: 120 })
    expect(w.get('[role="status"]').text()).toBe(locale === 'zh' ? '监控周期已保存。' : 'Monitoring window saved.')
  })

  it('loads a previously saved nondefault window', async () => {
    api.getAccountPerformanceSettings.mockResolvedValue({ window_minutes: 15 })
    const w = await open()
    expect(w.get<HTMLInputElement>('input').element.value).toBe('15')
  })

  it.each(['', '0', '-1', '1441', '1.5'])('rejects invalid minutes %s before saving', async value => {
    const w = await open()
    await w.get('input').setValue(value)
    await w.get('[data-testid="performance-window-save"]').trigger('click')
    expect(api.updateAccountPerformanceSettings).not.toHaveBeenCalled()
    expect(w.get('[role="alert"]').text()).toContain('whole number')
  })

  it('requires a successful load before editing and allows retry', async () => {
    api.getAccountPerformanceSettings.mockRejectedValueOnce(new Error('unavailable'))
    const w = await open()
    expect(w.get('[role="alert"]').text()).toContain('Reload before editing')
    expect(w.find('input').exists()).toBe(false)
    expect(w.find('[data-testid="performance-window-save"]').exists()).toBe(false)
    api.getAccountPerformanceSettings.mockResolvedValue({ window_minutes: 90 })
    await w.get('button').trigger('click')
    await flushPromises()
    expect(w.get<HTMLInputElement>('input').element.value).toBe('90')
  })

  it('retains edits on a save failure without claiming success', async () => {
    api.updateAccountPerformanceSettings.mockRejectedValue(new Error('write failed'))
    const w = await open()
    await w.get('input').setValue('30')
    await w.get('[data-testid="performance-window-save"]').trigger('click')
    await flushPromises()
    expect(w.get('[role="alert"]').text()).toContain('Unable to save')
    expect(w.get<HTMLInputElement>('input').element.value).toBe('30')
    expect(w.find('[role="status"]').exists()).toBe(false)
  })
})
