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
const baseline = '8a203b43231c1192cee17f854633a23308d73e7e'
const file = 'frontend/src/views/admin/PrioritySchedulingView.vue'
const baselineBytes = execFileSync('git', ['show', `${baseline}:${file}`])
const baselinePath = path.join(out, 'BaselinePrioritySchedulingView.vue')
await fs.writeFile(baselinePath, baselineBytes)
assert.deepEqual(await fs.readFile(baselinePath), execFileSync('git', ['show', `${baseline}:${file}`]))
const defaultGroupBaseline = '6fa7fb86eae7c732794450646dc13831dce87ff1'
const defaultGroupBaselinePath = path.join(out, 'DefaultGroupBaseline.vue')
await fs.writeFile(defaultGroupBaselinePath, execFileSync('git', ['show', `${defaultGroupBaseline}:${file}`]))
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
  client: `async function request(method,url,data,options){const query=new URLSearchParams(options?.params??{});const response=await fetch('/fixture'+url+(query.size?'?'+query:''),{method,signal:options?.signal,headers:{'Content-Type':'application/json'},body:data?JSON.stringify(data):undefined});if(!response.ok)throw Error('fixture '+response.status);return {data:await response.json()}}export const apiClient={get:(url,options)=>request('GET',url,null,options),put:(url,data)=>request('PUT',url,data),post:(url,data)=>request('POST',url,data)};`,
}
function html() {
  return `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module">
import {createApp,h} from 'vue';import {createI18n} from 'vue-i18n';import {createPinia} from 'pinia';
import Candidate from '/src/views/admin/PrioritySchedulingView.vue';import Baseline from '/@fs/${baselinePath}';import DefaultGroupBaseline from '/@fs/${defaultGroupBaselinePath}';const params=new URLSearchParams(location.search);const View=params.has('defaultGroupBaseline')?DefaultGroupBaseline:params.has('baseline')?Baseline:Candidate;
import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
const app=createApp({render:()=>h(View)});app.use(createPinia());app.use(createI18n({legacy:false,locale:params.get('locale')||'zh',messages:{zh,en}}));app.mount('#app');window.fixtureView=()=>app._instance.subTree.component.setupState;window.fixtureUnmount=()=>app.unmount();</script></html>`
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
const state = { count: 75, tick: 0, reverse: false, latencyError: false, snapshotError: false, model: 'demo-model', group: 11, revision: 'demo-v1', endpoint: '/v1/responses', delay: 0, defaultGroupScenario: false }
const config = { enabled: true, mode: 'balanced', group_ids: [11], models: ['demo-model'], window_minutes: 60, min_samples: 5, target_ttft_ms: 3000, latency_penalty_minutes: 10, latency_penalty_mode: 'deprioritize', max_load_percent: 80, min_quality_percent: 90, quality_max_age_hours: 24, quality_weight: 30, latency_weight: 25, load_weight: 25, cost_weight: 20, cache_weight: 25, latency_percentile: 90, latency_measurement: 'legacy_forward', balance_protocols: true, oauth_quota_priority: false, oauth_quota_threshold: 90, error_rate_weight: 0, error_rate_window_minutes: 10, error_rate_prior_samples: 5, recovery_exploration_percent: 0 }
const now = () => new Date(Date.UTC(2026, 10, 8, 9, state.tick)).toISOString()
const accounts = () => Array.from({ length: state.count }, (_, i) => ({ account_id: i + 1, account_name: `Demo account ${String(i + 1).padStart(3, '0')}`, score: 80 - i / 10 + state.tick, tier: i % 9 === 0 ? 'degraded' : 'eligible', reasons: i % 9 === 0 ? ['latency_above_target'] : [], priority: i % 3, concurrency: 10, load_factor: 10, load_percent: (i * 7) % 80, waiting: 0, rate: 0.5, revenue: 2.5, theoretical_cost: 0.5, profit: 2, margin: 0.8, economics_source: 'usage', profit_samples: 30, samples: 30, p90_ttft_ms: 1200 + i * 20 + state.tick, quality_passed: 29, quality_samples: 30, error_rate: 0.03, error_successes: 29, error_failures: 1, error_window_minutes: 10, error_endpoint: state.endpoint, error_observed_at: now(), bound_groups: 2, selection_weight: 2.5 }))
function snap() { const rows = accounts().map(row => ({ ...row, performance: { account_id: row.account_id, avg_tps: 25, avg_cache_percent: 86.8, cache_samples: 30, avg_ttft_ms: 4000, degradation_percent: 0, tps_samples: 30, ttft_samples: 30, quality_samples: 30, paused: false, window_start: '2026-11-08T08:00:00Z', window_end: '2026-11-08T09:00:00Z', upstream_error: { source: 'process_local_http_responses', failures: 1, total: 200, percent: 0.5, observed_since: '2026-11-08T08:00:00Z', window_complete: true } } })); return { at: now(), evaluated_at: now(), model: state.model, group_id: state.group, mode: 'balanced', config_revision: state.revision, history_ready: true, history_status: 'ready', latency_percentile: 90, latency_measurement: 'legacy_forward', window_minutes: 60, candidates: state.reverse ? rows.reverse() : rows } }
function latency(minutes) { const rows = accounts().map((r, i) => ({ ...r, p50_ttft_ms: 700 + i, p99_ttft_ms: 3500 + i, error_rate: 0.03, slow_samples: 1, error_samples: 1, buckets: Array.from({ length: minutes / 5 }, (_, b) => ({ account_id: r.account_id, bucket: new Date(Date.UTC(2026, 10, 8, 6, b * 5)).toISOString(), samples: 5, p50_ttft_ms: 700, p90_ttft_ms: 1200 + b * 40 + i, p99_ttft_ms: 3500, slow_samples: 1, error_samples: 1 })) })); return { at: now(), model: state.model, group_id: state.group, bucket_minutes: 5, target_ttft_ms: config.target_ttft_ms, config_revision: state.revision, latency_percentile: 90, latency_measurement: 'legacy_forward', accounts: state.reverse ? rows.reverse() : rows } }
let heldLatency = null
async function routeBackend(page) {
  await page.route('**/fixture/**', async route => {
    const url = new URL(route.request().url()), endpoint = url.pathname.split('/').at(-1)
    requests.push({ endpoint, query: url.search, method: route.request().method() })
    let body, status = 200
    if (url.pathname === '/fixture/admin/groups/all') body = [{id:11,name:'Demo group',platform:'openai'},{id:17,name:state.defaultGroupScenario?'GPT Demo group':'Archive group',platform:'openai'},{id:23,name:'Composite group',platform:'composite'},{id:29,name:'Anthropic group',platform:'anthropic'}].filter(group=>!url.searchParams.get('platform')||group.platform===url.searchParams.get('platform'))
    else if (endpoint === 'models') body = {models:['demo-model','archive-model','not-in-latest-pool']}
    else if (endpoint === 'config') body = config
    else if (endpoint === 'analysis') {
      const model=url.searchParams.get('model'), group=Number(url.searchParams.get('group_id'))||null, minutes=Number(url.searchParams.get('minutes'))
      assert(['demo-model','archive-model','not-in-latest-pool','public-alias'].includes(model)); assert([null,11,17,23].includes(group)); assert([15,60,180].includes(minutes))
      body={snapshot:{...snap(),model,group_id:group,window_minutes:minutes},latency:{...latency(minutes),model,group_id:group}}
      if (state.defaultGroupScenario && group === null) {
        body.snapshot.candidates = []; body.latency.accounts = []
      }
      if(state.latencyError||state.snapshotError)status=503
      if(heldLatency){const hold=heldLatency;heldLatency=null;hold.started();await hold.promise}
    }
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
  shots.push({ sha256: createHash('sha256').update(await fs.readFile(path.join(out,name))).digest('hex'), name, head: scope === 'baseline' ? baseline : scope === 'default-group-baseline' ? defaultGroupBaseline : head, viewport: page.viewportSize(), scope, synthetic: true, source: process.env.GITHUB_RUN_ID })
}
try {
  config.group_ids = []; state.defaultGroupScenario = true; state.count = 3
  for (const before of [true, false]) {
    const page = await newPage()
    await page.goto('http://127.0.0.1:4183/.issue733-fixture/index.html' + (before ? '?defaultGroupBaseline=1' : ''), { waitUntil: 'networkidle' })
    await page.waitForFunction(() => window.fixtureView().snapshot && !window.fixtureView().refreshing)
    const observed = await page.evaluate(() => ({ group: window.fixtureView().selectedGroup, candidates: window.fixtureView().snapshot.candidates.length }))
    assert.deepEqual(observed, { group: before ? 0 : 17, candidates: before ? 0 : 3 })
    await screenshot(page, `${before ? 'before' : 'after'}-default-gpt-group.png`, before ? 'default-group-baseline' : 'candidate')
    measurements.push({ phase: 'default-gpt-group', before, ...observed })
    await page.close()
  }
  config.group_ids = [11]; state.defaultGroupScenario = false; state.count = 75
  const page = await newPage()
  await page.goto('http://127.0.0.1:4183/.issue733-fixture/index.html', { waitUntil: 'networkidle' })
  await page.getByTestId('candidate-table').waitFor()
  assert.equal(await page.getByTestId('latency-panel').count(), 0)
  const first = page.locator('[data-metric="upstream"]').first()
  assert.match(await first.innerText(), /0.5%/)
  assert.match(await page.locator('[data-metric="tps"]').first().innerText(), /25.0 t\/s/)
  assert.match(await page.locator('[data-metric="ttft"]').first().innerText(), /4.00s/)
  assert.match(await page.locator('[data-metric="cache"]').first().innerText(), /86.8%/)
  await screenshot(page, 'after-shared-performance.png', 'candidate')
  const configuration = page.locator('details').filter({ has: page.getByTestId('cache-weight') }).first()
  await configuration.locator('summary').first().click()
  assert.equal(await page.getByTestId('cache-weight').inputValue(), '25')
  await page.getByTestId('cache-weight').scrollIntoViewIfNeeded()
  await screenshot(page, 'after-cache-weight-settings.png', 'candidate')
  await configuration.locator('summary').first().click()

  await page.locator('[data-testid="priority-group-filter"] button').first().click()
  await page.getByRole('option', { name: 'Archive group (#17)', exact: true }).click()
  await page.waitForFunction(() => window.fixtureView().snapshot?.group_id === 17 && !window.fixtureView().refreshing)
  await page.getByTestId('latency-window').selectOption('15')
  await page.waitForFunction(() => window.fixtureView().snapshot?.window_minutes === 15 && !window.fixtureView().refreshing)
  assert.match(await page.locator('[data-metric="tps"]').first().innerText(), /25.0 t\/s/)
  await page.setViewportSize({ width: 390, height: 844 })
  await screenshot(page, 'after-mobile.png', 'candidate')
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('http://127.0.0.1:4183/.issue733-fixture/index.html?locale=en', { waitUntil: 'networkidle' })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await screenshot(page, 'after-english-dark.png', 'candidate')
  assert.deepEqual(errors, [])
  await fs.writeFile(path.join(out, 'verification.json'), JSON.stringify({ head, baseline: defaultGroupBaseline, run: process.env.GITHUB_RUN_ID, synthetic: true, actualComponents: ['PrioritySchedulingView', 'AccountPerformanceCell', 'Select', 'DataTable', 'Pagination'], mocks: ['backend', 'auth', 'layout', 'navigation'], productionCalls: 0, paidCalls: 0, shots, measurements, requests, errors }, null, 2))
} catch (error) {
  for (const [index, page] of browser.contexts().flatMap(context => context.pages()).entries()) {
    await page.screenshot({ path: path.join(out, `failure-${index}.png`), fullPage: false }).catch(() => {})
    await fs.writeFile(path.join(out, `failure-${index}.txt`), await page.locator('body').innerText()).catch(() => {})
  }
  await fs.writeFile(path.join(out, 'failure.json'), JSON.stringify({ head, message: String(error), stack: error.stack, errors, networkFailures, measurements, shots, requests }, null, 2))
  throw error
} finally { await browser.close(); await server.close() }
