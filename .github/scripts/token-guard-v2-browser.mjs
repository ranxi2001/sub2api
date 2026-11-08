// CI only: render the actual before/after Vue page and project CSS with offline APIs.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.cwd(), frontend = path.join(root, 'frontend')
const out = process.env.TOKEN_GUARD_SCREENSHOT_DIR
assert(process.env.CI && out, 'This browser fixture runs only in remote CI')
await fs.mkdir(out, { recursive: true })
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.TOKEN_GUARD_EXPECTED_HEAD)
const baseline = 'dc1e1b072faa53ec2e6f0925be39d5d024ec1687'
const component = 'frontend/src/views/admin/ops/TokenGuardV2View.vue'
const baselineBytes = execFileSync('git', ['show', `${baseline}:${component}`])
const baselinePath = path.join(out, 'BaselineTokenGuardV2View.vue')
await fs.writeFile(baselinePath, baselineBytes)
assert.deepEqual(await fs.readFile(baselinePath), execFileSync('git', ['show', `${baseline}:${component}`]))

const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const fixture = path.join(frontend, '.token-guard-v2-fixture')
await fs.mkdir(fixture, { recursive: true })
const modules = {
  layout: `import {h} from 'vue';export default {setup(_,{slots}){return()=>h('main',{class:'mx-auto max-w-7xl p-4 sm:p-6'},slots.default?.())}}`,
  nav: `import {h} from 'vue';export default {setup(){return()=>h('nav',{class:'text-sm text-gray-500'},'Offline UI fixture · Synthetic accounts only')}}`,
  auth: `import {reactive} from 'vue';const auth=reactive({user:{id:1,role:'admin'}});export const useAuthStore=()=>auth;`,
  client: `async function request(method,url,data,options){const query=new URLSearchParams(options?.params??{});const response=await fetch('/fixture'+url+(query.size?'?'+query:''),{method,headers:{'Content-Type':'application/json'},body:data?JSON.stringify(data):undefined});if(!response.ok)throw Error('offline fixture '+response.status);return {data:await response.json()}}export const apiClient=Object.fromEntries(['get','post','put','patch','delete'].map(method=>[method,(url,data)=>method==='get'?request('GET',url,null,data):request(method.toUpperCase(),url,data)]));export const buildApiUrl=url=>'/fixture'+url;export const buildGatewayUrl=url=>'http://127.0.0.1:4187/fixture'+url;export default apiClient;`,
}
for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixture, name + '.js'), source)
const entry = path.join(fixture, 'index.html')
await fs.writeFile(entry, `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module">
import {createApp,h} from 'vue';import {createI18n} from 'vue-i18n';import {createPinia} from 'pinia';
import Candidate from '/src/views/admin/ops/TokenGuardV2View.vue';import Baseline from '/@fs/${baselinePath}';
import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
const params=new URLSearchParams(location.search);const app=createApp({render:()=>h(params.has('baseline')?Baseline:Candidate)});app.use(createPinia());app.use(createI18n({legacy:false,locale:params.get('locale')||'zh',messages:{zh,en}}));app.mount('#app');
</script></html>`)
const server = await createServer({ root: frontend, configFile: false, plugins: [vue()],
  resolve: { alias: [
    { find: '../client', replacement: path.join(fixture, 'client.js') },
    { find: '@/api/client', replacement: path.join(fixture, 'client.js') },
    { find: '@/stores/auth', replacement: path.join(fixture, 'auth.js') },
    { find: '@/components/layout/AppLayout.vue', replacement: path.join(fixture, 'layout.js') },
    { find: '@/components/admin/operations/SmartOpsNav.vue', replacement: path.join(fixture, 'nav.js') },
    { find: '@', replacement: path.join(frontend, 'src') },
  ] },
  css: { postcss: { plugins: [tailwind({ ...theme, content: [path.join(frontend, 'src/**/*.{vue,js,ts,jsx,tsx}'), baselinePath, path.join(root, '.github/scripts/token-guard-v2-browser.mjs')] }), autoprefixer()] } },
  define: { __INTLIFY_JIT_COMPILATION__: true },
  server: { host: '127.0.0.1', port: 4187, strictPort: true, fs: { allow: [root, out] } },
  optimizeDeps: { entries: [entry], include: ['vue', 'vue-i18n', 'pinia'] },
})

const now = '2026-11-08T10:00:00Z'
const task = (accountID, status) => ({ id: accountID + 100, account_id: accountID, status, stage: status, attempt: 1, created_at: now, updated_at: now, ...(status === 'succeeded' ? { finished_at: now } : {}) })
function account(id, extra = {}) {
  return { account_id: id, account_name: `Synthetic parent ${id}`, account_status: 'active', schedulable: true, enabled: true, auto_relogin_enabled: true,
    probe_state: 'auth', probe_detail: 'credential rejected', fail_streak: 1, last_probe_at: '2026-11-08T09:00:00Z', next_probe_at: now,
    login_config: { account_id: id, login_email: `demo${id}@example.test`, credential_mode: 'password_totp', engine: 'local_worker', proxy_source: 'account', password_configured: true, totp_configured: true, configured: true },
    probe_failure: { code: 'requires_relogin', category: 'requires_relogin', stage: 'inspection', action: 'reauthorize' }, ...extra }
}
const initial = () => [account(42), account(43, { latest_task: task(43, 'queued') }), account(44, { latest_task: task(44, 'running') }),
  account(45, { cooldown_until: '2099-11-08T10:00:00Z' }), account(46, { auto_relogin_exhausted: true, relogin_fail_streak: 3 }),
  account(47, { latest_task: task(47, 'succeeded') }), account(48, { blocked_reason: 'automatic re-login could not be queued' })]
const requests = [], errors = [], shots = [], checks = []
let rows = initial(), holdNextRead = null
const status = () => ({ accounts: structuredClone(rows), runtime_settings: { engine: 'local_worker', worker_concurrency: 3 }, worker: { state: 'running', mode: 'managed' },
  probe_interval_seconds: 1800, retry_interval_seconds: 300, relogin_cooldown_seconds: 1800, fail_streak_threshold: 2, relogin_backoff_max_seconds: 86400, relogin_max_failures: 3 })
const guardPath = '/admin/account-ops/token-guard-v2/accounts'
const guardReads = () => requests.filter(r => r.pathname === guardPath && r.method === 'GET').length
function deferRead() {
  let started, release
  const began = new Promise(resolve => { started = resolve })
  const promise = new Promise(resolve => { release = resolve })
  holdNextRead = { started, promise }
  return { began, release }
}
await server.listen()
const browser = await chromium.launch({ headless: true })
async function newPage(viewport = { width: 1440, height: 1100 }) {
  const page = await browser.newPage({ viewport })
  page.setDefaultTimeout(30000)
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
  await page.route('**/*', route => {
    if (new URL(route.request().url()).origin === 'http://127.0.0.1:4187') return route.continue()
    errors.push('External request blocked')
    return route.abort()
  })
  await page.route('**/fixture/**', async route => {
    const pathname = new URL(route.request().url()).pathname.replace('/fixture', '')
    const method = route.request().method()
    requests.push({ pathname, method })
    let body
    if (pathname === guardPath && method === 'GET') {
      body = status()
      if (holdNextRead) { const hold = holdNextRead; holdNextRead = null; hold.started(); await hold.promise }
    } else if (pathname.endsWith('/42/probe') && method === 'POST') {
      rows[0] = account(42, { probe_state: 'ok', probe_detail: 'credential accepted', fail_streak: 0, last_probe_at: '2026-11-08T10:01:00Z', probe_failure: undefined })
      body = rows[0]
    } else if (pathname.endsWith('/42/relogin') && method === 'POST') {
      rows[0] = account(42, { latest_task: task(42, 'queued') }); body = rows[0].latest_task
    } else if (pathname.endsWith('/encryption')) body = { configured: true, source: 'server_config' }
    else if (pathname === '/admin/accounts') body = { items: rows.map(a => ({ id: a.account_id, name: a.account_name, platform: 'openai', type: 'oauth', group_ids: [1] })), total: rows.length, page: 1, page_size: 200 }
    else if (pathname === '/admin/groups/all') body = [{ id: 1, name: 'Synthetic group' }]
    else if (pathname === '/admin/proxies/all') body = []
    else throw Error(`Unexpected offline API ${method} ${pathname}`)
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
  return page
}
const url = 'http://127.0.0.1:4187/.token-guard-v2-fixture/index.html'
const targetRow = page => page.locator('tbody tr').filter({ hasText: 'Synthetic parent 42' })
async function screenshot(page, name, source = head) {
  await page.evaluate(() => document.fonts.ready)
  await page.screenshot({ path: path.join(out, name), fullPage: true })
  shots.push({ name, source, viewport: page.viewportSize(), synthetic: true })
}
try {
  const before = await newPage()
  await before.goto(url + '?baseline=1', { waitUntil: 'networkidle' })
  await targetRow(before).waitFor()
  await screenshot(before, 'before-desktop.png', baseline)
  const oldReads = guardReads()
  await targetRow(before).getByRole('button', { name: '立即巡检', exact: true }).click()
  await before.getByText('已完成 Synthetic parent 42 的即时巡检', { exact: true }).waitFor()
  assert.equal(guardReads(), oldReads, 'Pinned baseline RED: action read is blocked by busyId')
  assert.match(await targetRow(before).locator('td').nth(4).innerText(), /鉴权失败/)
  checks.push({ name: 'baseline-action-read-blocked', pass: true, reads: oldReads, readsAfter: guardReads() })
  await screenshot(before, 'before-stale-after-inspection.png', baseline)
  await before.close()

  rows = initial()
  const page = await newPage()
  await page.goto(url, { waitUntil: 'networkidle' })
  await page.getByTestId('automation-state-42').waitFor()
  for (const [id, text] of [[42, '鉴权失败确认'], [43, '重登任务已排队'], [44, '正在重登'], [45, '自动重登冷却'], [46, '自动重登预算已耗尽'], [47, '新凭据已保存'], [48, '自动恢复暂不可用']]) {
    assert((await page.getByTestId(`automation-state-${id}`).innerText()).includes(text))
  }
  assert.equal(await page.getByTestId('probe-history-43').getAttribute('open'), null)
  await screenshot(page, 'after-desktop-stages.png')
  const held = deferRead(), readsBefore = guardReads()
  await targetRow(page).getByRole('button', { name: '立即巡检', exact: true }).click()
  await held.began
  assert.equal(guardReads(), readsBefore + 1, 'Action triggers an immediate actual API read')
  assert(await targetRow(page).getByRole('button', { name: '立即巡检', exact: true }).isDisabled(), 'Duplicate action remains blocked during readback')
  await screenshot(page, 'after-inspection-readback-pending.png')
  held.release()
  await page.waitForFunction(() => document.querySelector('[data-testid="automation-state-42"]')?.textContent === '最近巡检鉴权通过')
  assert.equal(await targetRow(page).getByRole('button', { name: '立即巡检', exact: true }).isDisabled(), false)
  await screenshot(page, 'after-inspection-fresh-read.png')
  checks.push({ name: 'immediate-readback-and-duplicate-action-block', pass: true, readsBefore, readsAfter: guardReads() })

  // An older refresh may finish after the action's queued-state read.
  const oldRead = deferRead()
  await page.getByRole('button', { name: '刷新', exact: true }).click(); await oldRead.began
  await targetRow(page).getByRole('button', { name: '立即重登', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('[data-testid="automation-state-42"]')?.textContent === '重登任务已排队')
  oldRead.release()
  await page.waitForLoadState('networkidle')
  assert.equal(await page.getByTestId('automation-state-42').innerText(), '重登任务已排队', 'Old refresh must not overwrite the queued action read')
  checks.push({ name: 'older-refresh-cannot-overwrite-queued-readback', pass: true })
  await screenshot(page, 'after-relogin-queued.png')
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByTestId('automation-state-42').scrollIntoViewIfNeeded()
  await screenshot(page, 'after-mobile-queued.png')
  checks.push({ name: 'mobile-real-table', viewport: page.viewportSize(), horizontalTableScroll: await page.locator('.table-wrap').evaluate(el => el.scrollWidth > el.clientWidth) })
  await page.setViewportSize({ width: 1440, height: 1100 })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await screenshot(page, 'after-desktop-dark.png')
  assert.deepEqual(errors, [])
  await fs.writeFile(path.join(out, 'verification.json'), JSON.stringify({ head, baseline, baselineSha256: createHash('sha256').update(baselineBytes).digest('hex'), source: `https://github.com/ranxi2001/sub2api/actions/runs/${process.env.GITHUB_RUN_ID}`, actualComponents: ['TokenGuardV2View', 'CredentialEncryptionSetup', 'CredentialOperationFailure', 'Pagination'], mocks: ['HTTP APIs', 'auth', 'layout', 'navigation'], synthetic: true, productionCalls: 0, paidCalls: 0, shots, checks, requests, errors }, null, 2))
} catch (error) {
  for (const [index, page] of browser.contexts().flatMap(context => context.pages()).entries()) {
    await page.screenshot({ path: path.join(out, `failure-${index}.png`), fullPage: true }).catch(() => {})
    await fs.writeFile(path.join(out, `failure-${index}.txt`), await page.locator('body').innerText()).catch(() => {})
  }
  await fs.writeFile(path.join(out, 'failure.json'), JSON.stringify({ head, message: String(error), errors, requests, checks }, null, 2))
  throw error
} finally {
  await browser.close(); await server.close(); await fs.rm(fixture, { recursive: true, force: true })
}
