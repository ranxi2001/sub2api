import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import AutoConfigHistory from '../AutoConfigHistory.vue'
import { getAutoConfigEvents, type AutoConfigEvent, type AutoConfigEventsPage } from '@/api/admin/autoConfig'

vi.mock('vue-i18n', async () => {
  const { default: autoConfig } = await import('@/i18n/locales/zh/autoConfig')
  return { useI18n: () => ({ t: (key: string, params: Record<string, unknown> = {}) => {
    const message = key.split('.').reduce<any>((value, part) => value?.[part], { autoConfig, common: { loading: '加载中' } })
    return typeof message === 'string' ? message.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? '')) : key
  } }) }
})
const state = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/api/admin/autoConfig', () => ({ getAutoConfigEvents: vi.fn() }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
const event = (id: number, kind: AutoConfigEvent['kind'] = 'concurrency_upgraded'): AutoConfigEvent => ({
  id, account_id: 7, account_name: 'OAuth example', platform: 'openai', kind, created_at: '2026-09-29T02:10:00Z',
  details: { priority: 1, load_factor: 10000, concurrency: 6, previous_concurrency: 5, cooldown_seconds: 60, group_ids: [3] }
})
let wrapper: VueWrapper | undefined
function render() {
  wrapper = mount(AutoConfigHistory, { props: { refreshKey: 0 } })
  return wrapper
}
beforeEach(() => {
  vi.resetAllMocks()
  state.auth = reactive({ user: { id: 1, role: 'admin' } })
  vi.mocked(getAutoConfigEvents).mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined })
describe('AutoConfigHistory', () => {
  it('shows only the applied degradation actions and reads legacy events as concurrency plus models', async () => {
    const legacy = event(13, 'degradation_applied')
    legacy.details.degradation = { revision:'legacy',models:['gpt-6-luna'],threshold_percent:50,degradation_percent:60,quality_samples:10,window_start:'start',window_end:'end',concurrency_cap:6 }
    const groupsOnly = event(14, 'degradation_applied')
    groupsOnly.details.degradation = { ...legacy.details.degradation,limit_concurrency:false,limit_models:false,limit_groups:true,models:null,group_ids:[5,6],removed_group_ids:[5] }
    vi.mocked(getAutoConfigEvents).mockResolvedValue({ items:[groupsOnly,legacy],has_more:false })
    const w = render(); await flushPromises()
    const rows = w.findAll('[data-testid=history-row]')
    expect(rows[0].text()).toContain('所选分组：#5 / #6；实际移出：#5')
    expect(rows[0].text()).not.toContain('并发 5 → 6')
    expect(rows[0].text()).not.toContain('模型：')
    expect(rows[1].text()).toContain('并发 5 → 6')
    expect(rows[1].text()).toContain('模型：gpt-6-luna')
  })
  it('renders initial mappings and the saved mapping template', async () => {
    const initial = event(11, 'initial_applied')
    initial.details.model_mapping = { 'gpt-5.4': 'gpt-5.5' }
    const saved = event(10, 'config_saved')
    saved.details.config = { enabled: false, platform: 'openai', priority: 50, load_factor: 1, concurrency: 3, group_ids: [], upgrade_enabled: false, upgrade_group_ids: [], successes_per_step: 20, upgrade_step: 1, max_concurrency: 100, cooldown_seconds: 60, revision: 'saved', model_mappings: [{ from: 'gpt-5.4', to: 'gpt-5.5' }] }
    vi.mocked(getAutoConfigEvents).mockResolvedValue({ items: [initial, saved], has_more: false })
    const w = render(); await flushPromises()
    expect(w.findAll('[data-testid=history-row]')[0].text()).toContain('模型映射：gpt-5.4 → gpt-5.5')
    expect(w.get('details').text()).toContain('模型映射')
    expect(w.get('details').text()).toContain('gpt-5.4 → gpt-5.5')
  })

  it('shows an honest empty state without invented history', async () => {
    const w = render(); await flushPromises()
    expect(w.get('[data-testid=history-empty]').text()).toContain('暂无自动配置日志')
    expect(w.text()).toContain('仅记录此功能上线后的操作')
    expect(getAutoConfigEvents).toHaveBeenCalledWith({ limit: 20, kind: '', before: undefined })
  })
  it('renders account identity, concurrency changes, cooldown and initial values', async () => {
    vi.mocked(getAutoConfigEvents).mockResolvedValue({ items: [event(8), event(7, 'failure_cooldown'), event(6, 'initial_applied')], has_more: false })
    const w = render(); await flushPromises()
    expect(w.findAll('[data-testid=history-row]')).toHaveLength(3)
    expect(w.text()).toContain('5 → 6')
    expect(w.text()).toContain('成功进度已清零')
    expect(w.text()).toContain('加入分组 #3')
    expect(w.text()).toContain('OAuth example')
    expect(w.text()).toContain('#7')
  })
  it('shows the complete saved rule and group snapshot, including zero priority', async () => {
    const saved = event(9, 'config_saved')
    saved.account_id = 0
    saved.details.config = { model_mappings: [{ from: 'gpt-5.4', to: 'gpt-6-luna' }], model_billing: { enabled: true, rules: [{ model: 'gpt-6-luna*', multiplier: 10, group_ids: [5,6] }, {model:'other',multiplier:2}] }, enabled: true, platform: 'openai', priority: 0, load_factor: 10000, concurrency: 5, group_ids: [3], upgrade_enabled: true, upgrade_group_ids: [5], successes_per_step: 20, upgrade_step: 2, max_concurrency: 100, cooldown_seconds: 60, revision: 'saved' }
    vi.mocked(getAutoConfigEvents).mockResolvedValue({ items: [saved], has_more: false })
    const w = render(); await flushPromises()
    expect(w.get('[data-testid=history-row]').text()).toContain('全局配置')
    expect(w.get('[data-testid=history-row]').text()).toContain('优先级 0')
    expect(w.get('details').text()).toContain('连续成功 20 次，提升 2 并发，上限 100，冷却 60 秒')
    expect(w.get('details').text()).toContain('模型计价：开启')
    expect(w.get('details').text()).toContain('gpt-6-luna* · 10×')
    expect(w.get('details').text()).toContain('适用分组（每条规则独立）：#5 / #6')
    expect(w.get('details').text()).toContain('other · 2× · 全部分组')
    expect(w.get('details').text()).toContain('gpt-5.4 → gpt-6-luna')
    expect(w.get('details').text()).toContain('首次加入分组：#3')
    expect(w.get('details').text()).toContain('允许升级的账号分组：#5')
  })
  it('uses a cursor for older events and refresh replaces the list', async () => {
    vi.mocked(getAutoConfigEvents).mockResolvedValueOnce({ items: [event(8)], has_more: true }).mockResolvedValueOnce({ items: [event(7)], has_more: false }).mockResolvedValueOnce({ items: [event(9)], has_more: true })
    const w = render(); await flushPromises()
    await w.get('[data-testid=history-more]').trigger('click'); await flushPromises()
    expect(getAutoConfigEvents).toHaveBeenLastCalledWith({ limit: 20, kind: '', before: 8 })
    expect(w.findAll('[data-testid=history-row]')).toHaveLength(2)
    await w.get('[data-testid=history-refresh]').trigger('click'); await flushPromises()
    expect(getAutoConfigEvents).toHaveBeenLastCalledWith({ limit: 20, kind: '', before: undefined })
    expect(w.findAll('[data-testid=history-row]')).toHaveLength(1)
  })
  it('ignores an older pending response after changing filter', async () => {
    let resolve!: (page: AutoConfigEventsPage) => void
    vi.mocked(getAutoConfigEvents).mockReturnValueOnce(new Promise(r => { resolve = r })).mockResolvedValueOnce({ items: [event(7, 'failure_cooldown')], has_more: false })
    const w = render()
    await w.get('[data-testid=history-kind]').setValue('failure_cooldown'); await flushPromises()
    resolve({ items: [event(8)], has_more: true }); await flushPromises()
    expect(getAutoConfigEvents).toHaveBeenLastCalledWith({ limit: 20, kind: 'failure_cooldown', before: undefined })
    expect(w.get('[data-testid=history-row]').text()).toContain('失败冷却')
    expect(w.find('[data-testid=history-more]').exists()).toBe(false)
  })
  it('preserves loaded rows after a pagination error and retries that cursor', async () => {
    vi.mocked(getAutoConfigEvents).mockResolvedValueOnce({ items: [event(8)], has_more: true }).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ items: [event(7)], has_more: false })
    const w = render(); await flushPromises()
    await w.get('[data-testid=history-more]').trigger('click'); await flushPromises()
    expect(w.findAll('[data-testid=history-row]')).toHaveLength(1)
    expect(w.get('[role=alert]').text()).toContain('日志加载失败')
    await w.get('[data-testid=history-retry]').trigger('click'); await flushPromises()
    expect(getAutoConfigEvents).toHaveBeenLastCalledWith({ limit: 20, kind: '', before: 8 })
    expect(w.findAll('[data-testid=history-row]')).toHaveLength(2)
  })
  it('refreshes after a successful save signal without changing the filter', async () => {
    const w = render(); await flushPromises()
    await w.get('[data-testid=history-kind]').setValue('config_saved'); await flushPromises()
    await w.setProps({ refreshKey: 1 }); await flushPromises()
    expect(getAutoConfigEvents).toHaveBeenCalledTimes(3)
    expect(getAutoConfigEvents).toHaveBeenLastCalledWith({ limit: 20, kind: 'config_saved', before: undefined })
  })
  it('clears private history and discards pending responses after logout', async () => {
    let resolve!: (page: AutoConfigEventsPage) => void
    vi.mocked(getAutoConfigEvents).mockReturnValueOnce(new Promise(r => { resolve = r }))
    const w = render(); state.auth.user = null
    resolve({ items: [event(8)], has_more: true }); await flushPromises()
    expect(w.findAll('[data-testid=history-row]')).toHaveLength(0)
    expect(getAutoConfigEvents).toHaveBeenCalledTimes(1)
  })
  it('does not fetch logs for a non-admin', async () => {
    state.auth.user.role = 'user'; render(); await flushPromises()
    expect(getAutoConfigEvents).not.toHaveBeenCalled()
  })
})

it('shows HTTP evidence without presenting absent probe verdicts as a threshold breach', async () => {
 const e=event(15,'degradation_applied')
 e.details.degradation={revision:'fixture',models:[],threshold_percent:9,degradation_percent:0,quality_samples:0,window_start:'start',window_end:'end',concurrency_cap:6,trigger_reason:'upstream_429',upstream_error:{source:'process_local_http_responses',total:25,failures:20,rate_limited:16,percent:80,observed_since:'start',window_complete:false},upstream_error_threshold_percent:80,upstream_429_share_threshold_percent:80}
 vi.mocked(getAutoConfigEvents).mockResolvedValue({items:[e],has_more:false})
 const w=render();await flushPromises()
 expect(w.text()).toContain('错误 20/25');expect(w.text()).toContain('429 16/20')
 expect(w.text()).not.toContain('0% ≥ 9%')
})
