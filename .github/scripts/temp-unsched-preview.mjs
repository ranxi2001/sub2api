// Remote CI evidence only: actual EditAccountModal, synthetic API/auth, no production.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, resolve, dirname } from 'node:path'
import { pathToFileURL } from 'node:url'
const repo = process.cwd(), root = join(repo, 'frontend'), output = resolve(process.env.EVIDENCE_DIR)
const source = 'd2b2b246fc0235740e87c742b24be5a5c9e055e0'
const baseline = 'e10fe8c43'
const git = (...args) => execFileSync('git', args, {cwd:repo,encoding:'utf8'}).trimEnd()
git('diff', '--exit-code', source, 'HEAD', '--', 'backend', 'frontend')
const req = createRequire(join(root, 'package.json'))
const {createServer} = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const {chromium} = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const entryName = '.temp-unsched-preview.js', entry = join(root,entryName), origin='http://127.0.0.1:4299'
const errors=[], captures=[], saves=[]; let server,browser,before=false
mkdirSync(output,{recursive:true})
try {
writeFileSync(entry, `
import {mount} from '@vue/test-utils';import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';import {defaultExcelBPSDefaults} from '/src/utils/excelBPSDefaults.ts';
import View from '/src/components/account/EditAccountModal.vue';import '/src/style.css';
const locale=new URL(location.href).searchParams.get('locale')||'zh';await loadLocaleMessages(locale);i18n.global.locale.value=locale;
window.__saved=[];
const account={id:90001,name:'Demo account',notes:'',platform:'openai',type:'apikey',credentials:{api_key:'fixture-only',base_url:'https://example.invalid',temp_unschedulable_enabled:true,temp_unschedulable_rules:[{error_code:429,keywords:['rate limit'],duration_minutes:10},{error_code:502,keywords:['bad gateway'],duration_minutes:30}]},extra:{},proxy_id:null,concurrency:1,priority:1,rate_multiplier:1,status:'active',group_ids:[],expires_at:null,auto_pause_on_expired:false};
window.__account=account;
apiClient.defaults.adapter=async request=>{let data={};if(request.url.includes('auto-config'))data={excel_bps:defaultExcelBPSDefaults()};else if(request.url.includes('tls-fingerprint'))data=[];else if(request.url.includes('management-capabilities'))data={web_search_enabled:false,account_quota_notify_enabled:false};else if(request.url.includes('web-search'))data={enabled:false,providers:[]};else if(request.url.includes('mixed-channel'))data={has_risk:false};else if(request.method==='put'&&request.url==='/admin/accounts/90001'){const body=typeof request.data==='string'?JSON.parse(request.data):request.data;window.__saved.push(body);data={...account,...body}};return {config:request,status:200,statusText:'OK',headers:{},data:{code:0,data}}};
window.preview=mount(View,{attachTo:'#app',props:{show:true,account,proxies:[],groups:[]},global:{plugins:[createPinia(),i18n],stubs:{ProxySelector:true,GroupSelector:true,ModelWhitelistSelector:true,OpenAIAuthConnections:true,SIWCAccountSettings:true}}});
`,{flag:'wx'})
process.chdir(root)
server=await createServer({root,configFile:false,cacheDir:join(output,'vite-cache'),resolve:{alias:{'@':join(root,'src')}},plugins:[{name:'fixture',enforce:'pre',load(id){const clean=id.split('?')[0];if(clean===join(root,'src/stores/auth.ts'))return 'export const useAuthStore=()=>({user:{id:41,role:"admin"},isAdmin:true,isSimpleMode:true})';if(before&&!id.includes('?')&&clean===join(root,'src/components/account/EditAccountModal.vue'))return git('show',baseline+':frontend/src/components/account/EditAccountModal.vue')},configureServer(v){v.middlewares.use((request,response,next)=>{if(request.url==='/'||request.url?.startsWith('/?')){response.setHeader('Content-Type','text/html');response.end('<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/'+entryName+'"></script></html>')}else if(request.url==='/favicon.ico'){response.statusCode=204;response.end()}else next()})}},vue()],server:{host:'127.0.0.1',port:4299,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n','vue-router','axios','@vue/test-utils']}})
await server.listen();browser=await chromium.launch({headless:true});const page=await browser.newPage({viewport:{width:1280,height:1100},timezoneId:'UTC'})
page.on('pageerror',e=>errors.push(e.message))
await page.route('**/*',route=>new URL(route.request().url()).origin===origin?route.continue():route.abort())
const section=()=>page.locator('#edit-account-form > div').filter({has:page.locator('.input-label', {hasText:/临时不可调度|Temporary Unschedulable/i})}).first()
const capture=async name=>{await section().screenshot({path:join(output,name),animations:'disabled'});captures.push(name)}
before=true;await page.goto(origin,{waitUntil:'networkidle',timeout:90000});await page.locator('#edit-account-form').waitFor();await capture('before-zh.png')
before=false;server.moduleGraph.invalidateAll();await page.reload({waitUntil:'networkidle'});await page.getByTestId('temp-unsched-trigger-count').first().waitFor();await capture('after-zh.png')
await page.locator('[data-tour="edit-account-form-name"]').fill('Renamed demo')
await page.locator('#edit-account-form').evaluate(form=>form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})))
await page.waitForFunction(()=>window.__saved.length===1)
const legacy=await page.evaluate(()=>window.__saved[0]);if(legacy.credentials.temp_unschedulable_rules.some(r=>'stream_timing' in r))throw Error('Unchanged legacy timing was migrated');saves.push(legacy)
await page.getByTestId('temp-unsched-stream-after_stream').nth(1).click();await page.getByTestId('temp-unsched-trigger-count').nth(1).fill('3');await page.getByTestId('temp-unsched-trigger-window').nth(1).fill('5')
await page.locator('#edit-account-form').evaluate(form=>form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));await page.waitForFunction(()=>window.__saved.length===2)
const edited=await page.evaluate(()=>window.__saved[1]);const rule=edited.credentials.temp_unschedulable_rules[1];if(rule.stream_timing!=='after_stream'||rule.trigger_count!==3||rule.trigger_window_minutes!==5||rule.duration_minutes!==30)throw Error('Saved timing/count/window/cooldown differs');saves.push(edited);await capture('after-saved-zh.png')
const badges=section().getByTestId('new-feature-badge');await badges.first().click();if(await badges.count())throw Error('Notice dismissal not shared');await page.reload({waitUntil:'networkidle'});await page.getByTestId('temp-unsched-trigger-count').first().waitFor();if(await badges.count())throw Error('Notice dismissal not persisted')
await page.goto(origin+'/?locale=en',{waitUntil:'networkidle'});await page.getByTestId('temp-unsched-trigger-count').first().waitFor();await capture('after-en.png')
await page.evaluate(()=>document.documentElement.classList.add('dark'));await page.setViewportSize({width:390,height:844});await capture('after-mobile-dark-en.png')
if(errors.length)throw Error(errors.join('\n'))
} finally {
writeFileSync(join(output,'manifest.json'),JSON.stringify({source,baseline,harness:git('rev-parse','HEAD'),captures,errors,saves:saves.map(x=>({name:x.name,rules:x.credentials.temp_unschedulable_rules})),boundary:'Actual EditAccountModal, dialog, controls and CSS; mocked API/auth and unrelated selectors. Tests preserve omitted legacy timing, explicit 3/5/30 rule save, shared and persisted notice dismissal. CreateAccountModal and production are not covered.'},null,2))
await browser?.close();await server?.close();rmSync(entry,{force:true})
}
