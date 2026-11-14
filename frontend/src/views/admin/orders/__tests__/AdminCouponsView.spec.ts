import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'
import AdminCouponsView from '../AdminCouponsView.vue'
import type { SimpleUser } from '@/api/admin/usage'

const api = vi.hoisted(() => ({ templates: vi.fn(), list: vi.fn(), issue: vi.fn() }))
const searchUsers = vi.hoisted(() => vi.fn())
vi.mock('@/api/coupons', () => ({ adminCouponAPI: api }))
vi.mock('@/api/admin/usage', () => ({ searchUsers, default: { searchUsers } }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const users: SimpleUser[] = [
  { id: 10, email: 'deanzfc@example.com', deleted: false },
  { id: 11, email: 'another@example.com', deleted: false }
]
let wrapper: VueWrapper

async function mountView() {
  wrapper = shallowMount(AdminCouponsView, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } } })
  await flushPromises()
  await wrapper.findAll('form')[0]!.get('select').setValue('1')
  return wrapper.findAll('form')[0]!
}

async function search(keyword: string) {
  const input = wrapper.get('#coupon-user-search')
  await input.trigger('focus')
  await input.setValue(keyword)
  await vi.advanceTimersByTimeAsync(300)
  await flushPromises()
}

async function selectUser(id: number) {
  const option = wrapper.findAll('button').find(button => button.text().includes(`#${id}`))
  expect(option).toBeDefined()
  await option!.trigger('click')
}

describe('admin coupon issuance', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.resetAllMocks()
    api.templates.mockResolvedValue({ data: [{ id: 1, name: 'Coupon', enabled: true, kind: 'fixed', currency: 'CNY', min_amount: 100, value: 10, issued_count: 0 }] })
    api.list.mockResolvedValue({ data: { items: [], total: 0 } })
    api.issue.mockResolvedValue({ data: { count: 2, ids: [1, 2] } })
    searchUsers.mockResolvedValue(users)
  })

  afterEach(() => {
    wrapper?.unmount()
    vi.useRealTimers()
  })

  it('searches account information, selects multiple users and reuses the request identifier after uncertain delivery', async () => {
    api.issue.mockRejectedValueOnce(new Error('network timeout')).mockResolvedValueOnce({ data: { count: 2, ids: [1, 2] } })
    const form = await mountView()

    await search('deanzfc')
    expect(searchUsers).toHaveBeenCalledWith('deanzfc')
    expect(form.text()).toContain('deanzfc@example.com')
    await selectUser(10)
    expect(wrapper.get<HTMLInputElement>('#coupon-user-search').element.value).toBe('')
    await search('another@example.com')
    await selectUser(11)
    await form.trigger('submit')
    await flushPromises()

    const first = api.issue.mock.calls[0]![0]
    expect(first).toMatchObject({ template_id: 1, user_ids: [10, 11], quantity: 1 })
    expect(first.request_id).toBeTruthy()
    expect(form.findAll('button[aria-label="coupon.removeUser"]')).toHaveLength(2)

    await form.trigger('submit')
    await flushPromises()
    expect(api.issue.mock.calls[1]![0]).toEqual(first)
    expect(wrapper.text()).toContain('coupon.issueSuccess')
    expect(form.findAll('button[aria-label="coupon.removeUser"]')).toHaveLength(0)
  })

  it('excludes deleted and already selected users and allows removing a recipient', async () => {
    searchUsers.mockResolvedValue([...users, { id: 12, email: 'deleted@example.com', deleted: true }])
    const form = await mountView()
    await search('example')
    expect(form.text()).not.toContain('deleted@example.com')
    await selectUser(10)
    await search('example')
    expect(form.findAll('button').filter(button => button.text().includes('#10'))).toHaveLength(0)
    await selectUser(11)
    await form.findAll('button[aria-label="coupon.removeUser"]')[0]!.trigger('click')
    await form.trigger('submit')
    await flushPromises()
    expect(api.issue.mock.calls[0]![0].user_ids).toEqual([11])
  })

  it('requires explicit selection and does not issue while another account search is unconfirmed', async () => {
    const form = await mountView()
    await search('deanzfc')
    await form.trigger('submit')
    expect(api.issue).not.toHaveBeenCalled()
    expect(form.get('button.btn-primary').attributes('disabled')).toBeDefined()

    await selectUser(10)
    await search('another')
    await form.trigger('submit')
    expect(api.issue).not.toHaveBeenCalled()
    await wrapper.get('#coupon-user-search').setValue('')
    await form.trigger('submit')
    await flushPromises()
    expect(api.issue.mock.calls[0]![0].user_ids).toEqual([10])
  })

  it('keeps only the latest search results when responses arrive out of order', async () => {
    let resolveFirst!: (users: SimpleUser[]) => void
    searchUsers.mockImplementationOnce(() => new Promise<SimpleUser[]>(resolve => { resolveFirst = resolve }))
    searchUsers.mockResolvedValueOnce([users[1]])
    const form = await mountView()
    await search('dean')
    await search('another')
    resolveFirst([users[0]!])
    await flushPromises()
    expect(form.text()).toContain('another@example.com')
    expect(form.text()).not.toContain('deanzfc@example.com')
  })

  it('discards pending results after clearing the account search', async () => {
    let resolveSearch!: (users: SimpleUser[]) => void
    searchUsers.mockImplementationOnce(() => new Promise<SimpleUser[]>(resolve => { resolveSearch = resolve }))
    const form = await mountView()
    await search('dean')
    await wrapper.get('#coupon-user-search').setValue('')
    resolveSearch(users)
    await flushPromises()
    await wrapper.get('#coupon-user-search').trigger('focus')
    expect(form.text()).not.toContain('deanzfc@example.com')
    expect(api.issue).not.toHaveBeenCalled()
  })

  it('shows empty results and search failures without losing selected recipients', async () => {
    const form = await mountView()
    await search('dean')
    await selectUser(10)
    searchUsers.mockResolvedValueOnce([])
    await search('missing')
    expect(form.text()).toContain('coupon.noMatchingUsers')
    searchUsers.mockRejectedValueOnce(new Error('search unavailable'))
    await search('retry')
    expect(form.text()).toContain('coupon.userSearchFailed')
    expect(form.text()).toContain('deanzfc@example.com')
    expect(api.issue).not.toHaveBeenCalled()
  })

  it('retains the batch quantity limit for selected users', async () => {
    const form = await mountView()
    searchUsers.mockResolvedValue(Array.from({ length: 11 }, (_, index) => ({ id: index + 1, email: `user${index + 1}@example.com`, deleted: false })))
    for (let id = 1; id <= 11; id++) {
      await search('user')
      await selectUser(id)
    }
    await form.get('input[type="number"]').setValue(100)
    await form.trigger('submit')
    expect(api.issue).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('coupon.invalidUsers')
  })
})
