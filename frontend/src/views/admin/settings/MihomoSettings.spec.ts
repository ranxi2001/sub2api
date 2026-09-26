import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import MihomoSettings from './MihomoSettings.vue'
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: { value: 'zh' } }) }))
const base = { installed: false, supported: true, running: false, busy: false, nodes: 0, subscriptions: 0, phase: 'not_installed', endpoint: 'http://127.0.0.1:3101' }
describe('Mihomo settings', () => {
  it('shows subscription names but uses stable IDs for node actions', async () => {
    const state = { ...base, installed: true, running: true, nodes: 1, node_states: [{ name: 'node-hash', display_name: '日本 东京 01', state: 'enabled' }] }
    get.mockResolvedValue({ data: state }); post.mockResolvedValue({ data: state })
    const wrapper = mount(MihomoSettings)
    try {
      await flushPromises()
      expect(wrapper.text()).toContain('日本 东京 01')
      expect(wrapper.text()).not.toContain('node-hash')
      await wrapper.findAll('button').find(b => b.text() === '停用')!.trigger('click')
      await flushPromises()
      expect(post).toHaveBeenCalledWith('/admin/system/mihomo', expect.objectContaining({ action: 'disable/node-hash' }))
    } finally { wrapper.unmount() }
  })
  it('saves a country filter separately and preserves the subscription draft', async () => {
    const state={...base,installed:true,running:true,nodes:1,country_filter:{mode:'off',codes:[],allow_unknown:false},country_codes:['HK','US'],node_states:[{name:'node-one',state:'enabled',country_code:'HK'}]}
    get.mockResolvedValue({data:state});post.mockResolvedValue({data:state})
    const wrapper=mount(MihomoSettings);await flushPromises()
    await wrapper.get('textarea').setValue('https://example.org/unsaved')
    await wrapper.findAll('button').find(b=>b.text().includes('快捷'))!.trigger('click')
    await wrapper.findAll('button').find(b=>b.text()==='保存地区规则')!.trigger('click');await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/system/mihomo',expect.objectContaining({action:'country_filter',subscriptions:[],country_filter:{mode:'exclude',codes:['HK'],allow_unknown:false,dynamic_provider_managed:false}}))
    expect(wrapper.get<HTMLTextAreaElement>('textarea').element.value).toBe('https://example.org/unsaved')
    wrapper.unmount()
  })
  it('explicitly enables use-once without replacing subscriptions', async () => {
    get.mockResolvedValue({data:{...base,installed:true,running:true,nodes:1,use_once:false,node_states:[{name:'node-one',state:'enabled'}]}})
    post.mockResolvedValue({data:{...base,installed:true,running:true,use_once:true}})
    const wrapper=mount(MihomoSettings);await flushPromises()
    await wrapper.get('textarea').setValue('https://example.org/unsaved')
    await wrapper.get('details input[type="checkbox"]').setValue(true);await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/system/mihomo',expect.objectContaining({action:'once_on',subscriptions:[]}))
    expect(wrapper.get<HTMLTextAreaElement>('textarea').element.value).toBe('https://example.org/unsaved')
    wrapper.unmount()
  })
  beforeEach(() => { vi.resetAllMocks(); get.mockResolvedValue({ data: base }); post.mockResolvedValue({ data: base }) })
  it('installs only after explicit action and does not emit an unready proxy', async () => {
    const wrapper = mount(MihomoSettings); await flushPromises()
    expect(post).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(b => b.text() === '检测并安装')!.trigger('click'); await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/system/mihomo', expect.objectContaining({ action: 'install' }))
    expect(wrapper.emitted('ready')).toBeUndefined(); wrapper.unmount()
  })
  it('submits multiple subscriptions without putting them in status output', async () => {
    get.mockResolvedValue({ data: { ...base, installed: true } })
    const wrapper = mount(MihomoSettings); await flushPromises()
    await wrapper.get('textarea').setValue('https://example.org/a?token=secret\nhttps://example.org/b')
    await wrapper.findAll('button').find(b => b.text() === '保存并应用')!.trigger('click'); await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/system/mihomo', expect.objectContaining({ action: 'apply', subscriptions: ['https://example.org/a?token=secret', 'https://example.org/b'] }))
    expect(wrapper.get<HTMLTextAreaElement>('textarea').element.value).toBe(''); wrapper.unmount()
  })
  it('applies dynamic proxies without sending them through the subscription URL field', async () => {
    get.mockResolvedValue({ data: { ...base, installed: true, running: true } })
    const wrapper = mount(MihomoSettings); await flushPromises()
    await wrapper.get('#mihomo-dynamic-proxies').setValue('user:pass@proxy.example:2000\nuser2:pass2@proxy.example:2001')
    await wrapper.findAll('button').find(b => b.text() === '应用动态代理')!.trigger('click'); await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/system/mihomo', expect.objectContaining({
      action: 'apply_dynamic',
      subscriptions: [],
      dynamic_proxies: ['http://user:pass@proxy.example:2000', 'http://user2:pass2@proxy.example:2001']
    }))
    expect(wrapper.get<HTMLTextAreaElement>('#mihomo-dynamic-proxies').element.value).toBe('')
    wrapper.unmount()
  })
  it('uses the selected protocol while preserving explicit prefixes and failed drafts', async () => {
    get.mockResolvedValue({ data: { ...base, installed: true, running: true } })
    post.mockRejectedValue({ message: 'dynamic proxy line 2: invalid proxy format' })
    const wrapper = mount(MihomoSettings); await flushPromises()
    await wrapper.get('#mihomo-dynamic-protocol').setValue('socks5')
    const draft = 'proxy.example:2000:user:pass\nhttps://user:pass@proxy.example:2001'
    await wrapper.get('#mihomo-dynamic-proxies').setValue(draft)
    await wrapper.findAll('button').find(b => b.text() === '应用动态代理')!.trigger('click'); await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/system/mihomo', expect.objectContaining({ dynamic_proxies: ['socks5://proxy.example:2000:user:pass', 'https://user:pass@proxy.example:2001'] }))
    expect(wrapper.text()).toContain('dynamic proxy line 2: invalid proxy format')
    expect(wrapper.get<HTMLTextAreaElement>('#mihomo-dynamic-proxies').element.value).toBe(draft)
    await wrapper.findAll('button').find(b => b.text() === '检测状态')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).not.toContain('dynamic proxy line 2: invalid proxy format')
    wrapper.unmount()
  })
  it('does not offer installation when status loading fails', async () => {
    get.mockRejectedValue(new Error('network'))
    const wrapper = mount(MihomoSettings); await flushPromises()
    expect(wrapper.text()).toContain('无法读取内核状态')
    expect(wrapper.text()).not.toContain('检测并安装')
    wrapper.unmount()
  })
})

it('shows warm target and shortage reuse without probing from the UI', async () => {
  get.mockResolvedValue({data:{...base,bps_warm_pool:{target:8,ready:2,checking:1,cooling:3,failure_reasons:{bps_access_denied:3}}}})
  const wrapper=mount(MihomoSettings)
  try {
    await flushPromises()
    const pool=wrapper.get('[data-testid="bps-warm-pool"]')
    expect(pool.text()).toContain('目标 8')
    expect(pool.text()).toContain('就绪 2')
    expect(pool.text()).toContain('不足时复用就绪出口')
    expect(pool.text()).toContain('bps_access_denied: 3')
  } finally { wrapper.unmount() }
})
