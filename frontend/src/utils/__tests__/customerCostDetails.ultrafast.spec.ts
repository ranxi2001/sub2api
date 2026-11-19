import { describe, expect, it } from 'vitest'
import type { UsageLog } from '@/types'
import { customerCachePrices, customerCostSnapshot, customerCostsUnknown } from '../customerCostDetails'

const fixture = (): UsageLog => ({
 service_tier:'ultrafast',rate_multiplier:.6,actual_cost:.000996,total_cost:.00996,
 input_cost:.00426,output_cost:.0057,cache_creation_cost:0,cache_read_cost:0,
 customer_cost_details:{version:1,free_fast:false,free_ultrafast:true,input_cost:.00071,output_cost:.00095,
 image_input_cost:0,image_output_cost:0,cache_creation_cost:0,cache_read_cost:0,total_cost:.00166,actual_cost:.000996,multiplier:.6},
} as UsageLog)
describe('UltraFast customer cost snapshots',()=>{
 it('uses the saved Standard quote while retaining sixfold upstream costs',()=>{
  const row=fixture();expect(customerCostSnapshot(row)?.input_cost).toBe(.00071)
  expect(row.total_cost).toBe(.00996);expect(customerCostsUnknown(row)).toBe(false)
 })
 it('does not accept a Free Fast marker as a Free UltraFast snapshot',()=>{
  const row=fixture();row.customer_cost_details!.free_ultrafast=false;row.customer_cost_details!.free_fast=true
  expect(customerCostSnapshot(row)).toBeNull();expect(customerCostsUnknown(row)).toBe(true)
 })
 it('does not reconstruct discounted prices without a snapshot',()=>{
  const row=fixture();row.customer_cost_details=null
  expect(customerCostSnapshot(row)).toBeNull();expect(customerCostsUnknown(row)).toBe(true)
 })
 it('rejects a snapshot inconsistent with actual billing',()=>{
  const row=fixture();row.actual_cost=.005976
  expect(customerCostSnapshot(row)).toBeNull()
 })
})

describe('image minimum charge and saved Standard tier costs', () => {
 it.each(['fast', 'ultrafast'])('renders v3 %s components and cache prices without repricing history', tier => {
  const row = fixture()
  row.service_tier = tier
  row.actual_cost = .01
  row.cache_read_tokens = 40
  row.customer_cost_details = {
   version: 3, free_fast: tier === 'fast', free_ultrafast: tier === 'ultrafast',
   input_cost: 0, output_cost: 0, image_input_cost: 0, image_output_cost: 0,
   cache_creation_cost: 0, cache_read_cost: .004, total_cost: .004, multiplier: .5,
   actual_cost: .01, image_minimum_charge: .01, image_minimum_charge_adjustment: .008,
   cache_cost_details: [{ kind: 'read', tokens: 40, unit_price: .0001 }],
  }
  expect(customerCostSnapshot(row)).toBe(row.customer_cost_details)
  expect(customerCostsUnknown(row)).toBe(false)
  expect(customerCachePrices(row)).toEqual([{ kind: 'read', tokens: 40, unit_price: .0001 }])
  row.rate_multiplier = 999
  row.total_cost = 123
  expect(customerCostSnapshot(row)?.multiplier).toBe(.5)
  row.customer_cost_details.image_minimum_charge_adjustment = .007
  expect(customerCostSnapshot(row)).toBeNull()
 })
 it('retains v3 Standard evidence when token costs already exceed the minimum', () => {
  const row = fixture()
  Object.assign(row.customer_cost_details!, {
   version: 3, image_minimum_charge: .0001, image_minimum_charge_adjustment: 0,
  })
  expect(customerCostSnapshot(row)).toBe(row.customer_cost_details)
  expect(customerCostsUnknown(row)).toBe(false)
 })
})
