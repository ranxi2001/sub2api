import {readFileSync, writeFileSync, mkdtempSync, mkdirSync, symlinkSync, existsSync, unlinkSync, rmSync, realpathSync} from 'node:fs';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {createRequire} from 'node:module';
import {pathToFileURL} from 'node:url';
import {execFileSync} from 'node:child_process';
const [repo,mode,output,before=''] = process.argv.slice(2);
if(!repo || !mode || !output) throw Error('Usage: node preview.mjs REPO sidebar|accounts OUTPUT [BASELINE_SHA]');
const root=join(repo,'frontend'), deps=process.env.PREVIEW_DEPS_ROOT || root;
const req=createRequire(join(deps,'package.json'));
const {createServer}=await import(pathToFileURL(join(deps,'node_modules/vite/dist/node/index.js')).href);
const vue=(await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default;
const {chromium}=await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE || req.resolve('playwright')).href);
const cacheDir=mkdtempSync(join(tmpdir(),'prmerge-vite-'));
const entryName='.prmerge-preview-'+process.pid+'.js', entry=join(root,entryName);
const origin='http://127.0.0.1:4298';
let server,context,browser,linked=false;
const errors=[];
mkdirSync(output,{recursive:true});
try {
 if(!existsSync(join(root,'node_modules'))){symlinkSync(join(deps,'node_modules'),join(root,'node_modules'),'dir');linked=true;}
 const common=`import {createApp,h,ref} from 'vue';import {createPinia} from 'pinia';import {createI18n} from 'vue-i18n';import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';`;
 const sidebar=common+`import Sidebar from '/src/components/settings/SmartOpsSidebarSettings.vue';import * as menus from '/src/constants/smartOpsMenu.ts';const hidden=ref(${before?'[]':'[...(menus.DEFAULT_SMART_OPS_HIDDEN_MENU_ITEMS||[])]'});createApp({render:()=>h('main',{class:'min-h-screen bg-gray-50 p-8 text-gray-900 dark:bg-dark-900 dark:text-gray-100'},[h('p',{class:'mb-5 text-sm text-gray-500'},'系统设置 → 功能开关 · 实际组件 / 本地虚构管理员 · ${before?'修改前':'修改后'}'),h(Sidebar,{modelValue:hidden.value,'onUpdate:modelValue':v=>hidden.value=v})])}).use(createPinia()).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');`;
 const accounts=`import {createApp,h} from 'vue';import {createPinia} from 'pinia';import {mount} from '@vue/test-utils';import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';import Accounts from '/src/views/admin/AccountsView.vue';import '/src/style.css';await loadLocaleMessages('zh');i18n.global.locale.value='zh';const pinia=createPinia();useAuthStore(pinia).user={id:99999,role:'admin',username:'预览管理员'};apiClient.defaults.adapter=async config=>{let data;switch(config.url){case '/admin/accounts':data={items:[],total:0,page:1,page_size:20,pages:0};break;case '/admin/accounts/management-capabilities':data={concurrency_upgrade_enabled:false};break;case '/admin/accounts/upstream-billing-probe/settings':data={enabled:false};break;case '/admin/proxies/all':case '/admin/groups/all':case '/admin/tls-fingerprint-profiles':data=[];break;case '/admin/ops/metric-thresholds':data={};break;default:data={};}return {config,status:200,statusText:'OK',headers:{},data:{code:0,data}}};document.getElementById('app').innerHTML='<p class="px-8 pt-5 text-sm text-gray-500">账户管理 → 自动刷新 · 实际页面组件 / 本地空账号列表 · ${before?'修改前':'修改后'}</p><div id="preview"></div>';window.preview=mount(Accounts,{attachTo:'#preview',global:{plugins:[pinia,i18n],stubs:{AppLayout:{template:'<main class="p-8"><slot /></main>'},CreateAccountModal:true,EditAccountModal:true,BulkEditAccountModal:true,SyncFromCrsModal:true,TempUnschedStatusModal:true,ImportDataModal:true,ReAuthAccountModal:true,AccountTestModal:true,AccountStatsModal:true,IQTestModal:true,ScheduledTestsPanel:true,ErrorPassthroughRulesModal:true,TLSFingerprintProfilesModal:true,NewAPIUpstreamConfigDialog:true,BulkExport2FADialog:true,TotpStepUpDialog:true}}});`;
 writeFileSync(entry,mode==='sidebar'?sidebar:accounts,{flag:'wx'});
 const git=(...args)=>execFileSync('git',args,{cwd:repo,encoding:'utf8'});
 process.chdir(root);
 server=await createServer({root,configFile:false,cacheDir,resolve:{alias:{'@':join(root,'src')}},plugins:[{name:'prmerge-preview',enforce:'pre',load(id){
  const clean=id.split('?')[0];
  if(mode==='sidebar' && (clean==='@/stores/auth' || clean===join(root,'src/stores/auth.ts')))return 'export const useAuthStore=()=>({user:{id:99999},isAdmin:true})';
  if(before && !id.includes('?') && [join(root,'src/views/admin/AccountsView.vue'),join(root,'src/i18n/locales/zh/admin/accounts.ts')].includes(clean))return git('show',before+':'+clean.slice(repo.length+1));
 },configureServer(s){s.middlewares.use((r,res,next)=>{if(r.url==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/'+entryName+'"></script></html>')}else if(r.url==='/favicon.ico'){res.statusCode=204;res.end()}else next()})}},vue()],server:{host:'127.0.0.1',port:4298,strictPort:true,fs:{allow:[root,deps]}},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n','vue-router','axios','@vue/test-utils','marked','dompurify']}});
 await server.listen();
 browser=process.env.CDP_ENDPOINT ? await chromium.connectOverCDP(process.env.CDP_ENDPOINT) : await chromium.launch({headless:true});
 context=await browser.newContext({viewport:{width:1280,height:1000},locale:'zh-CN'});
 const page=await context.newPage();
 page.on('pageerror',e=>{errors.push(e.message);console.error('PAGEERROR',e.message)});
 page.on('console',m=>{if(m.type()==='error'){errors.push(m.text());console.error(m.text())}});
 await page.route('**/*',route=>{const u=new URL(route.request().url());if(u.origin===origin||u.protocol==='data:')return route.continue();errors.push('External request blocked '+u.origin);return route.abort()});
 await page.goto(origin,{waitUntil:'domcontentloaded',timeout:90000});
 if(mode==='sidebar')await page.getByTestId('smart-ops-sidebar-settings').waitFor({timeout:90000});
 else {await page.getByTitle('自动刷新',{exact:true}).waitFor({timeout:90000});await page.getByTitle('自动刷新',{exact:true}).click();}
 await page.waitForTimeout(800);
 await page.screenshot({path:join(output,before?'before.png':'after.png'),fullPage:true});
 if(!before){
  if(mode==='sidebar'){await page.getByRole('button',{name:'全部显示',exact:true}).click();await page.screenshot({path:join(output,'show-all.png'),fullPage:true});}
  else {await page.getByTestId('auto-refresh-upstream-models-oauth').check();await page.screenshot({path:join(output,'oauth-only.png'),fullPage:true});}
  await page.evaluate(()=>document.documentElement.classList.add('dark'));
  await page.setViewportSize({width:430,height:950});
  await page.waitForTimeout(400);
  await page.screenshot({path:join(output,'mobile-dark.png'),fullPage:true});
 }
 writeFileSync(join(output,(before?'before-':'')+'manifest.json'),JSON.stringify({source:git('rev-parse','HEAD').trim(),baseline:before||null,mode,boundary:'Actual Vue component with local fictional data; no production or full navigation acceptance',errors},null,2));
 if(errors.length)throw Error(errors.join('\n'));
 console.log('CAPTURED '+output);
} finally {await context?.close();if(!process.env.CDP_ENDPOINT)await browser?.close();await server?.close();if(existsSync(entry))unlinkSync(entry);if(linked)unlinkSync(join(root,'node_modules'));rmSync(cacheDir,{recursive:true,force:true});}
