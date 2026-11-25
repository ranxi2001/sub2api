// Actual admin-key settings and confirmation dialog; all API data is synthetic.
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import { parseArgs } from 'node:util';
import { pathToFileURL } from 'node:url';
import { writeFileSync, mkdirSync, symlinkSync, unlinkSync, existsSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
const { values }=parseArgs({options:{repo:{type:'string',default:'.'},deps:{type:'string'},output:{type:'string'},baseline:{type:'string',default:'HEAD^'}}});
const repo=resolve(values.repo);
const root=repo+'/frontend';
const deps=resolve(values.deps || root);
const require=createRequire(deps+'/package.json');
const { createServer }=await import(pathToFileURL(deps+'/node_modules/vite/dist/node/index.js').href);
const vue=(await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default;
const { chromium }=await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE || require.resolve('playwright')).href);
if(!values.output)throw Error('--output is required');
const output=resolve(values.output);mkdirSync(output,{recursive:true});
const entry=root+'/.issue908-preview.js';
const source=execFileSync('git',['show',values.baseline+':frontend/src/components/admin/settings/AdminAccessKeysSettings.vue'],{cwd:repo,encoding:'utf8'});
let server,browser;const errors=[];process.chdir(root);
const linked=!existsSync(root+'/node_modules');if(linked)symlinkSync(deps+'/node_modules',root+'/node_modules');
try {
 browser=await chromium.launch({...(process.env.SUB2API_BROWSER_EXECUTABLE?{executablePath:process.env.SUB2API_BROWSER_EXECUTABLE}:{}),headless:true});
 for(const phase of ['before','after']) {
 writeFileSync(entry,`
 import { createApp, h } from 'vue';import { createPinia } from 'pinia';import { createI18n } from 'vue-i18n';
 import zhCommon from '/src/i18n/locales/zh/common.ts';import zhKeys from '/src/i18n/locales/zh/adminAccessKeys.ts';
 import Component from '/src/components/admin/settings/AdminAccessKeysSettings.vue';
 import { apiClient } from '/src/api/client.ts';import { useAuthStore } from '/src/stores/auth.ts';import '/src/style.css';
 const pinia=createPinia();useAuthStore(pinia).user={id:99999,role:'admin'};
 const policy={rules:[],global_permissions:[]};const item={id:'preview',name:'自动导号工具（虚构）',description:'仅本地预览',enabled:true,allowed_ips:['192.0.2.0/24'],expires_at:null,policy,key_prefix:'preview-prefix',last_used_at:null,last_used_ip:'',revoked_at:null};
 apiClient.defaults.adapter=async config=>{
 let data;
 if(config.url==='/admin/groups/all')data=[];
 else if(config.url==='/admin/settings/admin-api-keys'&&config.method==='get') data={items:[item],permissions:[]};
 else if(config.method==='put') { Object.assign(item,JSON.parse(config.data));data=item; }
 else throw Error('Unexpected mock API '+config.method+' '+config.url);
 return {config,status:200,statusText:'OK',headers:{},data:{code:0,data}};};
 const i18n=createI18n({legacy:false,locale:'zh',messages:{zh:{...zhCommon,adminAccessKeys:zhKeys}}});
 createApp({render:()=>h('main',{class:'mx-auto max-w-5xl p-6'},[h('p',{class:'mb-4 text-sm text-gray-500'},'管理员访问密钥 · ${phase==='before'?'改动前':'改动后'} · 实际组件 / 本地 mock / 虚构数据'),h(Component)])}).use(pinia).use(i18n).mount('#app');
 `);
 server=await createServer({root,configFile:false,cacheDir:output+'/cache-'+phase,resolve:{alias:{'@':root+'/src'}},plugins:[{name:'issue908-preview',enforce:'pre',load(id){if(phase==='before'&&id===root+'/src/components/admin/settings/AdminAccessKeysSettings.vue')return source;
if(phase==='before'&&id===root+'/src/i18n/locales/zh/adminAccessKeys.ts')return execFileSync('git',['show',values.baseline+':frontend/src/i18n/locales/zh/adminAccessKeys.ts'],{cwd:repo,encoding:'utf8'});},configureServer(s){s.middlewares.use((req,res,next)=>{if(req.url==='/'){res.setHeader('Content-Type','text/html;charset=utf-8');res.end('<html><meta charset="utf-8"><div id="app"></div><script type="module" src="/.issue908-preview.js"></script></html>');}else next();});}},vue()],server:{host:'127.0.0.1',port:4318,strictPort:true,fs:{allow:[root,deps]}},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n','axios','vue-router']}});
 await server.listen();const context=await browser.newContext({viewport:{width:1200,height:1000},locale:'zh-CN'});const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:4318'?route.continue():route.abort());
 await page.goto('http://127.0.0.1:4318');await page.getByRole('button',{name:'编辑权限',exact:true}).click();
 await page.screenshot({path:output+'/'+phase+'.png',fullPage:true});
 if(phase==='after'){
 await page.locator('[data-test="full-access"]').check();await page.locator('[data-test="full-access-phrase"]').fill('开启全权访问');await page.locator('.modal-content').screenshot({path:output+'/confirmation.png',animations:'disabled'});
 await page.locator('[data-test="full-access-confirm"]').click();await page.locator('[data-test="save"]').click();await page.getByRole('status').filter({hasText:'密钥已保存'}).waitFor();await page.screenshot({path:output+'/saved.png',fullPage:true});
 }
 await context.close();await server.close();server=null;
 }
 writeFileSync(output+'/manifest.json',JSON.stringify({head:execFileSync('git',['rev-parse','HEAD'],{cwd:repo,encoding:'utf8'}).trim(),baseline:values.baseline,boundary:'Actual settings component and confirmation dialog; local mock API, fictional data, no production connection or full-page navigation acceptance',errors},null,2));
 if(errors.length)throw Error(JSON.stringify(errors));
 console.log(output);
}finally{await server?.close();await browser?.close();if(existsSync(entry))unlinkSync(entry);if(linked)unlinkSync(root+'/node_modules');}
