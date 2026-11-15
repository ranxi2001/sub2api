import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'
import { mkdir, writeFile, unlink } from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const repo = process.cwd(), root = path.join(repo, 'frontend'), out = process.env.EVIDENCE_DIR
const requireFrontend = createRequire(path.join(root, 'package.json'))
const { createServer } = requireFrontend('vite'), vue = requireFrontend('@vitejs/plugin-vue')
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const base = 'b98e6acb2dc772651b6a597d40cce311f96a2ba5', head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const entry = path.join(root, '.repair-order-review.js'), errors = [], captures = []
let browser, server
await mkdir(out, { recursive: true })
try {
  await writeFile(entry, `
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';import Page from '/src/views/admin/orders/AdminOrdersView.vue';import '/src/style.css';
const lang=new URL(location.href).searchParams.get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
const order=(id,status,paid)=>({id,user_id:99,user_email:'synthetic@example.invalid',user_name:'Synthetic operator fixture',amount:100,pay_amount:90,bonus_amount:10,fee_rate:0,currency:'CNY',payment_type:'easypay',order_type:'balance',status,out_trade_no:'synthetic-order-'+id,created_at:'2026-11-14T12:00:00Z',expires_at:'2026-11-14T12:15:00Z',paid_at:paid?'2026-11-15T15:00:00Z':null,coupon:paid?{id:7,name:'Example reward coupon',discount_amount:10,bonus_amount:0,review_required:true}:undefined});
const items=[order(11,'CANCELLED',true),order(12,'EXPIRED',true),order(13,'CANCELLED',false)];window.calls=[];
apiClient.defaults.adapter=async r=>{window.calls.push({method:r.method,url:r.url});if(r.method!=='get'||r.url!=='/admin/payment/orders')throw Error('Unexpected fixture request '+r.method+' '+r.url);return {config:r,status:200,statusText:'OK',headers:{},data:{code:0,data:{items,total:3,page:1,page_size:20}}}};
const router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:Page}]});createApp(RouterView).use(createPinia()).use(router).use(i18n).mount('#app');
`, { flag: 'wx' })
  process.chdir(root)
  browser = await chromium.launch({ headless: true })
  const modes = process.env.REPAIR_CAPTURE_MODE === 'before' ? ['before'] : ['before', 'after']
  for (const mode of modes) {
    server = await createServer({ root, configFile: false, envFile: false, resolve: { alias: { '@': path.join(root, 'src') } }, plugins: [{ name: 'repair-order-review', enforce: 'pre', load(id) {
      if (mode === 'before' && id === path.join(root, 'src/views/admin/orders/AdminOrdersView.vue')) return execFileSync('git', ['show', base + ':frontend/src/views/admin/orders/AdminOrdersView.vue'], { cwd: repo, encoding: 'utf8' })
      if (id.endsWith('/src/stores/auth.ts')) return 'export const useAuthStore=()=>({user:{id:99999,role:"admin"},isAdmin:true,isObserver:false})'
      if (id.endsWith('/src/stores/app.ts')) return 'export const useAppStore=()=>({showError:m=>{throw Error(m)},showSuccess:()=>{}})'
      if (id.endsWith('/src/components/layout/AppLayout.vue')) return '<template><main class="mx-auto max-w-7xl p-5"><slot /></main></template>'
    }, configureServer(vite) { vite.middlewares.use((req,res,next)=>{if(!req.url?.startsWith('/admin/orders'))return next();res.setHeader('Content-Type','text/html');res.end('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/.repair-order-review.js"></script>')}) } }, (vue.default || vue)()], server: { host: '127.0.0.1', port: 4198, strictPort: true }, optimizeDeps: { entries: [], include: ['vue','pinia','vue-router','vue-i18n','axios'] } })
    await server.listen()
    for (const lang of ['zh','en']) {
      const page = await browser.newPage({ viewport: { width: 1440,height: 1000 }, timezoneId: 'UTC' })
      page.on('pageerror', e => errors.push(e.message))
      await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4198' ? route.continue() : route.abort())
      await page.goto('http://127.0.0.1:4198/admin/orders?lang='+lang, { waitUntil: 'networkidle' })
      await page.getByText('synthetic-order-11', { exact: true }).waitFor()
      const retry = page.getByRole('button', { name: lang === 'zh' ? '重试' : 'Retry', exact: true })
      assert.equal(await retry.count(), mode === 'before' ? 0 : 2, 'Paid recovery holds must require the explicit admin action')
      for (const [size,width,height] of [['desktop',1440,1000],['mobile',390,844]]) {
        await page.setViewportSize({width,height})
        const file='order-review-'+mode+'-'+lang+'-'+size+'.png'
        await page.screenshot({path:path.join(out,file),fullPage:true});captures.push(file)
      }
      assert.equal(await page.evaluate(()=>window.calls.some(c=>c.method!=='get')),false)
      await page.close()
    }
    await server.close();server=null
  }
  assert.deepEqual(errors,[])
  await writeFile(path.join(out,'evidence.json'),JSON.stringify({base,head,actualComponent:'AdminOrdersView.vue',synthetic:true,externalRequests:0,paymentWrites:0,captures,errors},null,2))
} catch(error) {
  await writeFile(path.join(out,'failure.json'),JSON.stringify({error:String(error),errors},null,2))
  for(const context of browser?.contexts()||[])for(const page of context.pages()){await page.screenshot({path:path.join(out,'failure.png'),fullPage:true});await writeFile(path.join(out,'failure.txt'),await page.locator('body').innerText())}
  throw error
} finally {await browser?.close();await server?.close();await unlink(entry)}
