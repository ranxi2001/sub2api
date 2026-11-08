// CI only: actual page, DataTable, Pagination, theme and CSS; synthetic backend/navigation.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'

const root = process.cwd(), frontend = path.join(root, 'frontend')
const out = process.env.ISSUE733_SCREENSHOT_DIR
assert(out && process.env.CI, 'This browser harness runs only on remote CI')
await fs.mkdir(out, { recursive: true })
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.ISSUE733_EXPECTED_HEAD)
const baseline = '8889aee65d908c2458becb86d4fe54f4359dc61d'
const file = 'frontend/src/views/admin/PrioritySchedulingView.vue'
const baselineBytes = execFileSync('git', ['show', `${baseline}:${file}`])
const baselinePath = path.join(out, 'BaselinePrioritySchedulingView.vue')
await fs.writeFile(baselinePath, baselineBytes)
assert.deepEqual(await fs.readFile(baselinePath), execFileSync('git', ['show', `${baseline}:${file}`]))
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const modules = {
  auth: `import {reactive} from 'vue'; export const auth=reactive({user:{id:1,role:'admin'}});window.fixtureAuth=auth;export const useAuthStore=()=>auth;`,
  layout: `import {h} from 'vue';export default {setup(_,{slots}){return()=>h('main',{class:'mx-auto max-w-7xl p-4 sm:p-6'},slots.default?.())}}`,
  nav: `import {h} from 'vue';export default {setup(){return()=>h('nav',{class:'text-sm text-gray-500'},'Offline UI fixture · Synthetic accounts only')}}`,
  client: `async function request(method,url,data,options){const query=new URLSearchParams(options?.params??{});const response=await fetch('/fixture'+url+(query.size?'?'+query:''),{method,headers:{'Content-Type':'application/json'},body:data?JSON.stringify(data):undefined});if(!response.ok)throw Error('fixture '+response.status);return {data:await response.json()}}export const apiClient={get:(url,options)=>request('GET',url,null,options),put:(url,data)=>request('PUT',url,data),post:(url,data)=>request('POST',url,data)};`,
}
function html() {
  return `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module">
import {createApp,h} from 'vue';import {createI18n} from 'vue-i18n';
import Candidate from '/src/views/admin/PrioritySchedulingView.vue';import Baseline from '/@fs/${baselinePath}';const params=new URLSearchParams(location.search);const View=params.has('baseline')?Baseline:Candidate;
import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
const app=createApp({render:()=>h(View)});app.use(createI18n({legacy:false,locale:params.get('locale')||'zh',messages:{zh,en}}));app.mount('#app');window.fixtureView=()=>app._instance.subTree.component.setupState;window.fixtureUnmount=()=>app.unmount();</script></html>`
}
const fixtureDir = path.join(frontend, '.issue733-fixture')
const fixtureHTML = path.join(fixtureDir, 'index.html')
await fs.mkdir(fixtureDir, {recursive:true})
await fs.writeFile(fixtureHTML, html())
for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixtureDir, name + '.js'), source)
const server = await createServer({ root: frontend, configFile: false, plugins: [vue()],
  resolve: { alias: [
    { find: '../client', replacement: path.join(fixtureDir, 'client.js') },
    { find: '@/stores/auth', replacement: path.join(fixtureDir, 'auth.js') },
    { find: '@/components/layout/AppLayout.vue', replacement: path.join(fixtureDir, 'layout.js') },
    { find: '@/components/admin/operations/SmartOpsNav.vue', replacement: path.join(fixtureDir, 'nav.js') },
    { find: '@', replacement: path.join(frontend, 'src') },
  ] },
  css: { postcss: { plugins: [tailwind({ ...theme, content: [path.join(frontend, 'index.html'), path.join(frontend, 'src/**/*.{vue,js,ts,jsx,tsx}'), baselinePath, path.join(root, '.github/scripts/issue733-browser.mjs')] }), autoprefixer()] } },
  define: { __INTLIFY_JIT_COMPILATION__: true },
  server: { host: '127.0.0.1', port: 4183, strictPort: true, fs: { allow: [root, out] } },
  optimizeDeps: { entries: [fixtureHTML], include: ['vue', 'vue-i18n', '@tanstack/vue-virtual'] },
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const shots = [], measurements = [], errors = [], requests = [], networkFailures = []
const state = { count: 75, tick: 0, reverse: false, latencyError: false, snapshotError: false, model: 'demo-model', group: 11, revision: 'demo-v1', endpoint: '/v1/responses', delay: 0 }
const config = { enabled: true, mode: 'balanced', group_ids: [11], models: ['demo-model'], window_minutes: 60, min_samples: 5, target_ttft_ms: 3000, latency_penalty_minutes: 10, latency_penalty_mode: 'deprioritize', max_load_percent: 80, min_quality_percent: 90, quality_max_age_hours: 24, quality_weight: 30, latency_weight: 25, load_weight: 25, cost_weight: 20, latency_percentile: 90, latency_measurement: 'legacy_forward', balance_protocols: true, oauth_quota_priority: false, oauth_quota_threshold: 90, error_rate_weight: 0, error_rate_window_minutes: 10, error_rate_prior_samples: 5, recovery_exploration_percent: 0 }
const now = () => new Date(Date.UTC(2026, 10, 8, 9, state.tick)).toISOString()
const accounts = () => Array.from({ length: state.count }, (_, i) => ({ account_id: i + 1, account_name: `Demo account ${String(i + 1).padStart(3, '0')}`, score: 80 - i / 10 + state.tick, tier: i % 9 === 0 ? 'degraded' : 'eligible', reasons: i % 9 === 0 ? ['latency_above_target'] : [], priority: i % 3, concurrency: 10, load_factor: 10, load_percent: (i * 7) % 80, waiting: 0, rate: 0.5, revenue: 2.5, theoretical_cost: 0.5, profit: 2, margin: 0.8, economics_source: 'usage', profit_samples: 30, samples: 30, p90_ttft_ms: 1200 + i * 20 + state.tick, quality_passed: 29, quality_samples: 30, error_rate: 0.03, error_successes: 29, error_failures: 1, error_window_minutes: 10, error_endpoint: state.endpoint, error_observed_at: now(), bound_groups: 2, selection_weight: 2.5 }))
function snap() { const rows = accounts(); return { at: now(), evaluated_at: now(), model: state.model, group_id: state.group, mode: 'balanced', config_revision: state.revision, history_ready: true, history_status: 'ready', latency_percentile: 90, latency_measurement: 'legacy_forward', window_minutes: 60, candidates: state.reverse ? rows.reverse() : rows } }
function latency(minutes) { const rows = accounts().map((r, i) => ({ ...r, p50_ttft_ms: 700 + i, p99_ttft_ms: 3500 + i, error_rate: 0.03, slow_samples: 1, error_samples: 1, buckets: Array.from({ length: minutes / 5 }, (_, b) => ({ account_id: r.account_id, bucket: new Date(Date.UTC(2026, 10, 8, 6, b * 5)).toISOString(), samples: 5, p50_ttft_ms: 700, p90_ttft_ms: 1200 + b * 40 + i, p99_ttft_ms: 3500, slow_samples: 1, error_samples: 1 })) })); return { at: now(), model: state.model, group_id: state.group, bucket_minutes: 5, config_revision: state.revision, latency_percentile: 90, latency_measurement: 'legacy_forward', accounts: state.reverse ? rows.reverse() : rows } }
let heldLatency = null
async function routeBackend(page) {
  await page.route('**/fixture/**', async route => {
    const url = new URL(route.request().url()), endpoint = url.pathname.split('/').at(-1)
    requests.push({ endpoint, query: url.search, method: route.request().method() })
    let body, status = 200
    if (endpoint === 'config') body = config
    else if (endpoint === 'snapshot') { body = { snapshot: snap() }; if (state.snapshotError) status = 503 }
    else if (endpoint === 'latency') {
      assert.equal(url.searchParams.get('bucket_minutes'), '5')
      const minutes = Number(url.searchParams.get('minutes')); assert([15, 60, 180].includes(minutes))
      body = latency(minutes); if (state.latencyError) status = 503
      if (heldLatency) { const hold = heldLatency; heldLatency = null; hold.started(); await hold.promise }
    } else if (endpoint === 'sticky-bindings') body = { group_id: Number(url.searchParams.get('group_id')), total: 0, by_account: [] }
    else if (endpoint === 'recompute') body = { before: snap(), after: snap(), diff: [], timed_out: false }
    else throw Error(`Unexpected offline endpoint ${endpoint}`)
    if (state.delay) await new Promise(resolve => setTimeout(resolve, state.delay))
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })
}
async function newPage(viewport = { width: 1440, height: 1000 }) {
  const page = await browser.newPage({ viewport })
  page.on('requestfailed', request => networkFailures.push({url:request.url(), error:request.failure()?.errorText}))
  page.on('response', response => {if(response.status() >= 400 && !response.url().includes('/fixture/')) networkFailures.push({url:response.url(),status:response.status()})})
  page.on('pageerror', error => errors.push(error.stack || error.message))
  await routeBackend(page)
  return page
}
async function screenshot(page, name, scope) {
  await page.evaluate(() => document.fonts.ready)
  await page.screenshot({ path: path.join(out, name), fullPage: false })
  shots.push({ name, head: scope === 'baseline' ? baseline : head, viewport: page.viewportSize(), scope, synthetic: true, source: process.env.GITHUB_RUN_ID })
}
async function refresh(page) { await page.evaluate(() => window.fixtureView().refresh()); await page.waitForFunction(() => !window.fixtureView().latencyLoading) }
async function setPager(page, name, pageSize, pageNumber) {
  // Exercise the real selector and real Pagination buttons.
  const pagination = page.getByTestId(`${name}-pagination`)
  await pagination.locator('.select-trigger').click()
  const dropdown = page.getByRole('listbox').last()
  await dropdown.getByRole('option', { name: String(pageSize), exact: true }).click()
  await dropdown.waitFor({state:'detached'})
  await pagination.getByRole('button', { name: `跳转到第 ${pageNumber} 页`, exact: true }).click()
}
async function geometry(page) {
  return page.evaluate(() => [...document.querySelectorAll('[data-reading-scroll]')].map(el => {
    const bounds = el.getBoundingClientRect(); let rows = [...el.querySelectorAll('[data-row-id]')]; if (!rows.length) rows = [...el.querySelectorAll('[data-field="account"] [data-reading-account]')].map(anchor=>{const card=anchor.closest('[data-field]').parentElement.parentElement;card.dataset.readingAccount=anchor.dataset.readingAccount;return card})
    const row = rows.find(item => item.getBoundingClientRect().bottom > bounds.top + 48) ?? rows.at(-1)
    return { name: el.dataset.readingScroll, left: el.scrollLeft, top: el.scrollTop, height: bounds.height, scrollHeight: el.scrollHeight, width: el.clientWidth, scrollWidth: el.scrollWidth, row: row?.dataset.rowId ?? row?.dataset.readingAccount, offset: row ? row.getBoundingClientRect().top - bounds.top : 0, ids: rows.map(item => item.dataset.rowId ?? item.dataset.readingAccount) }
  }))
}
try {
  const old = await newPage()
  await old.goto('http://127.0.0.1:4183/.issue733-fixture/index.html?baseline=1', { waitUntil: 'networkidle' })
  await old.getByTestId('latency-panel').getByText('Demo account 001', { exact: true }).first().waitFor()
  // Validate representative project utilities and the real computed table style before measuring.
  const cssProof = await old.evaluate(() => {
    const card = document.querySelector('[data-testid="latency-panel"]'), scroll = card.querySelector('.overflow-x-auto'), table = scroll.querySelector('table')
    return { padding: getComputedStyle(card).paddingTop, whiteSpace: getComputedStyle(table).whiteSpace, overflow: getComputedStyle(scroll).overflowX, radius: getComputedStyle(card).borderRadius }
  })
  assert.equal(cssProof.padding, '20px'); assert.equal(cssProof.whiteSpace, 'nowrap'); assert.equal(cssProof.overflow, 'auto'); assert.equal(cssProof.radius, '16px')
  await old.getByTestId('latency-panel').scrollIntoViewIfNeeded()
  await screenshot(old, 'before-desktop.png', 'baseline')
  const oldHeight = await old.getByTestId('latency-panel').evaluate(el => el.getBoundingClientRect().height)
  let release, started
  const began = new Promise(resolve => { started = resolve })
  heldLatency = { started, promise: new Promise(resolve => { release = resolve }) }
  await old.getByTestId('latency-refresh').click(); await began
  assert.equal(await old.getByTestId('latency-panel').locator('tbody tr').count(), 0, 'baseline RED must lose real account rows')
  const pendingHeight = await old.getByTestId('latency-panel').evaluate(el => el.getBoundingClientRect().height)
  assert(pendingHeight < oldHeight / 2)
  await screenshot(old, 'before-refresh-empty.png', 'baseline')
  measurements.push({ phase: 'baseline-red', cssProof, oldHeight, pendingHeight, rowsDuringRefresh: 0, baselineByteEqual: true })
  release(); await old.close()

  const page = await newPage()
  await page.goto('http://127.0.0.1:4183/.issue733-fixture/index.html', { waitUntil: 'networkidle' })
  await page.getByTestId('candidate-table').waitFor()
  assert.equal(await page.getByTestId('latency-toggle').getAttribute('aria-expanded'), 'false')
  assert.equal(await page.locator('#priority-latency-content').isVisible(), false)
  const candidateCSS = await page.getByTestId('candidate-table').evaluate(el => ({ overflow: getComputedStyle(el).overflowY, maxHeight: parseFloat(getComputedStyle(el).maxHeight), height: el.clientHeight, tableWidth: el.querySelector('table').getBoundingClientRect().width, firstColumn: getComputedStyle(el.querySelector('tbody td')).position }))
  assert.equal(candidateCSS.overflow, 'auto'); assert(candidateCSS.maxHeight <= 550); assert(candidateCSS.tableWidth >= 1216); assert.equal(candidateCSS.firstColumn, 'sticky')
  await screenshot(page, 'after-desktop-overview.png', 'candidate')
  await page.getByTestId('latency-toggle').click()
  await page.getByTestId('latency-window').selectOption('180')
  await page.waitForFunction(() => !window.fixtureView().latencyLoading)
  await setPager(page, 'latency', 10, 3)
  await setPager(page, 'candidate', 10, 3)
  await page.getByTestId('latency-panel').scrollIntoViewIfNeeded()
  await screenshot(page, 'after-latency-expanded-pagination.png', 'candidate')
  await page.getByTestId('candidate-table').scrollIntoViewIfNeeded()
  await screenshot(page, 'after-candidate-page3.png', 'candidate')
  await page.getByTestId('bucket-details').locator('summary').click()
  const panelHeight = await page.getByTestId('latency-panel').evaluate(el => el.getBoundingClientRect().height)
  assert(panelHeight <= page.viewportSize().height - 30)
  const bucketIDs = await page.getByTestId('latency-buckets').locator('tbody [data-row-id]').evaluateAll(rows => rows.map(row => row.dataset.rowId))
  const latencyIDs = await page.getByTestId('latency-table').locator('tbody [data-row-id]').evaluateAll(rows => rows.map(row => row.dataset.rowId))
  assert.deepEqual(bucketIDs, latencyIDs); assert.equal(latencyIDs.length, 10); assert.equal(latencyIDs[0], '21')
  await page.setViewportSize({width:960,height:1000})
  await page.getByText('调度配置', {exact:true}).click()
  await page.getByTestId('enabled').uncheck()
  await page.getByText('调度配置', {exact:true}).click()
  await page.getByTestId('candidate-table').locator('tbody tr').first().locator('details summary').click()
  await page.evaluate(() => { for (const el of document.querySelectorAll('[data-reading-scroll]')) { el.scrollLeft = 220; el.scrollTop = 110; el.dataset.savedNode = 'same' } })
  await page.getByTestId('candidate-table').evaluate(el => {const row=el.querySelector('tbody tr');el.scrollTop=row.offsetTop+row.offsetHeight+4})
  const before = await geometry(page)
  assert(before.filter(item => item.name !== 'buckets').every(item => item.scrollHeight > item.height)); assert(before.every(item => item.scrollWidth > item.width && item.left > 0), JSON.stringify(before))
  // Real 5-second timers, multiple polls, changing at/metrics and backend order.
  const startingReads = requests.filter(r => r.endpoint === 'snapshot').length
  state.reverse = true; state.tick++; state.endpoint='/v1/'+'synthetic-path-'.repeat(30)
  await page.waitForFunction(() => window.fixtureView().snapshot.at.endsWith('09:01:00.000Z'))
  for (let i = 0; i < 2; i++) { state.tick++; const at = now(); await page.waitForFunction(at => window.fixtureView().snapshot.at === at && !window.fixtureView().latencyLoading, at) }
  assert(requests.filter(r => r.endpoint === 'snapshot').length >= startingReads + 3)
  const after = await geometry(page)
  for (const item of before) {
    const next = after.find(row => row.name === item.name)
    assert.deepEqual(next.ids, item.ids); assert.equal(next.row, item.row); assert(Math.abs(next.offset - item.offset) < 1); assert.equal(next.left, item.left); if(item.name !== 'candidates') assert.equal(next.top, item.top); else assert(next.top > item.top, 'expanded earlier diagnostics require anchor compensation'); assert.equal(next.height, item.height)
    assert.equal(await page.locator(`[data-reading-scroll="${item.name}"]`).getAttribute('data-saved-node'), 'same')
  }
  assert.equal(await page.getByTestId('enabled').isChecked(), false)
  measurements.push({ phase: 'three-real-polls', before, after, candidateCSS })
  // Delay a real request: no replacement, geometry collapse or lost scroll.
  const heldBegan = new Promise(resolve => { started = resolve })
  heldLatency = { started, promise: new Promise(resolve => { release = resolve }) }
  await page.getByTestId('latency-refresh').click(); await heldBegan
  assert.equal(await page.getByTestId('latency-table').locator('[data-row-id]').count(), 10)
  assert.match(await page.getByTestId('latency-status').innerText(), /后台更新/)
  release(); await page.waitForFunction(() => !window.fixtureView().latencyLoading)
  state.latencyError = true; await refresh(page)
  assert.match(await page.getByTestId('latency-status').innerText(), /上次成功/)
  assert.equal(await page.getByTestId('latency-table').locator('[data-row-id]').count(), 10)
  state.latencyError = false; await refresh(page)
  // Shrink clamps both third pages, additions append in a readable ID order.
  state.count = 3; await refresh(page)
  for (const name of ['latency', 'candidate']) assert.equal(await page.getByTestId(`${name}-pagination`).locator('[aria-current="page"]').innerText(), '1')
  state.count = 75; await refresh(page)
  assert.equal(await page.getByTestId('candidate-table').locator('tbody [data-row-id]').first().getAttribute('data-row-id'), '1')

  // Short desktop: bounded panels remain internally scrollable.
  await page.setViewportSize({ width: 1280, height: 360 })
  const short = await geometry(page)
  for (const item of short) { assert(item.height <= 360 * 0.55 + 1); if (item.name !== 'buckets') assert(item.scrollHeight > item.height) }
  const shortPanel = await page.getByTestId('latency-panel').evaluate(el => ({height:el.getBoundingClientRect().height,maxHeight:getComputedStyle(el).maxHeight,transition:getComputedStyle(el).transitionProperty}))
  measurements.push({phase:'short-panel',...shortPanel})
  assert(shortPanel.height <= 330, JSON.stringify(shortPanel))
  await page.getByTestId('latency-table').scrollIntoViewIfNeeded()
  await screenshot(page, 'after-short-viewport.png', 'candidate')
  measurements.push({ phase: 'short-viewport', geometry: short })
  // Real responsive cards and reachable page-size controls on narrow screens.
  await page.setViewportSize({ width: 390, height: 640 })
  await page.getByTestId('latency-mobile-size').selectOption('20')
  await page.getByTestId('candidate-mobile-size').selectOption('20')
  assert.equal(await page.getByTestId('latency-table').locator('table').count(), 0)
  const mobile = await geometry(page)
  for (const item of mobile) assert(item.height <= 640 * 0.55 + 1)
  await page.getByTestId('candidate-table').locator('details summary').first().click()
  await page.getByTestId('candidate-table').evaluate(el=>{const name=el.querySelector('[data-field="account"] [data-reading-account]');const card=name.closest('[data-field]').parentElement.parentElement;el.scrollTop=card.offsetTop+card.offsetHeight+4})
  const mobileBefore = await geometry(page)
  assert(mobileBefore.find(item=>item.name==='candidates').top>0)
  state.endpoint='/v1/'+'mobile-synthetic-'.repeat(60)
  state.tick++; await refresh(page)
  const mobileAfter = await geometry(page)
  for(const item of mobileBefore){const next=mobileAfter.find(row=>row.name===item.name);assert.equal(next.row,item.row);assert(Math.abs(next.offset-item.offset)<1);assert(next.offset<next.height,'mobile anchor must belong to a visible account card')}
  measurements.push({phase:'mobile-poll',before:mobileBefore,after:mobileAfter})
  await page.getByTestId('latency-panel').scrollIntoViewIfNeeded()
  await screenshot(page, 'after-mobile.png', 'candidate')
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
  // English copy, same real page. Dark state uses the existing project theme.
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('http://127.0.0.1:4183/.issue733-fixture/index.html?locale=en', { waitUntil: 'networkidle' })
  assert.match(await page.getByTestId('latency-status').innerText(), /Stable account ID/)
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await screenshot(page, 'after-english-dark.png', 'candidate')
  // Unmount then wait longer than a real scheduled poll; no timer restart.
  const requestCount = requests.length
  await page.evaluate(() => window.fixtureUnmount())
  await page.waitForTimeout(5500)
  assert.equal(requests.length, requestCount)
  assert.deepEqual(errors, [])
  await fs.writeFile(path.join(out, 'verification.json'), JSON.stringify({ head, baseline, baselineSha256: createHash('sha256').update(baselineBytes).digest('hex'), run: process.env.GITHUB_RUN_ID, source: `https://github.com/ranxi2001/sub2api/actions/runs/${process.env.GITHUB_RUN_ID}`, synthetic: true, actualComponents: ['PrioritySchedulingView', 'DataTable', 'Pagination'], mocks: ['backend', 'auth', 'layout', 'navigation'], productionCalls: 0, paidCalls: 0, shots, measurements, requests, errors }, null, 2))
} catch (error) {
  for (const [index, page] of browser.contexts().flatMap(context => context.pages()).entries()) {
    await page.screenshot({ path: path.join(out, `failure-${index}.png`), fullPage: false }).catch(() => {})
    await fs.writeFile(path.join(out, `failure-${index}.txt`), await page.locator('body').innerText()).catch(() => {})
  }
  await fs.writeFile(path.join(out, 'failure.json'), JSON.stringify({ head, message: String(error), stack: error.stack, errors, networkFailures, measurements, shots, requests }, null, 2))
  throw error
} finally { await browser.close(); await server.close() }
