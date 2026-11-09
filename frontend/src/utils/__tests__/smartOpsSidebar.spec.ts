import { describe, expect, it } from 'vitest'
import { filterSmartOpsSidebar } from '../smartOpsSidebar'
import { DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS, SMART_OPS_MENU_ITEMS } from '@/constants/smartOpsMenu'

const smartOps = { path: '/admin/smart-ops', children: [{ path: '/admin/token-guard' }, { path: '/admin/harvest-flow' }] }
const settings = { path: '/admin/settings' }
describe('smart operations sidebar visibility', () => {
  it('shows exactly the six default entries when preferences are absent', () => {
    const allEntries = { path: '/admin/smart-ops', children: SMART_OPS_MENU_ITEMS.map(item => ({ ...item })) }
    const visible = filterSmartOpsSidebar([allEntries], DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS)
    expect(visible[0]?.children?.map(item => item.path)).toEqual([
      '/admin/auto-config',
      '/admin/priority-scheduling',
      '/admin/profit-operations',
      '/admin/account-quality',
      '/admin/token-guard-v2',
      '/admin/pelican-tests',
    ])
  })
  it('honors an explicit show-all preference and hides only the chosen children', () => {
    expect(filterSmartOpsSidebar([smartOps, settings], [])).toEqual([smartOps, settings])
    expect(filterSmartOpsSidebar([smartOps, settings], ['/admin/token-guard', '/admin/settings'])).toEqual([
      { ...smartOps, children: [{ path: '/admin/harvest-flow' }] }, settings,
    ])
    expect(smartOps.children).toHaveLength(2)
  })
  it('removes an empty group and restores it when preferences are cleared', () => {
    expect(filterSmartOpsSidebar([smartOps, settings], smartOps.children.map(item => item.path))).toEqual([settings])
    expect(filterSmartOpsSidebar([smartOps, settings], [])).toEqual([smartOps, settings])
  })
  it('does not resurrect feature-gated entries when all menus are selected', () => {
    const gated = { ...smartOps, children: [] }
    expect(filterSmartOpsSidebar([gated, settings], [])).toEqual([settings])
    expect(filterSmartOpsSidebar([smartOps], ['/admin/future-entry'])).toEqual([smartOps])
  })
})
