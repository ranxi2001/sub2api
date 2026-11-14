// Actual Vue view + shared components; synthetic API adapter; GitHub Actions only.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { pathToFileURL } from 'node:url'
const repo=process.cwd(), root=join(repo,'frontend'), output=process.env.EVIDENCE_DIR
if(!output || !process.env.GITHUB_ACTIONS) throw Error('GitHub Actions only')
const req=createRequire(join(root,'package.json'))
const {createServer}=await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')),'dist/node/index.js')).href)
const vue=(await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const {chromium}=await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const head=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()
const hash=x=>createHash('sha256').update(x).digest('hex')
const entryName=`.moderation-preview-${process.pid}.js`, entry=join(root,entryName), cacheDir=mkdtempSync(join(tmpdir(),'moderation-preview-'))
const captures=[],errors=[]
let server,browser
mkdirSync(output,{recursive:true})
try{
writeFileSync(entry,`
import {createApp} from 'vue'; import {createPinia} from 'pinia';
import {createRouter,createMemoryHistory} from 'vue-router';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import View from '/src/views/admin/RiskControlView.vue'; import '/src/style.css';
const locale=new URL(location.href).searchParams.get('locale')||'en';
await loadLocaleMessages(locale);i18n.global.locale.value=locale;
const cfg={enabled:true,mode:'pre_block',base_url:'https://audit.example',model:'fixture-moderation',api_key_statuses:[],api_key_masks:[],api_key_count:0,timeout_ms:3000,sample_rate:100,all_groups:true,group_ids:[],worker_count:4,queue_size:32768,block_status:403,block_message:'Synthetic fixture',thresholds:{},blocked_keywords:[],model_filter:{type:'all',models:[]}};
apiClient.defaults.adapter=async config=>{let data; const u=config.url;
if(u==='/admin/risk-control/config')data=cfg;
else if(u==='/admin/groups/all')data=[{id:4301,name:'Demo OpenAI',platform:'openai'},{id:4302,name:'Demo Anthropic',platform:'anthropic'}];
else if(u==='/admin/proxies/all')data=[];
else if(u==='/admin/risk-control/status')data={enabled:true,risk_control_enabled:true,mode:'pre_block',worker_count:4,api_key_statuses:[],pre_block_api_key_loads:[]};
else if(u==='/admin/risk-control/logs')data={items:[],total:0,page:1,page_size:20};
else throw Error('Unexpected offline API '+u);
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data}};};
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:View}]});
const app=createApp(View);app.use(createPinia());app.use(i18n);app.use(router);await router.isReady();app.mount('#app');
window.__t=key=>i18n.global.t(key);
`,{flag:'wx'})
process.chdir(root)
server=await createServer({root,configFile:false,cacheDir,resolve:{alias:{'@':join(root,'src')}},plugins:[{
name:'fixture-layout',enforce:'pre',load(id){if(id===join(root,'src/components/layout/AppLayout.vue'))return '<template><main class="p-4"><slot /></main></template>'},
configureServer(vite){vite.middlewares.use((req,res,next)=>{if(req.url==='/'||req.url?.startsWith('/?')){res.setHeader('Content-Type','text/html');res.end('<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module" src="/'+entryName+'"></script></body></html>')}else if(req.url==='/favicon.ico'){res.statusCode=204;res.end()}else next()})}},vue()],server:{host:'127.0.0.1',port:4299,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-router','vue-i18n','axios']}})
await server.listen();browser=await chromium.launch({headless:true})
for(const locale of ['en','zh'])for(const [device,width,height] of [['desktop',1280,1000],['mobile',390,844]]){
 const page=await browser.newPage({viewport:{width,height}})
 page.on('pageerror',e=>errors.push(e.message))
 await page.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:4299'?route.continue():route.abort())
 await page.goto('http://127.0.0.1:4299/?locale='+locale,{waitUntil:'networkidle'})
 const label=await page.evaluate(()=>window.__t('admin.riskControl.openSettings'))
 await page.getByRole('button',{name:label,exact:true}).click()
 const scope=await page.evaluate(()=>window.__t('admin.riskControl.tabs.scope'))
 await page.getByRole('button',{name:scope,exact:true}).click()
 await page.locator('[role="dialog"]').waitFor()
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true)
 const file='before-'+device+'-'+locale+'.png';await page.screenshot({path:join(output,file),fullPage:true})
 captures.push({file,sha256:hash(readFileSync(join(output,file))),viewport:{width,height},locale})
 await page.close()
}
assert.deepEqual(errors,[])
}finally{
writeFileSync(join(output,'manifest.json'),JSON.stringify({head,captures,errors,boundary:'Actual RiskControlView, BaseDialog, shared components, project CSS and locales. Layout shell fixture; synthetic offline axios API. No real upstreams.'},null,2)+'\n')
await browser?.close();await server?.close();rmSync(entry,{force:true});rmSync(cacheDir,{recursive:true,force:true})
}
