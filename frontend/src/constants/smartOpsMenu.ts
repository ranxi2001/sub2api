export const SMART_OPS_MENU_ITEMS = [
  { path: '/admin/lottery', label: 'lottery.adminTitle' },
  { path: '/admin/auto-config', label: 'autoConfig.title' },
  { path: '/admin/priority-scheduling', label: 'priorityScheduling.title' },
  { path: '/admin/channel-operations', label: 'channelOps.title' },
  { path: '/admin/profit-operations', label: 'profitOps.title' },
  { path: '/admin/account-quality', label: 'qualityOps.title' },
  { path: '/admin/controlled-experiments', label: 'controlledExperiments.title' },
  { path: '/admin/account-ops', label: 'accountOps.title' },
  { path: '/admin/token-guard', label: 'tokenGuard.title' },
  { path: '/admin/token-guard-v2', label: 'tokenGuardV2.title' },
  { path: '/admin/pelican-tests', label: 'pelicanTests.title' },
  { path: '/admin/group-cache-tests', label: 'admin.groupCacheTest.title' },
  { path: '/admin/request-captures', label: 'admin.requestCapture.title' },
  { path: '/admin/harvest-flow', label: 'nav.harvestFlow' },
] as const

// Used only when no visibility preferences are saved; an explicit [] shows all entries.
export const DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS: readonly string[] = [
  '/admin/lottery',
  '/admin/channel-operations',
  '/admin/controlled-experiments',
  '/admin/account-ops',
  '/admin/token-guard',
  '/admin/group-cache-tests',
  '/admin/request-captures',
  '/admin/harvest-flow',
]
