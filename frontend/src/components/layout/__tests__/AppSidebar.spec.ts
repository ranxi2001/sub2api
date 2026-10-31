import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AppSidebar.vue')
const componentSource = readFileSync(componentPath, 'utf8')
const stylePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../style.css')
const styleSource = readFileSync(stylePath, 'utf8')

describe('AppSidebar custom SVG styles', () => {
  it('does not override uploaded SVG fill or stroke colors', () => {
    expect(componentSource).toContain('.sidebar-svg-icon {')
    expect(componentSource).toContain('color: currentColor;')
    expect(componentSource).toContain('display: block;')
    expect(componentSource).not.toContain('stroke: currentColor;')
    expect(componentSource).not.toContain('fill: none;')
  })
})

describe('AppSidebar scroll position persistence', () => {
  it('binds a template ref to the sidebar nav element', () => {
    expect(componentSource).toContain('ref="sidebarNavRef"')
    expect(componentSource).toContain('sidebar-nav')
  })

  it('declares sidebarNavRef in script setup', () => {
    expect(componentSource).toContain("const sidebarNavRef = ref<HTMLElement | null>(null)")
  })

  it('saves scroll position on beforeUnmount', () => {
    expect(componentSource).toContain('onBeforeUnmount')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('sidebarNavRef.value.scrollTop')
  })

  it('restores scroll position on mount', () => {
    expect(componentSource).toContain('onMounted')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('nextTick')
  })
})

describe('AppSidebar collapsible groups', () => {
  it('lets the user collapse a group even while a child route is active', () => {
    // The expand state must come from the user's override first, falling back
    // to the active-route heuristic only when the user has not clicked yet.
    expect(componentSource).toContain('const groupExpandOverrides = ref<Map<string, boolean>>(new Map())')
    expect(componentSource).not.toContain('expandedGroups.value.has(item.path) || isGroupActive(item)')
  })
})

describe('AppSidebar header styles', () => {
  it('does not clip the version badge dropdown', () => {
    const sidebarHeaderBlockMatch = styleSource.match(/\.sidebar-header\s*\{[\s\S]*?\n {2}\}/)
    const sidebarBrandBlockMatch = componentSource.match(/\.sidebar-brand\s*\{[\s\S]*?\n\}/)

    expect(sidebarHeaderBlockMatch).not.toBeNull()
    expect(sidebarBrandBlockMatch).not.toBeNull()
    expect(sidebarHeaderBlockMatch?.[0]).not.toContain('@apply overflow-hidden;')
    expect(sidebarBrandBlockMatch?.[0]).not.toContain('overflow: hidden;')
  })
})

describe('AppSidebar subscription feature flag', () => {
  it('gates the My Subscriptions entry behind the subscription public-settings flag', () => {
    expect(componentSource).toContain('const flagSubscription = makeSidebarFlag(FeatureFlags.subscription)')
    expect(componentSource).toMatch(/path: '\/subscriptions'[^\n]*featureFlag: flagSubscription/)
  })

  it('also hides the admin Subscription Management entry on recharge-only sites', () => {
    expect(componentSource).toMatch(/path: '\/admin\/subscriptions'[^\n]*featureFlag: flagSubscription/)
  })

  it('derives the purchase entry label from the site billing mode', () => {
    expect(componentSource).toContain("import { resolveSiteBillingMode } from '@/utils/siteBillingMode'")
    expect(componentSource).toMatch(/case 'recharge_only':\s*return t\('nav\.recharge'\)/)
    expect(componentSource).toMatch(/case 'subscription_only':\s*return t\('nav\.subscribe'\)/)
    expect(componentSource).toMatch(/path: '\/purchase'[^\n]*label: purchaseNavLabel\.value/)
  })
})

describe('AppSidebar smart operations group', () => {
  const smartOpsBlock = componentSource.match(/path: '\/admin\/smart-ops'[\s\S]*?\n {4}\] \},/)?.[0] ?? ''
  const pathsIn = (source: string) => [...source.matchAll(/path: '([^']+)'/g)].map(match => match[1])

  it('nests request capture and the ticket harvest flow under 智能运维', () => {
    expect(smartOpsBlock).not.toBe('')
    expect(smartOpsBlock).toMatch(/path: '\/admin\/request-captures'[^\n]*featureFlag: \(\) => adminSettingsStore\.requestCaptureEnabled/)
    expect(smartOpsBlock).toContain("path: '/admin/harvest-flow'")
    // Each entry is declared once, so neither is still a top-level item.
    expect(componentSource.match(/path: '\/admin\/request-captures'/g)).toHaveLength(1)
    expect(componentSource.match(/path: '\/admin\/harvest-flow'/g)).toHaveLength(1)
  })

  it('keeps the group in the same order as the 智能运维 tab bar', () => {
    const navSource = readFileSync(resolve(dirname(componentPath), '../admin/operations/SmartOpsNav.vue'), 'utf8')
    expect(pathsIn(smartOpsBlock).slice(1)).toEqual(pathsIn(navSource))
  })
})

describe('AppSidebar support tickets', () => {
  it('gates both entries behind the opt-in switch and keeps admins on their own page', () => {
    expect(componentSource).toContain('const flagSupportTickets = makeSidebarFlag(FeatureFlags.supportTickets)')
    expect(componentSource).toContain('const flagUserSupportTickets = () => flagSupportTickets() && !authStore.isAdmin')
    expect(componentSource).toMatch(/path: '\/support-tickets'[^\n]*featureFlag: flagUserSupportTickets[^\n]*badge: \(\) => supportTicketStore\.userUnread/)
    expect(componentSource).toMatch(/path: '\/admin\/support-tickets'[^\n]*featureFlag: flagSupportTickets[^\n]*badge: \(\) => supportTicketStore\.adminPending/)
  })

  it('renders badges for every item list and caps the number', () => {
    expect(componentSource.match(/data-testid="sidebar-nav-badge"/g)).toHaveLength(3)
    expect(componentSource).toContain("return count > 99 ? '99+' : String(count)")
    expect(componentSource).toContain('.sidebar-nav-badge-collapsed {')
  })
})

describe('AppSidebar new feature notices', () => {
  it('shows the shared dismissible notice beside the admin settings entry', () => {
    expect(componentSource).toContain("import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'")
    expect(componentSource).toContain("NEW_FEATURE_NOTICES.protocolFeatureSwitches")
    expect(componentSource).toContain("item.path === '/admin/settings'")
    expect(componentSource).toContain('<NewFeatureBadge')
  })
})
