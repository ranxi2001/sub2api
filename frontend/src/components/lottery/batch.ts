export const DRAW_COUNTS = [1, 3, 5] as const
export type DrawCount = typeof DRAW_COUNTS[number]

export function readDrawKeys(value: string | null): string[] {
  if (!value) return []
  if (!value.startsWith('{')) return [value]
  try {
    const data = JSON.parse(value)
    if (data.version === 1 && Array.isArray(data.keys) && DRAW_COUNTS.includes(data.keys.length)
      && data.keys.every((key: unknown) => typeof key === 'string' && key.length > 0)
      && new Set(data.keys).size === data.keys.length) return data.keys
  } catch { /* Ignore malformed local state. */ }
  return []
}

export function serializeDrawKeys(keys: string[]) {
  return keys.length === 1 ? keys[0] : JSON.stringify({ version: 1, keys })
}

export function isDefinitiveDrawError(error: unknown) {
  const status = (error as { status?: number })?.status
  return !!status && status >= 400 && status < 500 && status !== 408 && status !== 429
}
