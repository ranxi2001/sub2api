// Actual AutoConfigView and API modules, synthetic local HTTP responses only.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const head = execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim()
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue')
const vue = vueModule.default || vueModule
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const output = process.env.MODEL_BILLING_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const baselineSHA = '973d4bbd0064121a5533e0d3232ece6043f0d5fc'
const baselinePath = path.join(frontend,'src/views/admin/ModelBillingBaseline.vue')
const baselineLocalePath = path.join(frontend,'src/views/admin/ModelBillingBaselineLocale.ts')
await fs.writeFile(baselinePath,execFileSync('git',['show',baselineSHA+':frontend/src/views/admin/AutoConfigView.vue'],{cwd:root}))
await fs.writeFile(baselineLocalePath,execFileSync('git',['show',baselineSHA+':frontend/src/i18n/locales/zh/autoConfig.ts'],{cwd:root}))
const virtual = {
  'fixture:auth': `export const useAuthStore=()=>({user:{id:1,role:'admin'}})`,
  'fixture:layout': `import {h} from 'vue';export default {setup:(_,ctx)=>()=>h('main',{class:'mx-auto max-w-6xl p-5'},ctx.slots.default?.())}`,
  'fixture:nav': `export default {render:()=>null}`,
  'fixture:whitelist': `export default {render:()=>null}`
}
const html = `<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp} from 'vue';
import {createPinia} from 'pinia';
const {default:Page}=new URLSearchParams(location.search).has('baseline')?await import('/src/views/admin/ModelBillingBaseline.vue'):await import('/src/views/admin/AutoConfigView.vue');
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {defaultExcelBPSDefaults} from '/src/utils/excelBPSDefaults.ts';
import {defaultOpenAITwoFAAdmissionConfig} from '/src/utils/openaiTwoFAAdmission.ts';
import '/src/style.css';
window.fixture={enabled:false,platform:'openai',priority:50,load_factor:1,concurrency:3,cost_multiplier:0.07,group_ids:[],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:'synthetic',excel_bps:defaultExcelBPSDefaults(),openai_two_fa_admission:defaultOpenAITwoFAAdmissionConfig(),model_mappings:[{from:'gpt-5.4',to:'gpt-5.5'}],model_billing:{enabled:true,rules:[{model:'gpt-6-luna*',multiplier:10,group_ids:[5]},{model:'claude-*',multiplier:3,group_ids:[6]}]}};
const lang=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
if(new URLSearchParams(location.search).has('baseline')){const {default:autoConfig}=await import('/src/views/admin/ModelBillingBaselineLocale.ts');i18n.global.mergeLocaleMessage('zh',{autoConfig})}
createApp(Page).use(i18n).use(createPinia()).mount('#app');</script>`
process.chdir(frontend)
const server = await createServer({
  root: frontend, configFile: false, envFile: false,
  define: { __INTLIFY_JIT_COMPILATION__: true, 'import.meta.env.VITE_API_BASE_URL': JSON.stringify('/api/v1') },
  plugins: [vue(), {
    name: 'model-billing-offline-page',
    resolveId(id) { if (id in virtual) return '\0' + id },
    load(id) { return virtual[id.slice(1)] },
    configureServer(vite) { vite.middlewares.use((req,res,next) => {
      if (req.url?.split('?')[0] !== '/') return next()
      res.setHeader('Content-Type','text/html')
      void vite.transformIndexHtml('/',html).then(value=>res.end(value))
    }) }
  }],
  resolve: { alias: [
    {find:'@/stores/auth',replacement:'fixture:auth'},
    {find:'@/components/layout/AppLayout.vue',replacement:'fixture:layout'},
    {find:'@/components/admin/operations/SmartOpsNav.vue',replacement:'fixture:nav'},
    {find:'@/components/account/ModelWhitelistSelector.vue',replacement:'fixture:whitelist'},
    {find:'@',replacement:path.join(frontend,'src')},
    {find:'vue-i18n',replacement:'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'}
  ] },
  server: {host:'127.0.0.1',port:4181,strictPort:true},
  optimizeDeps: {include:['vue','vue-i18n','axios']}
})
await server.listen()
const browser = await chromium.launch({headless:true})
const errors=[], external=[], requests=[], saves=[]
let saved, failSecondPage=false
const groups=[{id:5,name:'OpenAI 示例组',platform:'openai',status:'active'},{id:6,name:'Anthropic 示例组',platform:'anthropic',status:'active'}]
try {
  const context=await browser.newContext({viewport:{width:1280,height:1000}})
  await context.route('**/*',async route=>{
    const url=new URL(route.request().url())
    if(url.origin!=='http://127.0.0.1:4181'){external.push(url.origin);return route.abort()}
    if(!url.pathname.startsWith('/api/v1/'))return route.continue()
    requests.push({path:url.pathname,page:url.searchParams.get('page'),method:route.request().method()})
    let data
    if(url.pathname==='/api/v1/admin/account-ops/auto-config'){
      if(route.request().method()==='PUT'){saved=route.request().postDataJSON();saves.push(structuredClone(saved))}
      data=saved??await route.request().frame().evaluate(()=>window.fixture)
    } else if(url.pathname==='/api/v1/admin/groups/all')data=groups
    else if(url.pathname==='/api/v1/admin/groups'){
      const page=Number(url.searchParams.get('page'))
      if(page===2&&failSecondPage)return route.fulfill({status:503,json:{code:503,message:'synthetic page failure'}})
      data={items:[groups[page-1]],total:2,page,page_size:100,pages:2}
    } else if(url.pathname==='/api/v1/admin/account-quality-plans')data=[]
    else if(url.pathname==='/api/v1/admin/account-ops/auto-config/events')data={items:[],has_more:false}
    else throw new Error('Unexpected synthetic API: '+url.pathname)
    await route.fulfill({json:{code:0,message:'',data}})
  })
  const page=await context.newPage()
  page.on('pageerror',error=>errors.push(error.message))
  const open=async suffix=>{await page.goto('http://127.0.0.1:4181/'+(suffix??''),{waitUntil:'networkidle'});await page.getByTestId('model-billing-group-0-6').waitFor()}
  const save=async()=>{
    const response=page.waitForResponse(r=>r.url().includes('/account-ops/auto-config')&&r.request().method()==='PUT')
    await page.locator('button[type=submit]').click();await response
    await page.waitForFunction(()=>!document.querySelector('button[type=submit]')?.disabled)
  }
  await page.goto('http://127.0.0.1:4181/?baseline=1',{waitUntil:'networkidle'})
  await page.getByTestId('model-billing-model-0').waitFor()
  assert.equal(await page.getByTestId('model-billing-group-0-5').count(),0)
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'baseline-before-group-scope-zh.png')})
  await open()
  assert.equal(await page.getByTestId('model-billing-group-0-5').isChecked(),true)
  assert.equal(await page.getByTestId('model-billing-group-1-6').isChecked(),true)
  assert(requests.some(r=>r.path==='/api/v1/admin/groups'&&r.page==='2'))
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'selected-groups-desktop-zh.png')})
  await page.setViewportSize({width:390,height:1000})
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'selected-groups-mobile-zh.png')})
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
  await page.setViewportSize({width:1280,height:1000})
  const original=await page.evaluate(()=>window.fixture)
  await page.getByTestId('model-billing-group-0-6').check();await save()
  assert.deepEqual(saved.model_billing.rules.map(r=>r.group_ids),[[5,6],[6]])
  for(const key of ['openai_two_fa_admission','excel_bps','model_mappings','group_ids','upgrade_group_ids'])assert.deepEqual(saved[key],original[key])
  await open();assert.equal(await page.getByTestId('model-billing-group-0-6').isChecked(),true)
  await page.getByTestId('model-billing-group-0-5').uncheck()
  await open();assert.equal(await page.getByTestId('model-billing-group-0-5').isChecked(),true)
  await page.getByTestId('model-billing-clear-groups-0').click()
  assert.match(await page.getByTestId('model-billing-scope-0').innerText(),/全部分组/)
  await save();assert.deepEqual(saved.model_billing.rules.map(r=>r.group_ids),[[],[6]])
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'cleared-all-groups-zh.png')})
  saved.model_billing.rules[0].group_ids=[999];failSecondPage=true
  await page.reload({waitUntil:'networkidle'})
  await page.getByTestId('model-billing-groups-error').waitFor()
  assert.equal(await page.getByTestId('model-billing-clear-groups-0').isDisabled(),true)
  await save();assert.deepEqual(saved.model_billing.rules[0].group_ids,[999])
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'page-failure-retains-scope-zh.png')})
  failSecondPage=false
  await page.getByTestId('model-billing-groups-retry').click()
  await page.getByTestId('model-billing-unavailable-0-999').waitFor()
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'missing-id-retained-zh.png')})
  await open('?lang=en')
  await page.getByTestId('model-billing').screenshot({path:path.join(output,'missing-id-retained-en.png')})
  assert.deepEqual(errors,[]);assert.deepEqual(external,[])
  await fs.writeFile(path.join(output,'verification.json'),JSON.stringify({head,baselineSHA,synthetic:true,actualPage:'AutoConfigView.vue',actualApiModules:true,apiRequests:requests,saves,externalRequests:external,pageErrors:errors,checks:['two pages loaded','independent selection','save and reload','discard unsaved changes','clear applies all','2FA and other settings retained','partial page failure retains IDs','retry and unavailable ID warning','mobile no overflow','Chinese and English']},null,2))
} catch(error) {
  await fs.writeFile(path.join(output,'failure.json'),JSON.stringify({error:String(error),errors,external,requests},null,2))
  throw error
} finally {await browser.close();await server.close();await fs.rm(baselinePath,{force:true});await fs.rm(baselineLocalePath,{force:true})}
