// Remote CI only. Real GroupsView, AppLayout, AppHeader, TablePageLayout,
// DataTable, Select, Pagination, dialogs, API client and compiled project CSS.
// Only authentication/settings, unrelated navigation/onboarding and API responses are synthetic.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.cwd(), appRoot = process.env.GROUPS_APP_ROOT || root
const frontend = path.join(appRoot, 'frontend'), out = process.env.GROUPS_EVIDENCE_DIR
assert(process.env.CI && out, 'Application execution is allowed only on remote CI')
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.GROUPS_EXPECTED_HEAD)
const baseline = '338528c48fc480fa998c984d923b049841e20039'
const scope = process.env.GROUPS_SCOPE || (appRoot === root ? 'head' : 'baseline')
const productionSHA = process.env.GROUPS_APP_SHA || (appRoot === root ? head : baseline)
await fs.mkdir(out, { recursive: true })
const fixture = path.join(frontend, '.mobile-groups-fixture')
await fs.mkdir(fixture, { recursive: true })
const stores = `import {reactive} from 'vue';
const auth=reactive({user:{id:41,role:'admin',username:'演示管理员',balance:0},isAdmin:true,isSimpleMode:false});
const app=reactive({sidebarCollapsed:false,cachedPublicSettings:{subscription_enabled:false,model_plaza_enabled:false},docUrl:'',contactInfo:'',showError:m=>{throw Error(m)},showSuccess:()=>{},toggleMobileSidebar:()=>{}});
export const useAuthStore=()=>auth;export const useAppStore=()=>app;
export const useAdminSettingsStore=()=>({customMenuItems:[]});
export const useOnboardingStore=()=>({setReplayCallback:()=>{},isCurrentStep:()=>false,nextStep:()=>{},replay:()=>{}});`
await fs.writeFile(path.join(fixture, 'stores.js'), stores)
await fs.writeFile(path.join(fixture, 'empty.js'), 'export default {render:()=>null};')
await fs.writeFile(path.join(fixture, 'tour.js'), 'export const useOnboardingTour=()=>({replayTour:()=>{}});')
await fs.writeFile(path.join(fixture, 'index.html'), `<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createMemoryHistory} from 'vue-router';
import Page from '/src/views/admin/GroupsView.vue';import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import '/src/style.css';
await loadLocaleMessages('zh');i18n.global.locale.value='zh';
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/admin/groups',component:Page,meta:{title:'分组管理'}}]});await router.push('/admin/groups');await router.isReady();
createApp(Page).use(createPinia()).use(i18n).use(router).mount('#app');</script></body></html>`)
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue')
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const alias = (find, name) => ({ find, replacement: path.join(fixture, `${name}.js`) })
const server = await createServer({ root: frontend, configFile: false, envFile: false,
  plugins: [vueModule.default ? vueModule.default() : vueModule()],
  resolve: { alias: [
    alias(/^@\/stores$/, 'stores'), ...['app','auth','adminSettings','onboarding'].map(name => alias(`@/stores/${name}`, 'stores')),
    alias('@/composables/useOnboardingTour', 'tour'), alias('@/components/common/AnnouncementBell.vue', 'empty'),
    alias(/(?:.*\/)?AppSidebar\.vue$/, 'empty'), alias('@/components/common/SubscriptionProgressMini.vue', 'empty'),
    { find: '@', replacement: path.join(frontend, 'src') },
    { find: 'vue-i18n', replacement: 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' },
  ] },
  css: { postcss: { plugins: [tailwind({ ...theme, content: [path.join(frontend, 'src/**/*.{vue,js,ts}'), path.join(fixture, '*.{html,js}')] }), autoprefixer()] } },
  define: { __INTLIFY_JIT_COMPILATION__: true },
  server: { host: '127.0.0.1', port: 4187, strictPort: true, fs: { allow: [root, appRoot] } },
  optimizeDeps: { entries: [path.join(fixture, 'index.html')], include: ['vue','pinia','vue-router','vue-i18n','axios'] },
})
const groups = Array.from({ length: 20 }, (_, i) => ({
  id: i + 1, name: i === 0 ? '搜索目标演示组' : `演示分组 ${String(i + 1).padStart(2, '0')}`,
  platform: i % 2 ? 'anthropic' : 'openai', status: 'active', subscription_type: 'standard',
  rate_multiplier: 1, is_exclusive: false, account_count: 3, active_account_count: 3,
  sort_order: i, supported_model_scopes: [], created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
}))
const measurements = [], requests = [], errors = [], external = [], violations = [], searchViolations = [], screenshots = []
let browser, page, css, outcome = 'setup-or-interaction-failure'
const sourceHashes = {}
for (const file of ['src/views/admin/GroupsView.vue','src/components/layout/AppLayout.vue','src/components/layout/AppHeader.vue','src/components/layout/TablePageLayout.vue','src/components/common/DataTable.vue','src/style.css','tailwind.config.js']) {
  sourceHashes[file] = createHash('sha256').update(await fs.readFile(path.join(frontend,file))).digest('hex')
}
function check(value, message) { if (!value) violations.push(message) }
async function shot(name) {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false, animations: 'disabled' })
  screenshots.push({ name, viewport: page.viewportSize() })
}
async function geometry(label) {
  // AppLayout animates its desktop sidebar margin for 300ms after crossing lg.
  // Measure the settled layout, including after Vue handles the resize event.
  await page.evaluate(async () => {
    await new Promise(requestAnimationFrame)
    await Promise.allSettled(document.getAnimations()
      .filter(animation => animation.effect?.getTiming().iterations !== Infinity)
      .map(animation => animation.finished))
    // Native focus can start smooth document scrolling (html.scroll-smooth).
    // That scroll is not returned by getAnimations(); do not measure mid-flight.
    let previous=scrollY,stable=0
    for(let frame=0;frame<180 && stable<8;frame++){
      await new Promise(requestAnimationFrame)
      stable=Math.abs(scrollY-previous)<0.1?stable+1:0
      previous=scrollY
    }
    if(stable<8)throw Error('SCROLL_SETUP: document scrolling did not settle')
  })
  const value = await page.evaluate(() => {
    const rect = el => {
      if (!el) return null
      const r = el.getBoundingClientRect(), s = getComputedStyle(el)
      return { top:r.top,bottom:r.bottom,left:r.left,right:r.right,height:r.height,width:r.width,
        scrollTop:el.scrollTop,scrollHeight:el.scrollHeight,clientHeight:el.clientHeight,scrollWidth:el.scrollWidth,clientWidth:el.clientWidth,
        position:s.position,overflowX:s.overflowX,overflowY:s.overflowY,flexShrink:s.flexShrink,display:s.display,scrollMarginTop:s.scrollMarginTop }
    }
    const search = document.querySelector('.layout-section-fixed input'), r = search.getBoundingClientRect()
    const hit = document.elementFromPoint(r.left+r.width/2, r.top+r.height/2)
    return { viewport:{width:innerWidth,height:innerHeight},scrollX,scrollY,document:rect(document.documentElement),
      header:rect(document.querySelector('header')),main:rect(document.querySelector('main')),layout:rect(document.querySelector('.table-page-layout')),
      toolbar:rect(document.querySelector('.layout-section-fixed')),content:rect(document.querySelector('.layout-section-scrollable')),
      table:rect(document.querySelector('.table-wrapper')),search:rect(search),searchHit:hit===search,hitTag:hit?.tagName,hitClass:hit?.className,
      ancestors:[...function*(el){while(el){yield el;el=el.parentElement}}(search)].map(el=>({tag:el.tagName,class:el.className,...rect(el)})),
      scrollContainers:[...document.querySelectorAll('body *')].filter(el=>el.clientHeight>0&&el.scrollHeight>el.clientHeight+1&&/(auto|scroll|hidden)/.test(getComputedStyle(el).overflowY)).map(el=>({tag:el.tagName,class:el.className,...rect(el)})),
    }
  })
  measurements.push({label,...value});return value
}
async function top() {
  await page.evaluate(() => window.scrollTo({top:0,left:0,behavior:'instant'}))
  await page.waitForFunction(() => scrollY===0)
}
async function searchUsable(label) {
  const g = await geometry(label)
  const usable=g.search.top>=g.header.bottom && g.search.bottom<=g.viewport.height && g.search.left>=0 && g.search.right<=g.viewport.width && g.searchHit
  const failure=`${label}: search must be fully visible and hit-testable; ${JSON.stringify({scrollY:g.scrollY,search:g.search,header:g.header,hit:g.hitTag})}`
  check(usable,failure)
  if(!usable)searchViolations.push(failure)
  check(g.document.scrollWidth<=g.viewport.width && g.scrollX===0, `${label}: document must not overflow horizontally`)
  return g
}
async function middle() {
  await page.locator('.layout-section-fixed input').evaluate(el=>el.blur())
  await page.evaluate(()=>window.scrollTo({top:Math.round((document.documentElement.scrollHeight-innerHeight)/2),behavior:'instant'}))
}
async function returnToSearch(label, block) {
  await page.locator('.layout-section-fixed input').evaluate((el,block)=>{
    if(block)el.scrollIntoView({block,behavior:'instant'})
    el.focus()
  },block)
  assert(await page.locator('.layout-section-fixed input').evaluate(el=>el===document.activeElement),'Search return must actually focus the input')
  await searchUsable(label)
  await shot(label)
}
try {
  await server.listen();browser=await chromium.launch({headless:true})
  const context=await browser.newContext({viewport:{width:375,height:667},timezoneId:'UTC'})
  await context.route('**/*', async route => {
    const url=new URL(route.request().url())
    if(url.origin!=='http://127.0.0.1:4187'){external.push(url.origin);return route.abort()}
    if(!url.pathname.startsWith('/api/'))return route.continue()
    const request={path:url.pathname,query:Object.fromEntries(url.searchParams),method:route.request().method()};requests.push(request)
    assert.equal(request.method,'GET','No API mutations in this browser fixture')
    let data
    if(url.pathname==='/api/v1/admin/groups'){
      const search=url.searchParams.get('search')||'', platform=url.searchParams.get('platform')
      const items=groups.filter(g=>g.name.includes(search)&&(!platform||g.platform===platform))
      data={items,total:items.length,page:1,page_size:20,pages:1}
    } else if(url.pathname==='/api/v1/admin/groups/all')data=groups
    else if(/\/groups\/(usage-summary|capacity-summary)$/.test(url.pathname))data=[]
    else if(url.pathname==='/api/v1/admin/groups/live-capability')data={supported:false,reason:'synthetic'}
    else if(/\/groups\/\d+\/model-allowlist-candidates$/.test(url.pathname))data=[]
    else {errors.push('Unexpected API '+url.pathname);return route.fulfill({status:500,json:{code:500,message:'Unexpected fixture API'}})}
    return route.fulfill({json:{code:0,message:'',data}})
  })
  page=await context.newPage();page.setDefaultTimeout(15000)
  page.on('pageerror',e=>errors.push(e.message))
  await page.goto('http://127.0.0.1:4187/.mobile-groups-fixture/index.html',{waitUntil:'networkidle'})
  await page.getByText('搜索目标演示组',{exact:true}).first().waitFor()
  await page.evaluate(()=>document.fonts.ready)
  // Guard against an unstyled fixture before treating any assertion as a behavioral RED.
  css=await page.evaluate(()=>({header:getComputedStyle(document.querySelector('header')).position,headerHeight:document.querySelector('header>div').getBoundingClientRect().height,padding:getComputedStyle(document.querySelector('main')).paddingTop,toolbarShrink:getComputedStyle(document.querySelector('.layout-section-fixed')).flexShrink,display:getComputedStyle(document.querySelector('.table-page-layout')).display}))
  assert.deepEqual(css,{header:'sticky',headerHeight:64,padding:'16px',toolbarShrink:'0',display:'flex'},'CSS_SETUP: actual Tailwind utilities must be compiled')
  await page.locator('.layout-section-fixed input').focus()
  assert.equal((await searchUsable('initial-visible-search-focus')).scrollY,0,'Focusing an already visible search must not move the page')
  for(const viewport of [{width:375,height:667},{width:390,height:844},{width:320,height:667},{width:667,height:375},{width:844,height:390},{width:1023,height:768},{width:375,height:400},{width:375,height:320},{width:1280,height:900}]){
    await page.setViewportSize(viewport);await top()
    const label=`${viewport.width}x${viewport.height}`
    const g=await searchUsable(`${label}-top`)
    await shot(`${label}-top`)
    if(viewport.width<1024){
      check(g.layout.bottom>=g.content.bottom-1,`${label}: mobile layout must contain card content so the header sticky boundary follows the document; layout.bottom=${g.layout.bottom}, content.bottom=${g.content.bottom}`)
      await page.evaluate(()=>window.scrollTo({top:Math.round((document.documentElement.scrollHeight-innerHeight)/2),behavior:'instant'}))
      const mid=await geometry(`${label}-middle`)
      check(Math.abs(mid.header.top)<1,`${label}: header must remain at document top while scrolling cards; header.top=${mid.header.top}, scrollY=${mid.scrollY}`)
      await shot(`${label}-middle`)
      // Native DOM scrolling does not account for a sticky header by default.
      await returnToSearch(`${label}-search-start-from-middle`,'start')
      await middle();await returnToSearch(`${label}-search-focus-from-middle`,null)
      await top()
      // Recreate the supplied screenshot's scroll state: exclusive filter just
      // below the header, with search/platform/status above it. This is a saved
      // scroll offset simulation, not a claim about native browser history.
      const savedY=await page.evaluate(()=>{
        const filter=document.querySelectorAll('.layout-section-fixed .select-trigger')[2]
        return Math.max(0,filter.getBoundingClientRect().top+scrollY-document.querySelector('header').getBoundingClientRect().height)
      })
      await middle()
      await page.evaluate(y=>window.scrollTo({top:y,behavior:'instant'}),savedY)
      await geometry(`${label}-restored-filter-offset`);await shot(`${label}-restored-filter-offset`)
      await returnToSearch(`${label}-search-nearest-after-restore`,'nearest')
    } else {
      assert.equal(await page.locator('table').count(),1,'Desktop list must render')
      await page.locator('.table-wrapper').evaluate(el=>{el.scrollTop=el.scrollHeight/2})
      await geometry(`${label}-table-middle`)
      await page.locator('.table-wrapper').evaluate(el=>{el.scrollTop=0})
    }
    await top();await searchUsable(`${label}-returned-top`)
  }
  await page.setViewportSize({width:375,height:667});await top()
  const search=page.locator('.layout-section-fixed input')
  await middle();await returnToSearch('375x667-interactive-search-after-scroll','nearest')
  // Keyboard traversal, actual debounced API request and rendered filtering.
  await page.getByRole('button',{name:'Select option'}).first().focus();await page.keyboard.press('Shift+Tab')
  assert(await search.evaluate(el=>el===document.activeElement),'Search must be reachable by keyboard')
  await page.keyboard.type('搜索目标')
  await page.waitForFunction(()=>document.querySelectorAll('[data-field="name"]').length===1)
  assert(requests.some(r=>r.path==='/api/v1/admin/groups'&&r.query.search==='搜索目标'),'Typed search must reach the real API client')
  await searchUsable('filtered-search')
  await page.setViewportSize({width:375,height:320})
  await searchUsable('focused-search-simulated-keyboard')
  assert(await search.evaluate(el=>el===document.activeElement),'Viewport resize must retain search focus')
  await page.setViewportSize({width:375,height:667})
  await search.fill('');await page.waitForFunction(()=>document.querySelectorAll('[data-field="name"]').length===20)
  await page.locator('.layout-section-fixed .select-trigger').first().click()
  await page.getByRole('option',{name:'OpenAI',exact:true}).click()
  await page.waitForFunction(()=>document.querySelectorAll('[data-field="name"]').length===10)
  assert(requests.some(r=>r.query.platform==='openai'),'Platform filter must reach the API')
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/admin/groups')
  await page.getByTitle('刷新',{exact:true}).click();await response
  const sortResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/admin/groups'&&new URL(r.url()).searchParams.get('sort_by')==='name')
  await page.locator('[data-test="mobile-sort-controls"] select').selectOption('name');await sortResponse
  await page.getByTitle('排序',{exact:true}).click()
  const sortDialog=page.getByRole('dialog',{name:'排序',exact:true})
  await sortDialog.waitFor();await sortDialog.getByLabel('Close modal').click();await sortDialog.waitFor({state:'hidden'})
  await page.locator('[data-tour="groups-create-btn"]').click()
  const createDialog=page.getByRole('dialog',{name:'创建分组',exact:true})
  await createDialog.waitFor();await createDialog.getByLabel('Close modal').click();await createDialog.waitFor({state:'hidden'})
  await top();await searchUsable('toolbar-actions-returned-top')
  assert.deepEqual(errors,[],'No setup/runtime errors accepted as behavioral RED');assert.deepEqual(external,[])
  outcome=violations.length?'behavioral-red':'green'
  if(violations.length)throw new Error('GROUPS_LAYOUT_REGRESSION\n'+(searchViolations.length?'GROUPS_SEARCH_OCCLUSION\n':'')+violations.join('\n'))
  console.log('GROUPS_BROWSER_GREEN: real components, compiled CSS, scroll/resize and search assertions passed')
} catch(error) {
  if(page)await shot('failure').catch(()=>{})
  await fs.writeFile(path.join(out,'failure.txt'),String(error.stack||error))
  throw error
} finally {
  await fs.writeFile(path.join(out,'manifest.json'),JSON.stringify({head,baseline,scope,productionSHA,outcome,sourceHashes,css,synthetic:true,browser:`Playwright Chromium ${browser?.version()}; viewport emulation, not physical mobile/keyboard or native history restoration`,actualComponents:['GroupsView','AppLayout','AppHeader','TablePageLayout','DataTable','Select','Pagination','BaseDialog'],screenshots,measurements,requests,errors,external,violations,searchViolations},null,2))
  if(browser)await browser.close();await server.close()
  await fs.rm(fixture,{recursive:true,force:true})
}
