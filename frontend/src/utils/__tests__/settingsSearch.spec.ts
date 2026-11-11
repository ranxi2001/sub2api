import { describe, expect, it } from 'vitest'
import { parse } from 'vue/compiler-sfc'
import source from '@/views/admin/SettingsView.vue?raw'
import smartOpsSource from '@/components/settings/SmartOpsSidebarSettings.vue?raw'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import { buildFeatureSearchEntries, searchFeatures } from '../featureSearch'
import { SETTINGS_SECTIONS, settingsLocation, withSettingsSearch } from '../settingsSearch'

function translate(messages: unknown, key: string): string {
  const value = key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], messages)
  expect(typeof value, key).toBe('string')
  return value as string
}

describe('settings feature search', () => {
  it('finds risk switches in both languages even when the business page is absent', () => {
    for (const [locale, messages, queries] of [
      ['zh', zh, ['风控', 'cyber', '会话自动屏蔽', '屏蔽时长', '严格要求明确会话身份']],
      ['en', en, ['risk', 'cyber']]
    ] as const) {
      const entries = buildFeatureSearchEntries(withSettingsSearch([
        { path: '/admin/settings', label: '系统设置' }
      ], key => translate(messages, key), locale))
      for (const query of queries) {
        expect(searchFeatures(entries, query).some(item => item.path === '/admin/settings?tab=features#settings-section-features-risk-control'), query).toBe(true)
      }
      expect(entries.some(item => item.path === '/admin/risk-control')).toBe(false)
      expect(withSettingsSearch([{ path: '/admin/users', label: '用户' }], key => translate(messages, key), locale)[0]?.children).toBeUndefined()
    }
  })

  it('keeps every indexed heading in its actual settings tab with a unique anchor', () => {
    const root = parse(source).descriptor.template!.ast!
    type Node = typeof root.children[number]
    const anchors = new Map<string, string>()
    function visit(node: Node, tab = '') {
      if (node.type !== 1) return
      const show = node.props.find(prop => prop.type === 7 && prop.name === 'show')
      if (show?.type === 7 && show.exp?.type === 4) tab = show.exp.content.match(/activeTab === '([^']+)'/)?.[1] ?? tab
      const id = node.props.find(prop => prop.type === 6 && prop.name === 'id')
      if (id?.type === 6 && id.value?.content.startsWith('settings-section-')) {
        expect(anchors.has(id.value.content)).toBe(false)
        anchors.set(id.value.content, tab)
      }
      if (node.tag === 'SmartOpsSidebarSettings') {
        const childRoot = parse(smartOpsSource).descriptor.template!.ast!
        for (const child of childRoot.children) visit(child, tab)
      }
      for (const child of node.children) visit(child, tab)
    }
    for (const child of root.children) visit(child)
    expect(anchors.size).toBe(SETTINGS_SECTIONS.length)
    for (const section of SETTINGS_SECTIONS) expect(anchors.get(`settings-section-${section.id}`), section.id).toBe(section.tab)
  })

  it('finds the usage cache rate switches from the usage records card', () => {
    for (const [locale, messages, query] of [
      ['zh', zh, '缓存率'],
      ['en', en, 'cache rate'],
    ] as const) {
      const entries = buildFeatureSearchEntries(withSettingsSearch([
        { path: '/admin/settings', label: 'Settings' }
      ], key => translate(messages, key), locale))
      expect(searchFeatures(entries, query).some(item => item.path === '/admin/settings?tab=gateway#settings-section-usage-records'), query).toBe(true)
    }
  })

  it('validates locations and prefers the target card tab over a conflicting query', () => {
    expect(settingsLocation('security', '#settings-section-features-risk-control').tab).toBe('features')
    expect(settingsLocation(['features'], '#unknown')).toEqual({ tab: 'general', anchor: undefined, linked: false })
    expect(settingsLocation('email', '')).toEqual({ tab: 'email', anchor: undefined, linked: true })
  })
})
