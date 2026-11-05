import { describe, expect, it } from 'vitest'
import { filterSmartOpsSidebar } from '../smartOpsSidebar'

const smartOps = { path: '/admin/smart-ops', children: [{ path: '/admin/token-guard' }, { path: '/admin/harvest-flow' }] }
const settings = { path: '/admin/settings' }
describe('smart operations sidebar visibility', () => {
  it('keeps the existing menus by default and hides only the chosen children', () => {
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
