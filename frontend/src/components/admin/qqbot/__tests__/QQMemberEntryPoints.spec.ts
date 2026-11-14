import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import zh from '@/i18n/locales/zh'
import { defaultModerationConfig, type QQGroupAdmin, type QQMemberOption } from '@/api/qqbot'
import QQBotGroupAdminSettings from '../QQBotGroupAdminSettings.vue'
import QQBotModerationSettings from '../QQBotModerationSettings.vue'
import QQJobRecords from '../QQJobRecords.vue'
import QQModerationRecords from '../QQModerationRecords.vue'
import QQBotOverviewView from '@/views/admin/QQBotOverviewView.vue'
import QQBotRecordsView from '@/views/admin/QQBotRecordsView.vue'

const api = vi.hoisted(() => ({ members: vi.fn(), jobs: vi.fn(), moderationRecords: vi.fn(), config: vi.fn(), runtime: vi.fn(), overview: vi.fn(), diagnose: vi.fn() }))
vi.mock('@/api/qqbot', async original => ({ ...await original<typeof import('@/api/qqbot')>(), qqbotAPI: api }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
const members: QQMemberOption[] = [
  { group_id: 'first', member_id: 'first-member', nickname: 'Alice', email: 'alice@example.com', message_preview: '确认身份 4826', last_seen_at: '2026-10-24T01:00:00Z', group_label: 'First group' },
  { group_id: 'second', member_id: 'second-member', nickname: '', email: 'bob@example.com', message_preview: '确认身份 9921', last_seen_at: '2026-10-24T01:00:00Z', group_label: 'Second group' },
  { group_id: 'first', member_id: 'unbound-member', nickname: '', email: '', message_preview: '仅短期保存的身份确认消息', last_seen_at: '2026-10-24T01:00:00Z', group_label: 'First group' }
]
const wrappers: VueWrapper[] = []
const key = (group: string, member: string) => JSON.stringify([group, member])
const global = () => ({ plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh } })], stubs: { RouterLink: { template: '<a><slot /></a>' } } })
const page = () => ({ items: [], total: 0, page: 1, page_size: 20 })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const overviewConfig = { app_id: 'app', enabled: true, allowed_groups: ['first', 'second'], group_policies: [{ group_id: 'first', label: 'First group' }], total_daily_chat_limit: 100, total_daily_image_limit: 10 }
const overviewData = { day: '2026-10-24', checks: [], chat_remaining: 100, image_remaining: 10, failed: 0, pending_rewards: 0, reward_amount: 0, queued: 0, running: 0, chat_used: 0, image_used: 0 }

describe('QQ member selection entry points', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.members.mockImplementation(async (params: { app_id?: string; group_id?: string; member_id?: string }) => {
      const items = members.filter(member => (!params.group_id || member.group_id === params.group_id) && (!params.member_id || member.member_id === params.member_id))
      return { app_id: 'app', items, total: items.length, page: 1, page_size: 20 }
    })
    api.jobs.mockResolvedValue(page()); api.moderationRecords.mockResolvedValue(page())
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks() })

  it('clears a draft admin identity when its group changes and saves the selected email as a label', async () => {
    let admins: QQGroupAdmin[] = [{ app_id: 'app', group_id: 'first', member_id: 'first-member', nickname: 'Alice' }]
    const wrapper = mount(QQBotGroupAdminSettings, { props: {
      modelValue: admins, appId: 'app', groups: ['first', 'second'], groupLabels: { first: 'First group', second: 'Second group' }, runtime: null, disabled: false,
      'onUpdate:modelValue': (value: QQGroupAdmin[]) => { admins = value; void wrapper.setProps({ modelValue: value }) }
    }, global: global() })
    wrappers.push(wrapper); await flushPromises()
    expect(wrapper.get('[data-testid="qq-member-selected"]').text()).toContain('Alice')
    await wrapper.get('[data-testid="group-admin-group"]').setValue('second'); await flushPromises()
    expect(admins[0]).toEqual({ app_id: 'app', group_id: 'second', member_id: '', nickname: '' })
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('second', 'second-member')); await flushPromises()
    expect(admins[0]).toEqual({ app_id: 'app', group_id: 'second', member_id: 'second-member', nickname: 'bob@example.com' })
    expect(api.members).toHaveBeenCalledWith(expect.objectContaining({ app_id: 'app', group_id: 'second' }))
  })

  it('keeps an existing admin attached to its original app and disables editing after an app change', async () => {
    const admins: QQGroupAdmin[] = [{ app_id: 'app', group_id: 'first', member_id: 'first-member', nickname: 'Alice' }]
    const wrapper = mount(QQBotGroupAdminSettings, { props: { modelValue: admins, appId: 'app', groups: ['first'], runtime: null, disabled: false }, global: global() })
    wrappers.push(wrapper); await flushPromises()
    await wrapper.setProps({ appId: 'replacement-app' }); await flushPromises()
    expect(wrapper.text()).toContain(zh.qqbot.groupAdmins.appChanged)
    expect((wrapper.get('[data-testid="qq-member-select"]').element as HTMLSelectElement).disabled).toBe(true)
    expect(wrapper.get('[data-testid="group-admin-group"]').element.matches(':disabled')).toBe(true)
    expect(admins[0]).toEqual({ app_id: 'app', group_id: 'first', member_id: 'first-member', nickname: 'Alice' })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.get('[data-testid="group-admin-entry"] button').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[]])
  })

  it('keeps whitelist selection scoped and clears its identity on group or app changes', async () => {
    const config = reactive({ ...defaultModerationConfig(), enabled: true, groups: ['first', 'second'], exemptions: [{ group_id: 'first', member_id: 'first-member', nickname: 'Alice' }] })
    const wrapper = mount(QQBotModerationSettings, { props: { modelValue: config, groups: ['first', 'second'], appId: 'app', groupLabels: { second: 'Second group' }, runtime: null, disabled: false }, global: global() })
    wrappers.push(wrapper); await flushPromises()
    expect(wrapper.get('[data-testid="moderation-exempt-group"]').text()).toContain('Second group')
    await wrapper.get('[data-testid="moderation-exempt-group"]').setValue('second'); await flushPromises()
    expect(config.exemptions[0]).toEqual({ group_id: 'second', member_id: '', nickname: '' })
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('second', 'second-member')); await flushPromises()
    expect(config.exemptions[0]).toEqual({ group_id: 'second', member_id: 'second-member', nickname: 'bob@example.com' })
    await wrapper.setProps({ appId: 'replacement-app' }); await flushPromises()
    expect(config.exemptions[0]).toEqual({ group_id: 'second', member_id: '', nickname: '' })
    expect(config.enabled).toBe(true)
  })

  it('keeps a manually entered whitelist label without persisting the temporary message preview', async () => {
    const config = reactive({ ...defaultModerationConfig(), enabled: true, groups: ['first'], exemptions: [{ group_id: 'first', member_id: '', nickname: '' }] })
    const props = { modelValue: config, groups: ['first'], appId: 'app', runtime: null, disabled: false }
    let wrapper = mount(QQBotModerationSettings, { props, global: global() })
    wrappers.push(wrapper); await flushPromises()
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('first', 'unbound-member')); await flushPromises()
    expect(config.exemptions[0]).toEqual({ group_id: 'first', member_id: 'unbound-member', nickname: '' })
    await wrapper.get('[data-testid="moderation-exempt-nickname"]').setValue('已人工核对的小王')
    expect(config.exemptions[0]!.nickname).toBe('已人工核对的小王')
    wrapper.unmount()
    api.members.mockResolvedValue({ app_id: 'app', items: [], total: 0, page: 1, page_size: 20 })
    wrapper = mount(QQBotModerationSettings, { props, global: global() })
    wrappers.push(wrapper); await flushPromises()
    expect(wrapper.get('[data-testid="qq-member-selected"]').text()).toContain('已人工核对的小王')
    expect(wrapper.text()).not.toContain('仅短期保存的身份确认消息')
    expect(config.exemptions[0]!.member_id).toBe('unbound-member')
  })

  it.each([
    ['jobs', QQJobRecords, 'qq-job-group'],
    ['moderationRecords', QQModerationRecords, 'qq-moderation-group']
  ] as const)('%s synchronizes an unscoped member selection with its group and clears stale group filters', async (method, component, groupTestId) => {
    const wrapper = mount(component, { props: { appId: 'app' }, global: global() })
    wrappers.push(wrapper); await flushPromises()
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('second', 'second-member')); await flushPromises()
    expect((wrapper.get(`[data-testid="${groupTestId}"]`).element as HTMLInputElement).value).toBe('second')
    expect(wrapper.get('[data-testid="qq-member-selected"]').text()).toContain('bob@example.com')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 'second', member_id: 'second-member' }))
    await wrapper.get(`[data-testid="${groupTestId}"]`).setValue('first'); await flushPromises()
    expect(wrapper.find('[data-testid="qq-member-selected"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 'first', member_id: '' }))
  })

  it('diagnoses the selected member and group, then clears identity after group and app changes', async () => {
    const config = { app_id: 'app', enabled: true, allowed_groups: ['first', 'second'], group_policies: [{ group_id: 'first', label: 'First group' }], total_daily_chat_limit: 100, total_daily_image_limit: 10 }
    api.config.mockResolvedValue(config)
    api.overview.mockResolvedValue({ day: '2026-10-24', checks: [], chat_remaining: 100, image_remaining: 10, failed: 0, pending_rewards: 0, reward_amount: 0, queued: 0, running: 0, chat_used: 0, image_used: 0 })
    api.runtime.mockResolvedValue({ groups: [], jobs: [] })
    api.diagnose.mockResolvedValue({ checks: [], jobs: [] })
    const wrapper = mount(QQBotOverviewView, { global: global() })
    wrappers.push(wrapper); await flushPromises()
    expect(wrapper.get('[data-testid="qq-diagnose-group"]').text()).toContain('First group')
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('first', 'first-member')); await flushPromises()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.diagnose).toHaveBeenLastCalledWith('first', 'first-member')
    await wrapper.get('[data-testid="qq-diagnose-group"]').setValue('second'); await flushPromises()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.diagnose).toHaveBeenLastCalledWith('second', '')
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('second', 'second-member')); await flushPromises()
    api.config.mockResolvedValue({ ...config, app_id: 'replacement-app' })
    await wrapper.findAll('button')[0]!.trigger('click'); await flushPromises()
    expect((wrapper.get('[data-testid="qq-diagnose-group"]').element as HTMLSelectElement).value).toBe('')
    expect(wrapper.find('[data-testid="qq-member-selected"]').exists()).toBe(false)
  })

  it.each([
    ['jobs', QQJobRecords, 'qq-job-group'],
    ['moderationRecords', QQModerationRecords, 'qq-moderation-group']
  ] as const)('%s resets draft and applied filters and discards a late response after the app changes', async (method, component, groupTestId) => {
    const wrapper = mount(component, { props: { appId: 'app' }, global: global() })
    wrappers.push(wrapper); await flushPromises()
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('first', 'first-member')); await flushPromises()
    const late = deferred<unknown>()
    api[method].mockReturnValueOnce(late.promise)
    await wrapper.get('form').trigger('submit')
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 'first', member_id: 'first-member' }))
    await wrapper.setProps({ appId: 'replacement-app' }); await flushPromises()
    expect((wrapper.get(`[data-testid="${groupTestId}"]`).element as HTMLInputElement).value).toBe('')
    expect(wrapper.find('[data-testid="qq-member-selected"]').exists()).toBe(false)
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: '', member_id: '', status: '', from: '', to: '', page: 1 }))
    late.resolve({ items: [{ id: 'old-result', group_id: 'old-group', member_id: 'old-member', created_at: '2026-10-24T01:00:00Z', reason: 'repeat', mute_status: 'succeeded', mute_seconds: 300, recall_total: 1, recall_succeeded: 1, pending: 0, error: '', kind: 'chat', status: 'completed', input_tokens: 1, output_tokens: 1 }], total: 1, page: 1, page_size: 20 })
    await flushPromises()
    expect(wrapper.text()).not.toContain('old-group')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: '', member_id: '' }))
  })

  it.each(['tasks', 'moderation'])('the %s records page waits for the saved app before fetching member choices', async mode => {
    const configRequest = deferred<{ app_id: string }>()
    api.config.mockReturnValueOnce(configRequest.promise)
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/admin/qqbot/records', component: QQBotRecordsView }] })
    await router.push(`/admin/qqbot/records?tab=${mode}`)
    const options = global()
    const wrapper = mount(QQBotRecordsView, { global: { ...options, plugins: [...options.plugins, router] } })
    wrappers.push(wrapper); await flushPromises()
    expect(api.members).not.toHaveBeenCalled()
    expect(api.jobs).not.toHaveBeenCalled()
    expect(api.moderationRecords).not.toHaveBeenCalled()
    expect((wrapper.get('[data-testid="qq-member-select"]').element as HTMLSelectElement).disabled).toBe(true)
    configRequest.resolve({ app_id: 'app' }); await flushPromises()
    expect(api.members).toHaveBeenCalledWith(expect.objectContaining({ app_id: 'app' }))
    await wrapper.get('[data-testid="qq-member-select"]').setValue(key('first', 'first-member')); await flushPromises()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    const method = mode === 'tasks' ? 'jobs' : 'moderationRecords'
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 'first', member_id: 'first-member' }))
    api.config.mockResolvedValueOnce({ app_id: 'replacement-app' })
    await wrapper.get('[data-testid="qq-records-refresh-app"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="qq-member-selected"]').exists()).toBe(false)
    expect(api.members).toHaveBeenCalledWith(expect.objectContaining({ app_id: 'replacement-app' }))
    expect(api[method]).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: '', member_id: '' }))
    const recordCalls = api[method].mock.calls.length
    api.config.mockResolvedValueOnce({ app_id: 'replacement-app' })
    await wrapper.get('[data-testid="qq-records-refresh-app"]').trigger('click'); await flushPromises()
    expect(api[method].mock.calls.length).toBeGreaterThan(recordCalls)
  })

  it.each(['group', 'app'])('discards a late diagnosis after the selected %s changes', async scope => {
    api.config.mockResolvedValue(overviewConfig)
    api.overview.mockResolvedValue(overviewData)
    api.runtime.mockResolvedValue({ groups: [], jobs: [] })
    const late = deferred<unknown>()
    api.diagnose.mockReturnValueOnce(late.promise)
    const wrapper = mount(QQBotOverviewView, { global: global() })
    wrappers.push(wrapper); await flushPromises()
    await wrapper.get('[data-testid="qq-diagnose-group"]').setValue('first')
    await wrapper.get('form').trigger('submit')
    if (scope === 'app') {
      api.config.mockResolvedValueOnce({ ...overviewConfig, app_id: 'replacement-app' })
      await wrapper.findAll('button')[0]!.trigger('click'); await flushPromises()
    }
    await wrapper.get('[data-testid="qq-diagnose-group"]').setValue('second'); await flushPromises()
    api.diagnose.mockResolvedValueOnce({ checks: [{ code: 'current', ok: true, message: 'Current group diagnosis' }], jobs: [] })
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('Current group diagnosis')
    late.resolve({ checks: [{ code: 'stale', ok: true, message: 'Outdated group diagnosis' }], jobs: [] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('Outdated group diagnosis')
    expect(wrapper.text()).toContain('Current group diagnosis')
  })
})
