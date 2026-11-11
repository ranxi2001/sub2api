// Remote-only real component/CSS evidence, synthetic API, no external requests.
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { createHash } from 'node:crypto'
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
const baseline = '6fa7fb86eae7c732794450646dc13831dce87ff1', head = git('rev-parse', 'HEAD')
const cacheDir = mkdtempSync(join(tmpdir(), 'vip-credit-preview-'))
const entryName = `.vip-credit-preview-${process.pid}.js`, entry = join(root, entryName)
const origin = 'http://127.0.0.1:4298', captures = [], errors = [], browserWarnings = [], geometry = [], interactions = []
let before = true, server, browser, activePage
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import {apiClient} from '/src/api/client.ts';
window.fixtureState={config:{version:1,enabled:true,threshold:'50.00000000',amount:'300.00000000',cap:'500.00000000'},outstanding:'0.00000000',cap_blocked:false,entries:[],has_more:false,next_before_id:0};
window.fixtureRequests=[];
apiClient.defaults.adapter=async config=>{
if(config.url!=='/admin/users/2/auto-credit')throw Error('Unexpected synthetic API '+config.url);
window.fixtureRequests.push({method:config.method,url:config.url});
if(config.method==='put'){const patch=typeof config.data==='string'?JSON.parse(config.data):config.data;window.saved=structuredClone(patch);window.fixtureState.config={...patch,version:patch.version+1};}
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data:structuredClone(window.fixtureState)}};
};
await import('/src/__fixtures__/vip-auto-credit.ts');
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'vip-credit-baseline', enforce: 'pre', load(id) {
      const clean = id.split('?')[0]
      const path = 'frontend/src/components/admin/user/UserAutoCreditModal.vue'
      if (before && !id.includes('?') && clean === join(repo, path)) return git('show', baseline + ':' + path)
    }, configureServer(vite) { vite.middlewares.use((request, response, next) => {
      if (request.url === '/' || request.url?.startsWith('/?')) {
        response.setHeader('Content-Type', 'text/html')
        response.end('<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/' + entryName + '"></script></html>')
      } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
    }) } }, vue()],
    server: { host: '127.0.0.1', port: 4298, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'vue-router', 'axios'] }
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  for (const lang of ['zh', 'en']) for (const device of ['desktop', 'mobile']) {
    const context = await browser.newContext({ viewport: device === 'desktop' ? { width: 1280, height: 1000 } : { width: 390, height: 844 }, timezoneId: 'UTC' })
    const page = await context.newPage()
    activePage = page
    page.on('pageerror', e => errors.push(e.message))
    page.on('console', message => {
      if (message.type() !== 'error') return
      if (message.text() === 'Permissions policy violation: unload is not allowed in this document.') browserWarnings.push(message.text())
      else { errors.push(message.text()); console.error(message.text()) }
    })
    await page.route('**/*', route => {
      const url = new URL(route.request().url())
      if (url.origin === origin || url.protocol === 'data:') return route.continue()
      errors.push('Blocked external request ' + url.origin); return route.abort()
    })
    const screenshot = async (stage, focus = 'notification') => {
      if (focus === 'settings') await page.locator('[name="cap"]').scrollIntoViewIfNeeded()
      else await page.locator('.modal-body').evaluate(body => { body.scrollTop = 0 })
      const file = `${lang}-${device}-${stage}.png`
      await page.screenshot({ path: join(output, file), fullPage: true })
      const bytes = readFileSync(join(output, file))
      captures.push({ file, sha256: createHash('sha256').update(bytes).digest('hex'), bytes: bytes.length, source: before ? baseline : head, lang, device })
    }
    before = true; server.moduleGraph.invalidateAll()
    await page.goto(origin + '/?lang=' + lang, { waitUntil: 'networkidle' })
    await page.locator('[name="threshold"]').waitFor()
    if (await page.locator('[name="notify_email"]').count()) throw Error('Baseline unexpectedly contains notification checkbox')
    await screenshot('before')
    if (device === 'mobile') await screenshot('before-settings', 'settings')
    before = false; server.moduleGraph.invalidateAll()
    await page.reload({ waitUntil: 'networkidle' })
    const checkbox = page.locator('[name="notify_email"]')
    await checkbox.waitFor()
    if (await checkbox.isChecked()) throw Error('Legacy/new opt-in must default OFF')
    const inspect = async () => {
      for (const [field, value] of Object.entries({ threshold: '50.00000000', amount: '300.00000000', cap: '500.00000000' })) {
        if (await page.locator(`[name="${field}"]`).inputValue() !== value) throw Error('Fixture money changed: ' + field)
      }
      const layout = await page.locator('form').evaluate(form => {
        const grid = form.querySelector('.grid'), box = form.getBoundingClientRect()
        const check = form.querySelector('[name="notify_email"]'), label = check.closest('label').getBoundingClientRect()
        const panel = form.closest('.modal-content').getBoundingClientRect()
        return { columns: getComputedStyle(grid).gridTemplateColumns.split(' ').length, display: getComputedStyle(grid).display, overflow: label.right > panel.right + 1, formWidth: box.width, panelWidth: panel.width, innerWidth: window.innerWidth }
      })
      if (layout.display !== 'grid' || layout.columns !== (device === 'desktop' ? 3 : 1) || layout.overflow || layout.panelWidth > layout.innerWidth) throw Error('Actual CSS geometry failed: ' + JSON.stringify(layout))
      const text = await page.locator('form').innerText()
      if (!text.includes('1,000,000') || !text.includes('8')) throw Error('Precision/maximum copy changed')
      geometry.push({ lang, device, ...layout })
    }
    await inspect(); await screenshot('after-default-off')
    if (device === 'mobile') await screenshot('after-default-off-settings', 'settings')
    await checkbox.check(); await page.locator('form button[type="submit"]').click()
    await page.waitForFunction(() => window.saved?.notify_email === true && !document.querySelector('[name="notify_email"]').disabled)
    await page.getByRole('button', { name: lang === 'zh' ? '重新加载' : 'Reload', exact: true }).click()
    await page.waitForFunction(() => window.fixtureRequests.filter(r => r.method === 'get').length >= 2 && document.querySelector('[name="notify_email"]')?.checked === true && !document.querySelector('[name="notify_email"]').disabled)
    await inspect(); await screenshot('after-saved-reloaded-on')
    if (device === 'mobile') await screenshot('after-saved-reloaded-on-settings', 'settings')
    await checkbox.uncheck(); await page.locator('form button[type="submit"]').click()
    await page.waitForFunction(() => window.saved?.notify_email === false && !document.querySelector('[name="notify_email"]').disabled)
    await page.getByRole('button', { name: lang === 'zh' ? '重新加载' : 'Reload', exact: true }).click()
    await page.waitForFunction(() => window.fixtureRequests.filter(r => r.method === 'get').length >= 3 && document.querySelector('[name="notify_email"]')?.checked === false && !document.querySelector('[name="notify_email"]').disabled)
    await screenshot('after-disabled')
    interactions.push({ lang, device, saved: await page.evaluate(() => window.saved), requests: await page.evaluate(() => window.fixtureRequests) })
    await context.close()
  }
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ head, baseline, baselineComponentBlob: git('rev-parse', baseline + ':frontend/src/components/admin/user/UserAutoCreditModal.vue'), cssBlob: git('rev-parse', head + ':frontend/src/style.css'), captures, geometry, interactions, errors, boundary: 'GitHub Actions Chromium, actual UserAutoCreditModal and project CSS; isolated synthetic API/auth fixture only. No real users, email, paid API, full navigation or production validation.' }, null, 2) + '\n')
  if (errors.length) throw Error(errors.join('\n'))
} catch (error) {
  errors.push(error.stack || String(error))
  if (activePage && !activePage.isClosed()) {
    writeFileSync(join(output, 'failure-body.txt'), await activePage.locator('body').innerText())
    await activePage.screenshot({ path: join(output, 'failure.png'), fullPage: true })
    const bytes = readFileSync(join(output, 'failure.png'))
    captures.push({ file: 'failure.png', sha256: createHash('sha256').update(bytes).digest('hex'), bytes: bytes.length, source: before ? baseline : head, failure: true })
  }
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ head, baseline, baselineComponentBlob: git('rev-parse', baseline + ':frontend/src/components/admin/user/UserAutoCreditModal.vue'), cssBlob: git('rev-parse', head + ':frontend/src/style.css'), captures, geometry, interactions, errors, browserWarnings, boundary: 'GitHub Actions Chromium; actual component and CSS with synthetic API/auth. PNGs are evidence artifacts, not native PR attachments.' }, null, 2) + '\n')
  await browser?.close(); await server?.close()
  rmSync(entry, { force: true }); rmSync(cacheDir, { recursive: true, force: true })
}
