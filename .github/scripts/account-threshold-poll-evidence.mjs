import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

// Run only in the existing remote UI job, with its installed project dependencies.
const repo = resolve('.'), root = join(repo, 'frontend')
const output = join(process.env.EVIDENCE_DIR, 'threshold-poll')
const baseline = '29aa570ed820808fcaaae42ab75fa1b3e9a6ba67'
const candidate = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(candidate, process.env.EXPECTED_HEAD_SHA || candidate)
const { createServer } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')))
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const sourceFiles = ['src/views/admin/AccountOpsView.vue', 'src/components/admin/operations/AccountOpsRuleList.vue']
const oldSources = new Map(sourceFiles.map(file => [join(root, file), execFileSync('git', ['show', `${baseline}:frontend/${file}`], { encoding: 'utf8' })]))
const hash = value => createHash('sha256').update(value).digest('hex')
const cacheDir = mkdtempSync(join(tmpdir(), 'threshold-poll-'))
const entryName = `.threshold-poll-${process.pid}.js`, entry = join(root, entryName)
const origin = 'http://127.0.0.1:4321'
const manifest = {
  baseline, candidate, createdAt: new Date().toISOString(),
  workflowRun: process.env.GITHUB_RUN_ID, workflowAttempt: process.env.GITHUB_RUN_ATTEMPT,
  scope: 'Actual embedded AccountOpsView, real project Vite/Vue/Tailwind CSS, fictional data and mocked API adapter; not production',
  sources: sourceFiles.map(file => ({ path: `frontend/${file}`, before: hash(oldSources.get(join(root, file))), after: hash(readFileSync(join(root, file))) })),
  pipeline: ['frontend/vite.config.ts', 'frontend/tailwind.config.js', 'frontend/postcss.config.js', 'frontend/src/style.css', '.github/scripts/account-threshold-poll-evidence.mjs'].map(path => ({ path, sha256: hash(readFileSync(join(repo, path))) })),
  runs: [], images: [],
}
let phase = 'before', server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import {createApp,h,nextTick} from 'vue';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {useAuthStore} from '/src/stores/auth.ts';
import {useAppStore} from '/src/stores/app.ts';
import {apiClient} from '/src/api/client.ts';
import View from '/src/views/admin/AccountOpsView.vue';
import Toast from '/src/components/common/Toast.vue';
import '/src/style.css';
const language=new URLSearchParams(location.search).get('lang')||'zh';
await loadLocaleMessages(language);i18n.global.locale.value=language;document.documentElement.lang=language;
const pinia=createPinia(),app=useAppStore(pinia),auth=useAuthStore(pinia);
auth.user={id:42,role:'admin',username:'fictional-admin'};
const accountName=language==='zh'?'虚构余额账号':'Fictional balance account';
const groupName=language==='zh'?'虚构监控分组':'Fictional monitoring group';
const error=language==='zh'?'阈值账号暂时不可用（虚构响应）':'Threshold accounts unavailable (fictional response)';
const accounts=[{account_id:1,account_name:accountName,platform:'openai',type:'apikey',provider_id:42,group_id:'manual-example42',group_mode:'manual',balance:42,unit:'USD',balance_status:'ok',received_at:null,usage_windows:[]}];
const groups=[{id:'manual-example42',name:groupName,default_name:groupName,provider:'provider',provider_id:42,site:'https://example.test',mode:'manual',account_ids:[1]}];
const config={enabled:true,recipient:'ops@example.test',balance_low:true,weekly_quota:true,cooldown_minutes:60,groups,webhooks:[],balance_thresholds:[{account_id:1,enabled:true,threshold:50,unit:'USD',notify_alert:true,notify_recovery:true}],quota_thresholds:[]};
const state=window.__pollEvidence={mode:'success',calls:[],pending:[],errors:[],error,groupName,toastCalls:[],ready:false};
const originalShowError=app.showError.bind(app);app.showError=(...args)=>{state.toastCalls.push(args[0]);return originalShowError(...args)};
apiClient.defaults.adapter=async c=>{
  state.calls.push({url:c.url,mode:state.mode,time:Date.now()});
  let data;
  if(c.url.endsWith('/config'))data={config,smtp_configured:true,encryption_key_configured:true,dropped_signals:0,storage_failures:0};
  else if(c.url.endsWith('/alerts'))data={items:[],has_more:false};
  else if(c.url.endsWith('/threshold-accounts')){
    if(state.mode==='failure')throw Object.assign(new Error(error),{config:c,response:{status:503,data:{message:error},headers:{}}});
    data={items:accounts};
  }else if(c.url.endsWith('/groups')){
    if(state.mode==='failure')await new Promise(resolve=>state.pending.push(resolve));
    data={groups};
  }else throw new Error('Unexpected evidence API: '+c.url);
  return {config:c,status:200,statusText:'OK',headers:{},data:{code:0,data}};
};
state.release=()=>{for(const resolve of state.pending.splice(0))resolve()};
createApp({setup(){return()=>h('main',{class:'p-6 space-y-5'},[
  h('p',{class:'text-sm text-gray-500'},language==='zh'?'实际告警页面 · 虚构数据':'Actual alert page · fictional data'),
  h(View,{embedded:true}),h(Toast)
])}}).use(pinia).use(i18n).mount('#app');
await nextTick();state.ready=true;
`)
  // Tailwind content globs and the existing PostCSS config resolve from frontend.
  // configFile preserves project Vue plugins, aliases, define, theme and CSS pipeline.
  process.chdir(root)
  server = await createServer({ configFile: join(root, 'vite.config.ts'), root, cacheDir,
    server: { host: '127.0.0.1', port: 4321, strictPort: true },
    plugins: [{ name: 'threshold-poll-source-preview', enforce: 'pre', load(id) {
      if (phase === 'before' && oldSources.has(id)) return oldSources.get(id)
    }, configureServer(vite) { vite.middlewares.use((req, res, next) => {
      if (req.url?.split('?')[0] !== '/poll-evidence') return next()
      res.setHeader('Content-Type', 'text/html')
      res.end(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module" src="/${entryName}"></script></body></html>`)
    }) } }],
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  for (const language of ['zh', 'en']) for (phase of ['before', 'after']) {
    server.moduleGraph.invalidateAll()
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, timezoneId: 'UTC' })
    await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
    const page = await context.newPage(), errors = []
    await page.clock.install()
    page.on('pageerror', error => errors.push(error.message))
    const run = { phase, language, sha: phase === 'before' ? baseline : candidate, gates: [], snapshots: [] }
    manifest.runs.push(run)
    const gate = (name, passed) => run.gates.push({ name, passed })
    const snapshot = async label => {
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
      const filename = `${phase}-${language}-${label}.png`
      await page.screenshot({ path: join(output, filename), fullPage: true })
      manifest.images.push({ path: filename, sha256: hash(readFileSync(join(output, filename))), phase, language, state: label, sha: run.sha })
    }
    const state = async () => page.evaluate(() => {
      const s = window.__pollEvidence, rules = document.querySelector('[data-testid="account-ops-rules"]')
      return { calls: s.calls.filter(c => c.url.endsWith('/threshold-accounts') || c.url.endsWith('/groups')), toastCalls: [...s.toastCalls], inline: [...rules.querySelectorAll('[role="status"]')].some(el => el.textContent.includes(s.error)), nonempty: rules.textContent.includes(s.groupName) && rules.textContent.includes('42.00 USD'), pending: s.pending.length }
    })
    try {
      await page.goto(`${origin}/poll-evidence?lang=${language}`, { waitUntil: 'networkidle' })
      await page.waitForFunction(() => window.__pollEvidence?.ready && document.querySelector('[data-testid="rule-row-manual-example42"]'))
      assert.deepEqual(errors, [], 'compile/runtime failure is not an expected behavioral RED')
      const styles = await page.getByTestId('account-ops-rules').evaluate(el => ({ border: getComputedStyle(el).borderTopWidth, radius: getComputedStyle(el).borderRadius, background: getComputedStyle(el).backgroundColor, fontSize: getComputedStyle(el.querySelector('table')).fontSize, primary: getComputedStyle(document.querySelector('[role="tab"]')).color }))
      assert.equal(styles.border, '1px', 'real Tailwind border')
      assert.equal(styles.radius, '12px', 'real Tailwind rounded-xl')
      assert.equal(styles.background, 'rgb(255, 255, 255)', 'real background')
      assert.equal(styles.fontSize, '14px', 'real Tailwind table text-sm')
      assert.equal(styles.primary, 'rgb(15, 118, 110)', 'project primary-700 theme')
      run.styles = styles
      assert.equal((await state()).nonempty, true, 'initial nonempty real view must render')
      await snapshot('initial')
      // Both timer ticks are the production 30-second interval, advanced by Playwright's clock.
      await page.evaluate(() => { window.__pollEvidence.mode = 'failure' })
      await page.clock.runFor(30_000)
      await page.waitForFunction(() => window.__pollEvidence.pending.length > 0)
      await page.waitForTimeout(50)
      let observed = await state()
      run.snapshots.push({ label: 'first-failure', ...observed })
      gate('immediate-inline-error', observed.inline)
      gate('first-background-failure-has-no-toast', observed.toastCalls.length === 0)
      gate('nonempty-data-retained', observed.nonempty)
      await snapshot('failure-pending')
      const occupiedCalls = observed.calls.length
      await page.clock.runFor(30_000)
      observed = await state()
      gate('no-new-threshold-reads-while-sibling-pending', observed.calls.length === occupiedCalls)
      run.snapshots.push({ label: 'skipped-tick', ...observed })
      await page.evaluate(() => window.__pollEvidence.release())
      await page.waitForTimeout(50)
      // A second failed background read after occupancy is released.
      await page.clock.runFor(30_000)
      await page.waitForFunction(() => window.__pollEvidence.pending.length > 0)
      await page.waitForTimeout(50)
      observed = await state()
      gate('second-failure-inline-and-no-toast', observed.inline && observed.toastCalls.length === 0)
      gate('second-failure-retains-nonempty-data', observed.nonempty)
      await snapshot('second-failure')
      await page.evaluate(() => { window.__pollEvidence.mode = 'success'; window.__pollEvidence.release() })
      await page.waitForTimeout(50)
      const callsBeforeRecovery = (await state()).calls.length
      await page.clock.runFor(30_000)
      await page.waitForTimeout(50)
      observed = await state()
      gate('polling-resumes-after-both-settle', observed.calls.length === callsBeforeRecovery + 2)
      gate('success-clears-error-and-keeps-data', !observed.inline && observed.nonempty)
      await snapshot('recovered')
      assert.deepEqual(errors, [], 'compile/runtime failure cannot count as behavioral RED')
      run.errors = errors
      run.result = run.gates.every(g => g.passed) ? 'GREEN' : 'RED'
      console.log(JSON.stringify({ oracle: 'threshold-poll', ...run }))
      if (phase === 'before') {
        assert.equal(run.result, 'RED', 'original production source must fail the new behavioral oracle')
        assert.equal(run.gates.find(g => g.name === 'no-new-threshold-reads-while-sibling-pending').passed, false, 'RED must include the actual overlap, not a compilation failure')
      } else assert.equal(run.result, 'GREEN', 'candidate must pass every behavioral gate')
    } finally { await context.close() }
  }
} finally {
  writeFileSync(join(output, 'source-manifest.json'), JSON.stringify(manifest, null, 2))
  await browser?.close()
  await server?.close()
  rmSync(entry, { force: true })
  rmSync(cacheDir, { recursive: true, force: true })
}
