// CI-only: compile the actual Vue component with the production Vite config.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [repoArg, outputArg] = process.argv.slice(2)
const repo = resolve(repoArg), root = join(repo, 'frontend'), output = resolve(outputArg)
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(dirname(require.resolve('vite/package.json')), 'dist/node/index.js')).href)
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const temporary = mkdtempSync(join(tmpdir(), 'vip-production-'))
const entryName = `.vip-production-${process.pid}`, entry = join(root, entryName + '.js'), html = join(root, entryName + '.html')
const origin = 'http://127.0.0.1:4301', evidence = []
let server, browser
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
  writeFileSync(html, `<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/${entryName}.js"></script></html>`, { flag: 'wx' })
  process.chdir(root)
  await build({ root, build: { outDir: join(temporary, 'dist'), target: 'esnext', rollupOptions: { input: html } } })
  // The official config is needed for compilation only. Loading its checker
  // plugin again for static preview leaves development workers running.
  server = await preview({ root, configFile: false, build: { outDir: join(temporary, 'dist') }, preview: { host: '127.0.0.1', port: 4301, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  for (const lang of ['zh', 'en']) for (const device of ['desktop', 'mobile']) {
    const context = await browser.newContext({ viewport: device === 'desktop' ? { width: 1280, height: 900 } : { width: 390, height: 844 } })
    const page = await context.newPage(), errors = []
    page.on('pageerror', error => errors.push(error.stack || error.message))
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
    await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
    await page.goto(`${origin}/${entryName}.html?lang=${lang}`, { waitUntil: 'networkidle' })
    await page.getByTestId('settle-100').click()
    await page.screenshot({ path: join(output, `${lang}-${device}-clicked.png`) })
    const result = { lang, device, errors, confirmationCount: await page.getByTestId('confirm-settlement').count(), settlementRequests: await page.evaluate(() => window.settlements) }
    evidence.push(result)
    console.log(JSON.stringify(result))
    assert.equal(result.confirmationCount, 1, 'Click must display a confirmation in the production build')
    assert.deepEqual(errors, [])
    await page.getByTestId('cancel-settlement').click()
    assert.equal(await page.evaluate(() => window.settlements.length), 0)
    await page.getByTestId('settle-100').click()
    await page.getByTestId('confirm-settlement').click()
    await page.waitForFunction(() => window.settlements.length === 1 && !document.querySelector('[data-testid="settle-100"]'))
    assert.match(await page.getByTestId('outstanding').innerText(), /5700\.00/)
    assert.deepEqual(errors, [])
    result.settlementRequests = await page.evaluate(() => window.settlements)
    result.completed = true
    await context.close()
  }
} finally {
  writeFileSync(join(output, 'production-manifest.json'), JSON.stringify({ head: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim(), evidence, boundary: 'Production Vite config, actual component and CSS, synthetic API. No live accounts or real settlement.' }, null, 2) + '\n')
  await browser?.close()
  await new Promise(resolveClose => server ? server.httpServer.close(resolveClose) : resolveClose())
  rmSync(entry, { force: true }); rmSync(html, { force: true }); rmSync(temporary, { recursive: true, force: true })
}
console.log('Production collection regression completed; browser and preview closed.')
