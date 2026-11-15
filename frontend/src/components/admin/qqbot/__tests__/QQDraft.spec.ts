import { defineComponent, ref } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createI18n } from 'vue-i18n'
import { useQQDraft } from '../useQQDraft'
import zh from '@/i18n/locales/zh'

afterEach(() => vi.restoreAllMocks())

it('protects dirty inactive KeepAlive tabs and unregisters them after leaving', async () => {
  const Draft = defineComponent({
    setup() { const form = ref({ value: 'saved' }); const draft = useQQDraft(form); return { form, draft } },
    template: '<div><input v-model="form.value" /><button @click="draft.markSaved()">Save</button></div>'
  })
  const Tabs = defineComponent({
    components: { Draft },
    template: '<KeepAlive><Draft :key="String($route.query.tab || 1)" /></KeepAlive>'
  })
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/admin/qqbot/activities', component: Tabs }, { path: '/other', component: { template: '<p>Other</p>' } }
  ] })
  await router.push('/admin/qqbot/activities?tab=1')
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
  const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router, createI18n({ legacy: false, locale: 'zh', messages: { zh } })] } })
  await flushPromises()
  await wrapper.get('input').setValue('draft')
  await router.push('/admin/qqbot/activities?tab=2'); await flushPromises()
  expect(confirm).not.toHaveBeenCalled()
  expect((wrapper.get('input').element as HTMLInputElement).value).toBe('saved')
  await router.push('/other'); await flushPromises()
  expect(confirm).toHaveBeenCalledOnce()
  expect(router.currentRoute.value.path).toBe('/admin/qqbot/activities')
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  expect(event.defaultPrevented).toBe(true)
  await router.push('/admin/qqbot/activities?tab=1'); await flushPromises()
  expect((wrapper.get('input').element as HTMLInputElement).value).toBe('draft')
  await wrapper.get('button').trigger('click')
  await router.push('/other'); await flushPromises()
  expect(router.currentRoute.value.path).toBe('/other')
  expect(confirm).toHaveBeenCalledOnce()
  const clean = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(clean)
  expect(clean.defaultPrevented).toBe(false)
  wrapper.unmount()
})
