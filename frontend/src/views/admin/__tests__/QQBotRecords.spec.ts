import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotRecordsView from '../QQBotRecordsView.vue'
import type { QQActivityRecord } from '@/api/qqbot'

const api = vi.hoisted(() => ({ activityRecords: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
const participant = { member_id: 'invitee-openid', user_id: 0, email: '', nickname: '', bound: false, user_status: '', user_deleted: false }
const invitation: QQActivityRecord = {
  id: 'group:invitee-openid', group_id: 'group-openid', participant,
  inviter: { member_id: 'inviter-openid', user_id: 7, email: 'inviter@example.com', nickname: 'Inviter', bound: true, user_status: 'active', user_deleted: false },
  status: 'invitee_unbound', amount: 0, prize_name: '', created_at: '2026-10-15T08:00:00Z',
  invited_at: '2026-10-15T08:00:00Z', joined_at: '2026-10-15T08:01:00Z', reward_user_id: 0, reward_email: ''
}
function page(items: QQActivityRecord[] = [], total = items.length, current = 1) { return { items, total, page: current, page_size: 20 } }
async function setup(locale: 'zh' | 'en' = 'zh', query = '?tab=checkin') {
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/admin/qqbot/records', component: QQBotRecordsView },
    { path: '/admin/qqbot/activities', component: { template: '<div />' } }
  ] })
  await router.push(`/admin/qqbot/records${query}`)
  const translation = createI18n({ legacy: false, locale, messages: { zh, en } })
  const wrapper = mount(QQBotRecordsView, { global: { plugins: [translation, router] } })
  await flushPromises()
  return { wrapper, router, t: translation.global.t }
}

describe('QQ activity records', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.activityRecords.mockResolvedValue(page())
  })
  afterEach(() => vi.restoreAllMocks())

  it.each(['zh', 'en'] as const)('%s shows QQ invitation relationships and current bindings', async locale => {
    api.activityRecords.mockResolvedValue(page([invitation]))
    const { wrapper: w, t } = await setup(locale, '?tab=invite')
    expect(w.findAll('[role="tab"]')).toHaveLength(3)
    expect(api.activityRecords).toHaveBeenCalledWith('invite', '', 1)
    expect(w.text()).toContain(t('qqbot.recordsTitle'))
    expect(w.text()).toContain(t('qqbot.inviteRecordsHelp'))
    expect(w.text()).toContain('inviter-openid')
    expect(w.text()).toContain('invitee-openid')
    expect(w.text()).toContain('inviter@example.com')
    expect(w.text()).toContain(t('qqbot.recordBound'))
    expect(w.text()).toContain(t('qqbot.recordUnbound'))
    expect(w.text()).toContain(t('qqbot.recordStatus.invitee_unbound'))
    expect(w.text()).toContain(t('qqbot.recordStatus.joined'))
    expect(w.text()).not.toContain('$0.00')
    w.unmount()
  })

  it.each(['zh', 'en'] as const)('%s displays chance-only lottery rewards on desktop and mobile', async locale => {
    api.activityRecords.mockResolvedValue(page([{ ...invitation, id: 'draw-1', inviter: undefined, status: 'credited', amount: 0, chances: 2, prize_name: 'Try again', remaining: 3 }]))
    const { wrapper: w, t } = await setup(locale, '?tab=lottery')
    expect(w.text().split(t('qqbot.chanceReward', { count: 2 }))).toHaveLength(3)
    expect(w.text()).toContain('Try again')
    w.unmount()
  })

  it.each(['checkin', 'invite', 'lottery'])('shows coupon rewards in %s records on both layouts', async kind => {
    api.activityRecords.mockResolvedValue(page([{ ...invitation, status: 'credited', coupon_quantity: 2, coupon_template_id: 5 }]))
    const { wrapper: w, t } = await setup('zh', '?tab=' + kind)
    expect(w.get('article').text()).toContain(t('coupon.awardedQuantity', { count: 2 }))
    expect(w.get('tbody tr').text()).toContain(t('coupon.awardedQuantity', { count: 2 }))
    w.unmount()
  })

  it('keeps the original credited account visible after rebinding and formats amounts to two decimals', async () => {
    api.activityRecords.mockResolvedValue(page([{ ...invitation, status: 'rewarded', amount: 1.23456789,
      reward_user_id: 6, reward_email: 'original@example.com', rewarded_at: '2026-10-15T08:02:00Z' }]))
    const { wrapper: w } = await setup('zh', '?tab=invite')
    expect(w.text()).toContain('original@example.com')
    expect(w.text()).toContain('inviter@example.com')
    expect(w.text()).toContain('$1.23')
    expect(w.text()).not.toContain('1.23456789')
    w.unmount()
  })

  it('shows zero-value lottery participation and retains searches independently for each tab', async () => {
    const { wrapper: w, t } = await setup()
    api.activityRecords.mockResolvedValueOnce(page([], 21))
    await w.get('input').setValue('applied')
    await w.get('form').trigger('submit'); await flushPromises()
    api.activityRecords.mockResolvedValueOnce(page([], 21, 2))
    await w.get('input').setValue('draft')
    await w.findAll('button').find(b => b.text() === t('qqbot.nextPage'))!.trigger('click'); await flushPromises()
    expect(api.activityRecords).toHaveBeenLastCalledWith('checkin', 'applied', 2)
    api.activityRecords.mockResolvedValueOnce(page([{ ...invitation, inviter: undefined, status: 'no_prize', amount: 0, remaining: 0, prize_name: 'Thanks' }]))
    await w.get('#records-tab-lottery').trigger('click'); await flushPromises()
    expect(api.activityRecords).toHaveBeenLastCalledWith('lottery', '', 1)
    expect(w.text()).toContain('Thanks')
    expect(w.text()).toContain('$0.00')
    await w.get('#records-tab-checkin').trigger('click'); await flushPromises()
    expect((w.get('input').element as HTMLInputElement).value).toBe('draft')
    api.activityRecords.mockResolvedValueOnce(page([], 21, 2))
    await w.findAll('button').find(b => b.text() === t('common.refresh'))!.trigger('click'); await flushPromises()
    expect(api.activityRecords).toHaveBeenLastCalledWith('checkin', 'applied', 2)
    w.unmount()
  })

  it('shows a load failure and retries without presenting stale records', async () => {
    api.activityRecords.mockRejectedValueOnce(new Error('Load failed'))
    const { wrapper: w, t } = await setup('zh', '?tab=invite')
    expect(w.get('[role="alert"]').text()).toBe('Load failed')
    expect(w.find('tbody tr').exists()).toBe(false)
    api.activityRecords.mockResolvedValueOnce(page([invitation]))
    await w.findAll('button').find(b => b.text() === t('common.refresh'))!.trigger('click'); await flushPromises()
    expect(w.find('[role="alert"]').exists()).toBe(false)
    expect(w.text()).toContain('inviter-openid')
    w.unmount()
  })

  it.each(['zh', 'en'] as const)('%s labels guaranteed lottery results on mobile and desktop', async locale => {
    api.activityRecords.mockResolvedValue(page([{ ...invitation, inviter: undefined, status: 'credited', amount: 5, remaining: 2, prize_name: 'Guarantee', is_guaranteed: true }]))
    const { wrapper: w, t } = await setup(locale, '?tab=lottery')
    expect(w.get('article').text()).toContain(t('qqbot.guaranteedDraw'))
    expect(w.get('tbody tr').text()).toContain(t('qqbot.guaranteedDraw'))
    expect(w.text()).toContain('$5.00')
    w.unmount()
  })

  it('uses the linked tab, handles keyboard navigation and ignores responses from other tabs', async () => {
    let finish: (value: ReturnType<typeof page>) => void = () => {}
    api.activityRecords.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    const { wrapper: w, router } = await setup('zh', '?tab=invite')
    await w.get('#records-tab-invite').trigger('keydown', { key: 'Home' }); await flushPromises()
    expect(router.currentRoute.value.query.tab).toBe('checkin')
    expect(w.get('#records-tab-checkin').attributes('aria-selected')).toBe('true')
    finish(page([invitation])); await flushPromises()
    expect(w.text()).not.toContain('inviter-openid')
    await router.push('/admin/qqbot/records?tab=unknown'); await flushPromises()
    expect(w.get('#records-tab-checkin').attributes('aria-selected')).toBe('true')
    await w.get('#records-tab-invite').trigger('click'); await flushPromises()
    expect(w.text()).toContain('inviter-openid')
    w.unmount()
  })
})
