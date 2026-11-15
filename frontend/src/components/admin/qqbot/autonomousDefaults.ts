import { defaultAutonomousConfig, type QQAutonomousConfig } from '@/api/qqbot'

// Explicit presets are independent of participation policy. Never apply these
// on load: existing scopes, hours, zero budgets and custom values survive.
export const autonomousPresets = [
  { key: 'quiet', sample_percent: 5, cooldown_seconds: 300, min_messages: 10, member_cooldown_seconds: 600, hourly_replies: 1, daily_replies: 8, daily_calls: 15, merge_seconds: 2 },
  { key: 'moderate', sample_percent: 25, cooldown_seconds: 60, min_messages: 2, member_cooldown_seconds: 120, hourly_replies: 12, daily_replies: 80, daily_calls: 160, merge_seconds: 2 },
  { key: 'active', sample_percent: 60, cooldown_seconds: 10, min_messages: 1, member_cooldown_seconds: 15, hourly_replies: 60, daily_replies: 300, daily_calls: 600, merge_seconds: 2 },
  { key: 'allMessages', sample_percent: 100, cooldown_seconds: 1, min_messages: 0, member_cooldown_seconds: 0, hourly_replies: 600, daily_replies: 3000, daily_calls: 3600, merge_seconds: 2 }
] as const
export function applyAutonomousPreset(current: QQAutonomousConfig, key: typeof autonomousPresets[number]['key']): QQAutonomousConfig {
  const preset = autonomousPresets.find(p => p.key === key)!
  const { key: _key, ...fields } = preset
  void _key
  return { ...current, ...fields }
}

export function autonomousSummary(stats: { group_id: string; reason: string; count: number }[]) {
  const groups = new Map<string, Record<string, number>>()
  for (const stat of stats) {
    const values = groups.get(stat.group_id) ?? {}
    values[stat.reason] = (values[stat.reason] ?? 0) + stat.count
    groups.set(stat.group_id, values)
  }
  const percent = (numerator: number, denominator: number) => denominator > 0 ? `${(numerator / denominator * 100).toFixed(1)}%` : '—'
  return [...groups].map(([group, metrics]) => ({ group, metrics,
    selectionRate: percent(metrics.selected ?? 0, metrics.eligible ?? 0),
    replyRate: percent(metrics.generated ?? 0, metrics.model_calls ?? 0),
    deliveryRate: percent(metrics.sent ?? 0, metrics.generated ?? 0) }))
}

// Upgrade only the untouched legacy profile on opt-in. Custom scopes, zero
// budgets and other operator overrides remain authoritative.
const legacy = { sample_percent: 15, cooldown_seconds: 120, min_messages: 6,
  member_cooldown_seconds: 600, hourly_replies: 3, daily_replies: 20, daily_calls: 40,
  start_hour: 9, end_hour: 23, followup_seconds: 60, max_chars: 100, mention_user: false }
export function enableAutonomousChat(current: QQAutonomousConfig): QQAutonomousConfig {
  const next = { ...current, enabled: true }
  if (!current.enabled && (!current.reply_mode || current.reply_mode === 'natural') && !current.merge_seconds && (current.input_token_budget == null || current.input_token_budget === 8192) && Object.entries(legacy).every(([key, value]) => current[key as keyof QQAutonomousConfig] === value)) {
    const defaults = defaultAutonomousConfig()
    for (const key of Object.keys(legacy) as (keyof typeof legacy)[]) Object.assign(next, { [key]: defaults[key] })
  }
  if (!next.groups.length) next.inherit_groups = true
  return next
}
