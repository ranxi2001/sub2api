import type { ApiKey } from '@/types'

type ImportKey = Pick<ApiKey, 'id' | 'name' | 'key' | 'group_id' | 'group_ids' | 'group'>
type Endpoint = 'chat' | 'responses' | 'anthropic' | 'gemini'
type Endpoints = Partial<Record<Endpoint, string>>
type ImportResult =
  | { url: string }
  | { error: 'unsupportedGroup' | 'invalidBase' | 'invalidKey' }

function resolveEndpoints(row: ImportKey, root: string): Endpoints | undefined {
  const group = row.group
  // Multi-group and composite routing can choose a different platform per model.
  if (!group || row.group_id == null || (row.group_ids?.length ?? 0) > 1 || group.claude_code_only) {
    return
  }

  const v1 = `${root}/v1`
  const openai = { chat: v1, responses: v1 }
  switch (group.platform) {
    case 'anthropic':
      return { anthropic: root }
    case 'openai':
      return group.allow_messages_dispatch ? { ...openai, anthropic: root } : openai
    case 'gemini':
      return { gemini: `${root}/v1beta` }
    case 'antigravity': {
      const dedicated = root.endsWith('/antigravity') ? root : `${root}/antigravity`
      return { anthropic: dedicated, gemini: `${dedicated}/v1beta` }
    }
    // These platforms serve Messages without the OpenAI group dispatch switch.
    case 'grok':
    case 'kimi':
    case 'zhipu':
    case 'deepseek':
    case 'minimax':
    case 'opencode_go':
    case 'command_code':
    case 'cline':
    case 'mistral':
    case 'qwen':
      return { ...openai, anthropic: root }
  }
}

// Match the desktop importer's HTTP exception for loopback and private hosts.
// URL.hostname has already normalized IP literals and lowercased any DNS name.
function isLocalHost(hostname: string): boolean {
  if (hostname === 'localhost' || hostname.endsWith('.local') || hostname === '[::1]' || /^\[f[cd][0-9a-f]{2}:/.test(hostname)) {
    return true
  }
  const mapped = /^\[::ffff:([0-9a-f]+):([0-9a-f]+)\]$/.exec(hostname)
  if (!mapped && !/^\d+\.\d+\.\d+\.\d+$/.test(hostname)) return false
  const firstWord = mapped ? parseInt(mapped[1], 16) : 0
  const [a, b] = mapped ? [firstWord >> 8, firstWord & 255] : hostname.split('.').map(Number)
  return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168)
}

function normalizeRoot(baseUrl: string): string | undefined {
  const value = baseUrl.trim()
  // Check the raw separators too: URL.search/hash are empty for a lone ? or #.
  if (!/^https?:\/\/[^/]/i.test(value) || /[?#\\\s]/.test(value)) return

  try {
    const url = new URL(value)
    if (!url.hostname || url.username || url.password || (url.protocol === 'http:' && !isLocalHost(url.hostname))) {
      return
    }
    const path = url.pathname.replace(/\/+$/, '').replace(/\/v1(?:beta)?$/, '')
    return `${url.origin}${path}`
  } catch {
    return
  }
}

function instanceId(dashboardOrigin: string, root: string): string {
  // A synchronous fingerprint of public addresses keeps dispatch in the click
  // event. Never derive the provider ID from the credential or display name.
  let hash = 0xcbf29ce484222325n
  for (const byte of new TextEncoder().encode(JSON.stringify([dashboardOrigin, root]))) {
    hash = BigInt.asUintN(64, (hash ^ BigInt(byte)) * 0x100000001b3n)
  }
  return hash.toString(16).padStart(16, '0')
}

export function buildMagpieImportLink(input: {
  dashboardOrigin: string
  baseUrl?: string
  siteName?: string
  row: ImportKey
}): ImportResult {
  const { row, dashboardOrigin } = input
  const root = normalizeRoot(input.baseUrl?.trim() || dashboardOrigin)
  if (!root) return { error: 'invalidBase' }

  const endpoints = resolveEndpoints(row, root)
  if (!endpoints) return { error: 'unsupportedGroup' }

  const key = row.key.trim()
  if (!key || [...row.key].some(c => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127) || new TextEncoder().encode(key).length > 4096) {
    return { error: 'invalidKey' }
  }

  const site = input.siteName?.trim() || 'Sub2API'
  const name = row.name.trim() ? `${row.name.trim()} · ${site}` : site
  const query = new URLSearchParams({
    name,
    id: `sub2api-${instanceId(dashboardOrigin, root)}-${row.id}`,
    ...endpoints,
    key
  })
  return { url: `magpie://import?${query}` }
}
