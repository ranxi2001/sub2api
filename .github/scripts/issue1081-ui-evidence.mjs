// Remote CI only. Full fixed base first, then candidate; real pages and CSS.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const baseline='70abfd1c51e7d3b13ada8aa852bc695c09f9104a'
const candidate=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()
assert(process.env.CI && process.env.SCREENSHOT_DIR && process.env.PLAYWRIGHT_ROOT)
assert.equal(process.env.CANDIDATE_SHA,candidate)
assert.equal(execFileSync('git',['status','--porcelain'],{encoding:'utf8'}),'')
const output=resolve(process.env.SCREENSHOT_DIR),root=resolve('frontend'),beforeTree=join(output,'baseline-source')
mkdirSync(output,{recursive:true});mkdirSync(beforeTree)
const hash=data=>createHash('sha256').update(data).digest('hex')
execFileSync('git',['archive','--format=tar',`--output=${join(beforeTree,'source.tar')}`,baseline])
execFileSync('tar',['-xf',join(beforeTree,'source.tar'),'-C',beforeTree])
const beforeRoot=join(beforeTree,'frontend')
for(const path of ['package.json','pnpm-lock.yaml'])assert(readFileSync(join(beforeRoot,path)).equals(readFileSync(join(root,path))))
symlinkSync(join(root,'node_modules'),join(beforeRoot,'node_modules'),'dir')
const testSource=readFileSync(join(root,'src/views/admin/__tests__/SettingsView.spec.ts'),'utf8')
const start=testSource.indexOf('const baseSettingsResponse = {'),end=testSource.indexOf('\n};',start)+3
assert(start>=0&&end>start)
const fixture=testSource.slice(start,end).replaceAll('sub3.nebula-spaces.com','fixture.example.invalid')
const entry=`
import {createApp} from 'vue';import {createPinia} from 'pinia';
import {createRouter,createWebHistory,RouterView} from 'vue-router';
import {i18n,initI18n} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';
import {useAuthStore} from '/src/stores/auth.ts';import {useAppStore} from '/src/stores/app.ts';
import Settings from '/src/views/admin/SettingsView.vue';import Keys from '/src/views/user/KeysView.vue';import '/src/style.css';
${fixture}
const params=new URLSearchParams(location.search),view=params.get('view')||'settings',mode=params.get('mode');
const storage='ci-issue1081:'+mode,stored=localStorage.getItem(storage);
const settings={...structuredClone(baseSettingsResponse),site_name:'Fixture Gateway',site_logo:'',subscription_enabled:false,lottery_enabled:false};
if(stored!==null)settings.api_key_multi_group_enabled=stored==='true';
const primary={id:1,name:'Primary fixture',platform:'openai',status:'active',subscription_type:'standard',rate_multiplier:1};
const extra={...primary,id:2,name:'Additional fixture',rate_multiplier:0.1};
const key={id:11,user_id:7,key:'sk-fixture-masked-only',name:'Fixture key',status:'active',group_id:1,group_ids:[1,2],group:primary,quota:0,quota_used:0,concurrency_limit:0,ip_whitelist:[],ip_blacklist:[],rate_limit_5h:0,rate_limit_1d:0,rate_limit_7d:0,created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z',expires_at:null};
const state={mode,view,requests:[],writes:[],settings,key};window.__issue1081=state;
apiClient.defaults.adapter=async request=>{
 const path=request.url.split('?')[0],method=request.method||'get';state.requests.push({path,method});let data={};
 if(method!=='get'&&!(method==='put'&&(path==='/admin/settings'||path==='/admin/settings/web-search-emulation'||path==='/keys/11'))&&!(method==='post'&&path==='/keys'))throw Error('Unexpected fixture mutation '+path);
 if(path==='/admin/settings'||path==='/settings/public'){
  if(method==='put'){const body=JSON.parse(request.data);Object.assign(settings,body);state.writes.push({path,body});localStorage.setItem(storage,String(settings.api_key_multi_group_enabled));}
  data=structuredClone(settings);
 }else if(path==='/groups/available')data=[primary,extra];
 else if(path==='/groups/rates')data={};
 else if(path==='/keys'&&method==='get')data={items:[key],total:1,page:1,page_size:20,pages:1};
 else if(path==='/keys/11'||(path==='/keys'&&method==='post')){const body=JSON.parse(request.data);state.writes.push({path,body});Object.assign(key,body);data=key;}
 else if(path==='/keys/concurrency')data={queue_policy:{max_waiting:0,timeout_seconds:0},items:[{id:11,current_concurrency:0,current_waiting:0}]};
 else if(path.includes('/dashboard'))data={stats:{}};
 else if(path==='/admin/system/check-updates')data={current_version:'fixture',latest_version:'fixture',has_update:false,build_type:'source'};
 else if(path.includes('/announcements')||path.includes('/subscriptions/active'))data=[];
 else if(path.includes('/groups'))data=[];
 else if(path.includes('/proxies'))data={items:[],total:0};
 else if(path.includes('/providers'))data={data:[]};
 else if(path.includes('/web-search'))data={enabled:false,providers:[]};
 else if(path.includes('/admin-api-key'))data={exists:false,masked_key:''};
 else if(path.includes('/serverless'))data={config:{enabled:false,pods:[],regions:[]},pods:[],stats:{}};
 else if(path.includes('/settings'))data={enabled:false,seconds:20,interval_minutes:60,debounce_minutes:1,window_minutes:60,rules:[]};
 return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
};
const pinia=createPinia(),router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:view==='keys'?Keys:Settings}]});
const app=createApp(RouterView);app.use(pinia);
useAuthStore(pinia).user={id:41,username:'fixture-admin',role:'admin',status:'active',email:'admin@example.invalid',balance:0,frozen_balance:0};
await initI18n();await useAppStore(pinia).fetchPublicSettings();app.use(router).use(i18n);await router.isReady();app.mount('#app');
`
const {chromium}=await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT,'index.mjs')).href)
const browser=await chromium.launch({headless:true}),manifest={baseline,candidate,capture_order:[],captures:[],interactions:[],sources:{},errors:[],boundary:'Actual SettingsView/KeysView and repository CSS/components. Synthetic Axios adapter and localStorage persistence; no real database, production, upstream or external network. Before base is captured completely before candidate.'}
const paths=['src/views/admin/SettingsView.vue','src/views/user/KeysView.vue','src/style.css','tailwind.config.js','src/constants/newFeatureNotices.ts']
try{
for(const mode of ['before','after']){
 const sourceRoot=mode==='before'?beforeRoot:root,revision=mode==='before'?baseline:candidate
 manifest.capture_order.push({mode,revision,started_at:new Date().toISOString()})
 const require=createRequire(join(sourceRoot,'package.json'))
 const {build,preview}=await import(pathToFileURL(join(root,'node_modules/vite/dist/node/index.js')).href)
 const vue=(await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
 const tailwind=require('tailwindcss'),autoprefixer=require('autoprefixer'),config=(await import(pathToFileURL(join(sourceRoot,'tailwind.config.js')).href)).default
 const suffix='.ci-issue1081',html=join(sourceRoot,suffix+'.html'),js=join(sourceRoot,suffix+'.js'),outDir=join(output,mode+'-build')
 let server
 try{
 writeFileSync(html,`<html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`,{flag:'wx'});writeFileSync(js,entry,{flag:'wx'})
 manifest.sources[mode]={revision,files:Object.fromEntries(paths.map(path=>[path,{blob:execFileSync('git',['rev-parse',revision+':frontend/'+path],{encoding:'utf8'}).trim(),sha256:hash(readFileSync(join(sourceRoot,path)))}]))}
 await build({root:sourceRoot,base:'./',configFile:false,cacheDir:join(output,mode+'-cache'),resolve:{alias:{'@':join(sourceRoot,'src')}},plugins:[vue()],css:{postcss:{plugins:[tailwind({...config,content:[html,js,join(sourceRoot,'src/**/*.{vue,js,ts}')]}),autoprefixer()]}},build:{target:'esnext',outDir,emptyOutDir:true,minify:false,rollupOptions:{input:html}}})
 const css=readdirSync(join(outDir,'assets')).filter(f=>f.endsWith('.css'));assert(css.length)
 manifest.sources[mode].css=css.map(file=>({file,sha256:hash(readFileSync(join(outDir,'assets',file)))}))
 server=await preview({root:sourceRoot,configFile:false,build:{outDir},preview:{host:'127.0.0.1',port:4198,strictPort:true}})
 for(const [device,viewport] of [['desktop',{width:1280,height:1000}],['mobile',{width:390,height:844}]]){
  const context=await browser.newContext({viewport,locale:'zh-CN',timezoneId:'UTC',serviceWorkers:'block'})
  await context.addInitScript(()=>{localStorage.setItem('sub2api_locale','zh');localStorage.setItem('admin_guide_41_admin_v4_interactive','true')})
  await context.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:4198'?route.continue():route.abort())
  const page=await context.newPage();page.on('pageerror',e=>manifest.errors.push(e.message))
  const go=async view=>{await page.goto(`http://127.0.0.1:4198/${suffix}.html?view=${view}&mode=${mode}&tab=features`);await page.waitForFunction(()=>window.__issue1081?.requests.some(r=>r.path==='/settings/public'))}
  const capture=async(name,locator)=>{await locator.scrollIntoViewIfNeeded();const bounds=await locator.boundingBox();assert(bounds&&bounds.width>0);assert(bounds.x>=-1&&bounds.x+bounds.width<=viewport.width+1,'component exceeds viewport');await page.evaluate(()=>document.fonts.ready);const file=`${mode}-${device}-${name}.png`;await locator.screenshot({path:join(output,file),animations:'disabled'});manifest.captures.push({mode,device,viewport,name,file,sha256:hash(readFileSync(join(output,file))),observed_at:new Date().toISOString(),layout:await locator.evaluate(el=>({width:el.getBoundingClientRect().width,scrollWidth:document.documentElement.scrollWidth,viewport:innerWidth,font:getComputedStyle(el).fontSize}))})}
  await go('settings');await page.locator('[data-testid="protocol-feature-switches"]').waitFor()
  if(mode==='before'){
   assert.equal(await page.locator('#api-key-multi-group-enabled').count(),0)
   await capture('settings',page.locator('[data-testid="protocol-feature-switches"]'))
  }else{
   const card=page.locator('[data-testid="api-key-multi-group-settings"]'),toggle=page.locator('#api-key-multi-group-enabled')
   await toggle.waitFor({state:'visible'});assert.equal(await toggle.getAttribute('aria-checked'),'true');await capture('settings-enabled',card)
   await toggle.click();await page.locator('.settings-save-dock button[type="submit"]').click()
   await page.waitForFunction(()=>window.__issue1081.writes.some(w=>w.path==='/admin/settings'))
   assert.equal(await page.evaluate(()=>window.__issue1081.writes.find(w=>w.path==='/admin/settings').body.api_key_multi_group_enabled),false)
   await page.reload();await toggle.waitFor({state:'visible'});assert.equal(await toggle.getAttribute('aria-checked'),'false');await capture('settings-disabled',card)
   const badges=()=>page.locator('[data-testid="new-feature-badge"][data-notice="api-key-multi-group"]')
   assert.equal(await badges().count(),2)
   await badges().nth(device==='desktop'?0:1).click();assert.equal(await badges().count(),0)
   await page.reload();await toggle.waitFor({state:'visible'});assert.equal(await badges().count(),0)
   manifest.interactions.push({mode,device,save_false_and_reload:true,both_badge_entries_dismissed_and_persisted:true})
  }
  await go('keys');await page.locator('[data-tour="keys-create-btn"]').waitFor()
  await page.locator('[data-tour="keys-create-btn"]').click();await page.locator('#key-form').waitFor()
  assert.equal(await page.locator('[data-testid="key-additional-groups"]').count(),mode==='before'?1:0)
  await capture('key-create',page.locator('#key-form'))
  if(mode==='after'){
   await page.locator('[data-tour="key-form-name"]').fill('Fixture created')
   await page.locator('[data-tour="key-form-group"]').click()
   await page.getByRole('option').filter({hasText:'Primary fixture'}).click()
   await page.locator('[data-tour="key-form-submit"]').click()
   await page.waitForFunction(()=>window.__issue1081.writes.some(w=>w.path==='/keys'))
   const body=await page.evaluate(()=>window.__issue1081.writes.find(w=>w.path==='/keys').body)
   assert.equal(body.group_id,1);assert(!Object.hasOwn(body,'group_ids'))
   manifest.interactions.push({mode,device,disabled_create_omits_group_ids:true})
  }
  await page.reload();await page.locator('.key-group-control').waitFor()
  // Existing Key editing uses the actual action button, identified by its text.
  await page.getByRole('button',{name:'编辑',exact:true}).first().click();await page.locator('#key-form').waitFor()
  assert.equal(await page.locator('[data-testid="key-additional-groups"]').count(),mode==='before'?1:0)
  await capture('key-edit',page.locator('#key-form'))
  if(mode==='after'){
   await page.locator('[data-tour="key-form-submit"]').click()
   await page.waitForFunction(()=>window.__issue1081.writes.some(w=>w.path==='/keys/11'))
   const payload=await page.evaluate(()=>window.__issue1081.writes.find(w=>w.path==='/keys/11').body);assert(!Object.hasOwn(payload,'group_ids'))
   manifest.interactions.push({mode,device,disabled_edit_omits_group_ids:true,group_id:payload.group_id})
   await go('settings');await page.locator('#api-key-multi-group-enabled').click();await page.locator('.settings-save-dock button[type="submit"]').click();await page.waitForFunction(()=>window.__issue1081.writes.some(w=>w.path==='/admin/settings'))
   await go('keys');await page.getByRole('button',{name:'编辑',exact:true}).first().click();await page.locator('[data-testid="key-additional-groups"]').waitFor()
   assert(await page.locator('[data-testid="key-additional-groups"] input[value="2"]').isChecked());await capture('key-restored',page.locator('#key-form'))
   manifest.interactions.push({mode,device,reopen_restores_extra:true})
  }
  await context.close()
 }
 }finally{if(server)await new Promise(resolve=>server.httpServer.close(resolve));rmSync(html,{force:true});rmSync(js,{force:true})}
 manifest.capture_order.at(-1).completed_at=new Date().toISOString()
}
assert.deepEqual(manifest.errors,[])
writeFileSync(join(output,'manifest.json'),JSON.stringify(manifest,null,2)+'\n')
}catch(error){manifest.errors.push(String(error));writeFileSync(join(output,'failure-manifest.json'),JSON.stringify(manifest,null,2)+'\n');throw error}finally{await browser.close();rmSync(beforeTree,{recursive:true,force:true});for(const mode of ['before','after']){rmSync(join(output,mode+'-build'),{recursive:true,force:true});rmSync(join(output,mode+'-cache'),{recursive:true,force:true})}}
