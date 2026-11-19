import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PelicanShowcaseView from '../PelicanShowcaseView.vue'
import type { PelicanShowcaseItem, PelicanShowcaseView as ShowcaseData } from '@/api/pelicanShowcase'

const { getShowcase, getShowcaseItem, removeShowcaseItem, showError, showSuccess, auth } = vi.hoisted(() => ({
  getShowcase: vi.fn(),
  getShowcaseItem: vi.fn(),
  removeShowcaseItem: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  auth: { isAdmin: false },
}))
vi.mock('@/api/pelicanShowcase', () => ({ getShowcase, getShowcaseItem, removeShowcaseItem }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, named?: Record<string, unknown>) => (named ? `${key} ${JSON.stringify(named)}` : key) }),
}))

const item = (id: number, groupId: number): PelicanShowcaseItem => ({
  id, group_id: groupId, model_id: 'gpt-6-astra', reasoning_effort: 'high', latency_ms: 42300,
  generated_at: '2026-09-24T08:30:00Z',
})
const showcase = (overrides: Partial<ShowcaseData> = {}): ShowcaseData => ({
  enabled: true,
  api_enabled: true,
  max_items: 20,
  retention_days: 7,
  groups: [
    { id: 1, name: 'Claude Max', platform: 'anthropic', items: Array.from({ length: 10 }, (_, i) => item(100 + i, 1)) },
    { id: 2, name: 'GPT Plus', platform: 'openai', items: [item(200, 2)] },
    { id: 3, name: 'Empty', platform: 'gemini', items: [] },
  ],
  ...overrides,
})

const mountView = () => mount(PelicanShowcaseView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      Icon: true,
      PlatformIcon: true,
      RouterLink: RouterLinkStub,
      EmptyState: { props: ['title', 'description'], template: '<div class="empty-state">{{ title }}</div>' },
      ConfirmDialog: {
        props: ['show'], emits: ['confirm', 'cancel'],
        template: '<div v-if="show" class="confirm"><button class="confirm-yes" @click="$emit(\'confirm\')" /></div>',
      },
      BaseDialog: {
        props: ['show', 'title'], emits: ['close'],
        template: '<div v-if="show" class="dialog"><h3>{{ title }}</h3><slot /><slot name="footer" /></div>',
      },
    },
  },
})

// The shared test setup installs an observer that never fires; cards here are on screen.
class OnScreenObserver {
  constructor(private readonly callback: IntersectionObserverCallback) {}
  observe(target: Element) {
    this.callback([{ isIntersecting: true, target } as IntersectionObserverEntry], this as unknown as IntersectionObserver)
  }
  disconnect() {}
  unobserve() {}
}

// Cards report being on screen after a short dwell (see PelicanShowcaseCard); then their HTML is fetched.
async function settle() {
  await flushPromises()
  await vi.advanceTimersByTimeAsync(1000)
  await flushPromises()
}

let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  vi.stubGlobal('IntersectionObserver', OnScreenObserver)
  getShowcase.mockReset()
  getShowcaseItem.mockReset().mockImplementation(async (id: number) => ({
    ...item(id, 0),
    response_text: id === 201 ? '21' : `<svg data-item="${id}"></svg>`,
  }))
  removeShowcaseItem.mockReset().mockResolvedValue(undefined)
  showError.mockReset()
  showSuccess.mockReset()
  auth.isAdmin = false
})
afterEach(() => {
  wrapper?.unmount()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('PelicanShowcaseView', () => {
  it('explains that the gallery is closed without asking for items', async () => {
    getShowcase.mockResolvedValue(showcase({ enabled: false, groups: [] }))
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('.empty-state').text()).toBe('pelicanShowcase.disabled.title')
    expect(wrapper.findAll('[data-testid="pelican-showcase-card"]')).toHaveLength(0)
    expect(getShowcaseItem).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="showcase-api-open"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="showcase-notice"]').exists()).toBe(false)
  })

  it('puts the notice about imperfect drawings at the top of the page while there are groups', async () => {
    getShowcase.mockResolvedValue(showcase())
    wrapper = mountView()
    await flushPromises()

    const notice = wrapper.get('[data-testid="showcase-notice"]')
    expect(notice.text()).toBe('pelicanShowcase.notice')
    expect(notice.attributes('role')).toBe('note')
    // First element of the page, directly above the toolbar with the gallery rules.
    expect(notice.element.previousElementSibling).toBeNull()
    expect(notice.element.nextElementSibling?.querySelector('[data-testid="showcase-keep-rule"]')).not.toBeNull()
  })

  it('leaves the notice out of an empty gallery', async () => {
    getShowcase.mockResolvedValue(showcase({ groups: [] }))
    wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('.empty-state').text()).toBe('pelicanShowcase.empty.title')
    expect(wrapper.find('[data-testid="showcase-notice"]').exists()).toBe(false)
  })

  it('opens API examples with the effective access state and an existing result ID', async () => {
    getShowcase.mockResolvedValue(showcase())
    wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="showcase-api-dialog"]').exists()).toBe(false)
    await wrapper.get('[data-testid="showcase-api-open"]').trigger('click')
    const dialog = wrapper.get('[data-testid="showcase-api-dialog"]')
    expect(dialog.get('[data-testid="showcase-api-status"]').text()).toBe('pelicanShowcase.api.available')
    expect(dialog.get('[data-testid="showcase-api-item-url"]').text()).toContain('/items/100')
    expect(dialog.get('[data-testid="showcase-api-command"]').text()).toContain('Bearer YOUR_API_KEY')
    await dialog.get('[data-testid="showcase-api-keys"]').trigger('click')
    expect(wrapper.find('[data-testid="showcase-api-dialog"]').exists()).toBe(false)
  })

  it.each([false, undefined])('keeps API information available when api_enabled is %s', async (apiEnabled) => {
    getShowcase.mockResolvedValue(showcase({ api_enabled: apiEnabled }))
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="showcase-api-open"]').trigger('click')
    const dialog = wrapper.get('[data-testid="showcase-api-dialog"]')
    expect(dialog.get('[data-testid="showcase-api-status"]').text()).toBe('pelicanShowcase.api.unavailable')
    expect(dialog.text()).toContain('pelicanShowcase.api.unavailableHint')
    expect(dialog.get('[data-testid="showcase-api-command"]').text()).toContain('/api/v1/public/pelican-showcase')
    expect(wrapper.findAll('[data-testid="pelican-showcase-card"]')).toHaveLength(11)
  })

  it('lists every group as one row with the gallery rules and loads visible cards in a sandbox', async () => {
    getShowcase.mockResolvedValue(showcase())
    wrapper = mountView()
    await settle()

    expect(wrapper.get('[data-testid="showcase-keep-rule"]').text()).toContain('"count":20')
    expect(wrapper.get('[data-testid="showcase-retention-rule"]').text()).toContain('"days":7')
    expect(wrapper.findAll('[role="tab"]').map((tab) => tab.text())).toEqual([
      'pelicanShowcase.allGroups', 'Claude Max10', 'GPT Plus1', 'Empty0',
    ])
    expect(wrapper.get('[data-testid="showcase-group-3"]').text()).toContain('pelicanShowcase.groupEmpty')

    // Every item of a group sits in its row, in the order received (newest first); no paging.
    const firstRow = wrapper.get('[data-testid="showcase-group-1"] [data-testid="pelican-showcase-row"]')
    expect(firstRow.findAll('iframe').map((frame) => frame.attributes('srcdoc').match(/data-item="(\d+)"/)?.[1]))
      .toEqual(Array.from({ length: 10 }, (_, i) => String(100 + i)))
    expect(wrapper.find('[data-testid="showcase-group-3"] [data-testid="pelican-showcase-row"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="showcase-group-1"] [role="scrollbar"]').attributes('aria-label'))
      .toBe('pelicanShowcase.scrollLabel {"group":"Claude Max"}')

    const cards = wrapper.findAll('[data-testid="pelican-showcase-card"]')
    expect(cards).toHaveLength(11)
    expect(cards[0].classes()).toContain('shrink-0')
    expect(getShowcaseItem).toHaveBeenCalledTimes(11)
    const frame = cards[0].get('iframe')
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    expect(frame.attributes('srcdoc')).toContain('Content-Security-Policy')
    expect(frame.attributes('srcdoc')).toContain('data-item="100"')
    expect(cards[0].text()).toContain('gpt-6-astra')
    expect(cards[0].text()).toContain('"seconds":"42.3"')
    expect(cards[0].text()).toContain('pelicanShowcase.efforts.high')

    await wrapper.get('[data-testid="showcase-tab-2"]').trigger('click')
    expect(wrapper.find('[data-testid="showcase-group-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="showcase-group-2"]').exists()).toBe(true)
  })

  it('fetches HTML only for cards that reach the viewport', async () => {
    vi.unstubAllGlobals() // back to the setup observer, which never reports a card as visible
    getShowcase.mockResolvedValue(showcase())
    wrapper = mountView()
    await settle()
    expect(wrapper.findAll('[data-testid="pelican-showcase-card"]')).toHaveLength(11)
    expect(getShowcaseItem).not.toHaveBeenCalled()
  })

  it('shows a readable state for output without HTML and for failed loads', async () => {
    getShowcase.mockResolvedValue(showcase({
      groups: [{ id: 2, name: 'GPT Plus', platform: 'openai', items: [item(201, 2), item(202, 2)] }],
    }))
    getShowcaseItem.mockImplementation(async (id: number) => {
      if (id === 202) throw new Error('boom')
      return { ...item(id, 2), response_text: '21' }
    })
    wrapper = mountView()
    await settle()
    const cards = wrapper.findAll('[data-testid="pelican-showcase-card"]')
    expect(cards[0].find('iframe').exists()).toBe(false)
    expect(cards[0].text()).toContain('pelicanShowcase.invalidHtml')
    expect(cards[1].text()).toContain('pelicanShowcase.itemLoadError')

    // Refresh retries a failed card even though it already reported being on screen.
    getShowcaseItem.mockImplementation(async (id: number) => ({ ...item(id, 2), response_text: `<svg data-item="${id}"></svg>` }))
    await wrapper.get('button[aria-label="common.refresh"]').trigger('click')
    await flushPromises()
    expect(getShowcaseItem).toHaveBeenCalledTimes(3)
    expect(wrapper.findAll('[data-testid="pelican-showcase-card"]')[1].get('iframe').attributes('srcdoc')).toContain('data-item="202"')
  })

  it('previews a card, and only admins can take it down', async () => {
    getShowcase.mockResolvedValue(showcase())
    wrapper = mountView()
    await settle()

    await wrapper.get('[data-testid="showcase-group-2"] [data-testid="pelican-showcase-card"] button').trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[data-testid="showcase-preview"]')
    expect(dialog.get('iframe').attributes('srcdoc')).toContain('data-item="200"')
    expect(dialog.get('iframe').attributes('sandbox')).toBe('allow-scripts allow-pointer-lock')
    expect(dialog.get('[data-testid="showcase-preview-fit"]').attributes('aria-pressed')).toBe('true')
    await dialog.get('[data-testid="showcase-preview-actual"]').trigger('click')
    expect(dialog.get('[data-testid="showcase-preview-actual"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('[data-testid="showcase-remove"]').exists()).toBe(false)
    wrapper.unmount()

    auth.isAdmin = true
    wrapper = mountView()
    await settle()
    await wrapper.get('[data-testid="showcase-group-2"] [data-testid="pelican-showcase-card"] button').trigger('click')
    await wrapper.get('[data-testid="showcase-remove"]').trigger('click')
    await wrapper.get('.confirm-yes').trigger('click')
    await flushPromises()
    expect(removeShowcaseItem).toHaveBeenCalledWith(200)
    expect(showSuccess).toHaveBeenCalledWith('pelicanShowcase.removed')
    expect(wrapper.find('[data-testid="showcase-preview"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="showcase-group-2"]').text()).toContain('pelicanShowcase.groupEmpty')
  })
})
