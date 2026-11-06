import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import AccountTableFilters from '../AccountTableFilters.vue'
import Select from '@/components/common/Select.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { AdminGroup } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: { count: number }) => params ? `${key} (${params.count})` : key
    })
  }
})

const defaultFilters = () => ({
  platform: '', type: '', status: '', privacy_mode: '', group: '',
  search: 'existing search', lite: '1', sort_by: 'priority', sort_order: 'asc'
})

function mountFilters(filters: Record<string, unknown> = defaultFilters()) {
  return mount(AccountTableFilters, {
    attachTo: document.body,
    props: {
      searchQuery: 'existing search', filters,
      groups: [{ id: 42, name: 'A very long group name', platform: 'openai' } as AdminGroup]
    },
    global: { stubs: { Teleport: true } }
  })
}

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

describe('AccountTableFilters', () => {
  it('shows all five filters immediately and preserves selections on external updates', async () => {
    const filters = { ...defaultFilters(), type: 'bedrock', privacy_mode: '__unset__', group: 'ungrouped' }
    const wrapper = mountFilters(filters)
    const selects = wrapper.findAllComponents(Select)

    expect(selects).toHaveLength(5)
    expect(selects.every(select => select.isVisible())).toBe(true)
    expect(wrapper.find('button[aria-controls]').exists()).toBe(false)
    expect(wrapper.props('filters')).toEqual(filters)
    expect(wrapper.emitted('update:filters')).toBeUndefined()
    expect(wrapper.emitted('change')).toBeUndefined()

    await wrapper.setProps({ filters: defaultFilters() })
    expect(selects.every(select => select.isVisible())).toBe(true)
    expect(selects.every(select => select.props('modelValue') === '')).toBe(true)
    expect(wrapper.emitted('update:filters')).toBeUndefined()
    expect(wrapper.emitted('change')).toBeUndefined()
  })

  it('preserves every option and emits a merged filter snapshot plus change for every selection', async () => {
    const filters = { ...defaultFilters(), platform: 'openai', type: 'oauth', status: 'active', privacy_mode: '__unset__', group: '42' }
    const wrapper = mountFilters(filters)
    const expectedOptions = [
      ['platform', ['', ...CONCRETE_PLATFORM_OPTIONS.map(option => option.value)]],
      ['status', ['', 'active', 'inactive', 'error', 'rate_limited', 'temp_unschedulable', 'unschedulable']],
      ['type', ['', 'oauth', 'setup-token', 'apikey', 'bedrock']],
      ['privacy_mode', ['', '__unset__', 'training_off', 'training_set_cf_blocked', 'training_set_failed']],
      ['group', ['', 'ungrouped', '42']]
    ] as const
    let changes = 0
    for (const [index, [key, values]] of expectedOptions.entries()) {
      const select = wrapper.findAllComponents(Select)[index]
      expect(select.props('options').map(option => option.value)).toEqual(values)
      expect(select.props('modelValue')).toBe(filters[key])
      expect(select.get('button').attributes('aria-label')).not.toBe('Select option')
      for (const [optionIndex, value] of values.entries()) {
        await select.get('button').trigger('click')
        await select.findAll('[role="option"]')[optionIndex].trigger('click')
        changes += 1
        const group = key === 'platform' && value && value !== 'openai' ? '' : filters.group
        expect(wrapper.emitted('update:filters')?.at(-1)).toEqual([{ ...filters, group, [key]: value }])
        expect(wrapper.emitted('change')).toHaveLength(changes)
        expect(wrapper.props('filters')).toEqual(filters)
      }
    }
    expect(wrapper.findAllComponents(Select)[4].props('options')[2].label).toBe('A very long group name')
    wrapper.unmount()
  })

  it('only offers groups for the selected platform, including after the catalog loads', async () => {
    const wrapper = mountFilters({ ...defaultFilters(), platform: 'openai' })
    const groupSelect = wrapper.findAllComponents(Select)[4]
    await wrapper.setProps({ groups: [] })
    expect(groupSelect.props('options').map(option => option.value)).toEqual(['', 'ungrouped'])

    await wrapper.setProps({ groups: [
      { id: 42, name: 'OpenAI group', platform: 'openai' },
      { id: 43, name: 'Gemini group', platform: 'gemini' },
      { id: 44, name: 'Composite group', platform: 'composite' }
    ] as AdminGroup[] })
    expect(groupSelect.props('options').map(option => option.value)).toEqual(['', 'ungrouped', '42'])

    await wrapper.setProps({ filters: { ...defaultFilters(), platform: 'gemini' } })
    expect(groupSelect.props('options').map(option => option.value)).toEqual(['', 'ungrouped', '43'])

    await wrapper.setProps({ filters: defaultFilters() })
    expect(groupSelect.props('options').map(option => option.value)).toEqual(['', 'ungrouped', '42', '43', '44'])
    expect(wrapper.emitted('update:filters')).toBeUndefined()
    expect(wrapper.emitted('change')).toBeUndefined()
  })

  it.each([
    { platform: '', group: '42', target: 'gemini', expectedGroup: '' },
    { platform: 'openai', group: '42', target: 'gemini', expectedGroup: '' },
    { platform: '', group: '42', target: 'openai', expectedGroup: '42' },
    { platform: 'openai', group: '42', target: '', expectedGroup: '42' },
    { platform: 'openai', group: 'ungrouped', target: 'gemini', expectedGroup: 'ungrouped' },
    { platform: 'openai', group: '', target: 'gemini', expectedGroup: '' }
  ])('updates platform and group together: $platform/$group → $target/$expectedGroup', async ({ platform, group, target, expectedGroup }) => {
    const filters = { ...defaultFilters(), platform, group, status: 'active', privacy_mode: '__unset__' }
    const wrapper = mountFilters(filters)
    const platformSelect = wrapper.findAllComponents(Select)[0]
    await platformSelect.get('button').trigger('click')
    const optionIndex = platformSelect.props('options').findIndex(option => option.value === target)
    await platformSelect.findAll('[role="option"]')[optionIndex].trigger('click')

    expect(wrapper.emitted('update:filters')).toEqual([[{ ...filters, platform: target, group: expectedGroup }]])
    expect(wrapper.emitted('change')).toEqual([[]])
    expect(wrapper.props('filters')).toEqual(filters)
  })

  it('preserves immediate search updates and the debounced search change event', async () => {
    vi.useFakeTimers()
    const wrapper = mountFilters()
    const search = wrapper.getComponent(SearchInput)
    expect(search.props('modelValue')).toBe('existing search')
    expect(search.props('placeholder')).toBe('admin.accounts.searchAccounts')
    await search.get('input').setValue('new search')
    expect(wrapper.emitted('update:searchQuery')).toEqual([['new search']])
    expect(wrapper.emitted('change')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(300)
    expect(wrapper.emitted('change')).toEqual([[]])
    expect(wrapper.emitted('update:filters')).toBeUndefined()
    wrapper.unmount()
  })

  it('uses shrinkable, wrapping controls for all five filters', () => {
    const wrapper = mountFilters()
    expect(wrapper.classes()).toEqual(expect.arrayContaining(['min-w-0', 'grid-cols-2', 'sm:flex-wrap']))
    for (const select of wrapper.findAllComponents(Select)) {
      expect(select.classes()).toEqual(expect.arrayContaining(['min-w-0', 'w-full', 'sm:w-36']))
    }
  })
})
