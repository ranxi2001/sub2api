import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AnnouncementBell from '../AnnouncementBell.vue'
import { useAnnouncementStore } from '@/stores/announcements'
import { useLotteryStore } from '@/stores/lottery'

vi.mock('@/stores/lottery', async () => {
  const { reactive } = await import('vue')
  const state = reactive({ available: 0, pendingPools: [], refresh: vi.fn() })
  return { useLotteryStore: () => state }
})

const { markRead, showError, showSuccess } = vi.hoisted(() => ({ markRead: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api', () => ({ announcementsAPI: { markRead } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess, fetchPublicSettings: vi.fn().mockResolvedValue({}) }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/format', () => ({ formatRelativeTime: () => 'now', formatRelativeWithDateTime: () => 'now' }))
enableAutoUnmount(afterEach)
beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  useLotteryStore().available = 0
  useLotteryStore().pendingPools = []
  vi.spyOn(console, 'error').mockImplementation(() => {})
  useAnnouncementStore().announcements = [{ id: 1, title: 'Notice', content: 'Details', notify_mode: 'silent', created_at: '2026-09-30', updated_at: '2026-09-30' }]
})
afterEach(() => { vi.restoreAllMocks(); document.body.style.overflow = '' })

describe('announcement read confirmation', () => {
  it('shows available draws in the bell and links each pool without marking tickets read', async () => {
    const lottery = useLotteryStore()
    lottery.available = 3
    lottery.pendingPools = [{ id: 7, name: 'Credit pool', available: 3, draw_enabled: true }] as typeof lottery.pendingPools
    const wrapper = mount(AnnouncementBell, { global: { stubs: { Teleport: true, Transition: true, Icon: true, RouterLink: { props: ['to'], template: '<a :href="to.path + \'?pool=\' + to.query.pool"><slot /></a>' } } } })
    expect(wrapper.get('[data-testid="lottery-notification-badge"]').text()).toBe('3')
    await wrapper.get('button').trigger('click')
    expect(lottery.refresh).toHaveBeenCalledWith(true)
    expect(wrapper.get('a[href="/lottery?pool=7"]').text()).toContain('Credit pool')
    markRead.mockResolvedValue(undefined)
    await wrapper.findAll('button').find(b => b.text() === 'announcements.markAllRead')!.trigger('click')
    await flushPromises()
    expect(useAnnouncementStore().unreadCount).toBe(0)
    expect(wrapper.get('[data-testid="lottery-notification-badge"]').text()).toBe('3')
    lottery.available = 0
    lottery.pendingPools = []
    await flushPromises()
    expect(wrapper.find('[data-testid="lottery-notification-badge"]').exists()).toBe(false)
  })
  it('keeps a failed read confirmation open and only reports success after retry', async () => {
    markRead.mockRejectedValue(new Error('offline'))
    const wrapper = mount(AnnouncementBell, { global: { stubs: { Teleport: true, Transition: true, Icon: true } } })
    await wrapper.get('button').trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    await flushPromises()
    const confirm = () => wrapper.findAll('button').find(button => button.text() === 'announcements.markRead')!
    await confirm().trigger('click')
    await flushPromises()
    expect(markRead).toHaveBeenCalledTimes(2)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalled()
    expect(wrapper.find('.markdown-body').exists()).toBe(true)
    markRead.mockResolvedValue(undefined)
    await confirm().trigger('click')
    await flushPromises()
    expect(showSuccess).toHaveBeenCalledWith('announcements.markedAsRead')
    expect(wrapper.find('.markdown-body').exists()).toBe(false)
    expect(useAnnouncementStore().unreadCount).toBe(0)
  })
})
