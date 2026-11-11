// Actual account form with fictional data and no external network access.
import assert from 'node:assert/strict'
import {execFileSync} from 'node:child_process'
import {createRequire} from 'node:module'
import {mkdir,writeFile,unlink} from 'node:fs/promises'
import path from 'node:path'
import {pathToFileURL} from 'node:url'
const repo=process.cwd(),root=path.join(repo,'frontend'),out=process.env.EVIDENCE_DIR
const req=createRequire(path.join(root,'package.json'))
const {createServer}=req('vite'),vue=req('@vitejs/plugin-vue')
const {chromium}=await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const head=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()
const entry=path.join(root,'.qwen-platform-preview.js'),errors=[]
let browser,server
await mkdir(out,{recursive:true})
try{
 await writeFile(entry,`
import {mount} from '@vue/test-utils';import {h} from 'vue';import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';
import Form from '/src/components/account/CreateAccountModal.vue';import '/src/style.css';
const lang=new URL(location.href).searchParams.get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
window.requests=[];apiClient.defaults.adapter=async r=>{window.requests.push(r.url);let data;
if(r.method==='get'&&r.url==='/admin/accounts/management-capabilities')data={web_search_enabled:false,account_quota_notify_enabled:false};
else throw Error('Unexpected API: '+r.method+' '+r.url);
return {config:r,status:200,statusText:'OK',headers:{},data:{code:0,data}}};
mount(Form,{attachTo:'#app',props:{show:true,proxies:[],groups:[]},global:{plugins:[createPinia(),i18n],stubs:{BaseDialog:{props:['show'],setup(props,ctx){return()=>props.show?h('main',{class:'mx-auto max-w-4xl p-6 bg-white'},ctx.slots.default?.()):null}},UpstreamChannelSelector:true,OAuthAuthorizationFlow:true,ConfirmDialog:true,ProxySelector:true,ProxyAdBanner:true,GroupSelector:true,QuotaLimitCard:true}}});
`,{flag:'wx'})
 process.chdir(root)
 server=await createServer({root,configFile:false,envFile:false,resolve:{alias:{'@':path.join(root,'src')}},plugins:[{name:'qwen-preview',enforce:'pre',load(id){if(id.endsWith('/src/stores/auth.ts'))return 'export const useAuthStore=()=>({user:{id:99999,role:"admin"},isAdmin:true,isSimpleMode:true})'},configureServer(vite){vite.middlewares.use((r,s,next)=>{if(r.url?.split('?')[0]!=='/')return next();s.setHeader('Content-Type','text/html');s.end('<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module" src="/.qwen-platform-preview.js"></script>')})}},(vue.default||vue)()],server:{host:'127.0.0.1',port:4198,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n','vue-router','axios','@vue/test-utils']}})
 await server.listen();browser=await chromium.launch({headless:true})
 for(const lang of ['zh','en']){
  const page=await browser.newPage({viewport:{width:1200,height:1100},timezoneId:'UTC'})
  page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)})
  await page.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:4198'?route.continue():route.abort())
  await page.goto('http://127.0.0.1:4198/?lang='+lang)
  await page.getByTestId('platform-button-qwen').click()
  await page.locator('#create-account-form input[type=text]').first().fill('Qwen Demo')
  assert.equal(await page.getByTestId('generic-account-mode').count(),0)
  assert.ok((await page.locator('body').innerText()).includes('千问 Qwen'))
  await page.screenshot({path:path.join(out,'qwen-account-'+lang+'.png'),fullPage:true})
  await page.getByTestId('qwen-platform-notice').getByTestId('new-feature-badge').click()
  assert.equal(await page.getByTestId('qwen-platform-notice').getByTestId('new-feature-badge').count(),0)
  await page.close()
 }
 assert.deepEqual(errors,[])
 await writeFile(path.join(out,'evidence.json'),JSON.stringify({head,scope:'Actual CreateAccountModal, fictional data, no account creation or production requests',errors},null,2))
}catch(error){
 await writeFile(path.join(out,'failure.json'),JSON.stringify({head,errors,error:String(error)},null,2))
 for(const context of browser?.contexts()||[])for(const page of context.pages()){await page.screenshot({path:path.join(out,'failure.png'),fullPage:true});await writeFile(path.join(out,'failure.txt'),await page.locator('body').innerText())}
 throw error
}finally{await browser?.close();await server?.close();await unlink(entry)}
