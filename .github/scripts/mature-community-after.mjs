// Actual QQ components with synthetic metadata. No model, message or payment calls.
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'
import { mkdir, writeFile, unlink } from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const repo = process.cwd(), root = path.join(repo, 'frontend'), output = process.env.EVIDENCE_DIR
const requireFrontend = createRequire(path.join(root, 'package.json'))
const { createServer } = requireFrontend('vite'), vue = requireFrontend('@vitejs/plugin-vue')
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const entry = path.join(root, '.mature-community-preview.js'), errors = [], captures = []
let browser, server
await mkdir(output, { recursive: true })
try {
  await writeFile(entry, `
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';
import {defaultAutonomousConfig,defaultModerationConfig} from '/src/api/qqbot.ts';
import Overview from '/src/views/admin/QQBotOverviewView.vue';import Setup from '/src/views/admin/QQBotSetupView.vue';
import Config from '/src/views/admin/QQBotView.vue';import Coupons from '/src/views/admin/orders/AdminCouponsView.vue';import '/src/style.css';
const lang=new URL(location.href).searchParams.get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
const config={enabled:false,app_id:'synthetic-app',app_secret:'',app_secret_set:true,sandbox:true,allowed_groups:['synthetic-group'],group_policies:[{group_id:'synthetic-group',label:'Example community'}],base_url:'https://model.example.invalid/v1',api_key:'',api_key_set:true,chat_model:'example-chat',chat_protocol:'responses',image_enabled:false,image_base_url:'',image_api_key:'',image_api_key_set:false,image_model:'',image_size:'1024x1024',system_prompt:'',max_output_tokens:2048,history_messages:12,daily_chat_limit:30,daily_image_limit:2,total_daily_chat_limit:500,total_daily_image_limit:20,cooldown_seconds:5,checkin_reward:0,invite_enabled:false,invite_reward:0,invite_daily_limit:10,custom_commands:[],group_admins:[],section_versions:{},autonomous:defaultAutonomousConfig(),moderation:defaultModerationConfig(),notification_quiet_hours:{start_hour:23,end_hour:8},site_knowledge:{enabled:false,entries:[]},price_notifications:{enabled:false,groups:[]},health_notifications:{enabled:false,groups:[]}};
const page={app_id:'synthetic-app',items:[],total:0,page:1,page_size:20};window.requests=[];
apiClient.defaults.adapter=async r=>{window.requests.push({method:r.method,url:r.url});if(r.method!=='get')throw Error('Unexpected external-effect request '+r.url);let data;
if(r.url==='/admin/qqbot/config')data=config;
else if(r.url==='/admin/qqbot/runtime')data={encryption_ready:true,bindings:0,groups:[{id:'synthetic-group',last_event:'GROUP_AT_MESSAGE_CREATE',last_seen_at:'2026-11-14T12:00:00Z'}],jobs:[],rewards:[],recent_members:[],moderation_records:[]};
else if(r.url==='/admin/qqbot/overview')data={day:'2026-11-14',last_event:null,queued:0,running:0,failed:0,chat_used:0,image_used:0,chat_remaining:500,image_remaining:20,reward_amount:0,pending_rewards:0,reward_alert:false,checks:[]};
else if(r.url==='/admin/qqbot/setup/options')data={groups:[],image_groups:[],current_quota:0,current_used:0,current_image_quota:0,current_image_used:0,frontend_url:'https://site.example.invalid',email_configured:false};
else if(r.url==='/admin/qqbot/config-history'||r.url==='/admin/payment/coupons/templates')data=[];
else if(r.url.includes('/groups/')&&r.url.endsWith('/info'))data={app_id:'synthetic-app',sandbox:true,group_id:'synthetic-group',name:'Example community',status:'available'};
else if(r.url==='/admin/qqbot/members'||r.url==='/admin/payment/coupons'||r.url==='/admin/payment/coupons/usage')data=page;
else throw Error('Unspecified fixture request '+r.url);
return {config:r,status:200,statusText:'OK',headers:{},data:{code:0,data}}};
const router=createRouter({history:createWebHistory(),routes:[{path:'/admin/qqbot/overview',component:Overview},{path:'/admin/qqbot/setup',component:Setup},{path:'/admin/qqbot/groups',component:Config},{path:'/admin/orders/coupons',component:Coupons}]});
createApp(RouterView).use(createPinia()).use(router).use(i18n).mount('#app');
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({ root, configFile: false, envFile: false, resolve: { alias: { '@': path.join(root, 'src') } }, plugins: [{ name: 'mature-community-fixture', enforce: 'pre', load(id) {
    if (id.endsWith('/src/stores/auth.ts')) return 'export const useAuthStore=()=>({user:{id:99999,role:"admin"},isAdmin:true,isObserver:false})'
    if (id.endsWith('/src/components/layout/AppLayout.vue')) return '<template><main class="mx-auto max-w-7xl p-5"><slot /></main></template>'
  }, configureServer(vite) { vite.middlewares.use((req, res, next) => { if (!req.url?.startsWith('/admin/')) return next(); res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module" src="/.mature-community-preview.js"></script>') }) } }, (vue.default || vue)()], server: { host: '127.0.0.1', port: 4197, strictPort: true }, optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-router', 'vue-i18n', 'axios'] } })
  await server.listen(); browser = await chromium.launch({ headless: true })
  for (const lang of ['zh', 'en']) for (const view of ['overview', 'setup', 'groups', 'coupons']) {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, timezoneId: 'UTC' })
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4197' ? route.continue() : route.abort())
    await page.goto('http://127.0.0.1:4197' + (view === 'coupons' ? '/admin/orders/coupons' : '/admin/qqbot/' + view) + '?lang=' + lang, { waitUntil: 'networkidle' })
    await page.locator('h1,h2').first().waitFor()
    assert.equal(await page.locator('[role="alert"]').count(), 0, await page.locator('body').innerText())
    for (const [size, width, height] of [['desktop', 1440, 1000], ['mobile', 390, 844]]) {
      await page.setViewportSize({ width, height })
      const file = `qq-${view}-${lang}-${size}.png`
      await page.screenshot({ path: path.join(output, file), fullPage: true }); captures.push(file)
      const widths = await page.evaluate(() => [document.body.scrollWidth, innerWidth]); assert.ok(widths[0] <= widths[1] + 2)
    }
    await page.close()
  }
  assert.deepEqual(errors, [])
  await writeFile(path.join(output, 'evidence.json'), JSON.stringify({ head: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim(), actualComponents: ['QQBotOverviewView.vue', 'QQBotSetupView.vue', 'QQBotView.vue'], synthetic: true, liveModelCalls: 0, sentMessages: 0, financialWrites: 0, errors, captures }, null, 2))
} catch (error) {
  await writeFile(path.join(output, 'failure.json'), JSON.stringify({ error: String(error), errors }, null, 2))
  for (const context of browser?.contexts() || []) for (const page of context.pages()) { await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true }); await writeFile(path.join(output, 'failure.txt'), await page.locator('body').innerText()) }
  throw error
} finally { await browser?.close(); await server?.close(); await unlink(entry) }
