import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import type { QQAnnouncementResult, QQBotConfig } from '@/api/qqbot'
import QQBotAnnouncement from '../QQBotAnnouncement.vue'
import QQGroupPicker from '../QQGroupPicker.vue'

const api = vi.hoisted(() => ({ sendAnnouncement: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
const wrappers: VueWrapper[] = []
const config = { enabled: true, app_id: 'saved-app', sandbox: false, allowed_groups: ['first', 'blocked', 'second'], group_policies: [{ group_id: 'blocked', label: 'Blocked group', disable_notifications: true }] } as QQBotConfig
const result: QQAnnouncementResult = { batch_id: 'batch', jobs: [{ id: 'job-first', group_id: 'first' }], duplicate: false }
function mountAnnouncement(locale = 'zh') {
  const wrapper = mount(QQBotAnnouncement, { props: { config }, global: { plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })] } })
  wrappers.push(wrapper)
  return wrapper
}
async function ready(wrapper: VueWrapper) {
  wrapper.getComponent(QQGroupPicker).vm.$emit('update:modelValue', ['first'])
  await wrapper.get('[data-testid="qq-announcement-text"]').setValue('公告内容 🌟')
}
describe('QQ manual announcements', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    api.sendAnnouncement.mockResolvedValue(result)
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks() })

  it.each(['zh', 'en'])('requires explicit eligible recipients and confirms mention choice in %s', async locale => {
    const wrapper = mountAnnouncement(locale)
    expect(wrapper.getComponent(QQGroupPicker).props('groups')).toEqual(['first', 'second'])
    expect(wrapper.getComponent(QQGroupPicker).props('allowAll')).toBe(false)
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="qq-announcement-text"]').setValue('  公告内容 🌟  ')
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
    wrapper.getComponent(QQGroupPicker).vm.$emit('update:modelValue', ['blocked'])
    await flushPromises()
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
    wrapper.getComponent(QQGroupPicker).vm.$emit('update:modelValue', ['first'])
    await wrapper.get('[data-testid="qq-announcement-mention-all"]').setValue(true)
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click'); await flushPromises()
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('1'))
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining(locale === 'zh' ? '@所有人' : 'mention everyone'))
    expect(api.sendAnnouncement).toHaveBeenCalledWith({ app_id: 'saved-app', sandbox: false, groups: ['first'], text: '公告内容 🌟', mention_all: true, request_id: expect.stringMatching(/^[a-f0-9-]{36}$/) })
    expect(wrapper.emitted('queued')).toHaveLength(1)
    expect(wrapper.get('[role="status"]').text()).toContain(locale === 'zh' ? '加入公告发送队列' : 'queued')
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
  })

  it('retains the draft and reuses the same request after an ambiguous failure', async () => {
    api.sendAnnouncement.mockRejectedValueOnce(new Error('Network timeout'))
    const wrapper = mountAnnouncement(); await ready(wrapper)
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click'); await flushPromises()
    const first = api.sendAnnouncement.mock.calls[0]![0]
    expect(first.mention_all).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('Network timeout')
    expect(wrapper.get<HTMLTextAreaElement>('[data-testid="qq-announcement-text"]').element.value).toBe('公告内容 🌟')
    api.sendAnnouncement.mockResolvedValueOnce({ ...result, duplicate: true })
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click'); await flushPromises()
    expect(api.sendAnnouncement.mock.calls[1]![0]).toEqual(first)
    expect(wrapper.get('[role="status"]').text()).toContain('未重复提交')
  })

  it('changes the request ID after editing the announcement and prevents overlapping submissions', async () => {
    let resolve!: (value: QQAnnouncementResult) => void
    api.sendAnnouncement.mockReturnValueOnce(new Promise<QQAnnouncementResult>(done => { resolve = done }))
    const wrapper = mountAnnouncement(); await ready(wrapper)
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click')
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click')
    expect(api.sendAnnouncement).toHaveBeenCalledOnce()
    resolve(result); await flushPromises()
    const first = api.sendAnnouncement.mock.calls[0]![0]
    await wrapper.get('[data-testid="qq-announcement-text"]').setValue('更新后的公告')
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click'); await flushPromises()
    expect(api.sendAnnouncement).toHaveBeenCalledTimes(2)
    expect(api.sendAnnouncement.mock.calls[1]![0].request_id).not.toBe(first.request_id)
  })

  it('counts Unicode code points and blocks oversized, disabled and cancelled submissions', async () => {
    const wrapper = mountAnnouncement(); await ready(wrapper)
    await wrapper.get('[data-testid="qq-announcement-text"]').setValue('🌟'.repeat(1600))
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="qq-announcement-text"]').setValue('🌟'.repeat(1601))
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="qq-announcement-text"]').setValue('公告')
    vi.mocked(window.confirm).mockReturnValue(false)
    await wrapper.get('[data-testid="qq-announcement-send"]').trigger('click')
    expect(api.sendAnnouncement).not.toHaveBeenCalled()
    await wrapper.setProps({ config: { ...config, enabled: false } })
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
  })

  it('removes newly prohibited groups and clears recipients when the saved bot changes', async () => {
    const wrapper = mountAnnouncement(); await ready(wrapper)
    await wrapper.setProps({ config: { ...config, allowed_groups: ['second'] } })
    expect(wrapper.getComponent(QQGroupPicker).props('modelValue')).toEqual([])
    wrapper.getComponent(QQGroupPicker).vm.$emit('update:modelValue', ['second']); await flushPromises()
    await wrapper.setProps({ config: { ...config, app_id: 'new-app', sandbox: true } })
    expect(wrapper.getComponent(QQGroupPicker).props('modelValue')).toEqual([])
    expect(wrapper.get('[data-testid="qq-announcement-send"]').attributes('disabled')).toBeDefined()
  })
})
