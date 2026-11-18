import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'
const root = resolve('frontend'), out = join(process.env.EVIDENCE_DIR, 'balance-notification')
const { createServer } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')))
const { default: vue } = await import(pathToFileURL(join(root, 'node_modules/@vitejs/plugin-vue/dist/index.mjs')))
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const baseline = process.env.EVIDENCE_BASE_SHA || execFileSync('git', ['rev-parse', 'HEAD^'], { encoding: 'utf8' }).trim()
const component = join(root, 'src/components/admin/operations/AccountOpsRuleList.vue')
const oldComponent = execFileSync('git', ['show', `${baseline}:frontend/src/components/admin/operations/AccountOpsRuleList.vue`], { encoding: 'utf8' })
const origin = 'http://127.0.0.1:4311', entry = join(root, '.balance-notification-preview.js')
let before = true, server, browser
mkdirSync(out, { recursive: true })
try {
 writeFileSync(entry, `
import {createApp,h,ref} from 'vue';import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';
import Rules from '/src/components/admin/operations/AccountOpsRuleList.vue';import '/src/style.css';
const language=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(language);i18n.global.locale.value=language;
const accounts=[1,2].map(id=>({account_id:id,account_name:'示例账号 '+id,platform:'anthropic',type:'apikey',balance:35,unit:'USD',balance_status:'ok',received_at:null,usage_windows:[]}));
const groups=[{id:'manual-example',name:'示例渠道',provider:'manual',site:'example.test',mode:'manual',account_ids:[1,2]}];
const config=ref({enabled:true,recipient:'ops@example.test',balance_low:true,weekly_quota:true,cooldown_minutes:60,groups,balance_low_disabled_account_ids:[],balance_thresholds:[1,2].map(account_id=>({account_id,enabled:false,threshold:50,unit:'USD',notify_alert:false,notify_recovery:false}))});
apiClient.defaults.adapter=async c=>{
 if(c.url.includes('/balance-notification')){const id=Number(c.url.split('/').at(-2));const body=typeof c.data==='string'?JSON.parse(c.data):c.data;const ids=config.value.balance_low_disabled_account_ids.filter(v=>v!==id);if(!body.enabled)ids.push(id);return {config:c,status:200,statusText:'OK',headers:{},data:{code:0,data:{...config.value,balance_low_disabled_account_ids:ids}}}}
 throw new Error('Unexpected preview API');
};
createApp({setup(){return()=>h('main',{class:'p-6'},[h('p',{class:'mb-4'},'组件预览 · 虚构数据 / Component preview · synthetic data'),h(Rules,{accounts,groups,config:config.value,userId:42,loading:false,ready:true,error:'',onSaved:value=>config.value=value})])}}).use(createPinia()).use(i18n).mount('#app');
 `)
 process.chdir(root)
 server = await createServer({ root, configFile:false, server:{host:'127.0.0.1',port:4311,strictPort:true},resolve:{alias:{'@':join(root,'src')}},plugins:[{name:'balance-preview',enforce:'pre',load(id){if(before&&id===component)return oldComponent},configureServer(vite){vite.middlewares.use((req,res,next)=>{if(req.url?.split('?')[0]!=='/preview')return next();res.setHeader('Content-Type','text/html');res.end('<!doctype html><html><meta charset="utf-8"><div id="app"></div><script type="module" src="/.balance-notification-preview.js"></script></html>')})}},vue()] })
 await server.listen();browser=await chromium.launch({headless:true})
 for(const language of ['zh','en']) for(const phase of ['before','after']){
  before=phase==='before';server.moduleGraph.invalidateAll()
  const context=await browser.newContext({viewport:{width:1440,height:900}})
  await context.route('**/*',route=>route.request().url().startsWith(origin)?route.continue():route.abort())
  const page=await context.newPage(),errors=[];page.on('pageerror',error=>errors.push(error.message))
  await page.goto(`${origin}/preview?lang=${language}`,{waitUntil:'networkidle'})
  await page.getByTestId('account-ops-rules').waitFor()
  await page.screenshot({path:join(out,`${phase}-${language}.png`),fullPage:true})
  if(!before){
   await page.getByTestId('balance-notification-1').uncheck()
   await page.waitForFunction(()=>!document.querySelector('[data-testid="balance-notification-1"]').checked)
   if(!await page.getByTestId('balance-notification-2').isChecked())throw new Error('Other account was muted')
   await page.screenshot({path:join(out,`muted-${language}.png`),fullPage:true})
  }
  if(errors.length)throw new Error(errors.join('\n'))
  await context.close()
 }
 writeFileSync(join(out,'source.json'),JSON.stringify({head,baseline,scope:'Actual RuleList component, synthetic accounts, mocked settings API; no production or email validation'},null,2))
}finally{await browser?.close();await server?.close();rmSync(entry,{force:true})}
