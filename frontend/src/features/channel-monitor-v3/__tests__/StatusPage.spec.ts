import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import StatusPage from '../StatusPage.vue'
import type { MonitorV3Cell, MonitorV3ComponentStatus, MonitorV3StatusPage } from '@/api/channelMonitorV3'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => key.startsWith('channelMonitorV3.errors.') && !key.endsWith('.mystery'),
  }),
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ siteName: 'Demo Relay' }) }))

const cell = (status: MonitorV3Cell['status'], extra: Partial<MonitorV3Cell> = {}): MonitorV3Cell => ({
  start: '2026-10-04T07:35:00Z', status, success_rate: status === 'down' ? 0.7 : 0.99, ttft_p50_ms: 2000, ...extra,
})

function component(id: number, name: string, extra: Partial<MonitorV3ComponentStatus> = {}): MonitorV3ComponentStatus {
  return {
    id, name, multiplier: 0.2, status: 'operational', availability: 97.46, last_data_at: '2026-10-04T09:30:00Z',
    cells: [cell('operational'), null, cell('degraded', { ttft_p50_ms: 15000 }), cell('down', { top_error: 'upstream_5xx' })],
    ...extra,
  }
}

function page(extra: Partial<MonitorV3StatusPage> = {}): MonitorV3StatusPage {
  return {
    generated_at: '2026-10-04T09:31:20Z', interval_minutes: 5, cells: 4, availability_range: '7d',
    availability_since: '2026-09-27T09:00:00Z', down_error_rate: 0.2, degraded_error_rate: 0.05, degraded_ttft_ms: 10000,
    min_requests: 3, data_through: '2026-10-04T09:30:00Z', footer_note: '',
    window: { start: '2026-10-04T09:15:00Z', end: '2026-10-04T09:35:00Z', latest: true, has_older: true },
    featured: component(1, 'Claude Max', { multiplier: 0.9 }),
    categories: [
      { id: 1, name: 'GPT', description: 'OpenAI groups', availability: 88.6, components: [component(2, 'Codex'), component(3, 'Codex Plus', { multiplier: null, status: 'down' })] },
      { id: 2, name: 'Claude', availability: 80.66, components: [component(4, 'Kiro', { model: 'claude-opus-5-5' })] },
      { id: 3, name: 'Grok', availability: 85.37, components: [component(5, 'Grok heavy')] },
    ],
    open_incidents: 2,
    ...extra,
  }
}

const mounted: Array<ReturnType<typeof mount>> = []
function render(status: MonitorV3StatusPage | null, loading = false) {
  const wrapper = mount(StatusPage, { props: { status, loading }, attachTo: document.body, global: { stubs: { Icon: true } } })
  mounted.push(wrapper)
  return wrapper
}
const tooltip = () => document.body.querySelector('[data-testid="monitor-v3-tooltip"]')

afterEach(() => {
  while (mounted.length) mounted.pop()!.unmount()
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('V3 status page', () => {
  it('renders the featured card, categories, components and aligned bars', () => {
    const wrapper = render(page())
    const featured = wrapper.get('[data-testid="monitor-v3-featured"]')
    expect(featured.text()).toContain('Claude Max')
    expect(featured.text()).toContain('×0.9')
    expect(featured.text()).toContain('channelMonitorV3.page.availabilityRange.r7d')
    expect(featured.get('[data-testid="monitor-v3-featured-availability"]').text()).toBe('97.46%')
    expect(featured.get('[data-testid="monitor-v3-featured-status"]').text()).toBe('channelMonitorV3.headline.operational')
    expect(featured.get('[data-testid="monitor-v3-data-through"]').text()).toContain('channelMonitorV3.page.dataThrough')
    expect(featured.text()).not.toContain('channelMonitorV3.page.requestCount')

    const categories = wrapper.findAll('[data-testid="monitor-v3-category"]')
    expect(categories).toHaveLength(3)
    expect(categories[2].classes()).toContain('xl:col-span-2')
    expect(categories[0].classes()).not.toContain('xl:col-span-2')
    expect(categories[0].text()).toContain('channelMonitorV3.page.componentCount:{"count":2}')
    expect(categories[0].text()).toContain('88.6%')

    const rows = wrapper.findAll('[data-testid="monitor-v3-component"]')
    expect(rows).toHaveLength(4)
    expect(rows[0].text()).toContain('×0.2')
    expect(rows[1].text()).not.toContain('×')
    expect(rows[1].get('[data-testid="monitor-v3-status-dot"]').attributes('data-status')).toBe('down')
    const bar = rows[0].get('[data-testid="monitor-v3-bar"]')
    expect(bar.findAll('[data-testid="monitor-v3-cell"]').map((item) => item.attributes('data-status'))).toEqual(['operational', 'degraded', 'down'])
    expect(bar.findAll('[data-testid="monitor-v3-cell-empty"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="monitor-v3-rules"]').attributes('title')).toContain('"down":"20%"')
  })

  it('explains a slot from its traffic and shows volumes only when the server sent them', async () => {
    const status = page()
    status.categories[0].components[0].cells[3] = cell('down', { top_error: 'upstream_5xx', requests: 40, errors: 12, ignored_errors: 3 })
    const wrapper = render(status)
    const cells = wrapper.findAll('[data-testid="monitor-v3-component"]')[0].findAll('[data-testid="monitor-v3-cell"]')
    await cells[2].trigger('mouseenter')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.status.down')
    expect(tooltip()!.textContent).toContain('"value":"70%"')
    expect(tooltip()!.textContent).toContain('≤ 2s')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.tooltip.error')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.errors.upstream_5xx')
    expect(tooltip()!.querySelector('[data-testid="monitor-v3-tooltip-volume"]')!.textContent).toContain('"requests":40')

    await wrapper.findAll('[data-testid="monitor-v3-component"]')[1].findAll('[data-testid="monitor-v3-cell"]')[2].trigger('focus')
    expect(tooltip()!.querySelector('[data-testid="monitor-v3-tooltip-volume"]')).toBeNull()

    await cells[1].trigger('click')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.tooltip.note')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.tooltip.slow')
    expect(tooltip()!.textContent).toContain('≤ 15s')

    await cells[0].trigger('click')
    expect(tooltip()!.querySelector('[data-testid="monitor-v3-tooltip-note"]')).toBeNull()

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(tooltip()).toBeNull()
  })

  it('shows quiet slots as not judged and names the model filter', async () => {
    const status = page()
    status.categories[1].components[0].cells[0] = cell('insufficient')
    const wrapper = render(status)
    const kiro = wrapper.findAll('[data-testid="monitor-v3-component"]')[2]
    await kiro.findAll('[data-testid="monitor-v3-cell"]')[0].trigger('mouseenter')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.tooltip.insufficient:{"min":3}')
    expect(tooltip()!.textContent).toContain('claude-opus-5-5')
  })

  it('treats the slot still filling up as counting, not quiet', async () => {
    const status = page()
    status.categories[0].components[0].cells[3] = cell('insufficient')
    let wrapper = render(status)
    let last = wrapper.findAll('[data-testid="monitor-v3-component"]')[0].findAll('[data-testid="monitor-v3-cell"]').at(-1)!
    expect(last.classes()).toContain('animate-pulse')
    await last.trigger('mouseenter')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.page.collecting')
    expect(tooltip()!.textContent).not.toContain('channelMonitorV3.tooltip.insufficient')
    wrapper.unmount()
    mounted.pop()

    status.window = { ...status.window, latest: false }
    wrapper = render(status)
    last = wrapper.findAll('[data-testid="monitor-v3-component"]')[0].findAll('[data-testid="monitor-v3-cell"]').at(-1)!
    expect(last.classes()).not.toContain('animate-pulse')
    await last.trigger('mouseenter')
    expect(tooltip()!.textContent).toContain('channelMonitorV3.tooltip.insufficient')
  })

  it('closes a hovered tooltip after the pointer leaves', async () => {
    vi.useFakeTimers()
    const wrapper = render(page())
    const target = wrapper.findAll('[data-testid="monitor-v3-cell"]')[0]
    await target.trigger('mouseenter')
    expect(tooltip()).not.toBeNull()
    await target.trigger('mouseleave')
    vi.advanceTimersByTime(200)
    await wrapper.vm.$nextTick()
    expect(tooltip()).toBeNull()
  })

  it('pages by whole windows and returns to the latest', async () => {
    const wrapper = render(page())
    await wrapper.get('[data-testid="monitor-v3-older"]').trigger('click')
    const start = Date.parse('2026-10-04T09:15:00Z') / 1000
    expect(wrapper.emitted('navigate')![0]).toEqual([start - 300])
    expect(wrapper.get('[data-testid="monitor-v3-newer"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="monitor-v3-latest"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="monitor-v3-cell-empty"]').some((item) => item.classes().includes('animate-pulse'))).toBe(false)

    await wrapper.setProps({ status: page({ window: { start: '2026-10-04T08:55:00Z', end: '2026-10-04T09:15:00Z', latest: false, has_older: false } }) })
    expect(wrapper.get('[data-testid="monitor-v3-older"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="monitor-v3-newer"]').trigger('click')
    const end = Date.parse('2026-10-04T09:15:00Z') / 1000
    expect(wrapper.emitted('navigate')![1]).toEqual([end + 3 * 300])
    await wrapper.get('[data-testid="monitor-v3-latest"]').trigger('click')
    expect(wrapper.emitted('navigate')![2]).toEqual([null])
  })

  it('pulses the slot still being counted on the live window', () => {
    const status = page()
    status.categories[0].components[0].cells[3] = null
    const wrapper = render(status)
    const empty = wrapper.findAll('[data-testid="monitor-v3-component"]')[0].findAll('[data-testid="monitor-v3-cell-empty"]')
    expect(empty.at(-1)!.classes()).toContain('animate-pulse')
    expect(empty[0].classes()).not.toContain('animate-pulse')
  })

  it('collapses a category and keeps its header', async () => {
    const wrapper = render(page())
    const first = wrapper.findAll('[data-testid="monitor-v3-category"]')[0]
    await first.get('[data-testid="monitor-v3-category-toggle"]').trigger('click')
    expect(first.get('[data-testid="monitor-v3-category-toggle"]').attributes('aria-expanded')).toBe('false')
    expect(first.findAll('[data-testid="monitor-v3-component"]').every((row) => !row.isVisible())).toBe(true)
  })

  it('renders the footer, incident badge and empty states', async () => {
    const wrapper = render(page({ footer_note: 'Measured by us', featured: null, categories: [], data_through: null }))
    expect(wrapper.find('[data-testid="monitor-v3-featured"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="monitor-v3-empty"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="monitor-v3-footer-note"]').text()).toBe('Measured by us')
    expect(wrapper.text()).toContain('Demo Relay')
    await wrapper.get('[data-testid="monitor-v3-incidents"]').trigger('click')
    expect(wrapper.emitted('open-incidents')).toHaveLength(1)
    expect(wrapper.get('[data-testid="monitor-v3-incidents"]').text()).toContain('2')
    await wrapper.get('[data-testid="monitor-v3-refresh"]').trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)

    const admin = render(page({ featured: component(1, 'Claude Max', { requests: 12345 }) }))
    expect(admin.get('[data-testid="monitor-v3-featured"]').text()).toContain('channelMonitorV3.page.requestCount:{"count":12345}')
    expect(admin.get('[data-testid="monitor-v3-footer-note"]').text()).toBe('channelMonitorV3.page.defaultFooter')
  })

  it('shows a skeleton before the first load', () => {
    const wrapper = render(null, true)
    expect(wrapper.find('[aria-busy="true"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="monitor-v3-system"]').exists()).toBe(false)
  })
})
