import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupAntiDistillationSettings from '../GroupAntiDistillationSettings.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  save: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { groups: { getAntiDistillation: mocks.get, saveAntiDistillation: mocks.save } },
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

function mountSettings(groupId = 41, pinia = createPinia()) {
  return mount(GroupAntiDistillationSettings, {
    props: { groupId, userId: 7 },
    global: { plugins: [pinia] },
  })
}

describe('GroupAntiDistillationSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    mocks.get.mockResolvedValue({ enabled: false, pause_minutes: 5 })
    mocks.save.mockImplementation(async (_id, config) => config)
  })

  it('blocks edits before configuration loads and saves independently from the parent form', async () => {
    const pending = deferred<{ enabled: boolean; pause_minutes: number }>()
    mocks.get.mockReturnValue(pending.promise)
    const onSubmit = vi.fn()
    const host = defineComponent({
      components: { GroupAntiDistillationSettings },
      setup: () => ({ onSubmit }),
      template: '<form @submit.prevent="onSubmit"><GroupAntiDistillationSettings :group-id="41" :user-id="7" /></form>',
    })
    const wrapper = mount(host, { global: { plugins: [createPinia()] } })
    const save = wrapper.get<HTMLButtonElement>('[data-testid="anti-distillation-save"]')
    expect(save.element.disabled).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-testid="anti-distillation-toggle"]').element.disabled).toBe(true)
    expect(mocks.save).not.toHaveBeenCalled()

    pending.resolve({ enabled: false, pause_minutes: 5 })
    await flushPromises()
    await wrapper.get('[data-testid="anti-distillation-toggle"]').trigger('click')
    expect(wrapper.get<HTMLInputElement>('[data-testid="anti-distillation-minutes"]').element.value).toBe('5')
    await wrapper.get('[data-testid="anti-distillation-minutes"]').setValue('10')
    await save.trigger('click')
    await flushPromises()
    expect(mocks.save).toHaveBeenCalledWith(41, { enabled: true, pause_minutes: 10 })
    expect(onSubmit).not.toHaveBeenCalled()
    expect(mocks.showSuccess).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('requires a successful reload after a load failure before saving', async () => {
    mocks.get.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountSettings()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('loadFailed')
    expect(wrapper.get<HTMLButtonElement>('[data-testid="anti-distillation-save"]').element.disabled).toBe(true)
    expect(mocks.save).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="anti-distillation-retry"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get<HTMLButtonElement>('[data-testid="anti-distillation-save"]').element.disabled).toBe(false)
    wrapper.unmount()
  })

  it('rejects invalid durations and permits disabling even after an invalid edit', async () => {
    mocks.get.mockResolvedValue({ enabled: true, pause_minutes: 5 })
    const wrapper = mountSettings()
    await flushPromises()
    const duration = wrapper.get('[data-testid="anti-distillation-minutes"]')
    const save = wrapper.get<HTMLButtonElement>('[data-testid="anti-distillation-save"]')
    for (const value of ['', '0', '1441', '1.5', '-1', 'not-a-number']) {
      await duration.setValue(value)
      expect(save.element.disabled).toBe(true)
    }
    for (const value of ['1', '1440']) {
      await duration.setValue(value)
      expect(save.element.disabled).toBe(false)
    }
    await duration.setValue('')
    await wrapper.get('[data-testid="anti-distillation-toggle"]').trigger('click')
    await save.trigger('click')
    await flushPromises()
    expect(mocks.save).toHaveBeenCalledWith(41, { enabled: false, pause_minutes: 5 })
    wrapper.unmount()
  })

  it('preserves edits after a failed save and prevents duplicate saves in flight', async () => {
    mocks.save.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="anti-distillation-toggle"]').trigger('click')
    await wrapper.get('[data-testid="anti-distillation-minutes"]').setValue('12')
    await wrapper.get('[data-testid="anti-distillation-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('saveFailed')
    expect(wrapper.get<HTMLInputElement>('[data-testid="anti-distillation-minutes"]').element.value).toBe('12')
    const pending = deferred<{ enabled: boolean; pause_minutes: number }>()
    mocks.save.mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-testid="anti-distillation-save"]').trigger('click')
    await wrapper.get('[data-testid="anti-distillation-save"]').trigger('click')
    expect(mocks.save).toHaveBeenCalledTimes(2)
    pending.resolve({ enabled: true, pause_minutes: 12 })
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('ignores a stale response when switching groups and does not fetch an inactive editor', async () => {
    const pending = deferred<{ enabled: boolean; pause_minutes: number }>()
    mocks.get.mockReturnValueOnce(pending.promise)
    const wrapper = mountSettings(41)
    await wrapper.setProps({ groupId: 42 })
    await flushPromises()
    pending.resolve({ enabled: true, pause_minutes: 100 })
    await flushPromises()
    expect(wrapper.find('[data-testid="anti-distillation-minutes"]').exists()).toBe(false)
    await wrapper.get('[data-testid="anti-distillation-save"]').trigger('click')
    await flushPromises()
    expect(mocks.save).toHaveBeenCalledWith(42, { enabled: false, pause_minutes: 5 })
    mocks.get.mockClear()
    await wrapper.setProps({ active: false, groupId: 43 })
    expect(mocks.get).not.toHaveBeenCalled()
    expect(wrapper.get<HTMLButtonElement>('[data-testid="anti-distillation-save"]').element.disabled).toBe(true)
    wrapper.unmount()
  })

  it('reloads saved settings on reopening and ignores completion of a save from the closed editor', async () => {
    const wrapper = mountSettings(41)
    await flushPromises()
    await wrapper.get('[data-testid="anti-distillation-toggle"]').trigger('click')
    const pendingSave = deferred<{ enabled: boolean; pause_minutes: number }>()
    mocks.save.mockReturnValueOnce(pendingSave.promise)
    await wrapper.get('[data-testid="anti-distillation-save"]').trigger('click')
    await wrapper.setProps({ active: false })
    mocks.get.mockResolvedValueOnce({ enabled: true, pause_minutes: 15 })
    await wrapper.setProps({ active: true })
    await flushPromises()
    pendingSave.resolve({ enabled: true, pause_minutes: 5 })
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="anti-distillation-minutes"]').element.value).toBe('15')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(mocks.get).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('shares the announcement across entries and preserves A → B → C dismissal boundaries', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const notices = useNewFeatureNoticeStore()
    const oldId = NEW_FEATURE_NOTICES.protocolFeatureSwitches
    const newId = NEW_FEATURE_NOTICES.groupAntiDistillation
    notices.dismiss(7, oldId)
    const first = mountSettings(41, pinia)
    const second = mountSettings(42, pinia)
    await flushPromises()
    expect(first.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    expect(second.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    await first.get('[data-testid="new-feature-badge"]').trigger('click')
    expect(second.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(notices.isDismissed(7, oldId)).toBe(true)
    expect(notices.isDismissed(7, newId)).toBe(true)
    first.unmount()
    second.unmount()

    const reloadedPinia = createPinia()
    const afterReload = mountSettings(41, reloadedPinia)
    await flushPromises()
    expect(afterReload.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    const future = mount(NewFeatureBadge, {
      props: { noticeId: 'future-feature-C', userId: 7 },
      global: { plugins: [reloadedPinia] },
    })
    expect(future.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    expect(useNewFeatureNoticeStore(reloadedPinia).isDismissed(7, oldId)).toBe(true)
    afterReload.unmount()
    future.unmount()
  })
})
