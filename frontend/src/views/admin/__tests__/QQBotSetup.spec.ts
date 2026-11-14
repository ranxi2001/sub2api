import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotSetupView from '../QQBotSetupView.vue'
import QQBotDiagnostics from '@/components/admin/qqbot/QQBotDiagnostics.vue'
import QQBotActivityTemplates from '@/components/admin/qqbot/QQBotActivityTemplates.vue'
import { configSections, pickSection, cloneConfig, type ConfigSection } from '@/components/admin/qqbot/configSections'
import type { QQBotConfig, QQSetupRequest } from '@/api/qqbot'
const api = vi.hoisted(() => ({ config: vi.fn(), runtime: vi.fn(), saveSetup: vi.fn(), setupOptions: vi.fn(), setupChecks: vi.fn(), setupProbe: vi.fn(), activityTemplates: vi.fn(), applyActivityTemplate: vi.fn(), saveSection: vi.fn() }))
vi.mock('@/api/qqbot', async original => ({ ...await original<typeof import('@/api/qqbot')>(), qqbotAPI: api }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
let server: QQBotConfig
const wrappers: VueWrapper[] = []
const globals = () => ({ plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh, en } })], stubs: { RouterLink: { template: '<a><slot /></a>' } } })
async function setup() {
 const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/admin/qqbot/setup', component: QQBotSetupView }, { path: '/:pathMatch(.*)*', component: { template: '<p>Other</p>' } }] })
 await router.push('/admin/qqbot/setup')
 const wrapper = mount({ template: '<RouterView />' }, { attachTo: document.body, global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh, en } }), router], stubs: { teleport: true } } })
 wrappers.push(wrapper); await flushPromises(); return { wrapper, router }
}
async function next(w: VueWrapper) { await w.get('form').trigger('submit'); await flushPromises() }
beforeEach(() => {
 vi.resetAllMocks()
 vi.spyOn(window, 'confirm').mockReturnValue(true)
 server = { enabled: false, app_id: '', app_secret: '', app_secret_set: false, sandbox: false, allowed_groups: [], base_url: '', api_key: '', api_key_set: false, chat_model: '', chat_protocol: 'chat_completions', image_enabled: false, image_base_url: '', image_api_key: '', image_api_key_set: false, image_model: '', image_size: '1024x1024', system_prompt: '', max_output_tokens: 2048, history_messages: 12, daily_chat_limit: 30, daily_image_limit: 2, total_daily_chat_limit: 500, total_daily_image_limit: 20, cooldown_seconds: 5, checkin_reward: 0, invite_enabled: false, invite_reward: 0, invite_daily_limit: 10, custom_commands: [], section_versions: Object.fromEntries(Object.keys(configSections).map(key => [key, key + '-1'])) }
 api.config.mockImplementation(async () => cloneConfig(server))
 api.runtime.mockResolvedValue({ groups: [{ id: 'group' }] })
 api.setupOptions.mockResolvedValue({ groups: [{ id: 2, name: 'Test group', models: ['model'] }], image_groups: [{ id: 20, name: 'Image group', models: ['image-model'] }], current_quota: 0, current_used: 0, current_image_quota: 0, current_image_used: 0 })
 api.saveSetup.mockImplementation(async (req: QQSetupRequest) => { for (const section of req.sections as ConfigSection[]) Object.assign(server, pickSection(req.config, section)); if (server.app_secret) server.app_secret_set = true; server.app_secret = ''; if (server.ai_connection === 'site') { server.api_key_set = true; server.site_api_key_id = 101 } return cloneConfig(server) })
 api.setupChecks.mockResolvedValue({ ready: false, checks: [{ code: 'probe_qq', status: 'pending', message: 'QQ', action: '' }, { code: 'probe_chat_flow', status: 'pending', message: 'AI', action: '' }], version: '1' })
 api.setupProbe.mockResolvedValue({ kind: 'qq', status: 'ready', message: 'OK', at: '' })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks() })
describe('QQ quick setup', () => {
 it('saves credentials first, configures AI, then requires checks and review before activation', async () => {
  const { wrapper: w } = await setup()
  await w.get('[data-testid="setup-app-id"]').setValue('app'); await w.get('[data-testid="setup-secret"]').setValue('secret'); await next(w)
  expect(api.saveSetup.mock.calls[0]![0].sections).toEqual(['connection']); expect(api.saveSetup.mock.calls[0]![0].config.enabled).toBe(false)
  expect(w.text()).not.toContain('secret')
  await w.get('fieldset input[type="checkbox"]').setValue(true)
  await w.get('[data-testid="site-group"]').setValue('2')
  await w.get('[data-testid="setup-chat-model"] button').trigger('click'); await flushPromises()
  await w.get('[role="option"]').trigger('click'); await flushPromises(); await next(w)
  await next(w)
  expect(api.saveSetup.mock.calls[1]![0].sections).toEqual(expect.arrayContaining(['groups', 'chat', 'autonomous', 'moderation']))
  expect(w.get('[data-testid="setup-enable"]').attributes('disabled')).toBeDefined()
  api.setupChecks.mockResolvedValue({ ready: true, checks: [{ code: 'probe_qq', status: 'ready', message: 'OK', action: '' }], version: '2' })
  await w.findComponent(QQBotDiagnostics).findAll('button')[0]!.trigger('click'); await flushPromises()
  expect(w.get('[data-testid="setup-enable"]').attributes('disabled')).toBeDefined()
  await w.get('input[type="checkbox"]').setValue(true); await w.get('[data-testid="setup-enable"]').trigger('click'); await flushPromises()
  expect(api.saveSetup.mock.calls.at(-1)![0].sections).toEqual(['connection']); expect(server.enabled).toBe(true)
 })
 it('saves a manually entered site model from the searchable dropdown', async () => {
  const { wrapper: w } = await setup()
  await w.get('[data-testid="setup-app-id"]').setValue('app'); await w.get('[data-testid="setup-secret"]').setValue('secret'); await next(w)
  await w.get('fieldset input[type="checkbox"]').setValue(true)
  await w.get('[data-testid="site-group"]').setValue('2')
  await w.get('[data-testid="setup-chat-model"] button').trigger('click'); await flushPromises()
  await w.get('.select-search-input').setValue('custom-vision-model'); await flushPromises()
  await w.get('[role="option"]').trigger('click'); await flushPromises(); await next(w)
  await next(w)
  expect(api.saveSetup.mock.calls[1]![0].config.chat_model).toBe('custom-vision-model')
  expect(server.chat_model).toBe('custom-vision-model')
 })
 it('keeps the AI step open when the selected model is cleared', async () => {
  const { wrapper: w } = await setup()
  await w.get('[data-testid="setup-app-id"]').setValue('app'); await w.get('[data-testid="setup-secret"]').setValue('secret'); await next(w)
  await w.get('fieldset input[type="checkbox"]').setValue(true)
  await w.get('[data-testid="site-group"]').setValue('2')
  await w.get('[data-testid="setup-chat-model"] button').trigger('click'); await flushPromises()
  await w.get('[role="option"]').trigger('click'); await flushPromises()
  await w.get('[aria-label="Clear selection"]').trigger('click'); await next(w)
  expect(w.get('[role="alert"]').text()).toContain('1～200')
  expect(w.find('[data-testid="site-group"]').exists()).toBe(true)
  expect(api.saveSetup).toHaveBeenCalledTimes(1)
 })
 it('configures images independently without selecting a chat model or chat group', async () => {
  server.chat_disabled = true
  const { wrapper: w } = await setup()
  await w.get('[data-testid="setup-app-id"]').setValue('app'); await w.get('[data-testid="setup-secret"]').setValue('secret'); await next(w)
  await w.get('fieldset input[type="checkbox"]').setValue(true)
  expect(w.find('[data-testid="site-group"]').exists()).toBe(false)
  await next(w)
  await w.findAll('section > label input[type="checkbox"]')[1]!.setValue(true)
  await w.get('[data-testid="image-ai-source"]').setValue('site')
  await w.get('[data-testid="image-site-group"]').setValue('20')
  await w.get('[data-testid="image-site-quota"]').setValue('30')
  await w.get('[data-testid="image-model"] button').trigger('click'); await flushPromises()
  await w.get('[role="option"]').trigger('click'); await next(w)
  const request = api.saveSetup.mock.calls[1]![0] as QQSetupRequest
  expect(request.config.chat_disabled).toBe(true)
  expect(request.config.chat_model).toBe('')
  expect(request.config.site_group_id).toBeUndefined()
  expect(request.config.ai_connection).toBe('external')
  expect(request.config.image_ai_connection).toBe('site')
  expect(request.config.image_site_group_id).toBe(20)
  expect(request.config.image_model).toBe('image-model')
  expect(request.image_site_quota).toBe(30)
  expect(request.regenerate_image_site_key).toBe(false)
  expect(request.sections).toContain('images')
 })
 it('retains credentials and step when saving fails, and blocks navigation with an unsaved draft', async () => {
  const { wrapper: w, router } = await setup(); await w.get('[data-testid="setup-app-id"]').setValue('app'); await w.get('[data-testid="setup-secret"]').setValue('secret')
  api.saveSetup.mockRejectedValue(new Error('version conflict')); await next(w)
  expect((w.get('[data-testid="setup-secret"]').element as HTMLInputElement).value).toBe('secret'); expect(w.get('[role="alert"]').text()).toContain('version conflict')
  vi.mocked(window.confirm).mockReturnValue(false); await router.push('/other'); expect(router.currentRoute.value.path).toBe('/admin/qqbot/setup')
 })
 it('never silently disables an already enabled installation', async () => {
  server.enabled = true; server.app_id = 'app'; server.app_secret_set = true; server.allowed_groups = ['group']; server.chat_model = 'model'; server.base_url = 'https://example.invalid/v1'; server.api_key_set = true
  const { wrapper: w } = await setup(); await next(w); expect(api.saveSetup.mock.calls[0]![0].config.enabled).toBe(true)
 })
})
describe('QQ diagnostics and presets', () => {
 it('stops a paid batch at the first blocked result without retrying', async () => {
  const w = mount(QQBotDiagnostics, { global: globals() }); wrappers.push(w); await flushPromises()
  api.setupProbe.mockResolvedValue({ kind: 'qq', status: 'blocked', message: 'denied', at: '' })
  await w.findAll('button').find(b => b.text() === '测试未通过的能力')!.trigger('click'); await flushPromises()
  expect(api.setupProbe).toHaveBeenCalledTimes(1); expect(api.setupProbe).toHaveBeenCalledWith('qq'); expect(window.confirm).toHaveBeenCalledTimes(1)
 })
 it('does not submit a probe when cost confirmation is declined', async () => {
  const w = mount(QQBotDiagnostics, { global: globals() }); wrappers.push(w); await flushPromises(); vi.mocked(window.confirm).mockReturnValue(false)
  await w.findAll('button').find(b => b.text() === '测试未通过的能力')!.trigger('click'); expect(api.setupProbe).not.toHaveBeenCalled()
 })
 it('keeps selected preset and budget after a conflict without invalidating other drafts', async () => {
  const bundle = { version: 'v1', budget: 1, checkin: { min_amount: .01, max_amount: .05 }, lottery: { enabled: false }, invite: { enabled: false }, ranking: { rewards_enabled: false } }
  api.activityTemplates.mockResolvedValue({ current: bundle, light: bundle, active: bundle }); api.applyActivityTemplate.mockRejectedValue(new Error('version conflict'))
  const w = mount(QQBotActivityTemplates, { props: { beforeApply: () => true }, global: globals() }); wrappers.push(w); await flushPromises()
  await w.get('[data-testid="reward-budget"]').setValue('5'); await w.findAll('input[type="checkbox"]').at(-1)!.setValue(true); await w.get('[data-testid="apply-template"]').trigger('click'); await flushPromises()
  expect(api.applyActivityTemplate).toHaveBeenCalledWith({ version: 'v1', template: 'light', budget: 5, confirmed: true }); expect(w.emitted('applied')).toBeUndefined(); expect((w.get('[data-testid="reward-budget"]').element as HTMLInputElement).value).toBe('5'); expect(w.get('[role="alert"]').text()).toContain('version conflict')
 })
})
