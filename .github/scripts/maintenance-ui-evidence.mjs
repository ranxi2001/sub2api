// Remote CI only. Adapted from issue733-browser.mjs and account-connection-timing-browser.mjs.
// Full archived production components/CSS; synthetic API, auth and navigation only.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = process.cwd()
const branch = 'evidence/maintenance-ui-760-763-20261110'
const base = '45e92f44470f91435bd261ea18111c271063c0ac'
const commits = { base, pr760: '7aa315645d76c01f7afa2f3ee0de6af001cc97f2', pr763: '8ff898117438587b5aad0fe5895745d010c6b6e4' }
const components = {
  pr760: 'frontend/src/views/admin/PrioritySchedulingView.vue',
  pr763: 'frontend/src/components/account/SchedulableTimerControl.vue',
}
const blobs = {
  pr760: { before: '1fec8806cfa1986a1c0dd98f57db68105aa94110', after: '566b5dc22bb7b0e8ba9edac76f8d3584533c4aa0' },
  pr763: { before: '00edd09ceeca1f2842eefa497ef1a9726aee2bba', after: 'e1da8708cb2c5558c1f2d59aeeb37fcc03f1978f' },
}
const desktop = { width: 1600, height: 1000 }, mobile = { width: 390, height: 844 }
const runtime = process.env.EVIDENCE_RUNTIME, out = process.env.EVIDENCE_OUTPUT
const git = (...args) => execFileSync('git', args, { cwd: root, maxBuffer: 32 * 1024 * 1024 })
const gitText = (...args) => git(...args).toString().trim()
const digest = bytes => createHash('sha256').update(bytes).digest('hex')
const gitBlob = bytes => createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex')
function under(parent, child) {
  const relative = path.relative(parent, child)
  assert(relative && !relative.startsWith('..') && !path.isAbsolute(relative), `${child} must be inside ${parent}`)
}
assert.equal(process.env.GITHUB_ACTIONS, 'true', 'No local execution: GitHub Actions only')
assert.equal(process.env.GITHUB_EVENT_NAME, 'push')
assert.equal(process.env.GITHUB_REF, `refs/heads/${branch}`)
assert(runtime && out && process.env.RUNNER_TEMP && process.env.PLAYWRIGHT_ROOT)
under(process.env.RUNNER_TEMP, runtime); under(process.env.RUNNER_TEMP, out)
const head = gitText('rev-parse', 'HEAD')
assert.equal(head, process.env.EVIDENCE_HEAD)
assert.equal(head, process.env.GITHUB_SHA)
assert.equal(gitText('merge-base', commits.pr760, commits.pr763), base)
assert.equal(gitText('status', '--porcelain'), '')
for (const pr of Object.keys(components)) for (const phase of ['before', 'after']) {
  assert.equal(gitText('rev-parse', `${phase === 'before' ? base : commits[pr]}:${components[pr]}`), blobs[pr][phase])
}
for (const relative of ['.github/scripts/maintenance-ui-evidence.mjs', '.github/workflows/maintenance-ui-evidence.yml']) {
  assert.deepEqual(await fs.readFile(path.join(root, relative)), git('show', `${head}:${relative}`))
}
assert.equal(fileURLToPath(import.meta.url), path.join(root, '.github/scripts/maintenance-ui-evidence.mjs'))
await fs.mkdir(out, { recursive: true })
const manifest = {
  status: 'running', evidence_head: head, baseline: base, targets: commits,
  run_url: `https://github.com/${process.env.GITHUB_REPOSITORY}/actions/runs/${process.env.GITHUB_RUN_ID}`,
  run_attempt: process.env.GITHUB_RUN_ATTEMPT,
  scope: 'Isolated synthetic browser evidence; no backend, providers, production or paid calls',
  reused_patterns: ['.github/scripts/issue733-browser.mjs', '.github/scripts/account-connection-timing-browser.mjs'],
  shell: 'PR760 full view with auth/layout/navigation stubs; PR763 real DataTable + real timer + real BaseDialog, 224px timer slot; not a full AccountsView test',
  source_checks: [], screenshots: [], behaviors: [], api_requests: [], browser_errors: [], network_errors: [], traces: [],
}
const saveManifest = () => fs.writeFile(path.join(out, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
await saveManifest()

// Check every archived byte and path, not just the changed component. Dependencies,
// Vite caches, fixture entries, browser traces and screenshots live outside sources.
async function verifyArchive(label, phase) {
  const source = path.join(runtime, 'sources', label), commit = commits[label]
  assert.equal(gitText('rev-parse', `${commit}^{commit}`), commit)
  const entries = git('ls-tree', '-rz', commit).toString().split('\0').filter(Boolean)
  const expected = [], actual = []
  for (const entry of entries) {
    const [metadata, relative] = entry.split('\t'), [mode, type, blob] = metadata.split(' ')
    assert.equal(type, 'blob', `Unsupported archive entry ${relative}`)
    const file = path.join(source, relative), stat = await fs.lstat(file)
    const bytes = mode === '120000' ? Buffer.from(await fs.readlink(file)) : await fs.readFile(file)
    assert.equal(gitBlob(bytes), blob, `${phase}: ${label}/${relative}`)
    if (!stat.isSymbolicLink()) assert.equal(stat.mode & 0o222, 0, `Writable source: ${relative}`)
    expected.push(relative)
  }
  async function walk(dir, prefix = '') {
    assert.equal((await fs.stat(dir)).mode & 0o222, 0, `Writable source directory: ${dir}`)
    for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
      const relative = prefix + entry.name
      if (entry.isDirectory()) await walk(path.join(dir, entry.name), relative + '/')
      else actual.push(relative)
    }
  }
  await walk(source)
  assert.deepEqual(actual.sort(), expected.sort(), `${label}: unexpected source files`)
  const result = { label, phase, commit, tree: gitText('rev-parse', `${commit}^{tree}`), files: entries.length, bytes_equal: true, read_only: true }
  manifest.source_checks.push(result)
}

const modules = {
  auth: `import {reactive} from 'vue'; const auth=reactive({user:{id:41,role:'admin'}}); export const useAuthStore=()=>auth;`,
  layout: `import {h} from 'vue'; export default {setup(_,{slots}){return()=>h('main',{class:'mx-auto min-w-0 p-4 sm:p-6'},slots.default?.())}}`,
  nav: `import {h} from 'vue'; export default {setup(){return()=>h('nav',{class:'text-sm text-gray-500'},'离线证据 · 全部为虚构账号')}}`,
  client: `async function request(method,url,options){if(method!=='GET')throw Error('Evidence forbids API writes'); const query=new URLSearchParams(options?.params??{}); const response=await fetch('/fixture'+url+(query.size?'?'+query:''),{signal:options?.signal}); if(!response.ok)throw Error('fixture '+response.status); return {data:await response.json()}}
export const apiClient={get:(url,options)=>request('GET',url,options),post:()=>request('POST'),put:()=>request('PUT'),delete:()=>request('DELETE')};`,
}
const timerRows = [
  { id: 901, name: '虚构账号 A · 一次性开启', schedulable: false },
  { id: 902, name: '虚构账号 B · 一次性关闭', schedulable: true },
  { id: 903, name: '虚构账号 C · 单侧每天开启', schedulable: false },
  { id: 904, name: '虚构账号 D · 单侧每天关闭', schedulable: true },
  { id: 905, name: '虚构账号 E · 每天成对窗口', schedulable: true },
]
const commonEntry = `import {createApp,h,ref} from 'vue'; import {createPinia} from 'pinia'; import {createI18n} from 'vue-i18n';
import zh from '@/i18n/locales/zh/index.ts'; import en from '@/i18n/locales/en/index.ts'; import '@/style.css';
const i18n=createI18n({legacy:false,locale:'zh',messages:{zh,en}});`
const priorityEntry = `${commonEntry}
import View from '@/views/admin/PrioritySchedulingView.vue';
createApp({render:()=>h(View)}).use(createPinia()).use(i18n).mount('#app');`
const timerEntry = `${commonEntry}
import Control from '@/components/account/SchedulableTimerControl.vue'; import DataTable from '@/components/common/DataTable.vue';
const rows=${JSON.stringify(timerRows)};
const columns=[{key:'account',label:'虚构账号',class:'w-48'},{key:'status',label:'当前状态',class:'w-28'},{key:'schedulable',label:'允许调度',class:'w-56 max-w-56'}];
createApp({setup(){const refresh=ref(0),executed=ref(0); return()=>h('main',{class:'mx-auto max-w-5xl min-w-0 space-y-4 p-4 sm:p-6'},[
h('h1',{class:'text-2xl font-semibold'},'账号列表 · 离线虚构定时任务'),
h('p',{class:'text-sm text-gray-500'},'真实 DataTable / SchedulableTimerControl · 224px 窄列'),
h('button',{'data-testid':'fixture-refresh',class:'btn btn-secondary',onClick:()=>refresh.value++},'刷新虚构账号列表'),
h('output',{'data-testid':'fixture-executed',class:'ml-3 text-sm'},String(executed.value)),
h(DataTable,{columns,data:rows,rowKey:'id'}, {
'cell-account':({row})=>h('strong',{class:'font-medium'},row.name),
'cell-status':({row})=>h('span',row.schedulable?'允许调度':'已关闭调度'),
'cell-schedulable':({row})=>h('div',{'data-testid':'timer-row-'+row.id,class:'flex w-56 max-w-full min-w-0 flex-col items-start gap-1'},[
h(Control,{accountId:row.id,accountName:row.name,schedulable:row.schedulable,refreshKey:refresh.value,onExecuted:()=>{executed.value++;refresh.value++}})
])})
])}}).use(createPinia()).use(i18n).mount('#app');`

const config = { enabled: true, mode: 'balanced', group_ids: [11], models: ['synthetic-model'], window_minutes: 60, min_samples: 5, target_ttft_ms: 3000, latency_penalty_minutes: 10, latency_penalty_mode: 'deprioritize', max_load_percent: 80, min_quality_percent: 90, quality_max_age_hours: 24, quality_weight: 30, latency_weight: 25, load_weight: 25, cost_weight: 20, latency_percentile: 90, latency_measurement: 'legacy_forward', balance_protocols: true, oauth_quota_priority: false, oauth_quota_threshold: 90, error_rate_weight: 0, error_rate_window_minutes: 10, error_rate_prior_samples: 5, recovery_exploration_percent: 0 }
const serverNow = '2026-11-10T16:00:00Z'
function analysis(url, scenario) {
  const live = scenario.startsWith('live-')
  const row = { account_id: 901, account_name: '虚构历史账号 A', score: 75.3, tier: 'insufficient', reasons: ['load_unknown'], history_status: 'ready', priority: 1, concurrency: 10, load_factor: 1, load_percent: null, waiting: 0, rate: 0.1, revenue: 20, theoretical_cost: 10, profit: 10, margin: 0.5, economics_source: 'usage', profit_samples: 600, samples: 600, selected_ttft_ms: 1200, p90_ttft_ms: 1200, latency_percentile: 90, quality_passed: 30, quality_samples: 30, error_rate: null, error_successes: 0, error_failures: 0, error_window_minutes: 0, error_endpoint: '', error_observed_at: '0001-01-01T00:00:00Z', error_penalty: 1, base_score: 75.3, selection_weight: 0.16 }
  if (scenario === 'insufficient') row.reasons.push('quality_unknown')
  if (scenario === 'degraded') Object.assign(row, { tier: 'degraded', reasons: ['load_unknown', 'quality_below_target'] })
  if (scenario === 'recovery') Object.assign(row, { latency_recovery: true, reasons: ['load_unknown', 'latency_recovery'] })
  if (live) Object.assign(row, { tier: scenario.slice(5), reasons: [], load_percent: 25, error_observed_at: serverNow, error_window_minutes: 10, error_endpoint: '/v1/responses' })
  if (scenario === 'live-eligible') row.error_rate = 0
  if (scenario === 'live-degraded') Object.assign(row, { error_rate: 0.25, error_successes: 3, error_failures: 1 })
  const scope = { at: serverNow, model: url.searchParams.get('model'), group_id: Number(url.searchParams.get('group_id')) || null, config_revision: 'synthetic-history-v1', latency_percentile: 90, latency_measurement: 'legacy_forward' }
  return {
    snapshot: { ...scope, evaluated_at: serverNow, selection_policy: live ? 'capacity_first' : 'read_only_analysis', mode: 'balanced', history_ready: true, history_status: 'ready', min_samples: 5, window_minutes: Number(url.searchParams.get('minutes')), candidates: [row] },
    latency: { ...scope, bucket_minutes: 5, target_ttft_ms: 3000, accounts: [] },
  }
}
function timerTask(id, action, daily = false) {
  const open = action === 'open'
  return { account_id: id, action, version: 3, execute_at: open ? '2026-11-11T00:00:00Z' : '2026-11-10T17:00:00Z', timezone: 'Asia/Shanghai', local_date: '2026-11-11', local_time: open ? '08:00' : '01:00', offset: '+08:00', requested_by: 41, status: 'pending', attempts: 0, actual_at: null, reason: '', recurrence: daily ? 'daily' : 'once', ...(daily ? { daily_time: open ? '08:00' : '01:00' } : {}) }
}
function timerSnapshot(id, completed) {
  const tasks = id === 905 ? [timerTask(id, 'open', true), timerTask(id, 'close', true)] : [timerTask(id, id === 901 || id === 903 ? 'open' : 'close', id >= 903)]
  if (completed && id <= 902) Object.assign(tasks[0], { status: id === 901 ? 'executed' : 'cancelled', actual_at: id === 901 ? '2026-11-11T00:00:01Z' : null })
  return { tasks, history: [], server_now: completed ? '2026-11-11T00:00:05Z' : serverNow, poll_seconds: 30, open_grace_seconds: 900, max_delay_minutes: 527040 }
}

let browser
const contexts = new Set()
async function openPage(origin, label, state, viewport = desktop) {
  const context = await browser.newContext({ viewport, locale: 'zh-CN', timezoneId: 'Asia/Shanghai', reducedMotion: 'reduce', serviceWorkers: 'block' })
  contexts.add(context)
  await context.tracing.start({ screenshots: true, snapshots: true, sources: false })
  const page = await context.newPage()
  page.setDefaultTimeout(15000)
  await page.clock.install({ time: new Date(serverNow) })
  await page.addInitScript(() => {
    window.evidenceClicks = 0
    document.addEventListener('click', () => window.evidenceClicks++, true)
  })
  page.on('pageerror', error => manifest.browser_errors.push({ label, message: error.message }))
  page.on('console', message => { if (message.type() === 'error') manifest.browser_errors.push({ label, message: message.text() }) })
  page.on('response', response => { if (response.status() >= 400) manifest.network_errors.push({ label, url: response.url(), status: response.status() }) })
  page.on('requestfailed', request => manifest.network_errors.push({ label, url: request.url(), error: request.failure()?.errorText }))
  await page.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url())
    if (url.origin !== origin) {
      manifest.network_errors.push({ label, blocked: url.origin + url.pathname })
      return route.abort()
    }
    if (!url.pathname.startsWith('/fixture/')) return route.continue()
    try {
      assert.equal(request.method(), 'GET', 'No backend writes are allowed')
      let body
      if (url.pathname === '/fixture/admin/groups/all') body = url.searchParams.get('platform') === 'openai' ? [{ id: 11, name: '虚构分组', platform: 'openai' }] : []
      else if (url.pathname === '/fixture/admin/priority-scheduling/config') body = config
      else if (url.pathname === '/fixture/admin/priority-scheduling/models') body = { models: ['synthetic-model'] }
      else if (url.pathname === '/fixture/admin/priority-scheduling/analysis') {
        assert.equal(url.searchParams.get('model'), 'synthetic-model'); assert.equal(url.searchParams.get('group_id'), '11'); assert.equal(url.searchParams.get('minutes'), '60')
        body = analysis(url, state.scenario)
      } else if (url.pathname === '/fixture/admin/priority-scheduling/sticky-bindings') body = { group_id: 11, total: 0, by_account: [] }
      else {
        const match = url.pathname.match(/^\/fixture\/admin\/accounts\/(90[1-5])\/schedulable-timers$/)
        assert(match, `Unexpected fixture endpoint ${url.pathname}`)
        body = timerSnapshot(Number(match[1]), state.completed)
      }
      manifest.api_requests.push({ label, method: request.method(), path: url.pathname, query: url.search, scenario: state.scenario, completed: !!state.completed, response_sha256: digest(JSON.stringify(body)) })
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
    } catch (error) {
      manifest.network_errors.push({ label, message: error.message })
      await route.fulfill({ status: 500, body: 'Unexpected synthetic request' })
    }
  })
  await page.goto(origin, { waitUntil: 'networkidle' })
  return { page, async close() {
    const name = `${label}-trace.zip`
    await context.tracing.stop({ path: path.join(out, name) })
    manifest.traces.push({ name, sha256: digest(await fs.readFile(path.join(out, name))) })
    await context.close(); contexts.delete(context)
  } }
}

async function withSource(pr, phase, action) {
  const label = phase === 'before' ? 'base' : pr, frontend = path.join(runtime, 'sources', label, 'frontend')
  const fixture = path.join(runtime, 'fixtures', `${pr}-${phase}`)
  await fs.mkdir(fixture, { recursive: true })
  for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixture, `${name}.js`), source)
  await fs.writeFile(path.join(fixture, 'entry.js'), pr === 'pr760' ? priorityEntry : timerEntry)
  await fs.writeFile(path.join(fixture, 'index.html'), '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="icon" href="data:,"><title>离线虚构 UI 证据</title></head><body><div id="app"></div><script type="module" src="/entry.js"></script></body></html>')
  const requireDeps = createRequire(path.join(runtime, 'dependencies/package.json'))
  const { createServer } = requireDeps('vite'), vueModule = requireDeps('@vitejs/plugin-vue')
  const theme = requireDeps('tailwindcss/loadConfig')(path.join(frontend, 'tailwind.config.js'))
  const content = Array.isArray(theme.content) ? { files: theme.content } : theme.content
  const tailwindConfig = { ...theme, content: { ...content, files: [...content.files.map(file => typeof file === 'string' ? path.resolve(frontend, file) : file), path.join(fixture, '**/*.js')] } }
  const sourceInfo = { pr, phase, label, exact_commit: commits[label], target_exact_commit: commits[pr], baseline: base, component: components[pr], component_blob: blobs[pr][phase], component_sha256: digest(await fs.readFile(path.join(runtime, 'sources', label, components[pr]))), tailwind_content: tailwindConfig.content.files, theme_and_plugins_preserved: true, stylesheet_blob: gitText('rev-parse', `${commits[label]}:frontend/src/style.css`), tailwind_config_blob: gitText('rev-parse', `${commits[label]}:frontend/tailwind.config.js`) }
  assert.equal(gitText('rev-parse', `${commits[label]}:${components[pr]}`), sourceInfo.component_blob)
  const server = await createServer({
    root: fixture, configFile: false, cacheDir: path.join(runtime, 'vite-cache', `${pr}-${phase}`),
    plugins: [(vueModule.default || vueModule)()],
    resolve: { alias: [
      { find: /^(?:\.\.\/client|\.\/client|@\/api\/client)$/, replacement: path.join(fixture, 'client.js') },
      { find: '@/stores/auth', replacement: path.join(fixture, 'auth.js') },
      { find: '@/components/layout/AppLayout.vue', replacement: path.join(fixture, 'layout.js') },
      { find: '@/components/admin/operations/SmartOpsNav.vue', replacement: path.join(fixture, 'nav.js') },
      { find: '@', replacement: path.join(frontend, 'src') },
    ] },
    define: { __INTLIFY_JIT_COMPILATION__: true },
    css: { postcss: { plugins: [requireDeps('tailwindcss')(tailwindConfig), requireDeps('autoprefixer')()] } },
    server: { host: '127.0.0.1', port: 4196, strictPort: true, hmr: false, fs: { allow: [runtime] } },
    optimizeDeps: { entries: [path.join(fixture, 'index.html')], include: ['vue', 'vue-i18n', 'pinia', '@tanstack/vue-virtual'] },
  })
  try { await server.listen(); await action('http://127.0.0.1:4196', sourceInfo) }
  finally { await server.close() }
}

async function capture(page, name, source, computedStyles, assertions) {
  await page.evaluate(() => document.fonts.ready)
  const file = path.join(out, name)
  await page.screenshot({ path: file, fullPage: true, animations: 'disabled' })
  manifest.screenshots.push({ name, sha256: digest(await fs.readFile(file)), ...source, viewport: page.viewportSize(), browser_timezone: 'Asia/Shanghai', synthetic_accounts_only: true, computed_styles: computedStyles, behavior_assertions: assertions })
  await saveManifest()
}
function observe(name, data) { manifest.behaviors.push({ name, ...data }); return data }
async function cell(page, key) {
  const table = page.getByTestId('candidate-table')
  if (page.viewportSize().width < 768) return table.locator(`[data-field="${key}"] > div`).first()
  const index = ['account', 'tier', 'priority', 'score', 'errorRate', 'quality', 'latency', 'load', 'rate', 'profit', 'details'].indexOf(key)
  assert(index >= 0)
  return table.locator('[data-row-id="901"] > td').nth(index)
}
async function textCell(page, key) { return (await (await cell(page, key)).innerText()).trim() }
async function priorityCSS(page) {
  const css = await page.getByTestId('candidate-table').evaluate(table => {
    const section = table.closest('section'), header = document.querySelector('h1'), td = table.querySelector('tbody td')
    return { radius: getComputedStyle(section).borderRadius, background: getComputedStyle(section).backgroundColor, heading_size: getComputedStyle(header).fontSize, overflow_y: getComputedStyle(table).overflowY, desktop: !!td, cell_white_space: td && getComputedStyle(td).whiteSpace, table_min_width: td && getComputedStyle(table.querySelector('table')).minWidth, body_background: getComputedStyle(document.body).backgroundColor }
  })
  assert.equal(css.radius, '16px'); assert.equal(css.background, 'rgb(255, 255, 255)'); assert.equal(css.heading_size, '24px'); assert.equal(css.overflow_y, 'auto'); assert.equal(css.body_background, 'rgb(249, 250, 251)')
  assert.equal(css.desktop, page.viewportSize().width >= 768)
  if (css.desktop) { assert.equal(css.cell_white_space, 'nowrap'); assert.equal(css.table_min_width, '1328px') }
  return css
}
async function diagnostics(page) {
  const details = (await cell(page, 'details')).locator('details')
  await details.locator('summary').click()
  await page.getByTestId('error-rate-901').scrollIntoViewIfNeeded()
  return details
}
async function historicalAssertions(page, tier) {
  assert.equal(await textCell(page, 'tier'), tier)
  assert.equal(await textCell(page, 'errorRate'), '未采集'); assert.equal(await textCell(page, 'load'), '未采集')
  assert.equal(await textCell(page, 'score'), '75.3'); assert.equal(await textCell(page, 'quality'), '30/30')
  assert.match(await textCell(page, 'latency'), /1200 ms/)
  const errorText = (await page.getByTestId('error-rate-901').textContent()).trim()
  assert.equal(errorText, '未采集')
  const detailText = await (await cell(page, 'details')).textContent()
  for (const forbidden of ['0001', '公元1年', '1/1/1', '错误统计读取', '错误窗口', '得分乘数', '1.000', '0 min']) assert(!detailText.includes(forbidden), `Historical diagnostics contain ${forbidden}`)
  assert.match(detailText, /估算分流权重: 0\.16/)
  await page.getByTestId('priority-analysis-notice').waitFor({ state: 'visible' })
  return { tier, error: errorText, load: '未采集', score: '75.3', quality: '30/30', latency: '1200 ms', estimated_weight: '0.16', forbidden_diagnostic_fields_absent: true }
}
async function runPriority(phase) {
  await withSource('pr760', phase, async (origin, source) => {
    const state = { scenario: 'only-load' }, session = await openPage(origin, `pr760-${phase}`, state), { page } = session
    try {
      await page.getByTestId('candidate-table').waitFor()
      let css = await priorityCSS(page)
      if (phase === 'before') {
        assert.equal(await textCell(page, 'tier'), '数据不足')
        assert.equal(await page.getByTestId('priority-analysis-notice').count(), 0)
        await capture(page, 'pr760-before-desktop.png', source, css, observe('760 baseline overview', { tier: '数据不足', input: 'history ready, tier insufficient, reasons exactly [load_unknown], zero error samples, year 0001' }))
        const detail = await diagnostics(page), errorText = await page.getByTestId('error-rate-901').innerText()
        assert.match(errorText, /错误统计读取/); assert.match(errorText, /错误窗口: 0 min/); assert.match(errorText, /1\.000/)
        const formattedZeroTime = await page.evaluate(() => new Date('0001-01-01T00:00:00Z').toLocaleString())
        assert(errorText.includes(formattedZeroTime))
        await capture(page, 'pr760-before-desktop-diagnostics.png', source, css, observe('760 baseline diagnostics', { error_text: errorText, formatted_zero_time: formattedZeroTime, details_open: await detail.getAttribute('open') !== null }))
        return
      }
      let assertions = await historicalAssertions(page, '历史指标达标')
      await capture(page, 'pr760-after-desktop.png', source, css, observe('760 historical overview', assertions))
      await diagnostics(page)
      await capture(page, 'pr760-after-desktop-diagnostics.png', source, css, observe('760 historical diagnostics', await historicalAssertions(page, '历史指标达标')))
      await (await cell(page, 'details')).locator('summary').click()
      await page.setViewportSize(mobile)
      await page.waitForFunction(() => !document.querySelector('[data-testid="candidate-table"] table'))
      css = await priorityCSS(page); assertions = await historicalAssertions(page, '历史指标达标')
      await page.getByTestId('candidate-table').evaluate(el => { el.scrollTop = 0 })
      await capture(page, 'pr760-after-mobile.png', source, css, observe('760 mobile overview', assertions))
      await diagnostics(page)
      await capture(page, 'pr760-after-mobile-diagnostics.png', source, css, observe('760 mobile diagnostics', await historicalAssertions(page, '历史指标达标')))
      await page.setViewportSize(desktop)
      await page.waitForFunction(() => !!document.querySelector('[data-testid="candidate-table"] table'))
      for (const [scenario, expected] of [['insufficient', '历史数据不足'], ['degraded', '历史指标未达标'], ['recovery', '延迟恢复期'], ['live-insufficient', '数据不足'], ['live-eligible', '体验达标'], ['live-degraded', '体验未达标']]) {
        state.scenario = scenario
        const refresh = page.getByTestId('latency-refresh')
        assert.equal(await refresh.count(), 1)
        assert(await refresh.isEnabled(), 'Production analysis refresh button must be enabled')
        await Promise.all([
          page.waitForResponse(res => new URL(res.url()).pathname === '/fixture/admin/priority-scheduling/analysis'),
          refresh.click(),
        ])
        await page.waitForFunction(expected => document.querySelector('[data-row-id="901"]')?.children[1]?.textContent.trim() === expected, expected)
        if (!scenario.startsWith('live-')) observe(`760 ${scenario}`, await historicalAssertions(page, expected))
        else {
          assert.equal(await page.getByTestId('priority-analysis-notice').count(), 0)
          assert.equal(await textCell(page, 'load'), '25%')
          const errorRate = await textCell(page, 'errorRate')
          assert.equal(errorRate, scenario === 'live-insufficient' ? '无尝试样本' : scenario === 'live-eligible' ? '0.0%' : '25.0%')
          const text = await page.getByTestId('error-rate-901').textContent()
          assert.match(text, /错误统计读取/); assert.match(text, /错误窗口: 10 min/); assert.match(text, /1\.000/)
          observe(`760 ${scenario}`, { tier: expected, load: '25%', error_rate: errorRate, valid_observation_and_window_retained: true, neutral_or_real_penalty_retained: true })
        }
      }
    } finally { await session.close() }
  })
}

const timerRow = (page, id) => page.getByTestId(`timer-row-${id}`)
async function timerReady(page) {
  for (const row of timerRows) await page.waitForFunction(id => {
    const button = document.querySelector(`[data-testid="timer-row-${id}"] [data-test="timer-manage"]`)
    return button && !button.disabled
  }, row.id)
  assert.equal(await page.getByRole('dialog').count(), 0)
  for (const row of timerRows) assert.equal(await timerRow(page, row.id).locator('[data-test="timer-manage"]').getAttribute('aria-expanded'), 'false')
}
async function timerCSS(page, summaries) {
  const result = await timerRow(page, 901).evaluate(row => {
    const button = row.querySelector('[data-test="timer-manage"]'), summary = row.querySelector('[data-test="timer-pending-summary"]')
    return { slot_width: row.getBoundingClientRect().width, button_color: getComputedStyle(button).color, font_size: getComputedStyle(button).fontSize, summary: summary && { white_space: getComputedStyle(summary).whiteSpace, overflow_wrap: getComputedStyle(summary).overflowWrap, color: getComputedStyle(summary).color, width: summary.getBoundingClientRect().width, client_width: summary.clientWidth, scroll_width: summary.scrollWidth }, document_width: document.documentElement.scrollWidth, viewport: innerWidth }
  })
  assert(result.slot_width <= 224 && result.slot_width >= 180, `Expected narrow account cell: ${JSON.stringify(result)}`)
  assert.equal(result.button_color, 'rgb(13, 148, 136)'); assert.equal(result.font_size, '12px')
  assert(result.document_width <= result.viewport + 2, 'Page horizontal overflow')
  if (summaries) {
    assert.equal(result.summary.white_space, 'normal'); assert.equal(result.summary.overflow_wrap, 'break-word'); assert.equal(result.summary.color, 'rgb(75, 85, 99)')
    assert(result.summary.scroll_width <= result.summary.client_width + 2)
  }
  return result
}
async function timerAssertions(page) {
  const results = []
  for (const row of timerRows) {
    const summary = timerRow(page, row.id).locator('[data-test="timer-pending-summary"]')
    await summary.waitFor({ state: 'visible' })
    const text = await summary.innerText(), actions = row.id === 905 ? ['open', 'close'] : [row.id === 901 || row.id === 903 ? 'open' : 'close']
    for (const action of actions) {
      const item = summary.locator(`[data-test="timer-summary-${action}"]`)
      await item.waitFor({ state: 'visible' })
      const value = await item.innerText()
      assert(value.includes(action === 'open' ? '打开调度' : '关闭调度'))
      assert(value.includes(action === 'open' ? '08:00' : '01:00'))
      assert(value.includes('2026-11-11')); assert(value.includes('Asia/Shanghai')); assert(value.includes('UTC+08:00')); assert.match(value, /剩余 \d+ 小时 \d+ 分钟/)
      assert(value.includes(row.id >= 903 ? '每天' : '一次性'))
    }
    results.push({ account_id: row.id, visible_summary: text, actions })
  }
  const windowText = await timerRow(page, 905).locator('[data-test="timer-daily-window"]').innerText()
  assert.match(windowText, /08:00–次日 01:00/); assert.match(windowText, /Asia\/Shanghai/)
  assert.equal(await page.getByRole('dialog').count(), 0)
  return { summaries: results, paired_daily_window: windowText, dialog_closed: true }
}
async function runTimers(phase) {
  await withSource('pr763', phase, async (origin, source) => {
    const state = { completed: false }, session = await openPage(origin, `pr763-${phase}`, state), { page } = session
    try {
      await timerReady(page)
      assert.equal(await page.evaluate(() => window.evidenceClicks), 0)
      let css = await timerCSS(page, phase === 'after')
      if (phase === 'before') {
        assert.equal(await page.locator('[data-test="timer-pending-summary"]').count(), 0)
        for (const id of [901, 902, 903, 904]) {
          const text = await timerRow(page, id).innerText()
          assert(!text.includes('Asia/Shanghai')); assert(!text.includes('剩余')); assert(!text.includes('08:00')); assert(!text.includes('01:00'))
        }
        assert.match(await timerRow(page, 905).innerText(), /08:00–次日 01:00/)
        await capture(page, 'pr763-before-desktop.png', source, css, observe('763 baseline closed', { clicks: 0, pending_summary_count: 0, time_timezone_countdown_absent_for_once_and_single_daily: true, paired_daily_window_retained: true }))
        return
      }
      let assertions = { ...await timerAssertions(page), clicks: await page.evaluate(() => window.evidenceClicks) }
      await capture(page, 'pr763-after-desktop.png', source, css, observe('763 initial closed', assertions))
      await page.setViewportSize(mobile)
      await page.waitForFunction(() => !document.querySelector('[data-row-id="901"]'))
      css = await timerCSS(page, true); assertions = { ...await timerAssertions(page), clicks: await page.evaluate(() => window.evidenceClicks) }
      assert.equal(assertions.clicks, 0)
      await capture(page, 'pr763-after-mobile.png', source, css, observe('763 mobile without clicks', assertions))
      await timerRow(page, 903).locator('[data-test="timer-manage"]').click()
      const dialog = page.getByRole('dialog')
      await dialog.waitFor({ state: 'visible' })
      const dialogCSS = await dialog.evaluate(el => ({ position: getComputedStyle(el).position, display: getComputedStyle(el).display, content_display: getComputedStyle(el.querySelector('.modal-content')).display }))
      assert.deepEqual(dialogCSS, { position: 'fixed', display: 'flex', content_display: 'flex' })
      await capture(page, 'pr763-after-mobile-dialog.png', source, { ...css, dialog: dialogCSS }, observe('763 minimal open interaction', { real_dialog: true, daily_open_visible: await dialog.locator('[data-test="timer-daily-open"]').innerText() }))
      await dialog.getByRole('button', { name: 'Close modal', exact: true }).click()
      await dialog.waitFor({ state: 'detached' })
      await timerAssertions(page)
      state.completed = true
      await page.getByTestId('fixture-refresh').click()
      await page.waitForFunction(() => document.querySelector('[data-testid="fixture-executed"]')?.textContent === '1')
      await page.waitForFunction(() => [901, 902].every(id => !document.querySelector(`[data-testid="timer-row-${id}"] [data-test="timer-pending-summary"]`)))
      await timerReady(page)
      assert.equal(await timerRow(page, 903).locator('[data-test="timer-pending-summary"]').count(), 1)
      assert.equal(await timerRow(page, 905).locator('[data-test="timer-daily-window"]').count(), 1)
      observe('763 refresh and emitted parent reload', { executed_event_count: 1, executed_901_summary_absent: true, cancelled_902_summary_absent: true, pending_daily_and_pair_preserved: true })
      await page.setViewportSize(desktop)
      await page.waitForFunction(() => !!document.querySelector('[data-row-id="901"]'))
      css = await timerCSS(page, false)
      await capture(page, 'pr763-after-desktop-refreshed.png', source, css, manifest.behaviors.at(-1))
      await page.reload({ waitUntil: 'networkidle' }); await timerReady(page)
      for (const id of [901, 902]) assert.equal(await timerRow(page, id).locator('[data-test="timer-pending-summary"]').count(), 0)
      observe('763 browser reload retains completed-state absence', { executed_901_summary_absent: true, cancelled_902_summary_absent: true })
    } finally { await session.close() }
  })
}

try {
  for (const label of Object.keys(commits)) await verifyArchive(label, 'before-browser')
  for (const file of ['package.json', 'pnpm-lock.yaml', '.npmrc']) {
    const expected = await fs.readFile(path.join(runtime, 'sources/base/frontend', file))
    assert.deepEqual(await fs.readFile(path.join(runtime, 'dependencies', file)), expected)
    for (const pr of ['pr760', 'pr763']) assert.deepEqual(await fs.readFile(path.join(runtime, 'sources', pr, 'frontend', file)), expected)
  }
  const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
  browser = await chromium.launch({ headless: true })
  manifest.browser_version = browser.version()
  manifest.chinese_font = execFileSync('fc-match', ['-f', '%{family}', 'sans:lang=zh-cn'], { encoding: 'utf8' })
  assert.match(manifest.chinese_font, /Noto/)
  for (const phase of ['before', 'after']) await runPriority(phase)
  for (const phase of ['before', 'after']) await runTimers(phase)
  assert.equal(manifest.screenshots.length, 11)
  for (const pr of ['pr760', 'pr763']) for (const [phase, width] of [['before', 1600], ['after', 1600], ['after', 390]]) {
    assert(manifest.screenshots.some(shot => shot.pr === pr && shot.phase === phase && shot.viewport.width === width), `Missing ${pr}/${phase}/${width}`)
  }
  assert.deepEqual(manifest.browser_errors, []); assert.deepEqual(manifest.network_errors, [])
  assert(manifest.api_requests.every(request => request.method === 'GET'))
  manifest.status = 'passed'
} catch (error) {
  manifest.status = 'failed'; manifest.failure = error.stack || error.message
  process.exitCode = 1
  console.error(error)
} finally {
  for (const context of contexts) await context.close().catch(() => {})
  if (browser) await browser.close()
  try {
    for (const label of Object.keys(commits)) await verifyArchive(label, 'after-browser')
    assert.equal(gitText('rev-parse', 'HEAD'), head); assert.equal(gitText('status', '--porcelain'), '')
  } catch (error) {
    manifest.status = 'failed'; manifest.immutability_failure = error.stack || error.message; process.exitCode = 1
  }
  await saveManifest()
  console.log(JSON.stringify({ status: manifest.status, screenshots: manifest.screenshots.length, behaviors: manifest.behaviors.length, manifest: path.join(out, 'manifest.json') }))
}
