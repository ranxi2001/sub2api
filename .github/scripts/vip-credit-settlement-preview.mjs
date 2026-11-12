// CI-only browser regression: actual component/CSS, fictional ledger, isolated API.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const [repoArg, outputArg] = process.argv.slice(2)
const repo = resolve(repoArg), root = join(repo, 'frontend'), output = resolve(outputArg)
const req = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const baseline = '6c6b2edf8355fcd7e4e1e6d541ccc31042524ae8', head = git('rev-parse', 'HEAD')
const cacheDir = mkdtempSync(join(tmpdir(), 'vip-settlement-'))
const entry = join(root, `.vip-settlement-${process.pid}.js`)
const origin = 'http://127.0.0.1:4299', evidence = [], errors = []
let before = true, server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import {apiClient} from '/src/api/client.ts';
window.settlements=[];
window.fixtureState={config:{version:1,enabled:true,threshold:'50',amount:'300',cap:'10000'},outstanding:'6000',cap_blocked:false,entries:Array.from({length:20},(_,i)=>({id:100-i,user_id:2,epoch:i,source:'auto_credit',trigger:'initial-admission',amount:'300',balance_before:'40',balance_after:'340',note:'自动充值需索取费用',status:'pending',created_at:'2026-11-01T00:00:00Z',settled_at:null,settled_by:null})),has_more:false,next_before_id:0};
apiClient.defaults.adapter=async config=>{
if(config.url==='/admin/users/2/auto-credit'&&config.method==='get'){}
else if(config.url==='/admin/users/2/auto-credit/100/settle'&&config.method==='post'){
  if(JSON.parse(config.data).confirm!==true)throw Error('Missing explicit confirmation');
  window.settlements.push(config.url);
  window.fixtureState.entries[0].status='settled';window.fixtureState.outstanding='5700';
}else throw Error('Unexpected synthetic API '+config.method+' '+config.url);
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data:structuredClone(window.fixtureState)}};
};
await import('/src/__fixtures__/vip-auto-credit.ts');
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'settlement-baseline', enforce: 'pre', load(id) {
      const path = 'frontend/src/components/admin/user/UserAutoCreditModal.vue'
      if (before && id === join(repo, path)) return git('show', baseline + ':' + path)
    }, configureServer(vite) { vite.middlewares.use((request, response, next) => {
      if (request.url === '/' || request.url?.startsWith('/?')) {
        response.setHeader('Content-Type', 'text/html')
        response.end('<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/' + entry.split('/').pop() + '"></script></html>')
      } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
    }) } }, vue()], server: { host: '127.0.0.1', port: 4299, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'vue-router', 'axios'] }
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  for (const lang of ['zh', 'en']) for (const device of ['desktop', 'mobile']) {
    const context = await browser.newContext({ viewport: device === 'desktop' ? { width: 1280, height: 900 } : { width: 390, height: 844 } })
    const page = await context.newPage()
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
    const geometry = () => page.locator('[data-testid="confirm-settlement"]').evaluate(button => {
      const rect = button.getBoundingClientRect(), body = button.closest('.modal-body').getBoundingClientRect()
      return { visible: rect.top >= body.top && rect.bottom <= body.bottom && rect.left >= 0 && rect.right <= innerWidth, top: rect.top, bottom: rect.bottom, bodyBottom: body.bottom }
    })
    before = true; server.moduleGraph.invalidateAll()
    await page.goto(origin + '/?lang=' + lang, { waitUntil: 'networkidle' })
    await page.locator('[data-testid="settle-100"]').click()
    const oldGeometry = await geometry()
    assert.equal(oldGeometry.visible, false, 'Baseline must reproduce an offscreen confirmation')
    assert.equal(await page.evaluate(() => window.settlements.length), 0)
    await page.screenshot({ path: join(output, `${lang}-${device}-before.png`) })
    before = false; server.moduleGraph.invalidateAll()
    await page.reload({ waitUntil: 'networkidle' })
    await page.locator('[data-testid="settle-100"]').click()
    const fixedGeometry = await geometry()
    assert.equal(fixedGeometry.visible, true, 'Confirmation must be visible without manual scrolling')
    assert.equal(await page.locator('[role="alertdialog"]').evaluate(panel => document.activeElement === panel), true)
    assert.equal(await page.locator('article').first().locator('[role="alertdialog"]').count(), 1)
    assert.equal(await page.evaluate(() => window.settlements.length), 0)
    await page.screenshot({ path: join(output, `${lang}-${device}-after.png`) })
    await page.locator('[data-testid="cancel-settlement"]').click()
    assert.equal(await page.locator('[role="alertdialog"]').count(), 0)
    assert.equal(await page.evaluate(() => window.settlements.length), 0)
    await page.locator('[data-testid="settle-100"]').click()
    await page.locator('[data-testid="confirm-settlement"]').click()
    await page.waitForFunction(() => window.settlements.length === 1 && !document.querySelector('[data-testid="settle-100"]'))
    assert.equal(await page.locator('[role="alertdialog"]').count(), 0)
    assert.match(await page.locator('[data-testid="outstanding"]').innerText(), /5700\.00/)
    evidence.push({ lang, device, oldGeometry, fixedGeometry, settlementRequests: await page.evaluate(() => window.settlements) })
    await context.close()
  }
  assert.deepEqual(errors, [])
} finally {
  writeFileSync(join(output, 'settlement-manifest.json'), JSON.stringify({ baseline, head, evidence, errors, boundary: 'CI Chromium; actual component and CSS with 20 fictional entries and synthetic API. No production settlement or full-page navigation.' }, null, 2) + '\n')
  await browser?.close(); await server?.close()
  rmSync(entry, { force: true }); rmSync(cacheDir, { recursive: true, force: true })
}
