// Remote CI only: real KeysView, DataTable, GroupBadge, Pagination and project CSS.
// Authentication, the surrounding shell, unopened dialogs and API data are synthetic.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.cwd(), frontend = path.join(root, 'frontend')
const out = process.env.KEYS_LAYOUT_SCREENSHOT_DIR
assert(process.env.CI && out, 'Run this harness only on remote CI')
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.KEYS_LAYOUT_EXPECTED_HEAD)
const baseline = 'dc1e1b072faa53ec2e6f0925be39d5d024ec1687'
await fs.mkdir(out, { recursive: true })
const baselinePath = path.join(out, 'BaselineKeysView.vue')
await fs.writeFile(baselinePath, execFileSync('git', ['show', `${baseline}:frontend/src/views/user/KeysView.vue`]))
const baselineRiskPath = path.join(out, 'BaselineUserKeyRiskNotices.vue')
await fs.writeFile(baselineRiskPath, execFileSync('git', ['show', `${baseline}:frontend/src/features/ip-audit/UserKeyRiskNotices.vue`]))
const fixtureDir = path.join(frontend, '.keys-layout-fixture')
await fs.mkdir(fixtureDir, { recursive: true })

const group = { id: 7, name: '【演示渠道】超长企业研发测试分组-仅供离线布局回归-标准费率', platform: 'openai', subscription_type: 'standard', rate_multiplier: 0.3, status: 'active' }
const keys = Array.from({ length: 4 }, (_, i) => ({
  id: i + 1, user_id: 41, name: `Demo key ${i + 1}`, key: `sk-offline-example-${i + 1}-0000`,
  group_id: 7, group_ids: [7], group, status: 'active', quota: 1000, quota_used: 30.25,
  created_at: '2026-10-30T04:34:10Z', updated_at: '2026-10-30T04:34:10Z', expires_at: null,
  ip_whitelist: [], ip_blacklist: [], last_used_at: null, last_used_ip: null,
  current_concurrency: 0, concurrency_limit: 0,
  rate_limit_5h: 0, rate_limit_1d: 0, rate_limit_7d: 0, usage_5h: 0, usage_1d: 0, usage_7d: 0,
}))
const notices = keys.map(key => ({ event_id: key.id, generation: 1, rule: 'key_ip_spread', state: 'pending', key_ids: [key.id], keys: [{ id: key.id, name: key.name }], first_seen: '2026-11-08T01:00:00Z', last_seen: '2026-11-08T01:01:00Z', attempts: 0, max_reminders: 3, acknowledged_at: null }))
const modules = {
  auth: `export const useAuthStore=()=>({user:{id:41,role:'user'},isAdmin:false});`,
  app: `export const useAppStore=()=>({cachedPublicSettings:{server_utc_offset:8},showError:message=>{throw Error(message)},showSuccess:()=>{}});`,
  onboarding: `export const useOnboardingStore=()=>({isCurrentStep:()=>false,nextStep:()=>{}});`,
  clipboard: `export const useClipboard=()=>({copyToClipboard:async()=>true});`,
  dialog: `export default {render:()=>null};`,
  layout: `import {h} from 'vue';export default {setup(_,{slots}){return()=>h('div',{class:'min-h-screen bg-gray-50 dark:bg-dark-950'},[h('header',{class:'h-16 border-b px-4 py-5 text-sm'},'Offline Keys UI · synthetic data'),h('main',{class:'p-4 md:p-6 lg:ml-64 lg:p-8'},slots.default?.())])}};`,
  api: `const keys=${JSON.stringify(keys)},group=${JSON.stringify(group)};
    if(new URLSearchParams(location.search).get('amounts')==='large')for(const key of keys){key.quota=999999999999.99;key.quota_used=888888888888.88;}
    export const keysAPI={list:async()=>({items:keys,total:4,pages:1}),getConcurrency:async()=>({queue_policy:{max_waiting:0,timeout_seconds:0},items:keys.map(k=>({id:k.id,current_concurrency:0,current_waiting:0}))})};
    export const usageAPI={getDashboardApiKeysUsage:async(apiKeyIds,options)=>{
      (window.keyUsageQueries||=[]).push({apiKeyIds,start_date:options?.start_date,end_date:options?.end_date,timezone:options?.timezone});
      return {stats:Object.fromEntries(keys.map(k=>[k.id,{today_actual_cost:12.3456,total_actual_cost:456.789}]))};
    }};
    export const authAPI={getPublicSettings:async()=>({api_base_url:'https://example.invalid/',custom_endpoints:[]})};
    export const userGroupsAPI={getAvailable:async()=>[group],getUserGroupRates:async()=>({})};`,
  client: `export const apiClient={get:async url=>{if(url!=='/user/key-risks')throw Error('Unexpected fixture API: '+url);window.riskReads=(window.riskReads||0)+1;return {data:{items:${JSON.stringify(notices)}}}},post:async()=>{throw Error('Unexpected mutation')}};`,
}
for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixtureDir, `${name}.js`), source)
await fs.writeFile(path.join(fixtureDir, 'index.html'), `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module">
import {createApp,h} from 'vue';import {createI18n} from 'vue-i18n';
import Candidate from '/src/views/user/KeysView.vue';import Baseline from '/@fs/${baselinePath}';
import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
const params=new URLSearchParams(location.search);window.riskReads=0;
if(params.get('font')==='200'){const style=document.createElement('style');style.textContent='.key-usage-cell { font-size:200% !important; line-height:1.5 !important; }';document.head.append(style);}
createApp({render:()=>h(params.has('baseline')?Baseline:Candidate)}).use(createI18n({legacy:false,locale:params.get('locale')||'zh',messages:{zh,en}})).mount('#app');</script></html>`)

const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const alias = (find, name) => ({ find, replacement: path.join(fixtureDir, `${name}.js`) })
const pinnedDependencies = { name: 'pinned-baseline-risk-dependencies', enforce: 'pre', resolveId(source, importer) {
  if (importer?.split('?')[0] !== baselineRiskPath) return null
  if (source === './RiskIdentity.vue' || source === './keyRiskApi') return path.join(frontend, 'src/features/ip-audit', source.slice(2) + (source.endsWith('.vue') ? '' : '.ts'))
  return null
} }
const server = await createServer({ root: frontend, configFile: false, plugins: [pinnedDependencies, vue()],
  resolve: { alias: [
    { find: '@/features/ip-audit/UserKeyRiskNotices.vue', replacement: path.join(frontend, 'src/features/ip-audit/UserKeyRiskNotices.vue'), customResolver(source, importer) { return importer?.split('?')[0] === baselinePath ? baselineRiskPath : source } },
    alias(/^@\/api$/, 'api'), alias('@/api/client', 'client'),
    alias('@/stores/auth', 'auth'), alias('@/stores/app', 'app'), alias('@/stores/onboarding', 'onboarding'),
    alias('@/composables/useClipboard', 'clipboard'), alias('@/components/layout/AppLayout.vue', 'layout'),
    ...['keys/BulkEditKeysModal', 'admin/user/ApiKeyModelPolicyDialog', 'common/NewFeatureBadge', 'common/BaseDialog', 'common/ConfirmDialog', 'keys/UseKeyModal', 'keys/CodexQuickConfigModal'].map(name => alias(`@/components/${name}.vue`, 'dialog')),
    { find: '@', replacement: path.join(frontend, 'src') },
  ] },
  css: { postcss: { plugins: [tailwind({ ...theme, content: [path.join(frontend, 'src/**/*.{vue,js,ts}'), baselinePath, baselineRiskPath, path.join(fixtureDir, '*.{js,html}')] }), autoprefixer()] } },
  define: { __INTLIFY_JIT_COMPILATION__: true },
  server: { host: '127.0.0.1', port: 4184, strictPort: true, fs: { allow: [root, out] } },
  optimizeDeps: { entries: [path.join(fixtureDir, 'index.html')], include: ['vue', 'vue-i18n'] },
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const measurements = [], shots = [], errors = []
async function open(viewport, query = '') {
  const page = await browser.newPage({ viewport })
  // No production, external or paid requests can leave this fixture.
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.continue() : route.abort())
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(`http://127.0.0.1:4184/.keys-layout-fixture/index.html${query}`, { waitUntil: 'networkidle' })
  await page.getByText('Demo key 1', { exact: true }).first().waitFor({ state: 'attached' })
  await page.evaluate(() => document.fonts.ready)
  return page
}
async function screenshot(page, name, scope) {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false, animations: 'disabled' })
  shots.push({ name, scope, sha: scope === 'baseline' ? baseline : head, viewport: page.viewportSize() })
}
async function checkRiskSummary(page) {
  const toggle = page.locator('[data-test="toggle-risk-notices"]')
  const details = page.locator('[data-test="risk-notices-details"]')
  assert(await toggle.isVisible())
  assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
  assert.equal(await details.count(), 1)
  assert(await details.isHidden())
  const detailsId = await details.getAttribute('id')
  assert(detailsId)
  assert.equal(await toggle.getAttribute('aria-controls'), detailsId)
  assert.equal(await page.evaluate(() => window.riskReads), 1)
  await toggle.focus()
  await page.keyboard.press('Enter')
  assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
  assert(await details.isVisible())
  assert.equal(await toggle.getAttribute('aria-controls'), await details.getAttribute('id'))
  const bounded = await details.evaluate(el => ({ height: el.getBoundingClientRect().height, overflow: getComputedStyle(el).overflowY, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }))
  assert(bounded.height <= Math.min(page.viewportSize().height * 0.24, 256) + 1, JSON.stringify(bounded))
  assert.equal(bounded.overflow, 'auto')
  assert(bounded.scrollHeight > bounded.clientHeight, 'Long risk details must scroll internally')
  return { toggle, bounded }
}
try {
  const before = await open({ width: 1366, height: 768 }, '?baseline=1')
  assert.equal(await before.evaluate(() => window.riskReads), 1)
  const beforeHeight = await before.locator('.table-wrapper').evaluate(el => el.getBoundingClientRect().height)
  assert(beforeHeight < 5, `Pinned baseline must reproduce squeezed table, got ${beforeHeight}px`)
  await screenshot(before, 'before-desktop', 'baseline')
  await before.setViewportSize({ width: 320, height: 844 })
  await before.locator('[data-field="group"]').first().scrollIntoViewIfNeeded()
  await screenshot(before, 'before-mobile-320', 'baseline')
  await before.close()

  const desktop = await open({ width: 1366, height: 768 })
  const { toggle, bounded } = await checkRiskSummary(desktop)
  const table = desktop.locator('.table-wrapper')
  const expandedHeight = await table.evaluate(el => el.getBoundingClientRect().height)
  assert(expandedHeight > 100, `Expanded risk details must leave a readable table, got ${expandedHeight}px`)
  await screenshot(desktop, 'after-desktop-expanded', 'candidate')
  await toggle.click()
  assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
  assert.equal(await desktop.locator('[data-test="risk-notices-details"]').count(), 1)
  assert(await desktop.locator('[data-test="risk-notices-details"]').isHidden())
  const height = await table.evaluate(el => el.getBoundingClientRect().height)
  assert(height > 150, `Key table must retain readable space, got ${height}px`)
  await desktop.getByRole('columnheader', { name: '用量', exact: true }).scrollIntoViewIfNeeded()
  const usage = desktop.locator('tbody tr').first().getByText('$12.3456', { exact: true })
  await usage.scrollIntoViewIfNeeded()
  assert(await usage.isVisible())
  await screenshot(desktop, 'after-desktop', 'candidate')
  measurements.push({ viewport: desktop.viewportSize(), baselineTableHeight: beforeHeight, candidateTableHeight: height, expandedTableHeight: expandedHeight, riskDetails: bounded })
  await desktop.close()

  const short = await open({ width: 1366, height: 600 })
  const shortTable = short.locator('.table-wrapper')
  const collapsedShortHeight = await shortTable.evaluate(el => el.getBoundingClientRect().height)
  assert(collapsedShortHeight >= 220, `Short desktop needs readable collapsed table, got ${collapsedShortHeight}px`)
  const { bounded: shortRiskDetails } = await checkRiskSummary(short)
  const expandedShortHeight = await shortTable.evaluate(el => el.getBoundingClientRect().height)
  assert(expandedShortHeight >= 220, `Short desktop needs readable expanded table, got ${expandedShortHeight}px`)
  const shortUsage = short.locator('tbody tr').first().getByText('$12.3456', { exact: true })
  await shortUsage.scrollIntoViewIfNeeded()
  const shortUsageVisible = await shortUsage.evaluate(el => {
    const r = el.getBoundingClientRect(), table = el.closest('.table-wrapper').getBoundingClientRect()
    return r.top >= Math.max(0, table.top) && r.bottom <= Math.min(innerHeight, table.bottom) && r.left >= Math.max(0, table.left) && r.right <= Math.min(innerWidth, table.right)
  })
  assert(shortUsageVisible, 'Usage must be reachable in the short desktop viewport')
  const paginator = short.locator('[aria-current="page"]')
  await paginator.scrollIntoViewIfNeeded()
  const paginationVisible = await paginator.evaluate(el => { const r = el.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight })
  assert(paginationVisible, 'Page scrolling must keep pagination reachable')
  await screenshot(short, 'after-short-desktop-expanded', 'candidate')
  measurements.push({ viewport: short.viewportSize(), collapsedTableHeight: collapsedShortHeight, expandedTableHeight: expandedShortHeight, riskDetails: shortRiskDetails, usageReachable: shortUsageVisible, paginationReachable: paginationVisible })
  await short.close()

  for (const width of [320, 390]) {
    const mobile = await open({ width, height: 844 })
    const { toggle, bounded } = await checkRiskSummary(mobile)
    await screenshot(mobile, `after-mobile-${width}-expanded`, 'candidate')
    await toggle.click()
    const groupField = mobile.locator('[data-field="group"]').first()
    const usageField = mobile.locator('[data-field="usage"]').first()
    const geometry = await mobile.evaluate(() => {
      const rect = el => { const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, width: r.width, height: r.height, scrollWidth: el.scrollWidth, clientWidth: el.clientWidth } }
      const group = document.querySelector('[data-field="group"]'), usage = document.querySelector('[data-field="usage"]')
      const hint = group.querySelector('.key-group-hint')
      return { group: rect(group), usage: rect(usage), label: rect(group.firstElementChild), labelLineHeight: parseFloat(getComputedStyle(group.firstElementChild).lineHeight), hint: rect(hint), hintLineHeight: parseFloat(getComputedStyle(hint).lineHeight), overflowing: [group, usage, ...group.querySelectorAll('*'), ...usage.querySelectorAll('*')].filter(el => el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 1).map(el => el.className), documentWidth: document.documentElement.scrollWidth }
    })
    assert(geometry.label.height <= geometry.labelLineHeight + 1, 'Group label must stay horizontal')
    assert(geometry.hint.height <= geometry.hintLineHeight + 1, 'Select-group hint must stay horizontal')
    assert.deepEqual(geometry.overflowing, [], JSON.stringify(geometry))
    assert(geometry.documentWidth <= width, JSON.stringify(geometry))
    assert(geometry.group.right <= width && geometry.usage.right <= width)
    assert.match(await usageField.innerText(), /\$12\.3456/)
    assert.match(await usageField.innerText(), /\$456\.7890/)
    assert.match(await usageField.innerText(), /\$30\.25\s*\/\s*\$1000\.00/)
    await groupField.scrollIntoViewIfNeeded()
    await screenshot(mobile, `after-mobile-${width}`, 'candidate')
    measurements.push({ viewport: mobile.viewportSize(), riskDetails: bounded, ...geometry })
    await mobile.close()
  }
  // Text scaling is intentionally limited to the real usage cell. This is not
  // an assertion about full-page browser zoom or unrelated fixed-width filters.
  for (const textScalePercent of [100, 200]) {
    const mobile = await open({ width: 320, height: 844 }, `?amounts=large&font=${textScalePercent}`)
    const usageField = mobile.locator('[data-field="usage"]').first()
    await usageField.scrollIntoViewIfNeeded()
    assert.deepEqual((await usageField.locator('.key-quota-values .key-usage-amount').allTextContents()).map(value => value.replace(/\s+/g, '')), ['$888888888888.88', '$999999999999.99'])
    const geometry = await usageField.evaluate(el => {
      const rect = node => { const r = node.getBoundingClientRect(); return { left: r.left, right: r.right, width: r.width, height: r.height } }
      const amounts = [...el.querySelectorAll('.key-quota-values .key-usage-amount')]
      const nodes = [el, ...el.querySelectorAll('*')]
      return {
        usage: rect(el), amounts: amounts.map(rect),
        fontSize: parseFloat(getComputedStyle(el.querySelector('.key-usage-cell')).fontSize),
        overflowing: nodes.filter(node => node.clientWidth > 0 && node.scrollWidth > node.clientWidth + 1).map(node => node.className),
        documentWidth: document.documentElement.scrollWidth
      }
    })
    assert.equal(geometry.fontSize, 14 * textScalePercent / 100)
    assert.deepEqual(geometry.overflowing, [], JSON.stringify(geometry))
    assert(geometry.documentWidth <= 320, JSON.stringify(geometry))
    assert(geometry.usage.left >= 0 && geometry.usage.right <= 320, JSON.stringify(geometry))
    assert(geometry.amounts.every(amount => amount.left >= geometry.usage.left - 1 && amount.right <= geometry.usage.right + 1), JSON.stringify(geometry))
    await screenshot(mobile, `after-mobile-320-large-quota-font-${textScalePercent}`, 'candidate')
    measurements.push({ viewport: mobile.viewportSize(), scenario: 'large-quota', textScalePercent, textScaleScope: 'usage-text', ...geometry })
    await mobile.close()
  }
  for (const [locale, width] of [['zh', 1366], ['en', 320]]) {
    const page = await open({ width, height: 844 }, `?locale=${locale}`)
    const initialGeometry = await page.evaluate(() => ({ documentWidth: document.documentElement.scrollWidth, scrollX }))
    assert(initialGeometry.documentWidth <= width && initialGeometry.scrollX === 0, JSON.stringify(initialGeometry))
    const range = page.locator('[data-test="key-usage-date-range"]')
    const last24Hours = locale === 'zh' ? '近24小时' : 'Last 24 Hours'
    await range.locator('.date-picker-trigger').click()
    assert.equal(await range.getByRole('button', { name: last24Hours, exact: true }).count(), 0)
    const dates = await page.evaluate(() => {
      const end = new Date(), start = new Date(end)
      start.setDate(start.getDate() - 1)
      const format = date => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
      return { start: format(start), end: format(end), timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }
    })
    const inputs = range.locator('input[type="date"]')
    await inputs.nth(0).fill(dates.start)
    await inputs.nth(1).fill(dates.end)
    await range.locator('.date-picker-apply').click()
    await page.waitForFunction(({ start, end }) => {
      const query = window.keyUsageQueries?.at(-1)
      return query?.start_date === start && query?.end_date === end
    }, dates)
    const query = await page.evaluate(() => window.keyUsageQueries.at(-1))
    assert.deepEqual(query, { apiKeyIds: keys.map(key => key.id), start_date: dates.start, end_date: dates.end, timezone: dates.timezone })
    assert(!(await range.locator('.date-picker-trigger').innerText()).includes(last24Hours))
    await range.locator('.date-picker-trigger').click()
    const dropdown = range.locator('.date-picker-dropdown')
    await dropdown.waitFor({ state: 'visible' })
    await page.waitForFunction(() => {
      const dropdown = document.querySelector('[data-test="key-usage-date-range"] .date-picker-dropdown')
      return dropdown && getComputedStyle(dropdown).opacity === '1' &&
        !dropdown.classList.contains('date-picker-dropdown-enter-from') &&
        !dropdown.classList.contains('date-picker-dropdown-enter-active')
    })
    assert(await inputs.nth(0).evaluate(input => {
      const rect = input.getBoundingClientRect()
      return document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2) === input
    }), 'The open calendar must be visible above the key list')
    const bounds = await dropdown.boundingBox()
    assert(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width + 1, JSON.stringify(bounds))
    const finalGeometry = await page.evaluate(() => ({ documentWidth: document.documentElement.scrollWidth, scrollX }))
    assert(finalGeometry.documentWidth <= width && finalGeometry.scrollX === 0, JSON.stringify(finalGeometry))
    await screenshot(page, `after-calendar-range-${locale}-${width}`, 'candidate')
    measurements.push({ viewport: page.viewportSize(), scenario: 'calendar-only-date-range', locale, query, dropdown: bounds, initialGeometry, finalGeometry })
    await page.close()
  }
  assert.deepEqual(errors, [])
  await fs.writeFile(path.join(out, 'verification.json'), JSON.stringify({ head, baseline, synthetic: true, actualComponents: ['KeysView', 'DateRangePicker', 'TablePageLayout', 'DataTable', 'GroupBadge', 'Pagination'], mocked: ['API data', 'authentication', 'shell', 'unopened dialogs'], productionRequests: 0, shots, measurements }, null, 2))
} catch (error) {
  for (const [i, page] of browser.contexts().flatMap(context => context.pages()).entries()) await page.screenshot({ path: path.join(out, `failure-${i}.png`), fullPage: false }).catch(() => {})
  await fs.writeFile(path.join(out, 'failure.json'), JSON.stringify({ head, error: String(error), stack: error.stack, errors, measurements }, null, 2))
  throw error
} finally { await browser.close(); await server.close() }
