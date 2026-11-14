// Remote-only actual RiskControlView/selector/CSS; all APIs are synthetic.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { pathToFileURL } from 'node:url'
const repo=process.cwd(),root=join(repo,'frontend'),output=process.env.EVIDENCE_DIR
if(!output || !process.env.GITHUB_ACTIONS)throw Error('GitHub Actions only')
const req=createRequire(join(root,'package.json'))
const {createServer}=await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')),'dist/node/index.js')).href)
const vue=(await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const {chromium}=await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git=(...args)=>execFileSync('git',args,{cwd:repo,encoding:'utf8'}).trimEnd()
const head=git('rev-parse','HEAD'),base='4189ed821d6e4308b2f5fc3791b73d2eb53cda8a',hash=x=>createHash('sha256').update(x).digest('hex')
const sourceFiles=['frontend/src/views/admin/RiskControlView.vue','frontend/src/i18n/locales/en/admin/channels.ts','frontend/src/i18n/locales/zh/admin/channels.ts','frontend/src/constants/newFeatureNotices.ts','frontend/src/components/common/BaseDialog.vue','frontend/src/components/admin/operations/QualityAccountSelector.vue','frontend/src/style.css','frontend/tailwind.config.js','frontend/postcss.config.js']
const gitBytes=(ref,file)=>execFileSync('git',['show',`${ref}:${file}`],{cwd:repo})
const sources=Object.fromEntries(sourceFiles.map(file=>[file,{before:hash(gitBytes(base,file)),after:hash(gitBytes(head,file))}]))
for(const file of sourceFiles)assert.equal(hash(readFileSync(join(repo,file))),sources[file].after,'candidate worktree must match committed bytes')
sources['frontend/src/components/admin/moderation/ModerationUpstreamAccountSelector.vue']={after:hash(gitBytes(head,'frontend/src/components/admin/moderation/ModerationUpstreamAccountSelector.vue'))}
assert.equal(hash(readFileSync(join(root,'src/components/admin/moderation/ModerationUpstreamAccountSelector.vue'))),sources['frontend/src/components/admin/moderation/ModerationUpstreamAccountSelector.vue'].after)
const entryName=`.moderation-preview-${process.pid}.js`,entry=join(root,entryName),cacheDir=mkdtempSync(join(tmpdir(),'moderation-preview-'))
const captures=[],assertions=[],errors=[],reachability=[]
const baselineTree=join(cacheDir,'baseline')
assert.equal(git('rev-parse',`${base}:frontend/pnpm-lock.yaml`),git('rev-parse',`${head}:frontend/pnpm-lock.yaml`),'baseline and candidate use identical dependency lock')
git('worktree','add','--detach',baselineTree,base)
symlinkSync(join(root,'node_modules'),join(baselineTree,'frontend/node_modules'),'dir')
const frontendTrees={before:git('rev-parse',`${base}:frontend`),after:git('rev-parse',`${head}:frontend`)}
let before=true,server,browser,page
mkdirSync(output,{recursive:true})
try{
writeFileSync(entry,`
import {createApp,h} from 'vue';import {createPinia} from 'pinia';import {createRouter,createMemoryHistory} from 'vue-router';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';
import {useAuthStore} from '/src/stores/auth.ts';import Badge from '/src/components/common/NewFeatureBadge.vue';
import View from '/src/views/admin/RiskControlView.vue';import '/src/style.css';
const locale=new URL(location.href).searchParams.get('locale')||'en';await loadLocaleMessages(locale);i18n.global.locale.value=locale;
const defaults={enabled:true,mode:'pre_block',base_url:'https://audit.example',model:'fixture-moderation',api_key_statuses:[],api_key_masks:[],api_key_count:0,timeout_ms:3000,sample_rate:100,all_groups:true,group_ids:[],worker_count:4,queue_size:32768,block_status:403,block_message:'Synthetic fixture',thresholds:{},blocked_keywords:[],model_filter:{type:'all',models:[]},upstream_account_scope:'selected',upstream_account_ids:[121,999]};
let cfg=JSON.parse(localStorage.getItem('fixture-config')||'null')||defaults;
const catalogue=Array.from({length:225},(_,i)=>({id:i+1,name:'Demo account '+(i+1),platform:'openai',moderation_scope_eligible:true,type:i%2?'oauth':'apikey',group_ids:[i%2?4301:4302]}));
window.__writes=[];window.__reads=[];window.__held=[];window.__saved=()=>structuredClone(cfg);
apiClient.defaults.adapter=async config=>{let data;const u=config.url;window.__reads.push({url:u,params:config.params});
if(u==='/admin/risk-control/config'){
 if(config.method==='put'){const body=JSON.parse(config.data);window.__writes.push(body);cfg={...cfg,...body};localStorage.setItem('fixture-config',JSON.stringify(cfg));}
 data=structuredClone(cfg);
}else if(u==='/admin/accounts'){
 const p=config.params;let rows=catalogue.filter(a=>(!p.search||a.name.toLowerCase().includes(p.search.toLowerCase())||String(a.id)===p.search.replace(/^#/,''))&&(!p.type||a.type===p.type)&&(!p.group||a.group_ids.includes(Number(p.group))));
 data={items:rows.slice((p.page-1)*p.page_size,p.page*p.page_size),total:rows.length,page:p.page,pages:Math.ceil(rows.length/p.page_size),page_size:p.page_size};
 if(window.__holdNext){window.__holdNext=false;await new Promise(resolve=>window.__held.push(resolve));}
}else if(u==='/admin/groups/all')data=[{id:4301,name:'Demo OpenAI A',platform:'openai'},{id:4302,name:'Demo OpenAI B',platform:'openai'}];
else if(u==='/admin/proxies/all')data=[];
else if(u==='/admin/risk-control/status')data={enabled:true,risk_control_enabled:true,mode:'pre_block',worker_count:4,api_key_statuses:[],pre_block_api_key_loads:[]};
else if(u==='/admin/risk-control/logs')data={items:[],total:0,page:1,page_size:20};
else throw Error('Unexpected offline API '+u);
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data}};};
const pinia=createPinia();const auth=useAuthStore(pinia);auth.user={id:7,role:'admin',username:'Fixture admin'};
const storage='sub2api:new-feature-notices:v1:7';if(!localStorage.getItem(storage))localStorage.setItem(storage,JSON.stringify(['fixture-feature-a']));
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:View}]});
const app=createApp({render:()=>h('div',[h(View),h('div',{'data-testid':'fixture-badges'},[h(Badge,{noticeId:'fixture-feature-a',userId:7}),h(Badge,{noticeId:'fixture-feature-c',userId:7})])])});
app.use(pinia);app.use(i18n);app.use(router);await router.isReady();app.mount('#app');window.__t=key=>i18n.global.t(key);
`,{flag:'wx'})
const startServer=async(activeRoot)=>{
process.chdir(activeRoot)
server=await createServer({root:activeRoot,configFile:false,cacheDir,resolve:{alias:{'@':join(activeRoot,'src')}},plugins:[{
 name:'fixture-layout-and-pinned-baseline',enforce:'pre',load(id){
 if(id===join(activeRoot,'src/components/layout/AppLayout.vue'))return '<template><main class="p-4"><slot /></main></template>'
 },configureServer(vite){vite.middlewares.use((req,res,next)=>{if(req.url==='/'||req.url?.startsWith('/?')){res.setHeader('Content-Type','text/html');res.end('<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module" src="/'+entryName+'"></script></body></html>')}else if(req.url==='/favicon.ico'){res.statusCode=204;res.end()}else next()})}},vue()],server:{host:'127.0.0.1',port:4299,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-router','vue-i18n','axios']}})
await server.listen()
}
browser=await chromium.launch({headless:true})
const settle=async()=>{
 await page.waitForFunction(()=>{const d=document.querySelector('[role="dialog"]'),p=d?.querySelector('.modal-content');return d&&p&&getComputedStyle(d).opacity==='1'&&getComputedStyle(p).opacity==='1'&&!d.className.includes('modal-enter')&&d.getAnimations({subtree:true}).every(a=>a.playState==='finished')})
 await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
}
const openScope=async()=>{
 await page.getByRole('button',{name:await page.evaluate(()=>window.__t('admin.riskControl.openSettings')),exact:true}).click()
 await settle()
 const scope=page.getByRole('button',{name:await page.evaluate(()=>window.__t('admin.riskControl.tabs.scope')),exact:true})
 await scope.click();await settle()
 await page.waitForFunction(label=>Array.from(document.querySelectorAll('[role="dialog"] button')).some(el=>el.textContent.trim()===label&&el.classList.contains('bg-primary-50')),await scope.textContent().then(x=>x.trim()))
 const css=await page.locator('.modal-overlay').evaluate(el=>({position:getComputedStyle(el).position,display:getComputedStyle(el).display}))
 assert.deepEqual(css,{position:'fixed',display:'flex'})
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true)
}
const reach=async(locator,label)=>{
 for(let i=0;i<25;i++){
  const bounds=await locator.evaluate(el=>{const r=el.getBoundingClientRect(),p=el.closest('.modal-body')?.getBoundingClientRect();const x=r.x+r.width/2,y=r.y+r.height/2,hit=document.elementFromPoint(x,y);return {x,y,top:r.top,bottom:r.bottom,inside:r.left>=0&&r.right<=innerWidth&&r.top>=(p?.top??0)&&r.bottom<=(p?.bottom??innerHeight),hit:hit===el||el.contains(hit),parentTop:p?.top??0}})
  if(bounds.inside&&bounds.hit){reachability.push({label,...bounds});return}
  const box=await page.locator('.modal-body').boundingBox();await page.mouse.move(box.x+box.width/2,box.y+box.height/2);await page.mouse.wheel(0,bounds.top<bounds.parentTop?-240:240);await page.waitForTimeout(60)
 }
 throw Error('Control unreachable: '+label)
}
for(const phase of ['before','after']){
 before=phase==='before'
 const activeRoot=before?join(baselineTree,'frontend'):root
 if(before)writeFileSync(join(activeRoot,entryName),readFileSync(entry))
 await startServer(activeRoot)
 for(const locale of ['en','zh'])for(const [device,width,height] of [['desktop',1280,1000],['mobile',390,844]]){
  page=await browser.newPage({viewport:{width,height}});page.on('pageerror',e=>errors.push(e.message))
  await page.route('**/*',route=>{const u=new URL(route.request().url());if(u.origin==='http://127.0.0.1:4299'||u.protocol==='data:')return route.continue();errors.push('external request '+u.origin);return route.abort()})
  await page.goto('http://127.0.0.1:4299/?locale='+locale,{waitUntil:'networkidle'});await openScope()
  if(!before){await page.locator('[data-account-id="50"]').waitFor();await reach(page.getByTestId('quality-select-page'),'select page '+device+' '+locale)}
  const file=phase+'-'+device+'-'+locale+'.png';await page.screenshot({path:join(output,file),fullPage:false});captures.push({file,sha256:hash(readFileSync(join(output,file))),viewport:{width,height},locale,source:before?base:head,settled:true,activeTab:'scope'})
  if(!before){
   const picker=page.getByTestId('moderation-upstream-selector'),search=page.locator('#moderation-upstream-search')
   assert.equal(await picker.locator('[data-account-id]').count(),50)
   await reach(picker.getByTestId('quality-select-page'),'select page '+device+' '+locale);await picker.getByTestId('quality-select-page').click()
   await reach(picker.getByRole('button',{name:await page.evaluate(()=>window.__t('qualityOps.nextAccountPage'))}),'next page')
   await picker.getByRole('button',{name:await page.evaluate(()=>window.__t('qualityOps.nextAccountPage'))}).click();await picker.locator('[data-account-id="51"]').waitFor()
   await reach(picker.locator('[data-account-id="51"] input'),'account row '+device+' '+locale);await picker.locator('[data-account-id="51"] input').check()
   await reach(search,'search');await search.fill('#125');await picker.locator('[data-account-id="125"]').waitFor();assert.equal(await picker.locator('[data-account-id]').count(),1)
   await reach(picker.locator('[data-account-id="125"] input'),'searched account '+device+' '+locale);await picker.locator('[data-account-id="125"] input').check()
   await page.evaluate(()=>window.__holdNext=true);await search.fill('Demo account 1');await page.waitForFunction(()=>window.__held.length===1)
   await search.fill('#124');await picker.locator('[data-account-id="124"]').waitFor();await page.evaluate(()=>window.__held.shift()());await page.waitForTimeout(100);assert.equal(await picker.locator('[data-account-id]').count(),1)
   await search.fill('Demo account');await reach(picker.getByTestId('quality-account-type'),'type filter '+device+' '+locale);await picker.getByTestId('quality-account-type').selectOption('oauth');await reach(picker.getByTestId('quality-account-group'),'group filter '+device+' '+locale);await picker.getByTestId('quality-account-group').selectOption('4301');await page.waitForFunction(()=>document.querySelectorAll('[data-account-id]').length===50)
   await reach(picker.getByTestId('quality-select-all'),'batch '+device+' '+locale);await picker.getByTestId('quality-select-all').click();await page.waitForFunction(()=>document.querySelector('[data-testid="quality-select-all"]').disabled===false)
   const save=page.getByRole('button',{name:await page.evaluate(()=>window.__t('admin.riskControl.saveConfig')),exact:true});await reach(save,'save '+device+' '+locale);await save.click();await page.locator('[role="dialog"]').waitFor({state:'detached'})
   const written=await page.evaluate(()=>window.__writes.at(-1));assert(written.upstream_account_ids.includes(999));assert(written.upstream_account_ids.includes(121));assert(written.upstream_account_ids.includes(125));assert(written.upstream_account_ids.includes(124));assert.equal(written.upstream_account_scope,'selected');const expected=[...new Set([999,121,...Array.from({length:51},(_,i)=>i+1),125,...Array.from({length:112},(_,i)=>(i+1)*2)])].sort((a,b)=>a-b);assert.deepEqual([...written.upstream_account_ids].sort((a,b)=>a-b),expected)
   await page.reload({waitUntil:'networkidle'});await openScope();await picker.locator('[data-account-id="50"]').waitFor();assert.equal(await picker.locator('[data-account-id="50"] input').isChecked(),true);assert.deepEqual((await page.evaluate(()=>window.__saved().upstream_account_ids)).sort((a,b)=>a-b),expected)
   await reach(picker.getByTestId('quality-clear-selection'),'clear '+device+' '+locale);await picker.getByTestId('quality-clear-selection').click();await page.getByRole('button',{name:await page.evaluate(()=>window.__t('common.cancel')),exact:true}).click();await page.locator('[role="dialog"]').waitFor({state:'detached'});await openScope();await picker.locator('[data-account-id="50"]').waitFor();assert.equal(await picker.locator('[data-account-id="50"] input').isChecked(),true)
   const badges=page.locator('[role="dialog"] [data-testid="new-feature-badge"]');assert.equal(await badges.count(),2);await reach(badges.last(),'notice '+device+' '+locale);await badges.last().click();assert.equal(await badges.count(),0)
   await page.reload({waitUntil:'networkidle'});await openScope();assert.equal(await page.locator('[role="dialog"] [data-testid="new-feature-badge"]').count(),0);assert.equal(await page.locator('[data-testid="fixture-badges"] [data-testid="new-feature-badge"]').count(),1)
   assertions.push(device+' '+locale+': Real parent/selector: name and #ID, type/group filters, pages, hidden selected IDs, batch union, late response fence, save/reload, cancel/reopen; A dismissed/B visible/shared dismissal/reload/C visible. Synthetic API transport; server persistence is a separate PostgreSQL acceptance job, never inferred from this API adapter.')
  }
  await page.close();page=undefined
 }
 await server.close();server=undefined
}
assert.deepEqual(errors,[])
}catch(error){if(page)await page.screenshot({path:join(output,'failure.png'),fullPage:true}).catch(()=>{});errors.push(error.stack);throw error}
finally{writeFileSync(join(output,'manifest.json'),JSON.stringify({head,base,frontendTrees,sources,sourceHashNormalization:'exact bytes',captures,reachability,assertions,errors,boundary:'Actual RiskControlView, shared QualityAccountSelector, BaseDialog, project CSS/locales; layout shell and synthetic offline APIs only; separate full baseline/candidate frontend trees with identical dependency lock, transitions settled and active scope tab asserted.'},null,2)+'\n');await browser?.close();await server?.close();rmSync(entry,{force:true});git('worktree','remove','--force',baselineTree);rmSync(cacheDir,{recursive:true,force:true})}
