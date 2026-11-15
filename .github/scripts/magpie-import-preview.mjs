import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, resolve, dirname } from 'node:path'
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'
const repo=process.cwd(), root=join(repo,'frontend'), out=resolve(process.env.EVIDENCE_DIR)
const require=createRequire(join(root,'package.json'))
const {createServer}=await import(pathToFileURL(join(dirname(require.resolve('vite/package.json')),'dist/node/index.js')))
const vue=(await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')))).default
const {chromium}=await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const baseline='713cb0f3efe3f838d159cd57998128c2cdaa84b1', target=join(root,'src/views/user/KeysView.vue')
const beforeSource=execFileSync('git',['show',baseline+':frontend/src/views/user/KeysView.vue'],{encoding:'utf8'})
const source=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()
let before=true, server, browser
const entry=join(root,'.magpie-preview.js'), origin='http://127.0.0.1:4311'
mkdirSync(out,{recursive:true})
try {
 writeFileSync(entry,`
import {h} from 'vue';import {mount} from '@vue/test-utils';import {createPinia,setActivePinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';import View from '/src/views/user/KeysView.vue';import '/src/style.css';
const locale=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(locale);i18n.global.locale.value=locale;
const pinia=createPinia();setActivePinia(pinia);useAuthStore().user={id:42,role:'user',username:'preview'};
const group={id:7,name:'Example OpenAI',platform:'openai',status:'active',rate_multiplier:1,allow_messages_dispatch:false};
const items=[1,2].map(id=>({id,user_id:42,name:'Example key '+id,key:'synthetic-key-'+id,group_id:7,group_ids:[7],group,status:'active',ip_whitelist:[],ip_blacklist:[],quota:0,quota_used:0,expires_at:null,last_used_at:null,last_used_ip:null,created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z',concurrency_limit:0,rate_limit_5h:0,rate_limit_1d:0,rate_limit_7d:0,usage_5h:0,usage_1d:0,usage_7d:0}));
apiClient.defaults.adapter=async config=>{let data={};switch(config.url){case '/keys':data={items,total:2,page:1,page_size:20,pages:1};break;case '/groups/available':data=[group];break;case '/groups/rates':data={};break;case '/settings/public':data={api_base_url:'https://api.example.test',site_name:'Example gateway',show_ccs_import_button:true};break;case '/usage/dashboard/api-keys-usage':data={stats:{}};break;case '/keys/concurrency':data={items:[{id:1,current_concurrency:0,current_waiting:0},{id:2,current_concurrency:0,current_waiting:0}],queue_policy:{max_waiting:0,timeout_seconds:0}};break;}return {config,status:200,statusText:'OK',headers:{},data:{code:0,data}}};
window.open=url=>{window.importedURL=String(url);return null};
window.preview=mount(View,{attachTo:'#app',global:{plugins:[pinia,i18n],stubs:{AppLayout:{setup:(_props,{slots})=>()=>h('main',{class:'p-6'},slots.default?.())}}}});
`)
 process.chdir(root)
 server=await createServer({root,configFile:false,resolve:{alias:{'@':join(root,'src')}},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n','axios','@vue/test-utils','vue-router']},plugins:[{name:'preview',enforce:'pre',load(id){if(before&&id===target)return beforeSource},configureServer(vite){vite.middlewares.use((req,res,next)=>{if(req.url?.split('?')[0]!=='/preview')return next();res.setHeader('Content-Type','text/html');res.end('<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/.magpie-preview.js"></script></html>')})}},vue()],server:{host:'127.0.0.1',port:4311,strictPort:true}})
 await server.listen();browser=await chromium.launch({headless:true})
 const captures=[]
 for(const lang of ['zh','en'])for(const phase of ['before','after']){
  before=phase==='before';server.moduleGraph.invalidateAll()
  const context=await browser.newContext({viewport:{width:1920,height:1000}}),page=await context.newPage(),errors=[]
  page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)});page.on('console',m=>{if(m.type()==='warning'||m.type()==='error')console.error('preview console:',m.text())})
  await page.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort())
  await page.goto(origin+'/preview?lang='+lang,{waitUntil:'networkidle'})
  try { await page.getByText('Example key 2',{exact:true}).waitFor() } catch(error) {
   await page.screenshot({path:join(out,'failure-'+phase+'-'+lang+'.png'),fullPage:true})
   console.error('fixture visible text:',await page.locator('body').innerText());throw error
  }
  const buttons=page.getByTestId('magpie-import-button')
  if(before){if(await buttons.count())throw Error('baseline has Magpie button')}
  else{
   if(await buttons.count()!==2)throw Error('expected one import per key')
   await buttons.nth(1).click()
   await page.waitForFunction(()=>!!window.importedURL)
   const valid=await page.evaluate(()=>{const u=new URL(window.importedURL);return u.protocol==='magpie:'&&u.searchParams.get('key')==='synthetic-key-2'&&u.searchParams.get('responses')==='https://api.example.test/v1'})
   if(!valid)throw Error('selected row or endpoint is incorrect')
  }
  await page.screenshot({path:join(out,phase+'-'+lang+'.png'),fullPage:true})
  if(errors.length)throw Error(errors.join('\n'))
  captures.push({phase,lang,errors});await context.close()
 }
 writeFileSync(join(out,'evidence.json'),JSON.stringify({source,baseline,captures,boundary:'Actual KeysView and project CSS; fictional API adapter; native app launch intercepted; no production validation'},null,2))
}finally{await browser?.close();await server?.close();rmSync(entry,{force:true})}
