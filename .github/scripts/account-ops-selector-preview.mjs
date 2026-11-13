// Remote-only offline acceptance: actual Vue components, Tailwind, locales and BaseDialog.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { pathToFileURL } from 'node:url'

const repo = process.cwd(), root = join(repo, 'frontend')
const output = process.env.EVIDENCE_DIR
if (!output || !process.env.GITHUB_ACTIONS) throw Error('Run only in GitHub Actions with EVIDENCE_DIR')
const req = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const head = git('rev-parse', 'HEAD'), base = 'f895ea743f34a4c4ab6c9d01c01e13c931625a6e'
const hash = data => createHash('sha256').update(data).digest('hex')
const sourceFiles = [
  'frontend/src/components/admin/operations/AccountOpsGroupDialog.vue',
  'frontend/src/components/admin/operations/QualityAccountSelector.vue',
  'frontend/src/components/common/BaseDialog.vue', 'frontend/src/style.css',
  'frontend/tailwind.config.js', 'frontend/postcss.config.js',
  'frontend/src/i18n/locales/en/accountOps.ts', 'frontend/src/i18n/locales/zh/accountOps.ts',
]
const sources = Object.fromEntries(sourceFiles.map(file => [file, { before: hash(git('show', `${base}:${file}`)), after: hash(readFileSync(join(repo, file), 'utf8').trimEnd()) }]))
const cacheDir = mkdtempSync(join(tmpdir(), 'account-ops-preview-'))
const entryName = `.account-ops-preview-${process.pid}.js`, entry = join(root, entryName)
const origin = 'http://127.0.0.1:4299'
const captures = [], assertions = [], reachability = [], errors = [], browserWarnings = []
let before = true, server, browser, context, page
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import {createApp,h,ref} from 'vue';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import Dialog from '/src/components/admin/operations/AccountOpsGroupDialog.vue';
import Selector from '/src/components/admin/operations/QualityAccountSelector.vue';
import '/src/style.css';
const params=new URL(location.href).searchParams;
const locale=params.get('locale')||'zh';
await loadLocaleMessages(locale); i18n.global.locale.value=locale;document.documentElement.lang=locale;
if(params.has('dark'))document.documentElement.classList.add('dark');
const groups=[
{id:'manual-alpha',name:locale==='zh'?'Alpha 示例站点':'Alpha demo site',default_name:'Alpha',provider:'sub2api',site:'https://alpha.example',mode:'manual',account_ids:[121,999]},
{id:'manual-beta',name:locale==='zh'?'Beta 示例站点':'Beta demo site',default_name:'Beta',provider:'newapi',site:'https://beta.example',mode:'manual',account_ids:[2,60,998]}
];
const accounts=Array.from({length:125},(_,i)=>({account_id:i+1,account_name:'Demo account '+(i+1),platform:'openai',type:i%2?'oauth':'apikey',balance:null,unit:'USD',balance_status:'unknown',received_at:null,usage_windows:[]}));
window.__writes=[];window.__savedEvents=[];window.__closed=0;
apiClient.defaults.adapter=async config=>{
if(config.method!=='put'||config.url!=='/admin/account-ops/groups')throw Error('Unexpected offline API '+config.method+' '+config.url);
const payload=typeof config.data==='string'?JSON.parse(config.data):config.data;window.__writes.push(structuredClone(payload));
await new Promise(resolve=>{window.__releaseSave=resolve});
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data:{enabled:false,recipient:'',balance_low:true,weekly_quota:true,cooldown_minutes:60,groups:payload.groups}}};
};
const show=ref(true), selected=ref([99]), search=ref(''),group=ref(''),type=ref(''),statuses=ref([]),scope=ref('manual');
const app=createApp({setup(){return()=>params.has('quality')?h('main',{class:'p-6'},[h(Selector,{
modelValue:selected.value,'onUpdate:modelValue':v=>selected.value=v,search:search.value,'onUpdate:search':v=>search.value=v,group:group.value,'onUpdate:group':v=>group.value=v,type:type.value,'onUpdate:type':v=>type.value=v,statuses:statuses.value,'onUpdate:statuses':v=>statuses.value=v,scope:scope.value,'onUpdate:scope':v=>scope.value=v,
accounts:[{id:1,name:'Available demo'},{id:2,name:'Already used demo'}],groups:[],accountsLoading:false,selectingAccounts:false,accountsError:'',accountPage:1,accountPages:1,accountsTotal:2,bulk:false,disabledReason:a=>a.id===2?i18n.global.t('qualityOps.accountHasRule'):'',
onSelectPage:()=>selected.value=[...new Set([...selected.value,1])],onSelectAll:()=>selected.value=[...new Set([...selected.value,1])],onClear:()=>selected.value=[]
})]):h(Dialog,{show:show.value,groups,accounts,onClose:()=>{window.__closed++;show.value=false},onSaved:config=>window.__savedEvents.push(config)})}});
app.use(createPinia());app.use(i18n);app.mount('#app');window.__selected=()=>selected.value;
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({
    root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'account-ops-pinned-before', enforce: 'pre', load(id) {
      const clean = id.split('?')[0]
      if (before && !id.includes('?')) {
        const file = sourceFiles.find(file => join(repo, file) === clean)
        if (file) return git('show', `${base}:${file}`)
      }
    }, configureServer(vite) {
      vite.middlewares.use((request, response, next) => {
        if (request.url === '/' || request.url?.startsWith('/?')) {
          response.setHeader('Content-Type', 'text/html')
          response.end('<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module" src="/' + entryName + '"></script></body></html>')
        } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
      })
    } }, vue()],
    server: { host: '127.0.0.1', port: 4299, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'axios'] },
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  context = await browser.newContext({ viewport: { width: 1280, height: 1000 }, timezoneId: 'UTC' })
  page = await context.newPage()
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => {
    if (message.type() !== 'error') return
    if (message.text() === 'Permissions policy violation: unload is not allowed in this document.') browserWarnings.push(message.text())
    else errors.push(message.text())
  })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin === origin || url.protocol === 'data:') return route.continue()
    errors.push(`Blocked external request: ${url.origin}`)
    return route.abort()
  })
  const capture = async name => {
    const file = name + '.png'
    await page.screenshot({ path: join(output, file), fullPage: true })
    captures.push({ file, sha256: hash(readFileSync(join(output, file))), viewport: page.viewportSize() })
  }
  const load = async query => {
    await page.goto(origin + '/' + query, { waitUntil: 'networkidle' })
    await page.locator('[role="dialog"]').waitFor()
    const css = await page.locator('.modal-overlay').evaluate(el => ({ position: getComputedStyle(el).position, display: getComputedStyle(el).display }))
    assert.equal(css.position, 'fixed', 'Tailwind project modal CSS must be generated')
    assert.equal(css.display, 'flex')
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'no horizontal viewport overflow')
  }
  // Scroll the real modal body with wheel input; require full bounds and a clear
  // center hit before interacting with controls near the fixed footer.
  const reach = async (locator, name) => {
    for (let attempt = 0; attempt < 20; attempt++) {
      const bounds = await locator.evaluate(el => {
        const rect = el.getBoundingClientRect(), body = el.closest('.modal-body').getBoundingClientRect()
        const x = rect.x + rect.width / 2, y = rect.y + rect.height / 2
        const hit = document.elementFromPoint(x, y)
        return { x: rect.x, y: rect.y, width: rect.width, height: rect.height, top: body.top, bottom: body.bottom,
          inside: rect.top >= body.top && rect.bottom <= body.bottom && rect.left >= 0 && rect.right <= innerWidth,
          centerHit: hit === el || el.contains(hit) }
      })
      if (bounds.inside && bounds.centerHit) { reachability.push({ name, ...bounds }); return }
      const body = await page.locator('.modal-body').boundingBox()
      await page.mouse.move(body.x + body.width / 2, body.y + body.height / 2)
      await page.mouse.wheel(0, bounds.y < bounds.top ? -260 : 260)
      await page.waitForTimeout(80)
    }
    throw Error('Control unreachable by natural modal scrolling: ' + name)
  }
  await load('?locale=zh')
  assert.equal(await page.locator('[data-testid^="account-group-member-manual-alpha-"]').count(), 125)
  await capture('before-desktop-zh')
  await page.setViewportSize({ width: 390, height: 844 })
  await load('?locale=en')
  await capture('before-mobile-en')
  before = false
  server.moduleGraph.invalidateAll()
  await page.setViewportSize({ width: 1280, height: 1000 })
  await load('?locale=zh')
  const picker = page.getByTestId('account-ops-member-picker')
  const search = picker.locator('input:not([type])')
  assert.equal(await picker.locator('[data-account-id]').count(), 50)
  assert.equal(await picker.locator('[aria-busy]').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), 2, 'actual desktop Tailwind grid')
  await capture('after-desktop-zh')
  await search.fill('DEMO account 125')
  assert.deepEqual(await picker.locator('[data-account-id]').evaluateAll(rows => rows.map(row => row.dataset.accountId)), ['125'])
  await search.press('Enter')
  await search.fill('#124')
  await picker.locator('[data-account-id="124"]').waitFor()
  assert.equal(await picker.locator('[data-account-id]').count(), 1)
  await search.fill('')
  await picker.getByTestId('quality-select-page').click()
  await picker.locator('button[aria-label]').last().click()
  await picker.locator('[data-account-id="51"]').waitFor()
  assert.equal(await picker.locator('[data-account-id="60"] input').isChecked(), false)
  await search.fill('#125')
  await picker.getByTestId('quality-select-all').click()
  await search.fill('#121')
  await picker.locator('[data-account-id="121"] input').waitFor()
  assert.equal(await picker.locator('[data-account-id="121"] input').isChecked(), true)
  await capture('after-desktop-hidden-selection')
  await page.getByTestId('save-account-groups').click()
  await page.waitForFunction(() => window.__writes.length === 1)
  assert.equal(await picker.getByTestId('quality-select-page').isDisabled(), true)
  assert.equal(await picker.locator('[data-account-id="121"] input').isDisabled(), true)
  const writes = await page.evaluate(() => window.__writes)
  assert.deepEqual([...writes[0].groups[0].account_ids].sort((a,b)=>a-b), [...Array.from({length:50},(_,i)=>i+1),121,125,999])
  assert.deepEqual(writes[0].groups[1].account_ids, [60, 998], 'source group keeps an unselected listed account and a hidden ID')
  assert.equal(writes[0].groups[0].site, 'https://alpha.example')
  assert.equal(writes[0].groups[1].site, 'https://beta.example')
  writeFileSync(join(output, 'synthetic-save-payload.json'), JSON.stringify(writes, null, 2) + '\n')
  await page.evaluate(() => window.__releaseSave())
  await page.waitForFunction(() => window.__savedEvents.length === 1)
  assertions.push('Name/ID search and Enter; pagination keeps hidden selections; page/all-match selection unions IDs; moving ID 2 keeps unselected listed ID 60 and hidden ID 998 in Beta; complete target payload retains ID 999; busy controls disabled')

  await load('?locale=en')
  await picker.getByTestId('quality-account-group').selectOption('manual-beta')
  await picker.locator('[data-account-id="2"]').waitFor()
  assert.equal(await picker.locator('[data-account-id]').count(), 2)
  await picker.getByTestId('quality-account-type').selectOption('apikey')
  await picker.locator('[data-account-id="2"]').waitFor({ state: 'detached' })
  await picker.getByTestId('quality-account-group').selectOption('')
  await picker.getByTestId('quality-select-all').click()
  await search.fill('#125')
  assert.equal(await picker.locator('[data-account-id="125"] input').isChecked(), true)
  await capture('after-desktop-en')
  await page.getByTestId('cancel-account-groups').click()
  await page.waitForFunction(() => window.__closed === 1)
  assert.equal(await page.evaluate(() => window.__writes.length), 0)
  assertions.push('Actual draft-group and API key filters; select all matching across pages; cancel makes no API write')

  await page.setViewportSize({ width: 390, height: 844 })
  await load('?locale=en')
  assert.equal(await picker.locator('[aria-busy]').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), 1, 'actual mobile Tailwind grid')
  await capture('after-mobile-en-overview')
  await reach(search, 'mobile search input')
  const nextPage = picker.locator('button[aria-label]').last()
  await reach(nextPage, 'mobile next page')
  await nextPage.click()
  await picker.locator('[data-account-id="51"]').waitFor()
  await reach(picker.locator('button[aria-label]').first(), 'mobile previous page')
  await picker.locator('button[aria-label]').first().click()
  await reach(page.getByTestId('add-account-group'), 'mobile add group')
  await page.getByTestId('add-account-group').click()
  assert.equal(await page.locator('[data-testid^="account-group-name-"]').count(), 3)
  await capture('after-mobile-add-group')
  await load('?locale=en')
  await reach(search, 'mobile search input after reopening')
  await search.fill('#125')
  await search.press('Tab')
  assert.equal(await page.evaluate(() => document.activeElement?.tagName), 'BUTTON')
  await page.keyboard.press('Enter')
  await picker.locator('[data-account-id="125"] input').focus()
  await page.keyboard.press('Space')
  assert.equal(await picker.locator('[data-account-id="125"] input').isChecked(), true)
  await capture('after-mobile-en')
  await page.keyboard.press('Escape')
  await page.waitForFunction(() => window.__closed === 1)
  assert.equal(await page.evaluate(() => window.__writes.length), 0)
  await load('?locale=zh&dark=1')
  await capture('after-mobile-zh-dark')
  assertions.push('390px phone layout and top overview; natural wheel scrolling makes search, both page buttons and Add group fully visible with clear center hit-tests; Add group click works; keyboard Tab/Enter/Space/Escape; Chinese/English; dark mode; no viewport overflow')

  await page.goto(origin + '/?locale=en&quality=1', { waitUntil: 'networkidle' })
  const used = page.locator('[data-account-id="2"] input')
  assert.equal(await used.isDisabled(), true)
  await page.getByTestId('quality-select-page').click()
  assert.deepEqual(await page.evaluate(() => window.__selected()), [99, 1])
  assert.equal(await used.isChecked(), false)
  await capture('quality-existing-disabled-contract')
  assertions.push('Shared selector still honors existing already-used disabledReason; hidden selection retained by page union')
  assert.deepEqual(errors, [])
} catch (error) {
  if (page) await page.screenshot({ path: join(output, 'failure.png'), fullPage: true }).catch(() => {})
  errors.push(error.stack)
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ head, base, sources, sourceHashNormalization: 'trimEnd', captures, assertions, reachability, errors, browserWarnings, boundary: 'Actual GroupDialog, QualityAccountSelector, BaseDialog, project Tailwind/PostCSS, real locales. Synthetic DTOs and offline axios adapter only. Before uses pinned base source overrides; after uses final checkout. No production or upstream requests.' }, null, 2) + '\n')
  await context?.close()
  await browser?.close()
  await server?.close()
  rmSync(entry, { force: true })
  rmSync(cacheDir, { recursive: true, force: true })
}
