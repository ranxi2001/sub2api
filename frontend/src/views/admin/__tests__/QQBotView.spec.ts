import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import QQBotView from '../QQBotView.vue'
import QQBotChatPreview from '@/components/admin/qqbot/QQBotChatPreview.vue'
import QQBotAnnouncement from '@/components/admin/qqbot/QQBotAnnouncement.vue'
import QQBotSiteAISettings from '@/components/admin/qqbot/QQBotSiteAISettings.vue'
import QQBotImageAISettings from '@/components/admin/qqbot/QQBotImageAISettings.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import { configSections, pickSection, cloneConfig } from '@/components/admin/qqbot/configSections'
import { defaultAutonomousConfig, defaultModerationConfig, type QQSetupRequest, type QQBotConfig, type QQMemberOption } from '@/api/qqbot'
const api = vi.hoisted(() => ({ config: vi.fn(), setupOptions: vi.fn(), saveSetup: vi.fn(), saveSection: vi.fn(), runtime: vi.fn(), groupInfo: vi.fn(), bindings: vi.fn(), members: vi.fn(), deleteDiscoveredGroup: vi.fn(), probe: vi.fn(), configHistory: vi.fn(), commands: vi.fn(), chatPreview: vi.fn(), sendAnnouncement: vi.fn() }))
vi.mock('@/api/qqbot', async original => ({ ...await original<typeof import('@/api/qqbot')>(), qqbotAPI: api }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
const wrappers: VueWrapper[] = []
let server: QQBotConfig
const memberOptions: QQMemberOption[] = [
 { group_id: 'group', member_id: 'trusted-member', nickname: 'Trusted member', email: '', message_preview: '', last_seen_at: '', group_label: 'Primary group' },
 { group_id: 'group', member_id: 'existing-member', nickname: 'Existing member', email: '', message_preview: '', last_seen_at: '', group_label: 'Primary group' },
 { group_id: 'group', member_id: 'recent-member', nickname: '', email: '', message_preview: '我是小王，确认身份 4826', last_seen_at: '2026-10-23T10:30:00+08:00', group_label: 'Primary group' },
 { group_id: 'second-group', member_id: 'bound-member', nickname: 'Alice', email: 'alice@example.com', message_preview: '', last_seen_at: '', group_label: 'Second group' }
]
function memberKey(group: string, member: string) { return JSON.stringify([group, member]) }
async function mountView(locale: 'zh' | 'en' = 'zh', path = '/admin/qqbot/config') {
 const router = createRouter({ history: createMemoryHistory(), routes: [
  { path: '/admin/qqbot/config', component: QQBotView }, { path: '/admin/qqbot/groups', component: QQBotView }, { path: '/other', component: { template: '<p>Other</p>' } }, { path: '/admin/qqbot/setup', component: { template: '<p>Setup</p>' } }
 ] })
 await router.push(path)
 const wrapper = mount({ template: '<RouterView />' }, { attachTo: document.body, global: { plugins: [createI18n({ legacy: false, locale, messages: { zh, en } }), router], stubs: { teleport: true } } })
 wrappers.push(wrapper); await flushPromises(); return wrapper
}
async function category(w: VueWrapper, text: string) { await w.findAll('button').find(button => button.text() === text)!.trigger('click') }

describe('QQ module settings', () => {
 beforeEach(() => {
  vi.resetAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  server = { enabled: false, app_id: 'app', app_secret: '', app_secret_set: true, sandbox: false, allowed_groups: ['group', 'other-group'], group_policies: [], base_url: 'https://example.invalid/v1', api_key: '', api_key_set: true, chat_model: 'model', chat_protocol: 'responses', image_enabled: false, image_base_url: '', image_api_key: '', image_api_key_set: false, image_model: '', image_size: '1024x1024', system_prompt: 'Helpful', max_output_tokens: 2048, history_messages: 12, daily_chat_limit: 30, daily_image_limit: 2, total_daily_chat_limit: 500, total_daily_image_limit: 20, cooldown_seconds: 5, checkin_reward: 0, invite_enabled: false, invite_reward: 0, invite_daily_limit: 10, custom_commands: [], section_versions: Object.fromEntries(Object.keys(configSections).map(key => [key, `${key}-1`])) }
  api.config.mockImplementation(async () => cloneConfig(server))
  api.setupOptions.mockResolvedValue({ groups: [{ id: 2, name: 'Managed group', models: ['model'] }], image_groups: [{ id: 20, name: 'Image group', models: ['image-model'] }, { id: 21, name: 'Another image group', models: ['image-alternative'] }], current_quota: 10, current_used: 1, current_image_quota: 25, current_image_used: 4 })
  api.runtime.mockResolvedValue({ groups: [], jobs: [], rewards: [], bindings: 0, encryption_ready: true })
  api.groupInfo.mockImplementation(async (appID: string, groupID: string) => ({ app_id: appID, sandbox: server.sandbox, group_id: groupID, name: '', status: 'unavailable' }))
  api.members.mockImplementation(async (params: { group_id?: string; member_id?: string; search?: string }) => {
   const items = memberOptions.filter(member => (!params.group_id || member.group_id === params.group_id) && (!params.member_id || member.member_id === params.member_id) && (!params.search || [member.nickname, member.email, member.message_preview].join(' ').toLowerCase().includes(params.search.toLowerCase())))
   return { app_id: 'app', items, total: items.length, page: 1, page_size: 20 }
  })
  api.commands.mockResolvedValue([]); api.configHistory.mockResolvedValue([])
  api.saveSection.mockImplementation(async (section: keyof typeof configSections, _version: string, draft: QQBotConfig) => { Object.assign(server, pickSection(draft, section)); server.section_versions![section] += '-next'; return cloneConfig(server) })
  api.saveSetup.mockImplementation(async (req: QQSetupRequest) => {
   for (const section of req.sections as (keyof typeof configSections)[]) {
    Object.assign(server, pickSection(req.config, section)); server.section_versions![section] += '-next'
   }
   return cloneConfig(server)
  })
 })
 afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); try { expect(console.error).not.toHaveBeenCalled() } finally { vi.restoreAllMocks() } })
 it.each(['zh', 'en'] as const)('%s shows official group names with IDs and searches names without changing policies', async locale => {
  api.groupInfo.mockImplementation(async (appID: string, groupID: string) => ({ app_id: appID, sandbox: false, group_id: groupID, name: groupID === 'group' ? '技术交流群' : '客户服务群', status: 'available' }))
  api.runtime.mockResolvedValue({ groups: [{ id: 'discovered', last_event: '', last_seen_at: '' }], jobs: [], rewards: [], bindings: 0, encryption_ready: true })
  const w = await mountView(locale, '/admin/qqbot/groups')
  const panel = w.get('#qqbot-settings-panel-groups')
  const card = panel.findAll('article')[0]!
  expect(card.text()).toContain('技术交流群')
  expect(card.get('code').text()).toBe('group')
  expect((card.get('input[maxlength="60"]').element as HTMLInputElement).value).toBe('')
  expect(panel.text()).toContain('客户服务群')
  expect(w.get('#qqbot-settings-tab-groups').text()).not.toContain('•')
  const picker = panel.find('fieldset')
  await picker.get('input.input').setValue('技术')
  expect(picker.findAll('input[type="checkbox"]')).toHaveLength(1)
  expect(picker.get('code').text()).toBe('group')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.group_policies).toEqual([])
 })
 it('uses the saved nickname before the official name and saves only the manual label', async () => {
  server.group_policies = [{ group_id: 'group', label: 'VIP 客户群', disable_chat: false, disable_images: false, disable_activities: false, disable_notifications: false, chat_daily_limit: null, image_daily_limit: null }]
  api.groupInfo.mockImplementation(async (appID: string, groupID: string) => ({ app_id: appID, sandbox: false, group_id: groupID, name: '官方群名称', status: 'available' }))
  const w = await mountView('zh', '/admin/qqbot/groups')
  const panel = w.get('#qqbot-settings-panel-groups')
  const card = panel.findAll('article')[0]!
  expect(card.get('p').text()).toBe('VIP 客户群')
  const picker = panel.find('fieldset')
  await picker.get('input.input').setValue('官方群名称')
  expect(picker.text()).toContain('VIP 客户群')
  await card.get('input[maxlength="60"]').setValue('更新的昵称')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.group_policies?.[0]?.label).toBe('更新的昵称')
  expect(server.group_policies).toHaveLength(1)
 })
 it('explains missing QQ permissions while allowing manual nicknames', async () => {
  api.groupInfo.mockImplementation(async (appID: string, groupID: string) => ({ app_id: appID, sandbox: false, group_id: groupID, name: '', status: 'permission_required' }))
  const w = await mountView('zh', '/admin/qqbot/groups')
  const panel = w.get('#qqbot-settings-panel-groups')
  expect(panel.text()).toContain('QQ 尚未授予')
  expect(api.groupInfo).toHaveBeenCalledTimes(1)
  await panel.findAll('article')[0]!.get('input[maxlength="60"]').setValue('手填群昵称')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.group_policies?.[0]?.label).toBe('手填群昵称')
 })
 it.each(['zh', 'en'] as const)('%s groups navigation and saves only the selected module', async locale => {
  const w = await mountView(locale)
  expect(w.findAll('[role="tab"]')).toHaveLength(3)
  expect(w.get('#qqbot-settings-panel-connection').isVisible()).toBe(true)
  await w.get('#qqbot-settings-panel-connection input[autocomplete="off"]').setValue('unsaved-app')
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  await w.get('[data-testid="history-messages"]').setValue(25)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).toHaveBeenCalledWith('chat', 'chat-1', expect.objectContaining({ history_messages: 25 }))
  expect(server.app_id).toBe('app')
  await w.get('#qqbot-settings-tab-connection').trigger('click')
  expect((w.get('#qqbot-settings-panel-connection input[autocomplete="off"]').element as HTMLInputElement).value).toBe('unsaved-app')
  expect(w.get('#qqbot-settings-tab-connection').text()).toContain('•')
 })
 it('enables saving when managed key regeneration is the only change', async () => {
  Object.assign(server, { ai_connection: 'site', site_api_key_id: 5, site_group_id: 2 })
  const w = await mountView('zh', '/admin/qqbot/config?tab=chat')
  expect(w.get('button[type="submit"]').attributes('disabled')).toBeDefined()
  await w.getComponent(QQBotSiteAISettings).get('input[type="checkbox"]').setValue(true)
  expect(w.get('#qqbot-settings-tab-chat').text()).toContain('•')
  expect(w.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSetup).toHaveBeenCalledWith(expect.objectContaining({ sections: ['chat'], regenerate_site_key: true, site_quota: 10 }))
  expect(w.get('#qqbot-settings-tab-chat').text()).not.toContain('•')
  expect(w.get('button[type="submit"]').attributes('disabled')).toBeDefined()
 })
 it('confirms and discards pending key regeneration when reloading chat settings', async () => {
  Object.assign(server, { ai_connection: 'site', site_api_key_id: 5, site_group_id: 2 })
  const w = await mountView('zh', '/admin/qqbot/config?tab=chat')
  await w.getComponent(QQBotSiteAISettings).get('input[type="checkbox"]').setValue(true)
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
  const reload = w.findAll('button').find(button => button.text() === '重新加载本模块')!
  await reload.trigger('click'); await flushPromises()
  expect(confirm).toHaveBeenCalledOnce()
  expect(w.get('#qqbot-settings-tab-chat').text()).toContain('•')
  confirm.mockReturnValue(true)
  await reload.trigger('click'); await flushPromises()
  expect(w.get('#qqbot-settings-tab-chat').text()).not.toContain('•')
  expect((w.getComponent(QQBotSiteAISettings).get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)
  expect(api.saveSetup).not.toHaveBeenCalled()
 })
 it.each(['zh', 'en'] as const)('%s saves GIF request format only in the images module', async locale => {
  const w = await mountView(locale)
  await w.get('#qqbot-settings-tab-images').trigger('click')
  expect((w.get('[data-testid="image-output-format"]').element as HTMLSelectElement).value).toBe('')
  await w.get('[data-testid="image-output-format"]').setValue('gif')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).toHaveBeenCalledWith('images', 'images-1', expect.objectContaining({ image_output_format: 'gif' }))
  expect(server.image_output_format).toBe('gif')
  expect(server.chat_model).toBe('model')
  expect(w.get('#qqbot-settings-panel-images').text()).toContain('--gif')
  expect(w.get('#qqbot-settings-tab-images').text()).not.toContain('•')
 })
 it('saves an independent managed image connection and preserves the unsaved chat draft', async () => {
  const w = await mountView('zh', '/admin/qqbot/config?tab=chat')
  await w.get('[data-testid="history-messages"]').setValue(25)
  await w.get('#qqbot-settings-tab-images').trigger('click')
  await w.get('#qqbot-settings-panel-images input[type="checkbox"]').setValue(true)
  await w.get('[data-testid="image-ai-source"]').setValue('site')
  await w.get('[data-testid="image-site-group"]').setValue('20')
  await w.get('[data-testid="image-site-quota"]').setValue('40')
  const images = w.getComponent(QQBotImageAISettings)
  await images.get('[data-testid="image-model"] button').trigger('click'); await flushPromises()
  await images.get('[role="option"]').trigger('click'); await flushPromises()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSetup).toHaveBeenCalledWith(expect.objectContaining({ sections: ['images'], image_site_quota: 40, regenerate_image_site_key: false, config: expect.objectContaining({ image_ai_connection: 'site', image_site_group_id: 20, image_model: 'image-model' }) }))
  expect(server.history_messages).toBe(12)
  expect(server.chat_model).toBe('model')
  expect(w.get('#qqbot-settings-tab-chat').text()).toContain('•')
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  expect(w.get<HTMLInputElement>('[data-testid="history-messages"]').element.value).toBe('25')
 })
 it('tracks image key regeneration independently and discards it when reloading images', async () => {
  Object.assign(server, { image_ai_connection: 'site', image_site_api_key_id: 88, image_site_group_id: 20, image_model: 'image-model' })
  const w = await mountView('zh', '/admin/qqbot/config?tab=images')
  await w.get('[data-testid="image-regenerate-key"]').setValue(true)
  await w.get('[data-testid="image-site-quota"]').setValue('77')
  await w.get('#qqbot-settings-tab-chat').trigger('click'); await flushPromises()
  await w.get('#qqbot-settings-tab-images').trigger('click'); await flushPromises()
  expect(w.get<HTMLInputElement>('[data-testid="image-site-quota"]').element.value).toBe('77')
  expect(w.get('#qqbot-settings-tab-images').text()).toContain('•')
  expect(w.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
  await w.findAll('button').find(button => button.text() === '重新加载本模块')!.trigger('click'); await flushPromises()
  expect(confirm).toHaveBeenCalledOnce()
  expect(w.get('#qqbot-settings-tab-images').text()).not.toContain('•')
  await w.get('[data-testid="image-regenerate-key"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSetup).toHaveBeenCalledWith(expect.objectContaining({ sections: ['images'], site_quota: 0, image_site_quota: 25, regenerate_image_site_key: true }))
  expect(api.saveSetup.mock.calls[0]![0].regenerate_site_key).toBeUndefined()
 })
 it('preserves an unsaved image group and its editable quota across tabs', async () => {
  Object.assign(server, { image_ai_connection: 'site', image_site_api_key_id: 88, image_site_group_id: 20, image_model: 'image-model' })
  const w = await mountView('zh', '/admin/qqbot/config?tab=images')
  await w.get('[data-testid="image-site-group"]').setValue('21')
  await w.get('[data-testid="image-site-quota"]').setValue('60')
  await w.get('#qqbot-settings-tab-chat').trigger('click'); await flushPromises()
  await w.get('#qqbot-settings-tab-images').trigger('click'); await flushPromises()
  expect(w.get<HTMLSelectElement>('[data-testid="image-site-group"]').element.value).toBe('21')
  const quota = w.get<HTMLInputElement>('[data-testid="image-site-quota"]')
  expect(quota.element.disabled).toBe(false)
  expect(quota.element.value).toBe('60')
  expect(w.get('#qqbot-settings-tab-images').text()).toContain('•')
 })
 it.each(['cancel', 'source'])('restores the saved image quota before saving after cancelling replacement (%s)', async mode => {
  Object.assign(server, { image_ai_connection: 'site', image_site_api_key_id: 88, image_site_group_id: 20, image_model: 'image-model' })
  const w = await mountView('zh', '/admin/qqbot/config?tab=images')
  await w.get('[data-testid="image-regenerate-key"]').setValue(true)
  await w.get('[data-testid="image-site-quota"]').setValue('100')
  if (mode === 'source') {
   await w.get('[data-testid="image-ai-source"]').setValue('reuse_chat')
   await w.get('[data-testid="image-ai-source"]').setValue('site')
  } else await w.get('[data-testid="image-regenerate-key"]').setValue(false)
  await w.get('[data-testid="image-output-format"]').setValue('gif')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSetup).toHaveBeenCalledWith(expect.objectContaining({ sections: ['images'], image_site_quota: 25, regenerate_image_site_key: false }))
 })
 it('uses managed setup when switching away from a managed image connection', async () => {
  Object.assign(server, { image_ai_connection: 'site', image_site_api_key_id: 88, image_site_group_id: 20, image_model: 'image-model' })
  const w = await mountView('zh', '/admin/qqbot/config?tab=images')
  await w.get('[data-testid="image-ai-source"]').setValue('reuse_chat')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSetup).toHaveBeenCalledWith(expect.objectContaining({ sections: ['images'], config: expect.objectContaining({ image_ai_connection: 'reuse_chat' }) }))
  expect(api.saveSection).not.toHaveBeenCalled()
 })
 it('does not validate unrelated drafts and keeps original revisions for unsaved modules', async () => {
  const w = await mountView()
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  await w.get('[data-testid="history-messages"]').setValue(101)
  server.chat_model = 'changed-elsewhere'; server.section_versions!.chat = 'chat-2'
  await w.get('#qqbot-settings-tab-connection').trigger('click')
  await w.get('#qqbot-settings-panel-connection input[type="checkbox"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).toHaveBeenCalledWith('connection', 'connection-1', expect.anything())
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  await w.get('[data-testid="history-messages"]').setValue(30)
  api.saveSection.mockRejectedValueOnce(new Error('此模块已被其他页面更新'))
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).toHaveBeenLastCalledWith('chat', 'chat-1', expect.objectContaining({ chat_model: 'model' }))
  expect(w.get('[role="alert"]').text()).toContain('其他页面')
  expect((w.get('[data-testid="history-messages"]').element as HTMLInputElement).value).toBe('30')
 })
 it('validates the active module and protects drafts on refresh and navigation', async () => {
  const w = await mountView()
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  await w.get('[data-testid="history-messages"]').setValue(101)
  const input = w.get('[data-testid="history-messages"]').element as HTMLInputElement
  const validity = vi.spyOn(input, 'reportValidity')
  await w.get('form').trigger('submit'); expect(validity).toHaveBeenCalledOnce(); expect(api.saveSection).not.toHaveBeenCalled()
  const event = new Event('beforeunload', { cancelable: true }); window.dispatchEvent(event); expect(event.defaultPrevented).toBe(true)
  vi.spyOn(window, 'confirm').mockReturnValue(false)
  await w.findAll('button').find(b => b.text() === '重新加载本模块')!.trigger('click'); await flushPromises()
  expect((input).value).toBe('101')
  await w.vm.$router.push('/other'); expect(w.vm.$route.path).toBe('/admin/qqbot/config')
 })
 it('saves an independent chat switch and retains zero shared history', async () => {
  const w = await mountView()
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  await w.get('#qqbot-settings-panel-chat input[type="checkbox"]').setValue(true)
  await w.get('[data-testid="history-messages"]').setValue(0)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.chat_disabled).toBe(true); expect(server.history_messages).toBe(0)
 })
 it('requires explicit all-groups selection and saves notification scope', async () => {
  const w = await mountView(); await category(w, '通知与额度'); await w.get('#qqbot-settings-tab-notifications').trigger('click')
  const pickers = w.get('#qqbot-settings-panel-notifications').findAllComponents({ name: 'QQGroupPicker' })
  const pricePicker = pickers.find(picker => picker.props('allowAll'))!
  await pricePicker.findAll('input[type="checkbox"]')[1]!.setValue(true)
  await w.get('[data-testid="price-notifications-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.price_notifications?.groups).toEqual(['group'])
  await pricePicker.findAll('input[type="checkbox"]')[1]!.setValue(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.price_notifications?.groups).toEqual(['group'])
 })
 it('saves separate mention-everyone switches without queuing an announcement', async () => {
  const w = await mountView('zh', '/admin/qqbot/config?tab=notifications')
  expect(w.get<HTMLInputElement>('[data-testid="price-notifications-mention-all"]').element.checked).toBe(false)
  expect(w.get<HTMLInputElement>('[data-testid="health-mention-all"]').element.checked).toBe(false)
  await w.get('[data-testid="price-notifications-mention-all"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.price_notifications?.mention_all).toBe(true)
  expect(server.health_notifications?.mention_all).toBe(false)
  await w.get('[data-testid="health-mention-all"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.health_notifications?.mention_all).toBe(true)
  expect(api.sendAnnouncement).not.toHaveBeenCalled()
 })
 it('queues using saved connection and group policies while preserving configuration drafts', async () => {
  server.enabled = true
  api.sendAnnouncement.mockResolvedValue({ batch_id: 'batch', jobs: [{ id: 'announcement-job', group_id: 'group' }], duplicate: false })
  const w = await mountView()
  await w.get('#qqbot-settings-panel-connection input[autocomplete="off"]').setValue('unsaved-app')
  await category(w, '通知与额度'); await w.get('#qqbot-settings-tab-notifications').trigger('click')
  const announcement = w.getComponent(QQBotAnnouncement)
  expect(announcement.props('config').app_id).toBe('app')
  announcement.getComponent({ name: 'QQGroupPicker' }).vm.$emit('update:modelValue', ['group'])
  await announcement.get('[data-testid="qq-announcement-text"]').setValue('后台公告')
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  api.runtime.mockResolvedValue({ groups: [], jobs: [{ id: 'announcement-job', group_id: 'group', member_id: '', kind: 'announcement', status: 'queued', error: '', input_tokens: 0, output_tokens: 0, created_at: '2026-11-11T08:00:00Z' }], rewards: [], bindings: 0, encryption_ready: true })
  await announcement.get('[data-testid="qq-announcement-send"]').trigger('click'); await flushPromises()
  expect(api.sendAnnouncement).toHaveBeenCalledWith(expect.objectContaining({ app_id: 'app', sandbox: false, groups: ['group'], text: '后台公告', mention_all: false }))
  expect(api.runtime).toHaveBeenCalledTimes(2)
  expect(w.get('#qqbot-settings-panel-notifications').text()).toContain('待发送')
  expect(api.saveSection).not.toHaveBeenCalled()
  expect(w.get('#qqbot-settings-panel-connection input[autocomplete="off"]').element).toHaveProperty('value', 'unsaved-app')
 })
 it('presets keep autonomous participation opt-in and bounded', async () => {
  const w = await mountView(); await category(w, '对话与指令')
  await w.findAll('button').find(b => b.text() === '安静')!.trigger('click')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.autonomous).toMatchObject({ enabled: false, groups: [], sample_percent: 5, daily_calls: 15, daily_replies: 8 })
 })

 it.each(['zh', 'en'] as const)('%s renders autonomous funnel, costs and input breakdown', async locale => {
  api.runtime.mockResolvedValue({ groups: [], rewards: [], bindings: 0, encryption_ready: true,
   autonomous_stats: Object.entries({ received: 10, eligible: 8, selected: 4, model_calls: 4, generated: 2, sent: 1, input_tokens: 120, output_tokens: 30, skip_tokens: 60, discarded_tokens: 30, model_latency_ms: 500, delivery_latency_ms: 900 }).map(([reason, count]) => ({ group_id: 'group', reason, count })),
   jobs: [{ id: 'auto-record', group_id: 'group', member_id: 'alice', kind: 'ambient_chat', status: 'skipped', error: '', trigger_reason: 'sampled', decision_reason: 'model_skip_already_answered', input_tokens: 20, output_tokens: 10, created_at: '2026-11-09T10:00:00+08:00', autonomous_data: { batch_messages: 2, model_ms: 500, input_estimate: { system: 100, knowledge: 20, history: 30, current: 40, images: 1024, total: 1214, removed: 50 } } }]
  })
  const w = await mountView(locale, '/admin/qqbot/config?tab=autonomous')
  const summary = w.get('[data-testid="autonomous-summary"]')
  expect(summary.text()).toContain('50.0%')
  expect(summary.text()).toContain('120')
  expect(w.get('#qqbot-settings-panel-autonomous').text()).toContain('1214')
  expect(w.get('#qqbot-settings-panel-autonomous').text()).toContain(locale === 'zh' ? '模型跳过：已有人回答' : 'Skipped: already answered')
 })
 it('saves always-reply policy separately from explicit frequency changes', async () => {
  server.autonomous = { ...defaultAutonomousConfig(), daily_calls: 0 }
  const w = await mountView('zh', '/admin/qqbot/config?tab=autonomous')
  await w.get('[data-testid="autonomous-mode"]').setValue('always')
  expect((w.get('[data-testid="autonomous-daily_calls"]').element as HTMLInputElement).value).toBe('0')
  expect(w.get('[data-testid="autonomous-sample_percent"]').attributes('disabled')).toBeDefined()
  expect(w.get('[data-testid="autonomous-limit-warning"]').exists()).toBe(true)
  await w.get('[data-testid="autonomous-apply-always"]').trigger('click')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.autonomous).toMatchObject({ reply_mode: 'always', sample_percent: 100, min_messages: 0, cooldown_seconds: 1, member_cooldown_seconds: 0, merge_seconds: 2, daily_calls: 3600 })
 })
 it('enables auto chat with one switch and keeps all tuning collapsed', async () => {
  const w = await mountView('zh', '/admin/qqbot/config?tab=autonomous')
  expect(w.find('#qqbot-settings-tab-knowledge').exists()).toBe(false)
  expect(w.get('[data-testid="autonomous-advanced"]').attributes('open')).toBeUndefined()
  await w.get('[data-testid="autonomous-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.autonomous).toMatchObject({ enabled: true, inherit_groups: true, min_messages: 2, followup_seconds: 90 })
  expect(api.saveSetup).not.toHaveBeenCalled()
 })
 it('saves conversation and knowledge atomically while preserving another module draft', async () => {
  const w = await mountView('zh', '/admin/qqbot/config?tab=chat')
  await w.get('#qqbot-settings-panel-chat textarea').setValue('Keep this persona draft')
  await category(w, '对话与指令')
  await w.get('[data-testid="autonomous-enabled"]').setValue(true)
  await w.get('[data-testid="knowledge-enabled"]').setValue(true)
  await w.get('[data-testid="knowledge-content"]').setValue('本站接入教程')
  api.saveSetup.mockRejectedValueOnce(new Error('知识已被其他页面更新'))
  await w.get('form').trigger('submit'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toContain('其他页面')
  expect(server.site_knowledge).toBeUndefined()
  expect((w.get('[data-testid="autonomous-enabled"]').element as HTMLInputElement).checked).toBe(true)
  expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).value).toBe('本站接入教程')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSetup).toHaveBeenLastCalledWith(expect.objectContaining({ sections: ['autonomous', 'knowledge'], versions: expect.objectContaining({ autonomous: 'autonomous-1', knowledge: 'knowledge-1' }) }))
  expect(server.site_knowledge?.enabled).toBe(true)
  expect(server.autonomous?.enabled).toBe(true)
  expect(server.system_prompt).toBe('Helpful')
  await category(w, '连接与模型'); await w.get('#qqbot-settings-tab-chat').trigger('click')
  expect((w.get('#qqbot-settings-panel-chat textarea').element as HTMLTextAreaElement).value).toBe('Keep this persona draft')
 })
 it('reloads both conversation sections without discarding other drafts', async () => {
  server.autonomous = defaultAutonomousConfig()
  const w = await mountView('zh', '/admin/qqbot/config?tab=autonomous')
  await w.get('[data-testid="autonomous-enabled"]').setValue(true)
  await w.get('[data-testid="knowledge-content"]').setValue('Unsaved guide')
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  server.site_knowledge = { enabled: true, content: 'Fresh guide' }
  await w.findAll('button').find(b => b.text().includes('重新加载本模块'))!.trigger('click'); await flushPromises()
  expect((w.get('[data-testid="autonomous-enabled"]').element as HTMLInputElement).checked).toBe(false)
  expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).value).toBe('Fresh guide')
  expect(w.get('#qqbot-settings-tab-autonomous').text()).not.toContain('•')
 })
 it('saves custom replies and preserves drafts on validation failure', async () => {
  const w = await mountView(); await category(w, '对话与指令'); await w.get('#qqbot-settings-tab-commands').trigger('click')
  const panel = w.get('#qqbot-settings-panel-commands'); await panel.findAll('button')[0]!.trigger('click')
  await panel.get('input[placeholder="a"]').setValue('签到'); await panel.get('textarea').setValue('固定回复')
  api.saveSection.mockRejectedValueOnce(new Error('与内置指令重名'))
  await w.get('form').trigger('submit'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toContain('重名')
  await panel.get('input[placeholder="a"]').setValue('faq'); await w.get('form').trigger('submit'); await flushPromises()
  expect(server.custom_commands).toEqual([{ command: '/faq', reply: '固定回复', enabled: true }])
  expect((panel.get('input[placeholder="a"]').element as HTMLInputElement).value).toBe('faq')
 })
 it.each(['zh', 'en'] as const)('%s supports missing legacy knowledge and saves it independently of chat drafts', async locale => {
  const w = await mountView(locale)
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  await w.get('#qqbot-settings-panel-chat textarea').setValue('Unsaved persona')
  await category(w, locale === 'zh' ? '对话与指令' : 'Conversations & commands')
  await w.get('#qqbot-settings-tab-autonomous').trigger('click')
  expect(w.get('#qqbot-settings-panel-autonomous').isVisible()).toBe(true)
  expect((w.get('[data-testid="knowledge-enabled"]').element as HTMLInputElement).checked).toBe(false)
  expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).value).toBe('')
  await w.get('[data-testid="knowledge-content"]').setValue('官网：https://example.com\n接入教程：在控制台创建 API Key。')
  await w.get('[data-testid="knowledge-enabled"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).toHaveBeenLastCalledWith('knowledge', 'knowledge-1', expect.objectContaining({ site_knowledge: { enabled: true, content: '官网：https://example.com\n接入教程：在控制台创建 API Key。' } }))
  expect(server.system_prompt).toBe('Helpful')
  expect(server.site_knowledge?.enabled).toBe(true)
  await category(w, locale === 'zh' ? '连接与模型' : 'Connections & models')
  await w.get('#qqbot-settings-tab-chat').trigger('click')
  expect((w.get('#qqbot-settings-panel-chat textarea').element as HTMLTextAreaElement).value).toBe('Unsaved persona')
 })
 it('validates enabled knowledge and counts supplementary characters once', async () => {
  const w = await mountView('zh', '/admin/qqbot/config?tab=knowledge')
  const content = w.get('[data-testid="knowledge-content"]')
  await w.get('[data-testid="knowledge-enabled"]').setValue(true)
  await content.setValue(' \n\t')
  const reportValidity = vi.spyOn(content.element as HTMLTextAreaElement, 'reportValidity')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(reportValidity).toHaveBeenCalledOnce()
  expect(api.saveSection).not.toHaveBeenCalled()
  await content.setValue('𠮷'.repeat(12001))
  expect(w.get('[data-testid="knowledge-count"]').text()).toContain('12001 / 12000 字')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).not.toHaveBeenCalled()
  await content.setValue('𠮷'.repeat(12000))
  expect(w.get('[data-testid="knowledge-count"]').text()).toContain('12000 / 12000 字')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.site_knowledge?.content).toBe('𠮷'.repeat(12000))
 })
 it('tries questions independently with saved configuration and retains knowledge drafts', async () => {
  server.site_knowledge = { enabled: true, content: 'Saved knowledge' }
  api.chatPreview.mockResolvedValue({ text: '试问回答', action: 'chat', input_tokens: 1, output_tokens: 1, duration_ms: 1, knowledge_sources: [] })
  const w = await mountView('zh', '/admin/qqbot/config?tab=knowledge')
  await w.get('[data-testid="knowledge-content"]').setValue('Unsaved knowledge')
  const preview = w.getComponent(QQBotChatPreview)
  expect(preview.props('config').site_knowledge.content).toBe('Saved knowledge')
  await w.get('[data-testid="qq-preview-group"]').setValue('group')
  await w.get('[data-testid="qq-preview-question"]').setValue('API 怎么接入？')
  await w.get('[data-testid="qq-preview-run"]').trigger('click'); await flushPromises()
  expect(api.chatPreview).toHaveBeenCalledWith({ group_id: 'group', member_id: undefined, question: 'API 怎么接入？' })
  expect(api.saveSection).not.toHaveBeenCalled()
  expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).value).toBe('Unsaved knowledge')
  expect(w.get('[data-testid="qq-preview-result"]').text()).toContain('试问回答')
 })
 it('retains knowledge text when disabled and keeps the draft on version conflicts', async () => {
  server.site_knowledge = { enabled: true, content: 'Existing site guide' }
  const w = await mountView('zh', '/admin/qqbot/config?tab=knowledge')
  await w.get('[data-testid="knowledge-enabled"]').setValue(false)
  api.saveSection.mockRejectedValueOnce(new Error('此模块已被其他页面更新'))
  await w.get('form').trigger('submit'); await flushPromises()
  expect(w.get('[role="alert"]').text()).toContain('其他页面')
  expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).value).toBe('Existing site guide')
  expect((w.get('[data-testid="knowledge-enabled"]').element as HTMLInputElement).checked).toBe(false)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.saveSection).toHaveBeenLastCalledWith('knowledge', 'knowledge-1', expect.anything())
  expect(server.site_knowledge).toEqual({ enabled: false, content: 'Existing site guide' })
 })
 it('restores knowledge history as an unsaved draft', async () => {
  server.site_knowledge = { enabled: true, content: 'Current site guide' }
  api.configHistory.mockResolvedValue([{ at: '2026-10-30T10:00:00+08:00', section: 'knowledge', before: { site_knowledge: { enabled: false, content: 'Previous site guide' } }, after: { site_knowledge: server.site_knowledge } }])
  const w = await mountView('zh', '/admin/qqbot/config?tab=knowledge')
  const history = w.findAll('details').find(details => details.get('summary').text() === '最近 30 次配置变更')!
  history.element.open = true; await history.trigger('toggle'); await flushPromises()
  await history.find('button').trigger('click'); await flushPromises()
  expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).value).toBe('Previous site guide')
  expect(server.site_knowledge?.content).toBe('Current site guide')
  expect(api.saveSection).not.toHaveBeenCalled()
  expect(w.get('#qqbot-settings-tab-autonomous').text()).toContain('•')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.site_knowledge).toEqual({ enabled: false, content: 'Previous site guide' })
 })
 it('shows and saves moderation only on the group page', async () => {
  const w = await mountView('zh', '/admin/qqbot/groups')
  expect(w.findAll('[role="tab"]')).toHaveLength(3)
  await w.get('#qqbot-settings-tab-moderation').trigger('click')
  await w.get('[data-testid="moderation-enabled"]').setValue(true)
  await w.findAll('[data-testid="moderation-group"]')[0]!.setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.moderation).toMatchObject({ enabled: true, groups: ['group'] })
 })
 it('preserves legacy moderation rules and exemptions without exposing report controls', async () => {
  api.config.mockResolvedValue({ ...cloneConfig(server), moderation: {
   enabled: true, groups: ['group'], repeat_window_seconds: 12, repeat_count: 6,
   burst_window_seconds: 8, burst_count: 9, mute_minutes: 7,
   repeat_offense_minutes: 45, escalated_mute_minutes: 60,
   exemptions: [{ group_id: 'group', member_id: 'existing-member' }]
  } })
  const w = await mountView('zh', '/admin/qqbot/groups')
  await w.get('#qqbot-settings-tab-moderation').trigger('click')
  expect(w.find('[data-testid="moderation-report-enabled"]').exists()).toBe(false)
  expect(w.find('[data-testid="moderation-report-mute-minutes"]').exists()).toBe(false)
  expect((w.get('[data-testid="moderation-repeat_count"]').element as HTMLInputElement).value).toBe('6')
  expect(w.get('[data-testid="moderation-exempt-member"]').text()).toContain('existing-member')
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.moderation).toMatchObject({ enabled: true, report_enabled: false, report_mute_minutes: 5,
   repeat_window_seconds: 12, repeat_count: 6, burst_window_seconds: 8, burst_count: 9,
   mute_minutes: 7, repeat_offense_minutes: 45, escalated_mute_minutes: 60,
   groups: ['group'], exemptions: [{ group_id: 'group', member_id: 'existing-member' }]
  })
 })
 it.each(['zh', 'en'] as const)('%s saves flood control independently of chat while preserving exemptions', async locale => {
  server.chat_disabled = true
  const w = await mountView(locale, '/admin/qqbot/groups')
  await w.get('#qqbot-settings-tab-moderation').trigger('click')
  expect(w.get('[data-testid="moderation-shared-groups"]').attributes('disabled')).toBeDefined()
  await w.get('[data-testid="moderation-enabled"]').setValue(true)
  expect(w.get('[data-testid="moderation-shared-groups"]').attributes('disabled')).toBeUndefined()
  expect(w.get('[data-testid="moderation-flood-fields"]').attributes('disabled')).toBeUndefined()
  await w.findAll('[data-testid="moderation-group"]')[0]!.setValue(true)
  await w.get('[data-testid="moderation-add-exemption"]').trigger('click')
  await flushPromises()
  await w.get('[data-testid="moderation-exempt-member"] [data-testid="qq-member-select"]').setValue(memberKey('group', 'trusted-member'))
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.moderation).toMatchObject({ enabled: true, report_enabled: false,
   groups: ['group'], exemptions: [{ group_id: 'group', member_id: 'trusted-member', nickname: 'Trusted member' }]
  })
  expect(server.chat_disabled).toBe(true)
  await w.get('[data-testid="moderation-enabled"]').setValue(false)
  expect(w.get('[data-testid="moderation-shared-exemptions"]').attributes('disabled')).toBeDefined()
  await w.get('form').trigger('submit'); await flushPromises()
  expect(server.moderation?.exemptions).toEqual([{ group_id: 'group', member_id: 'trusted-member', nickname: 'Trusted member' }])
  expect(server.moderation?.groups).toEqual(['group'])
 })
 it('does not expose retired report settings or enable flood controls for old report-only configs', async () => {
  server.moderation = { ...defaultModerationConfig(), report_enabled: true, groups: ['group'] }
  const w = await mountView('zh', '/admin/qqbot/groups')
  await w.get('#qqbot-settings-tab-moderation').trigger('click')
  expect(w.find('[data-testid="moderation-report-enabled"]').exists()).toBe(false)
  expect(w.find('[data-testid="moderation-report-mute-minutes"]').exists()).toBe(false)
  expect(w.get('[data-testid="moderation-flood-fields"]').attributes('disabled')).toBeDefined()
  expect(w.get('[data-testid="moderation-shared-groups"]').attributes('disabled')).toBeDefined()
  expect(w.get('[data-testid="moderation-shared-exemptions"]').attributes('disabled')).toBeDefined()
 })
  it.each(['zh', 'en'] as const)('%s selects group admins by recent messages and bound identity and supports removal', async locale => {
    server.allowed_groups = ['group', 'second-group']
    server.group_policies = [{ group_id: 'group', label: 'Primary group', disable_chat: false, disable_images: false, disable_activities: false, disable_notifications: false, chat_daily_limit: null, image_daily_limit: null }]
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    let wrapper = await mountView(locale, '/admin/qqbot/groups')
    await wrapper.get('#qqbot-settings-tab-group-admins').trigger('click')
    expect(wrapper.text()).toContain(i18n.global.t('qqbot.groupAdmins.help'))
    await wrapper.get('[data-testid="add-group-admin"]').trigger('click'); await flushPromises()
    let first = wrapper.findAll('[data-testid="group-admin-entry"]')[0]!
    expect(first.get('[data-testid="group-admin-group"]').text()).toContain('Primary group')
    expect(first.text()).toContain('我是小王，确认身份 4826')
    await first.get('[data-testid="qq-member-search"]').setValue('4826')
    await first.get('[data-testid="qq-member-search"]').trigger('keydown.enter'); await flushPromises()
    expect(api.members).toHaveBeenCalledWith(expect.objectContaining({ app_id: 'app', group_id: 'group', search: '4826' }))
    await first.get('[data-testid="qq-member-select"]').setValue(memberKey('group', 'recent-member'))
    await wrapper.get('[data-testid="add-group-admin"]').trigger('click'); await flushPromises()
    const second = wrapper.findAll('[data-testid="group-admin-entry"]')[1]!
    await second.get('[data-testid="group-admin-group"]').setValue('second-group'); await flushPromises()
    await second.get('[data-testid="qq-member-select"]').setValue(memberKey('second-group', 'bound-member'))
    expect(second.get('[data-testid="qq-member-selected"]').text()).toContain('alice@example.com')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    const saved = api.saveSection.mock.calls.at(-1)![2]
    expect(saved.group_admins).toEqual([
      { app_id: 'app', group_id: 'group', member_id: 'recent-member', nickname: '' },
      { app_id: 'app', group_id: 'second-group', member_id: 'bound-member', nickname: 'Alice' }
    ])
    expect(api.bindings).not.toHaveBeenCalled()
    wrapper.unmount()
    wrapper = await mountView(locale, '/admin/qqbot/groups')
    await wrapper.get('#qqbot-settings-tab-group-admins').trigger('click'); await flushPromises()
    expect(wrapper.findAll('[data-testid="group-admin-entry"]')).toHaveLength(2)
    first = wrapper.findAll('[data-testid="group-admin-entry"]')[0]!
    expect(first.get('[data-testid="qq-member-selected"]').text()).toContain('我是小王，确认身份 4826')
    await first.get('button').trigger('click')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.saveSection.mock.calls.at(-1)![2].group_admins).toEqual([saved.group_admins[1]])
  })


})
