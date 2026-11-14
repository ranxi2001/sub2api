import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import Selector from '../ModerationUpstreamAccountSelector.vue'
import QualityAccountSelector from '@/components/admin/operations/QualityAccountSelector.vue'

const { list } = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { list } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const row = (id: number) => ({ id, name: `Fixture ${id}`, platform: 'openai', type: 'oauth' })
const response = (ids: number[], page = 1, pages = 3) => ({ items: ids.map(row), page, pages, total: 125 })
function mountPicker() {
  return mount(defineComponent({ components: { Selector }, setup() { return { ids: ref([999]), scope: ref('selected') } }, template: '<Selector v-model="ids" v-model:scope="scope" :groups="[{id:1,name:\'Demo\'}]" />' }))
}
beforeEach(() => list.mockReset())
describe('Moderation real QualityAccountSelector integration', () => {
  it('retains hidden IDs across search, type/group filters, pagination and batch union', async () => {
    list.mockResolvedValue(response([1, 2]))
    const wrapper = mountPicker(); await flushPromises()
    expect(wrapper.findComponent(QualityAccountSelector).exists()).toBe(true)
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    expect(wrapper.vm.ids).toEqual([999, 1, 2])
    list.mockResolvedValue(response([51], 2))
    wrapper.findComponent(QualityAccountSelector).vm.$emit('search', 2); await flushPromises()
    expect(list.mock.lastCall?.[0]).toBe(2)
    await wrapper.get('[data-account-id="51"] input').setValue(true)
    await wrapper.get('#moderation-upstream-search').setValue('#125'); await flushPromises()
    await wrapper.get('[data-testid="quality-account-type"]').setValue('oauth'); await flushPromises()
    await wrapper.get('[data-testid="quality-account-group"]').setValue('1'); await flushPromises()
    expect(list.mock.lastCall?.[2]).toMatchObject({ search: '#125', type: 'oauth', group: '1' })
    list.mockResolvedValueOnce(response([125], 1, 2)).mockResolvedValueOnce(response([126], 2, 2))
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(wrapper.vm.ids).toEqual([999, 1, 2, 51, 125, 126])
    wrapper.unmount()
  })
  it('fences late page responses and a batch after clear or filter change', async () => {
    let oldPage!: (value: ReturnType<typeof response>) => void
    list.mockImplementationOnce(() => new Promise(resolve => { oldPage = resolve }))
    const wrapper = mountPicker()
    list.mockResolvedValue(response([125]))
    await wrapper.get('#moderation-upstream-search').setValue('#125'); await flushPromises()
    oldPage(response([1])); await flushPromises()
    expect(wrapper.find('[data-account-id="125"]').exists()).toBe(true)
    expect(wrapper.find('[data-account-id="1"]').exists()).toBe(false)
    let batch!: (value: ReturnType<typeof response>) => void
    list.mockImplementationOnce(() => new Promise(resolve => { batch = resolve }))
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click')
    await wrapper.get('[data-testid="quality-clear-selection"]').trigger('click')
    batch(response([1], 1, 1)); await flushPromises()
    expect(wrapper.vm.ids).toEqual([])
    await wrapper.get('[data-testid="moderation-upstream-all"]').setValue(true)
    expect(wrapper.findComponent(QualityAccountSelector).exists()).toBe(false)
    expect(wrapper.vm.ids).toEqual([])
    wrapper.unmount()
  })
})
