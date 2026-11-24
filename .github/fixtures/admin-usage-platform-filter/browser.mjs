// Remote Actions only. Actual UsageView, filters, table, summary, pagination and CSS;
// synthetic API/auth and inert surrounding navigation/unopened dialogs.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

assert(process.env.CI && process.env.PLATFORM_EVIDENCE_DIR, 'Remote CI only')
const root = process.cwd(), frontend = path.join(root, 'frontend'), out = process.env.PLATFORM_EVIDENCE_DIR
const captureSHA = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(captureSHA, process.env.PLATFORM_EXPECTED_HEAD)
const baseline = 'ccf997c73868083d96c0bec56e71c1a34d26c98a'
await fs.mkdir(out, { recursive: true })
const fixtureDir = path.join(frontend, '.usage-platform-fixture')
await fs.mkdir(fixtureDir, { recursive: true })
const sources = ['views/admin/UsageView.vue', 'components/admin/usage/UsageFilters.vue']
for (const source of sources) {
  const target = path.join(fixtureDir, `Baseline${path.basename(source)}`)
  const original = execFileSync('git', ['show', `${baseline}:frontend/src/${source}`], { encoding: 'utf8' })
  // Pin the baseline dependency before Vite's alias resolver runs. Only import
  // locations change; the rendered component/template remains the fixed source.
  const pinnedSource = original
    .replace('@/components/admin/usage/UsageFilters.vue', './BaselineUsageFilters.vue')
    .replace('./observerUsageContext', '@/components/admin/usage/observerUsageContext')
  await fs.writeFile(target, pinnedSource)
}
const catalog = JSON.parse(await fs.readFile(path.join(frontend, 'src/constants/platformCatalog.builtin.json'), 'utf8'))
const modules = {
  auth: `export const useAuthStore=()=>({user:{id:41,role:'admin'},isAdmin:true});`,
  app: `export const useAppStore=()=>({cachedPublicSettings:{},showError:msg=>{throw Error(msg)},showWarning:()=>{},showSuccess:()=>{}});`,
  router: `export default {push:()=>{},currentRoute:{value:{fullPath:'/admin/usage'}}};export const useRoute=()=>({query:{start_date:'2026-11-01',end_date:'2026-11-07'}});export const useRouter=()=>({push:()=>{}});`,
  blank: `export default {render:()=>null};`,
  layout: `import {h} from 'vue';export default {setup(_,{slots}){return()=>h('main',{class:'min-h-screen bg-gray-50 p-4 dark:bg-dark-950 sm:p-6'},[h('div',{class:'mb-4 text-xs text-gray-500'},'Offline synthetic fixture · Admin usage / 管理用量 · No production data'),slots.default?.()])}};`,
  api: `const platforms=${JSON.stringify(catalog.platforms)};
    const rows=platforms.flatMap((p,i)=>Array.from({length:12},(_,j)=>({id:i*12+j+1,platform:p.id,user_id:42,api_key_id:8,account_id:9,group_id:j%2?7:8,request_id:'synthetic-'+i+'-'+j,model:'fixture-model',created_at:'2026-11-03T10:00:00Z',input_tokens:100,output_tokens:50,cache_read_tokens:20,cache_creation_tokens:0,total_cost:0.01,actual_cost:0.005,rate_multiplier:0.5,request_type:'sync',duration_ms:1200,user:{email:'demo@example.invalid'},api_key:{name:'Synthetic key'},account:{name:'Synthetic account'},group:{name:j%2?'Synthetic group':'Other group',platform:'composite'}})));
    rows.push({...rows[0],id:999,platform:null,request_id:'synthetic-legacy'});
    const scoped=p=>rows.filter(r=>(!p.platform||(p.platform==='unknown'?!r.platform:r.platform===p.platform))&&(!p.group_id||r.group_id===p.group_id));
    const record=(kind,p)=>{(window.usageRequests||=[]).push({kind,...p});return scoped(p)};
    export const adminUsageAPI={list:async(p={})=>{const r=record('list',p);const page=p.page||1,size=p.page_size||20;return{items:r.slice((page-1)*size,page*size),total:r.length,pages:Math.ceil(r.length/size)}},getStats:async(p={})=>{const r=record('stats',p);return{total_requests:r.length,total_input_tokens:r.length*100,total_output_tokens:r.length*50,total_cache_tokens:r.length*20,total_tokens:r.length*170,total_cost:r.length*0.01,total_actual_cost:r.length*0.005,average_duration_ms:1200}},searchUsers:async()=>[],searchApiKeys:async()=>[],listCleanupTasks:async()=>({items:[]})};
    export const adminAPI={usage:adminUsageAPI,groups:{list:async()=>({items:[{id:7,name:'Synthetic group'},{id:8,name:'Other group'}]})},dashboard:{getModelStats:async p=>{record('models',p);return{models:[]}},getSnapshotV2:async p=>{record('snapshot',p);return{trend:[],groups:[]}}}};`,
}
for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixtureDir, `${name}.js`), source)
await fs.writeFile(path.join(fixtureDir, 'index.html'), `<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module">
import {createApp,h} from 'vue';import {createPinia} from 'pinia';import {createI18n} from 'vue-i18n';
import Candidate from '/src/views/admin/UsageView.vue';import Baseline from './BaselineUsageView.vue';
import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
const p=new URLSearchParams(location.search);createApp({render:()=>h(p.has('baseline')?Baseline:Candidate)}).use(createPinia()).use(createI18n({legacy:false,locale:p.get('locale')||'en',messages:{zh,en}})).mount('#app');</script></html>`)
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue')
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const alias = (find, name) => ({ find, replacement: path.join(fixtureDir, `${name}.js`) })
const server = await createServer({ root: frontend, configFile: false, plugins: [(vueModule.default || vueModule)()], resolve: {alias:[
  alias(/^@\/api\/admin$/, 'api'),alias(/^@\/api\/admin\/usage$/, 'api'),alias('@/stores/auth','auth'),alias('@/stores/app','app'),alias(/^vue-router$/,'router'),alias('@/components/layout/AppLayout.vue','layout'),
  ...['admin/usage/UsageConnections','admin/usage/LatencyObservabilityPanel','usage/TrafficPanel','admin/usage/UserTokenRanking','admin/usage/UsageCleanupDialog','admin/user/UserBalanceHistoryModal'].map(name=>alias(`@/components/${name}.vue`,'blank')),
  alias('@/views/admin/ops/components/OpsErrorLogTable.vue','blank'),alias('@/views/admin/ops/components/OpsErrorDetailModal.vue','blank'),
  alias(/^@\/router$/, 'router'),
  {find:'@',replacement:path.join(frontend,'src')},
]},css:{postcss:{plugins:[tailwind({...theme,content:[path.join(frontend,'src/**/*.{vue,ts,js}'),path.join(fixtureDir,'*.{vue,js,html}')]}),autoprefixer()]}},
  define:{__INTLIFY_JIT_COMPILATION__:true},server:{host:'127.0.0.1',port:4186,strictPort:true},optimizeDeps:{entries:[path.join(fixtureDir,'index.html')],include:['vue','vue-i18n','pinia']}})
await server.listen()
const browser = await chromium.launch({headless:true})
const shots=[],errors=[],checks=[]
async function capture(page,name,sourceSHA){
  const filename=`${name}.png`
  await page.screenshot({path:path.join(out,filename),fullPage:true,animations:'disabled'})
  shots.push({filename,capture_sha:captureSHA,source_sha:sourceSHA,viewport:page.viewportSize(),sha256:createHash('sha256').update(await fs.readFile(path.join(out,filename))).digest('hex')})
}
try {
  for(const phase of process.env.PLATFORM_CAPTURE_PHASE==='baseline'?['before']:['before','after']){
    for(const locale of ['en','zh'])for(const [device,viewport]of Object.entries({desktop:{width:1440,height:1050},mobile:{width:390,height:844}})){
      const page=await browser.newPage({viewport})
      await page.route('**/*',route=>new URL(route.request().url()).hostname==='127.0.0.1'?route.continue():route.abort())
      page.on('pageerror',e=>errors.push(e.message))
      await page.goto(`http://127.0.0.1:4186/.usage-platform-fixture/index.html?locale=${locale}${phase==='before'?'&baseline=1':''}`,{waitUntil:'networkidle'})
      await page.locator('[data-testid="usage-detail-tab"]').first().waitFor()
      await page.evaluate(()=>document.fonts.ready)
      assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'page must fit viewport')
      const filter=page.locator('[data-testid="usage-platform-filter"]')
      assert.equal(await filter.count(),phase==='before'?0:1)
      if(phase==='after'){
        assert.equal(await filter.locator('.select-trigger').innerText(),locale==='en'?'All platforms':'全部平台')
        await filter.locator('.select-trigger').click()
        for(const p of catalog.platforms)assert.equal(await page.getByRole('option',{name:p.display_name,exact:true}).count(),1)
        await page.getByRole('option',{name:'OpenAI',exact:true}).click()
        await page.waitForFunction(()=>window.usageRequests.some(r=>r.kind==='stats'&&r.platform==='openai'))
        const last=await page.evaluate(()=>window.usageRequests.filter(r=>r.kind==='list').at(-1))
        assert.equal(last.platform,'openai');assert.equal(last.page,1);assert.equal(last.exact_total,true)
        const badge=filter.locator('[data-testid="new-feature-badge"]')
        assert.equal(await badge.count(),1)
        await capture(page,`${phase}-${locale}-${device}-selected`,captureSHA)
        await badge.click()
        await page.reload({waitUntil:'networkidle'})
        assert.equal(await filter.locator('[data-testid="new-feature-badge"]').count(),0)
        checks.push({locale,device,all_platforms:true,selected_query:last,badge_persisted:true,no_horizontal_overflow:true})
      }
      await capture(page,`${phase}-${locale}-${device}`,phase==='before'?baseline:captureSHA)
      await page.close()
    }
  }
  assert.deepEqual(errors,[])
}finally{
  await fs.writeFile(path.join(out,'manifest.json'),JSON.stringify({capture_sha:captureSHA,baseline_sha:baseline,synthetic:true,components:'UsageView / UsageFilters / Select / UsageTable / DataTable / UsageStatsCards / Pagination with project CSS',shots,checks,errors},null,2))
  await browser.close();await server.close()
}
