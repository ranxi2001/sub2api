// Actual frozen/current PaymentView with synthetic, metadata-only checkout data.
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'
import { mkdir, writeFile, unlink } from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const repo=process.cwd(),root=path.join(repo,'frontend'),out=process.env.EVIDENCE_DIR
const req=createRequire(path.join(root,'package.json')),{createServer}=req('vite'),vue=req('@vitejs/plugin-vue')
const {chromium}=await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const base='37dc4620a41a68abaeab329358c15821c41e3f7f',head=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()
const entry=path.join(root,'.mature-payment-preview.js'),errors=[],captures=[]
let browser,server,before=true
await mkdir(out,{recursive:true})
try {
 await writeFile(entry,`
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';import Page from '/src/views/user/PaymentView.vue';import '/src/style.css';
const lang=new URL(location.href).searchParams.get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
window.calls=[];apiClient.defaults.adapter=async r=>{window.calls.push({method:r.method,url:r.url});let data;
if(r.url==='/payment/checkout-info')data={methods:{wxpay:{currency:'CNY',daily_limit:0,daily_used:0,daily_remaining:0,single_min:0,single_max:0,fee_rate:2,fee_rate_min:2,fee_rate_max:2,available:true}},global_min:0,global_max:0,plans:[],balance_disabled:false,balance_recharge_multiplier:1,subscription_usd_to_cny_rate:0,recharge_fee_rate:2,help_text:'',help_image_url:'',stripe_publishable_key:'',recharge_bonus_tiers:[],recharge_bonus_mode:'bonus',recharge_bonus_notice:''};
else if(r.url==='/lottery/pools')data={enabled:false,pools:[]};
else if(r.url==='/payment/coupons')data={items:[{id:7,snapshot:{name:'Example reward coupon',currency:'CNY',min_amount:100,kind:'fixed',value:10,stackable:false},expires_at:'2099-01-01T00:00:00Z'}],total:1,page:1,page_size:100};
else if(r.url==='/payment/coupons/7/quote')data={coupon_id:7,name:'Example reward coupon',currency:'CNY',original_amount:100,pay_base:90,pay_amount:91.8,credited:100,bonus_amount:10,discount_amount:10,coupon_bonus:0,fee_rate:2};
else throw Error('Unexpected payment fixture request '+r.method+' '+r.url);
return {config:r,status:200,statusText:'OK',headers:{},data:{code:0,data}}};
const router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:Page}]});createApp(RouterView).use(createPinia()).use(router).use(i18n).mount('#app');
`,{flag:'wx'})
 process.chdir(root)
 browser=await chromium.launch({headless:true})
 for(const mode of ['before','after']){
  before=mode==='before'
  server=await createServer({root,configFile:false,envFile:false,resolve:{alias:{'@':path.join(root,'src')}},plugins:[{name:'payment-comparison',enforce:'pre',load(id){
   if(before&&id===path.join(root,'src/views/user/PaymentView.vue'))return execFileSync('git',['show',base+':frontend/src/views/user/PaymentView.vue'],{cwd:repo,encoding:'utf8'})
   if(id.endsWith('/src/stores/auth.ts'))return 'export const useAuthStore=()=>({user:{id:99999,username:"Synthetic user",balance:10},isAdmin:false,refreshUser:async()=>{}})'
   if(id.endsWith('/src/stores/subscriptions.ts'))return 'export const useSubscriptionStore=()=>({activeSubscriptions:[],fetchActiveSubscriptions:async()=>{}})'
   if(id.endsWith('/src/stores/index.ts'))return 'export const useAppStore=()=>({cachedPublicSettings:{subscription_enabled:false},showError:message=>{throw Error(message)},showInfo:()=>{},showWarning:()=>{}})'
   if(id.endsWith('/src/components/layout/AppLayout.vue'))return '<template><main class="mx-auto max-w-5xl p-5"><slot /></main></template>'
  },configureServer(vite){vite.middlewares.use((r,s,next)=>{if(!r.url?.startsWith('/purchase'))return next();s.setHeader('Content-Type','text/html');s.end('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/.mature-payment-preview.js"></script>')})}},(vue.default||vue)()],server:{host:'127.0.0.1',port:4196,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-router','vue-i18n','axios']}})
  await server.listen()
  for(const lang of ['zh','en']){
   const page=await browser.newPage({viewport:{width:1440,height:1000},timezoneId:'UTC'})
   page.on('pageerror',e=>errors.push(e.message));await page.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:4196'?route.continue():route.abort())
   await page.goto('http://127.0.0.1:4196/purchase?lang='+lang,{waitUntil:'networkidle'})
   await page.getByRole('button',{name:/^(¥|￥)?100$/}).first().click()
   if(!before){await page.locator('#recharge-coupon').selectOption('7');await page.waitForFunction(()=>window.calls.some(c=>c.url==='/payment/coupons/7/quote'));await page.getByTestId('recharge-discount-row').waitFor()}
   for(const [size,width,height] of [['desktop',1440,1000],['mobile',390,844]]){
    await page.setViewportSize({width,height});const file='payment-'+mode+'-'+lang+'-'+size+'.png';await page.screenshot({path:path.join(out,file),fullPage:true});captures.push(file)
    const widths=await page.evaluate(()=>[document.body.scrollWidth,innerWidth]);assert.ok(widths[0]<=widths[1]+2)
   }
   assert.equal(await page.evaluate(()=>window.calls.some(c=>c.url==='/payment/orders')),false)
   await page.close()
  }
  await server.close();server=null
 }
 assert.deepEqual(errors,[])
 await writeFile(path.join(out,'evidence.json'),JSON.stringify({base,head,actualComponent:'PaymentView.vue',synthetic:true,paymentOrdersCreated:0,liveRequests:0,errors,captures},null,2))
}catch(error){
 await writeFile(path.join(out,'failure.json'),JSON.stringify({error:String(error),errors},null,2))
 for(const c of browser?.contexts()||[])for(const p of c.pages()){await p.screenshot({path:path.join(out,'failure.png'),fullPage:true});await writeFile(path.join(out,'failure.txt'),await p.locator('body').innerText())}
 throw error
}finally{await browser?.close();await server?.close();await unlink(entry)}
