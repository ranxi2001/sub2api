import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import Cell from '../AccountPerformanceCell.vue'
import en from '@/i18n/locales/en'
const render = (metrics = {}) => mount(Cell, {props:{metrics},global:{plugins:[createI18n({legacy:false,locale:'en',messages:{en}})]}})
describe('Account performance rolling window',()=>{
 it('renders exactly three metric lines and missing samples as uncollected',()=>{
  const w=render();expect(w.findAll('[data-metric]')).toHaveLength(3)
  expect(w.findAll('[data-metric]').every(r=>r.text().includes('Not collected'))).toBe(true)
  expect(w.text()).toContain('Probe degradation · 60m')
  expect(w.attributes('title')).toContain('Rolling 60 minutes')
 })
 it('uses the response window for labels and tooltip and updates after refresh',async()=>{
  const window_end='2026-11-02T01:00:00Z'
  const w=render({window_start:'2026-11-02T00:45:00Z',window_end})
  expect(w.text()).toContain('Probe degradation · 15m')
  expect(w.attributes('title')).toContain('Rolling 15 minutes')
  await w.setProps({metrics:{window_start:'2026-11-01T23:00:00Z',window_end}})
  expect(w.text()).toContain('Probe degradation · 120m')
  expect(w.attributes('title')).toContain('Rolling 120 minutes')
 })
 it('preserves a measured zero degradation and shows independent automatic pause',()=>{
  const w=render({avg_tps:25,degradation_percent:0,avg_ttft_ms:1500,paused:true})
  expect(w.text()).toContain('25.0 t/s');expect(w.text()).toContain('0.0%');expect(w.text()).toContain('1.50s')
  expect(w.text()).toContain('Automatically paused')
  expect(w.find('button').exists()).toBe(true)
 })
})

it('emits configure and distinguishes load failure from missing samples', async () => {
 const w=mount(Cell,{props:{unavailable:true},global:{plugins:[createI18n({legacy:false,locale:'en',messages:{en}})]}})
 expect(w.text()).toContain('Unavailable')
 await w.get('button').trigger('click')
 expect(w.emitted('configure')).toHaveLength(1)
})
