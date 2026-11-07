interface SidebarEntry {
  path: string
  children?: SidebarEntry[]
}

// Apply presentation preferences after feature gates so showing an entry never
// overrides its feature's enablement conditions. Other menu groups are untouched.
export function filterSmartOpsSidebar<T extends SidebarEntry>(items: T[], hiddenPaths: readonly string[]): T[] {
  return items.flatMap(item => {
    if (item.path !== '/admin/smart-ops' || !item.children) return [item]
    const children = item.children.filter(child => !hiddenPaths.includes(child.path))
    return children.length ? [{ ...item, children }] : []
  })
}
