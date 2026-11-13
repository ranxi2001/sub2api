import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import PlazaModelPricingTable from '../PlazaModelPricingTable.vue'
import zh from '@/i18n/locales/zh/dashboard'
import en from '@/i18n/locales/en/dashboard'
import type { PlazaModel } from '@/api/modelPlaza'

enableAutoUnmount(afterEach)

function mediaModel(mode: 'video' | 'image' | 'per_request', tiers = false): PlazaModel {
  return {
    name: `${mode}-demo`,
    platform: 'openai',
    official_pricing: null,
    pricing: {
      billing_mode: mode,
      input_price: null,
      output_price: null,
      cache_write_price: null,
      cache_read_price: null,
      image_input_price: null,
      image_output_price: null,
      per_request_price: 0.54,
      intervals: tiers ? [0.54, 1.08].map((price, index) => ({
        min_tokens: 0,
        max_tokens: null,
        tier_label: index === 0 ? '480p' : '720p',
        input_price: null,
        output_price: null,
        cache_write_price: null,
        cache_read_price: null,
        per_request_price: price
      })) : []
    }
  }
}

describe.each([
  { locale: 'zh', label: '按秒计费', suffix: '/ 秒', requestLabel: '按次计费', requestSuffix: '/ 次', imageLabel: '按图片计费', imageSuffix: '/ 张' },
  { locale: 'en', label: 'Per second', suffix: '/ second', requestLabel: 'Per request', requestSuffix: '/ request', imageLabel: 'Per image', imageSuffix: '/ image' }
])('PlazaModelPricingTable video units ($locale)', (language) => {
  function render(model: PlazaModel, extraProps = {}) {
    return mount(PlazaModelPricingTable, {
      props: { models: [model], rateMultiplier: 0.5, ...extraProps },
      global: {
        plugins: [createI18n({ legacy: false, locale: language.locale, messages: { zh, en } })]
      }
    })
  }

  it.each([
    { name: 'base price', tiers: false, props: {}, prices: ['$0.27'], rate: '0.5x' },
    { name: 'user discount', tiers: false, props: { userRateMultiplier: 0.25 }, prices: ['$0.135'], rate: '0.25x' },
    { name: 'independent video rate', tiers: false, props: { userRateMultiplier: 0.25, videoRateIndependent: true, videoRateMultiplier: 2 }, prices: ['$1.08'], rate: '2x' },
    { name: 'resolution prices with user discount', tiers: true, props: { userRateMultiplier: 0.25 }, prices: ['$0.135', '$0.27'], rate: '0.25x' },
    { name: 'resolution prices with independent rate', tiers: true, props: { userRateMultiplier: 0.25, videoRateIndependent: true, videoRateMultiplier: 2 }, prices: ['$1.08', '$2.16'], rate: '2x' }
  ])('shows seconds and preserves $name', ({ tiers, props, prices, rate }) => {
    const wrapper = render(mediaModel('video', tiers), {
      imageRateIndependent: true,
      imageRateMultiplier: 9,
      ...props
    })
    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toContain(language.label)
    expect(cells[0].text()).not.toContain(language.requestLabel)
    expect(cells[1].attributes('colspan')).toBe('3')
    expect(cells[1].text()).not.toContain(language.requestSuffix)
    const units = cells[1].findAll('span').filter(span => span.text() === language.suffix)
    expect(units).toHaveLength(prices.length)
    if (tiers) {
      const chips = cells[1].findAll('div > span')
      expect(chips).toHaveLength(2)
      chips.forEach((chip, index) => {
        expect(chip.text()).toContain(index === 0 ? '480p' : '720p')
        expect(chip.text()).toContain(prices[index])
        expect(chip.text()).toContain(language.suffix)
      })
    } else {
      expect(cells[1].text()).toContain(prices[0])
    }
    expect(cells.at(-1)!.text()).toContain(rate)
    expect(cells.slice(2, 5).map(cell => cell.text())).toEqual(['-', '-', '-'])
    expect(wrapper.text()).not.toContain('modelPlaza.table.')
  })

  it.each(['image', 'per_request'] as const)('preserves %s labels, units and prices', (mode) => {
    const cells = render(mediaModel(mode)).findAll('tbody td')
    expect(cells[0].text()).toContain(mode === 'image' ? language.imageLabel : language.requestLabel)
    expect(cells[1].text()).toContain(mode === 'image' ? language.imageSuffix : language.requestSuffix)
    expect(cells[1].text()).toContain('$0.27')
    expect(cells[1].text()).not.toContain(language.suffix)
    expect(cells.at(-1)!.text()).toBe('0.5x')
  })

  it('preserves token columns, per-million prices and custom discounts', () => {
    const model = mediaModel('video')
    model.pricing = {
      ...model.pricing!, billing_mode: 'token', input_price: 2e-6, output_price: 8e-6,
      cache_write_price: 4e-6, cache_read_price: 1e-6, per_request_price: null
    }
    const wrapper = render(model, { userRateMultiplier: 0.25 })
    const cells = wrapper.findAll('tbody td')
    expect(cells).toHaveLength(8)
    expect(cells[0].text()).toBe(model.name)
    expect(cells[1].text()).toBe('$0.50')
    expect(cells[2].text()).toBe('$2.00')
    expect(cells[3].text()).toContain('$1.00')
    expect(cells[3].text()).toContain('$0.25')
    expect(wrapper.find('thead').text()).toContain(language.locale === 'zh' ? '$ / 1M token' : '$ / 1M tokens')
    expect(wrapper.find('tbody').text()).not.toContain(language.suffix)
    expect(cells[7].text()).toContain('0.25x')
  })
})
