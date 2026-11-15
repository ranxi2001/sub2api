import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQMemberPicker from '../QQMemberPicker.vue'
import type { QQMemberOption, QQMemberPage } from '@/api/qqbot'

const api = vi.hoisted(() => ({ members: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
const member = (overrides: Partial<QQMemberOption> = {}): QQMemberOption => ({ group_id: 'g1', member_id: 'opaque-member-a', nickname: '小王', email: 'wang@example.com', message_preview: '我是小王，确认身份 4826', last_seen_at: '2026-10-24T01:00:00Z', group_label: '技术交流群', ...overrides })
const page = (items: QQMemberOption[], overrides: Partial<QQMemberPage> = {}): QQMemberPage => ({ app_id: 'app', items, total: items.length, page: 1, page_size: 20, ...overrides })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const wrappers: VueWrapper[] = []
function setup(props: Partial<InstanceType<typeof QQMemberPicker>['$props']> = {}, locale = 'zh') {
  const wrapper = mount(QQMemberPicker, { props: { modelValue: '', appId: 'app', groupId: 'g1', ...props }, global: { plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })] } })
  wrappers.push(wrapper)
  return wrapper
}
beforeEach(() => { vi.resetAllMocks(); api.members.mockResolvedValue(page([member()])) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.useRealTimers() })

describe('recognizable QQ member selection', () => {
  it.each(['zh', 'en'])('shows identity evidence and only emits an explicitly selected real identity in %s', async locale => {
    vi.useFakeTimers()
    const wrapper = setup({}, locale)
    await flushPromises()
    const select = wrapper.get('[data-testid="qq-member-select"]')
    expect(select.text()).toContain('小王')
    expect(select.text()).toContain('wang@example.com')
    expect(select.text()).toContain('4826')
    expect(select.text()).not.toContain('opaque-member-a')
    await wrapper.get('[data-testid="qq-member-search"]').setValue('123456789')
    await vi.advanceTimersByTimeAsync(250); await flushPromises()
    expect(api.members).toHaveBeenLastCalledWith(expect.objectContaining({ search: '123456789', group_id: 'g1', app_id: 'app' }))
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await select.setValue(JSON.stringify(['g1', 'opaque-member-a']))
    expect(wrapper.emitted('update:modelValue')).toEqual([['opaque-member-a']])
    expect(wrapper.emitted('select')).toEqual([[member()]])
  })

  it('distinguishes same-name members and excludes other groups from the response', async () => {
    api.members.mockResolvedValue(page([member(), member({ member_id: 'opaque-member-b', email: 'other@example.com', message_preview: '我是另一位小王 9127' }), member({ group_id: 'other', member_id: 'wrong-group', nickname: '不应显示' })]))
    const wrapper = setup(); await flushPromises()
    const select = wrapper.get('select')
    expect(select.findAll('option')).toHaveLength(3)
    expect(select.text()).toContain('other@example.com')
    expect(select.text()).not.toContain('不应显示')
    await select.setValue(JSON.stringify(['g1', 'opaque-member-b']))
    expect(wrapper.emitted('update:modelValue')).toEqual([['opaque-member-b']])
  })

  it('adds a secondary distinction only when recognizable labels collide', async () => {
    api.members.mockResolvedValue(page([member(), member({ member_id: 'opaque-member-b' }), member({ group_id: 'g2' })]))
    const wrapper = setup({ groupId: '' }); await flushPromises()
    const labels = wrapper.findAll('option').slice(1).map(option => option.text())
    expect(new Set(labels).size).toBe(3)
    expect(labels[0]).toContain('g1/member-a')
    expect(labels[2]).toContain('g2/member-a')
  })

  it('retains an unresolved saved identity and label without selecting a replacement', async () => {
    api.members.mockImplementation(async (params: { member_id?: string }) => page(params.member_id ? [] : [member()]))
    const wrapper = setup({ modelValue: 'saved-only', selectedLabel: '原先确认的管理员', required: true })
    await flushPromises()
    expect((wrapper.get('select').element as HTMLSelectElement).value).toBe(JSON.stringify(['g1', 'saved-only']))
    expect(wrapper.get('[data-testid="qq-member-selected"]').text()).toContain('原先确认的管理员')
    expect(wrapper.text()).toContain('已保留原选择')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect((wrapper.get('select').element as HTMLSelectElement).checkValidity()).toBe(true)
  })

  it('does not allow selecting an unidentifiable directory entry', async () => {
    api.members.mockResolvedValue(page([member({ nickname: '', email: '', message_preview: '', last_seen_at: '' })]))
    const wrapper = setup(); await flushPromises()
    expect(wrapper.findAll('option')[1].attributes('disabled')).toBeDefined()
    await wrapper.get('select').setValue(JSON.stringify(['g1', 'opaque-member-a']))
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('clears on a real group change and ignores a slow response from the old group', async () => {
    const old = deferred<QQMemberPage>()
    api.members.mockImplementation((params: { group_id?: string }) => params.group_id === 'g1' ? old.promise : Promise.resolve(page([member({ group_id: 'g2', member_id: 'new-member', nickname: '新群成员', message_preview: '我是新群成员 1234' })])))
    const wrapper = setup({ modelValue: 'opaque-member-a' })
    await wrapper.setProps({ groupId: 'g2', modelValue: '' }); await flushPromises()
    old.resolve(page([member()])); await flushPromises()
    expect(wrapper.get('select').text()).toContain('新群成员')
    expect(wrapper.get('select').text()).not.toContain('小王')
    expect(wrapper.emitted('update:modelValue')).toEqual([['']])
  })

  it('keeps an explicit global selection when the parent synchronizes its group', async () => {
    const wrapper = setup({ groupId: '' }); await flushPromises()
    await wrapper.get('select').setValue(JSON.stringify(['g1', 'opaque-member-a']))
    await wrapper.setProps({ groupId: 'g1', modelValue: 'opaque-member-a' }); await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toEqual([['opaque-member-a']])
    expect((wrapper.get('select').element as HTMLSelectElement).value).toBe(JSON.stringify(['g1', 'opaque-member-a']))
  })

  it('discards old searches during debounce and queries server pagination', async () => {
    vi.useFakeTimers()
    const old = deferred<QQMemberPage>()
    api.members.mockReturnValueOnce(old.promise).mockResolvedValue(page([member({ member_id: 'matched' })], { total: 42 }))
    const wrapper = setup()
    await wrapper.get('input').setValue('匹配')
    old.resolve(page([member({ nickname: '过期结果' })])); await flushPromises()
    expect(wrapper.get('select').text()).not.toContain('过期结果')
    await vi.advanceTimersByTimeAsync(250); await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === '下一页')!.trigger('click'); await flushPromises()
    expect(api.members).toHaveBeenLastCalledWith(expect.objectContaining({ search: '匹配', page: 2, page_size: 20 }))
  })

  it('rejects responses for another bot and clears the old member on app changes', async () => {
    const wrapper = setup({ modelValue: 'opaque-member-a' }); await flushPromises()
    await wrapper.setProps({ appId: 'new-app', modelValue: '' }); await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toEqual([['']])
    expect(wrapper.get('[role="alert"]').text()).toContain('机器人配置已变化')
    expect(wrapper.get('select').attributes('disabled')).toBeDefined()
    expect(wrapper.get('select').text()).not.toContain('小王')
  })

  it('pins a discovered bot identity when no app prop is supplied', async () => {
    const wrapper = setup({ appId: undefined }); await flushPromises()
    api.members.mockResolvedValue(page([member({ nickname: '其他机器人成员' })], { app_id: 'other-app' }))
    await wrapper.findAll('button')[0].trigger('click'); await flushPromises()
    expect(api.members).toHaveBeenLastCalledWith(expect.objectContaining({ app_id: 'app' }))
    expect(wrapper.get('[role="alert"]').text()).toContain('机器人配置已变化')
    expect(wrapper.get('select').text()).not.toContain('其他机器人成员')
  })

  it('keeps a failed lookup visible and prevents disabled interaction', async () => {
    api.members.mockRejectedValue(new Error('成员查询失败'))
    const wrapper = setup({ modelValue: 'saved', selectedLabel: '已确认成员', disabled: true }); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('成员查询失败')
    expect(wrapper.get('select').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('已确认成员')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps required-field validation active while lookup is loading or failed', async () => {
    const pending = deferred<QQMemberPage>()
    api.members.mockReturnValueOnce(pending.promise)
    const wrapper = setup({ required: true })
    const select = wrapper.get('select').element as HTMLSelectElement
    expect(select.disabled).toBe(false)
    expect(select.checkValidity()).toBe(false)
    pending.resolve(page([])); await flushPromises()
    api.members.mockRejectedValue(new Error('暂时不可用'))
    await wrapper.findAll('button')[0].trigger('click'); await flushPromises()
    expect(select.disabled).toBe(false)
    expect(select.checkValidity()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
