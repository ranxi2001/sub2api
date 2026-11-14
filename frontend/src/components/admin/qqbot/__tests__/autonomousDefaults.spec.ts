import { describe, expect, it } from 'vitest'
import { defaultAutonomousConfig } from '@/api/qqbot'
import { enableAutonomousChat, autonomousPresets, applyAutonomousPreset, autonomousSummary } from '../autonomousDefaults'
describe('one-switch auto chat', () => {
 it('keeps frequency presets monotonic and active above defaults', () => {
  for (let i = 1; i < autonomousPresets.length; i++) {
   const before = autonomousPresets[i - 1]!, after = autonomousPresets[i]!
   for (const key of ['sample_percent', 'hourly_replies', 'daily_replies', 'daily_calls'] as const) expect(after[key]).toBeGreaterThan(before[key])
   for (const key of ['cooldown_seconds', 'min_messages', 'member_cooldown_seconds'] as const) expect(after[key]).toBeLessThan(before[key])
  }
  expect(applyAutonomousPreset(defaultAutonomousConfig(), 'active').hourly_replies).toBeGreaterThan(defaultAutonomousConfig().hourly_replies)
 })
 it('changes frequency explicitly while preserving scope, hours and policy', () => {
  const original = { ...defaultAutonomousConfig(), reply_mode: 'always' as const, groups: ['group'], start_hour: 9, end_hour: 23, daily_calls: 0 }
  expect(enableAutonomousChat(original).daily_calls).toBe(0)
  expect(applyAutonomousPreset(original, 'allMessages')).toMatchObject({ enabled: false, reply_mode: 'always', groups: ['group'], start_hour: 9, end_hour: 23, cooldown_seconds: 1, merge_seconds: 2, member_cooldown_seconds: 0 })
  expect(original.daily_calls).toBe(0)
 })
 it('calculates per-group rates without NaN or cross-group mixing', () => {
  const result = autonomousSummary([
   { group_id: 'a', reason: 'eligible', count: 10 }, { group_id: 'a', reason: 'selected', count: 5 },
   { group_id: 'a', reason: 'model_calls', count: 4 }, { group_id: 'a', reason: 'generated', count: 2 },
   { group_id: 'a', reason: 'sent', count: 1 }, { group_id: 'b', reason: 'received', count: 3 }
  ])
  expect(result[0]).toMatchObject({ group: 'a', selectionRate: '50.0%', replyRate: '50.0%', deliveryRate: '50.0%' })
  expect(result[1]).toMatchObject({ group: 'b', selectionRate: '—', replyRate: '—', deliveryRate: '—' })
 })
 it('inherits allowed groups without an extra picker', () => {
  const c = defaultAutonomousConfig()
  expect(enableAutonomousChat(c)).toMatchObject({ enabled: true, inherit_groups: true })
  expect(c.enabled).toBe(false)
 })
 it('preserves explicit custom scope, zero limits and hours', () => {
  const c = { ...defaultAutonomousConfig(), groups: ['group'], inherit_groups: false, daily_calls: 0, start_hour: 9 }
  expect(enableAutonomousChat(c)).toEqual({ ...c, enabled: true })
 })
 it('upgrades untouched legacy defaults only on opt-in', () => {
  const c = { ...defaultAutonomousConfig(), sample_percent: 15, cooldown_seconds: 120, min_messages: 6, member_cooldown_seconds: 600, hourly_replies: 3, daily_replies: 20, daily_calls: 40, start_hour: 9, end_hour: 23, followup_seconds: 60 }
  expect(enableAutonomousChat(c)).toMatchObject({ min_messages: 2, followup_seconds: 90, inherit_groups: true })
  expect(enableAutonomousChat({ ...c, daily_calls: 0 }).daily_calls).toBe(0)
  expect(enableAutonomousChat({ ...c, enabled: true }).min_messages).toBe(6)
 })
})
