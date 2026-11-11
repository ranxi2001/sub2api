import { describe, expect, it } from 'vitest'
import {
  harvestAccountLabel,
  harvestAvailabilityRecoverClock,
  harvestAvailabilityRank,
  orderHarvestAccounts,
  resolveHarvestAvailability
} from '../harvestAvailability'

describe('harvest availability', () => {
  it('falls back when the backend field is missing', () => {
    expect(resolveHarvestAvailability({ status: 'error', schedulable: true })).toBe('error')
    expect(resolveHarvestAvailability({ status: 'active', schedulable: false })).toBe('disabled')
    expect(resolveHarvestAvailability({ status: 'active', schedulable: true })).toBe('available')
    expect(resolveHarvestAvailability({ availability: 'rate_limited', status: 'active', schedulable: true })).toBe('rate_limited')
  })

  it('orders available accounts first and matches search without dropping the rest', () => {
    const accounts = [
      { id: 3, name: '5x', availability: 'available', schedulable: true },
      { id: 2, name: '20x', availability: 'rate_limited', schedulable: true, recover_at: '2026-09-21T00:00:00+08:00' },
      { id: 4, name: '91topgo', availability: 'disabled', schedulable: false }
    ]
    expect(orderHarvestAccounts(accounts, '').map(item => item.id)).toEqual([3, 2, 4])
    expect(orderHarvestAccounts(accounts, '20x').map(item => item.id)).toEqual([2, 3, 4])
    expect(harvestAvailabilityRank('available')).toBeLessThan(harvestAvailabilityRank('expired'))
  })

  it('keeps a selected account label from filtering the list to itself', () => {
    const selected = { id: 2, name: '20x', availability: 'available', schedulable: true }
    const accounts = [selected, { id: 3, name: '5x', availability: 'available', schedulable: true }]
    expect(orderHarvestAccounts(accounts, harvestAccountLabel(selected), selected).map(item => item.id)).toEqual([2, 3])
  })

  it('shows clock-only recovery on the same day', () => {
    const now = new Date(2026, 8, 20, 23, 13)
    expect(harvestAvailabilityRecoverClock(new Date(2026, 8, 20, 23, 52).toISOString(), now)).toEqual({ clock: '23:52' })
    expect(harvestAvailabilityRecoverClock(new Date(2026, 8, 21, 0, 40).toISOString(), now)).toEqual({ clock: '00:40', date: '09-21' })
  })
})