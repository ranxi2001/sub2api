import { apiClient } from '../client'

export type TimerAction = 'open' | 'close'
interface OneShotTimerInput {
  mode: 'relative' | 'absolute'
  hours: number
  minutes: number
  date: string
  time: string
  timezone: string
}
export type TimerInput = OneShotTimerInput | {
  mode: 'daily'
  time: string
  timezone: string
}
export interface TimerPreview {
  execute_at: string
  timezone: string
  local_date: string
  local_time: string
  offset: string
  server_now: string
}
export interface SchedulableTimer {
  account_id: number
  action: TimerAction
  version: number
  execute_at: string
  timezone: string
  local_date?: string
  local_time?: string
  offset?: string
  requested_by: number
  status: 'pending' | 'executed' | 'skipped' | 'cancelled' | 'failed'
  attempts: number
  actual_at: string | null
  reason: string
  // Older API responses have no recurrence fields and represent one-shot plans.
  recurrence?: 'once' | 'daily'
  daily_time?: string
}
export interface TimerAudit {
  action: TimerAction
  version: number
  actor_id: number
  execute_at: string
  timezone: string
  actual_at: string
  result: string
  reason: string
}
export interface TimerSnapshot {
  tasks: SchedulableTimer[]
  history: TimerAudit[]
  server_now: string
  poll_seconds: number
  open_grace_seconds: number
  max_delay_minutes: number
}
export interface DailyWindowInput {
  open_time: string
  close_time: string
  timezone: string
  expected_open_version: number
  expected_close_version: number
}
export const schedulableTimersAPI = {
  async list(id: number) {
    return (await apiClient.get<TimerSnapshot>(`/admin/accounts/${id}/schedulable-timers`)).data
  },
  async preview(id: number, input: TimerInput) {
    return (await apiClient.post<TimerPreview>(`/admin/accounts/${id}/schedulable-timers/preview`, input)).data
  },
  async save(id: number, input: TimerInput & { action: TimerAction; expected_version: number; confirmed_at?: string }) {
    return (await apiClient.post<SchedulableTimer>(`/admin/accounts/${id}/schedulable-timers`, input)).data
  },
  async saveDailyWindow(id: number, input: DailyWindowInput) {
    return (await apiClient.post<TimerSnapshot>(`/admin/accounts/${id}/schedulable-timers/daily-window`, input)).data
  },
  async cancel(id: number, action: TimerAction, version: number) {
    await apiClient.delete(`/admin/accounts/${id}/schedulable-timers/${action}`, { params: { version } })
  }
}
