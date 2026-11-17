// Remote CI only. Real account management + profit + channel views with a
// synthetic Axios adapter. No network API or provider requests are permitted.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
const root=process.cwd(), frontend=path.join(root,'frontend'), requireFrontend=createRequire(path.join(frontend,'package.json'))
const {createServer}=requireFrontend('vite'), vueModule=requireFrontend('@vitejs/plugin-vue'), vue=vueModule.default||vueModule
const tailwind=requireFrontend('tailwindcss'),autoprefixer=requireFrontend('autoprefixer')
const {chromium}=createRequire(path.join(process.env.PLAYWRIGHT_ROOT,'package.json'))('playwright')
const output=process.env.UPSTREAM_SCREENSHOT_DIR
await fs.mkdir(output,{recursive:true})
const fixture=`
import {apiClient} from '/src/api/client.ts';
const channel={id:7,code:'supplier-a',name:'手工上游渠道 A',remark:'支持未知 URL 的多账号渠道',revision:3,members:2};
const now=new Date().toISOString();
const account=(id,type='apikey')=>({id,name:'合成账号 '+id,notes:'',platform:'anthropic',type,credentials:{base_url:'https://fixture.example/v1'},credentials_status:{has_api_key:true},extra:{cost_multiplier:.25},proxy_id:null,concurrency:3,priority:50,rate_multiplier:1,group_rate_multiplier:1,status:'active',schedulable:true,auto_pause_on_expired:true,groups:[],group_ids:[],created_at:now,updated_at:now,upstream_channel_binding:{account_id:id,account_name:'合成账号 '+id,deleted:false,channel_id:7,revision:1,channel:{...channel}}});
let accounts=[account(41),account(42,'oauth')],bindings=accounts.map(a=>({...a.upstream_channel_binding}));
const calls=[];globalThis.__entityFixture={calls,channel,accounts,bindings};
apiClient.defaults.adapter=async config=>{
 const method=config.method,url=config.url,body=typeof config.data==='string'?JSON.parse(config.data):config.data;
 calls.push({method,url,body,params:config.params});let data={};
 if(url==='/admin/upstream-channels'){
  if(method==='post'){Object.assign(channel,body,{id:8,revision:1,members:0});data={...channel}}else data={items:[{...channel}],total:1};
 }else if(url.startsWith('/admin/upstream-channels/')){
  if(url.endsWith('/members'))data={items:bindings.filter(b=>b.channel_id===channel.id),total:bindings.filter(b=>b.channel_id===channel.id).length};
  else{if(method==='put')Object.assign(channel,body,{revision:channel.revision+1});data={...channel};}
 }else if(url==='/admin/upstream-channel-bindings'){
  if(method==='put'){for(const b of body.bindings){let current=bindings.find(v=>v.account_id===b.account_id);if(!current){current={account_id:b.account_id,account_name:'合成新账号',revision:0};bindings.push(current)}Object.assign(current,{channel_id:body.channel_id,revision:current.revision+1,channel:body.channel_id?{...channel}:undefined});}data={items:bindings.filter(b=>body.bindings.some(v=>v.account_id===b.account_id)).map(b=>({...b}))};}
  else{const ids=String(config.params.account_ids).split(',').map(Number);data={items:bindings.filter(b=>ids.includes(b.account_id)).map(b=>({...b}))};}
 }else if(url==='/admin/accounts'){
  if(method==='post'){const a=account(99);a.name=body.name;accounts.push(a);bindings.push({account_id:99,account_name:a.name,channel_id:body.upstream_channel_id||0,revision:1,channel:body.upstream_channel_id?{...channel}:undefined});data=a;}
  else data={items:accounts,total:accounts.length,page:1,page_size:20,pages:1};
 }else if(/^\\/admin\\/accounts\\/\\d+$/.test(url))data=accounts.find(a=>a.id===Number(url.split('/').at(-1)));
 else if(url.includes('upstream-billing-rates'))data={items:[],total:0,page:1,page_size:20};
 else if(url==='/admin/profit-operations/providers')data={items:[{provider_id:1,provider_name:'合成服务商',site_type:'sub2api',status:'connected',observed_at:now,revision:1,channel_id:channel.id,channel_name:channel.name,metrics:{today_spend:60,total_spend:600,currency:'USD'}}],total:1,page:1,page_size:20};
 else if(url==='/admin/profit-operations/snapshot'){
  const params=config.params||{};
  const start=new Date(); start.setHours(0,0,0,0);
  const from=new Date(params.from), until=new Date(params.until);
  const day=d=>d.getFullYear()+'-'+d.getMonth()+'-'+d.getDate();
  const yesterdayStart=new Date(start); yesterdayStart.setDate(yesterdayStart.getDate()-1);
  const yesterday=day(from)===day(yesterdayStart)&&day(until)===day(start)&&until.getHours()===0&&until.getMinutes()===0;
  const drilled=params.dimension==='account'||params.dimension==='model'||Number(params.account_id)>0||Number(params.group_id)>0||String(params.model||'')!=='';
  const metrics={requests:2,standard_charge:20,request_income:8,supplier_requests:2,bill_requests:0,estimated_requests:0,upstream_cost:drilled?null:(yesterday?4:5),gross_profit:drilled?null:(yesterday?4:3),gross_margin:drilled?null:(yesterday?0.5:0.375),cost_covered_requests:2,unknown_cost_requests:0,unknown_upstream_requests:0,cost_source:drilled?'wallet_not_split':(yesterday?'wallet_window':'wallet_today'),wallet_basis:yesterday?'wallet_window':'wallet_today',wallet_currency:'USD'};
  data={scope:params,queried_at:now,currency:'USD',usage:{summary:metrics,wallet_synced_at:now,rows:[{...metrics,key:drilled?'41':'channel:7',name:drilled?'合成账号 41':channel.name,provider_id:1,provider_site_type:'sub2api',channel_id:7,channel_code:channel.code,channel_remark:channel.remark,assignment:'explicit'}],total:1,catalog:[{key:'channel:7',name:channel.name,accounts:2,channel_id:7,channel_code:channel.code,channel_remark:channel.remark,assignment:'explicit'}],catalog_total:1,members:[],member_total:0,accounts:[],capacity:{accounts:2,schedulable:2,configured_concurrency:6}},quality:{status:'ready',data:{summary:{},rows:[]}},errors:{status:'ready',data:{rows:[]}}};
 }else if(url.includes('management-capabilities'))data={web_search_enabled:false,account_quota_notify_enabled:false};
 else if(url.includes('credential-encryption'))data={configured:true,source:'server_config'};
 else if(url.includes('check-mixed-channel'))data={has_risk:false};
 else if(url.includes('/settings'))data={run_mode:'simple',site_name:'Synthetic upstream channels'};
 else if(url.includes('/groups')||url.includes('tls-fingerprint')||url.includes('/proxies'))data=[];
 else if(url.includes('/models'))data={models:[]};
 else if(url.includes('/auto-config'))data={};
 else if(url.includes('/stats'))data={};
 else data=[];
 return {data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config};
};
`
const shell=`import {h} from 'vue';export default {setup(_,ctx){return()=>h('main',{class:'mx-auto max-w-7xl p-4'},ctx.slots.default?.())}}`
const html=`<!doctype html><html lang="zh"><meta charset="utf-8"><div id="app"></div><script type="module">
import 'virtual:entity-fixture';import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';import {createI18n} from 'vue-i18n';import {useAuthStore} from '/src/stores/auth.ts';
import Profit from '/src/views/admin/ProfitOperationsView.vue';import Channels from '/src/views/admin/UpstreamChannelsView.vue';const Accounts=()=>import('/src/views/admin/AccountsView.vue');import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
const pinia=createPinia();const router=createRouter({history:createWebHistory(),routes:[{path:'/admin/profit-operations',component:Profit},{path:'/admin/upstream-channels',component:Channels},{path:'/admin/accounts',component:Accounts}]});
const app=createApp(RouterView);app.use(pinia);const auth=useAuthStore();auth.user={id:1,email:'synthetic@test.invalid',role:'admin',status:'active',balance:0,concurrency:1};app.use(router).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');</script></html>`
process.chdir(frontend)
const server=await createServer({root:frontend,configFile:false,plugins:[vue(),{name:'entity-offline',resolveId(id){if(id.startsWith('virtual:entity-'))return '\0'+id},load(id){if(id==='\0virtual:entity-fixture')return fixture;if(id==='\0virtual:entity-shell')return shell},configureServer(vite){vite.middlewares.use((req,res,next)=>{if(['/admin/profit-operations','/admin/upstream-channels','/admin/accounts'].includes(req.url?.split('?')[0])){res.setHeader('Content-Type','text/html');void vite.transformIndexHtml(req.url,html).then(v=>res.end(v));return}next()})}}],resolve:{alias:[{find:'@/components/layout/AppLayout.vue',replacement:'virtual:entity-shell'},{find:'@',replacement:path.join(frontend,'src')}]},css:{postcss:{plugins:[tailwind({config:path.join(frontend,'tailwind.config.js')}),autoprefixer()]}},server:{host:'127.0.0.1',port:4185,strictPort:true},optimizeDeps:{noDiscovery:true,include:['vue','vue-i18n','pinia','vue-router','axios','@vueuse/core','@tanstack/vue-virtual','chart.js','vue-chartjs','dompurify','qrcode','driver.js']}})
await server.listen();const browser=await chromium.launch({headless:true})
const page=await browser.newPage({viewport:{width:1440,height:1100}}),errors=[],network=[]
page.on('pageerror',e=>errors.push(e.message));await page.route('**/*',r=>{const url=new URL(r.request().url());if(url.pathname.startsWith('/api/')||url.hostname!=='127.0.0.1'){network.push(r.request().url());return r.abort()}return r.continue()})
try{
 await page.goto('http://127.0.0.1:4185/admin/profit-operations',{waitUntil:'networkidle'})
 await page.getByTestId('profit-summary').waitFor();await page.getByText('显式渠道',{exact:false}).first().waitFor()
 const row = page.getByTestId('profit-drilldown').locator('tbody tr').first()
 const wallet = '渠道运维「今日消费」'
 const windowSpend = '此时段上游钱包消费'
 await row.getByTestId('cost-source').filter({hasText:wallet}).waitFor()
 await page.getByTestId('profit-summary').getByTestId('cost-source').filter({hasText:wallet}).waitFor()
 await page.getByTestId('wallet-synced').waitFor()
 await row.locator('[data-field="gross_margin"]').filter({hasText:'37.50%'}).waitFor()
 if(await page.getByRole('button',{name:'同步上游账单'}).count()) throw new Error('profit report must not offer upstream bill sync')
 await page.getByTestId('yesterday-filter').click()
 await row.getByTestId('cost-source').filter({hasText:windowSpend}).waitFor()
 await row.locator('[data-field="upstream_cost"]').filter({hasText:'4.000000'}).waitFor()
 await page.getByTestId('today-filter').click()
 await row.getByTestId('cost-source').filter({hasText:wallet}).waitFor()
 await row.getByTestId('drill-row').click()
 await row.getByTestId('cost-source').filter({hasText:'不按账号或模型拆分'}).waitFor()
 await row.locator('[data-field="upstream_cost"]').filter({hasText:'不可用'}).waitFor()
 await page.getByRole('button',{name:'返回渠道总览'}).click()
 await row.getByTestId('cost-source').filter({hasText:wallet}).waitFor()
 await page.getByTestId('open-channel-manager').click();await page.getByTestId('edit-upstream-channel').click();await page.getByTestId('channel-remark').fill('实体备注保存后真实 GET 读回（合成）');await page.getByTestId('confirm-channel-edit').check();await page.getByTestId('save-upstream-channel').click();await page.getByTestId('channel-entity-draft').waitFor({state:'detached'})
 await page.getByTestId('refresh-operations').click();await page.getByText('实体备注保存后真实 GET 读回（合成）',{exact:false}).first().waitFor();await page.screenshot({path:path.join(output,'entity-profit-desktop.png'),fullPage:true})
 await page.goto('http://127.0.0.1:4185/admin/upstream-channels',{waitUntil:'networkidle'});await page.getByTestId('edit-upstream-channel').waitFor();await page.screenshot({path:path.join(output,'entity-management-desktop.png'),fullPage:true})
 await page.goto('http://127.0.0.1:4185/admin/accounts',{waitUntil:'networkidle'});await page.getByTestId('manage-upstream-channels').waitFor();await page.screenshot({path:path.join(output,'entity-accounts-current-ownership.png'),fullPage:true})
 // Exercise the actual account table's create/edit/bulk controls by their
 // existing localized labels; component tests cover failures and retries.
 const create=page.getByRole('button',{name:'添加账号',exact:false}).first();
 if(await create.count())await create.click();else await page.getByRole('button',{name:'创建账号',exact:false}).first().click()
 await page.getByTestId('upstream-channel-select').last().focus();await page.getByTestId('upstream-channel-select').last().selectOption('7');await page.getByTestId('confirm-create-channel').check();await page.screenshot({path:path.join(output,'entity-account-create-confirmation.png'),fullPage:true})
 await page.getByRole('button',{name:'Claude Console',exact:false}).click().catch(async()=>{await page.getByRole('button',{name:'控制台',exact:false}).first().click()})
 await page.locator('form#create-account-form input[type="text"]').first().fill('浏览器创建渠道账号')
 await page.locator('form#create-account-form input[type="password"]').first().fill('synthetic-browser-fixture')
 await page.getByTestId('confirm-create-channel').check()
 await page.locator('form#create-account-form').evaluate(form=>form.requestSubmit())
 await page.waitForFunction(()=>globalThis.__entityFixture.calls.some(c=>c.method==='post'&&c.url==='/admin/accounts'&&c.body.upstream_channel_id===7))
 await page.getByTestId('confirm-create-channel').waitFor({state:'detached'})
 // Render the same real edit/bulk dialogs through table actions where exposed.
 await page.getByRole('button',{name:'编辑',exact:true}).first().click();await page.getByTestId('open-channel-bindings').click();await page.getByTestId('upstream-channel-select').waitFor();await page.getByTestId('upstream-channel-select').selectOption('0');await page.getByTestId('confirm-channel-binding').check();await page.getByTestId('save-channel-binding').click();await page.getByText('已保存并从服务器读回确认',{exact:true}).waitFor();await page.screenshot({path:path.join(output,'entity-account-edit-bindings.png'),fullPage:true});await page.keyboard.press('Escape')
 await page.locator('table input[type="checkbox"]').first().check();await page.getByRole('button',{name:'批量编辑',exact:false}).first().click();await page.getByTestId('open-channel-bindings').click();await page.getByTestId('upstream-channel-select').focus();await page.getByTestId('upstream-channel-select').selectOption('7');await page.getByTestId('confirm-channel-binding').check();await page.getByTestId('save-channel-binding').click();await page.getByText('已保存并从服务器读回确认',{exact:true}).waitFor();await page.screenshot({path:path.join(output,'entity-account-bulk-bindings.png'),fullPage:true})
 if(errors.length||network.length)throw new Error(JSON.stringify({errors,network}))
 await fs.writeFile(path.join(output,'entity-verification.json'),JSON.stringify({commit:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),actualViews:['ProfitOperationsView','UpstreamChannelsView','AccountsView'],synthetic:true,liveApiCalls:0,paidRequests:0,browserErrors:errors,calls:await page.evaluate(()=>globalThis.__entityFixture.calls)},null,2))
}catch(error){await page.screenshot({path:path.join(output,'entity-failure.png'),fullPage:true});await fs.writeFile(path.join(output,'entity-failure.txt'),String(error)+'\n'+JSON.stringify({errors,network})+'\n'+await page.locator('body').innerText());throw error}finally{await browser.close();await server.close()}
