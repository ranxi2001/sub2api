// Remote CI only. Actual page + synthetic offline data; SQL/API acceptance runs separately.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const output = process.env.UPSTREAM_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const fixture = `
let failed=false;
globalThis.__upstreamFixture={queries:[],fail:()=>failed=true};
export async function readOperations(kind,query){
 globalThis.__upstreamFixture.queries.push({kind,...query});if(failed)throw new Error('synthetic query failure');
 const now=new Date().toISOString();
 const metrics={requests:1604,standard_charge:120,request_income:45,estimated_cost:30,estimated_profit:15,margin:1/3,covered_estimated_cost:30,covered_income:45,cost_covered_requests:1604,unknown_cost_requests:0,unknown_upstream_requests:4,saved_multiplier_requests:1600,default_multiplier_requests:4,saved_stats_base_requests:1500,standard_base_fallback_requests:104,subscription_requests:10,deleted_account_requests:2,samples:1320,unknown_samples:88,p50_ms:820,p90_ms:1900,p95_ms:2450,duration_p90_ms:4300,oldest_config_updated_at:now,latest_config_updated_at:now};
 const key=query.dimension==='upstream'?'https://relay.example:8443/tenant/v1':query.dimension==='account'?'42':'vendor-model';
 return {scope:query,queried_at:now,attribution:'current_account_config_estimate_no_historical_snapshot',currency:'USD billing units',
 usage:{summary:metrics,total:1,rows:[{...metrics,key,name:key}],catalog:[{key:'https://relay.example:8443/tenant/v1',name:'https://relay.example:8443/tenant/v1',accounts:2}],catalog_total:1,member_total:1,members:[{id:42,name:'合成账号 A',cost_basis:'current_saved_multiplier',multiplier:0.25}],account_details_limit:100,accounts:[{id:42,name:'合成账号 A',key:'https://relay.example:8443/tenant/v1',attribution:'current_config',multiplier:0.25,cost_basis:'current_saved_multiplier',updated_at:now,deleted:false}],capacity:{accounts:2,schedulable:2,configured_concurrency:20}},
 quality:{status:'ready',data:{summary:{rounds:2,passed:1,degraded:0,inconclusive:1},total:2,rows:[{id:9,account_id:42,plan_id:8,measured_model:'vendor-model',observed_at:now,probes:1,passed_probes:1,expected:2,outcome:'inconclusive',round_complete:false,round_probes:1,round_expected:2,actions:[]}]},queried_at:now},
 errors:{status:'ready',data:{records:5,upstream_5xx:2,rows:[{error_type:'upstream',error_phase:'forward',status_code:502,records:5}]},queried_at:now}};
}
`
const settings = `export const useAdminSettingsStore=()=>({requestCaptureEnabled:false})`
const shell = `import {h} from 'vue';export default {setup(_,ctx){return ()=>h('main',{class:'mx-auto max-w-7xl p-4 text-gray-900'},ctx.slots.default?.())}}`
const html = `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';import {createI18n} from 'vue-i18n';
import ProfitPage from '/src/views/admin/ProfitOperationsView.vue';import UpstreamPage from '/src/views/admin/UpstreamChannelOperationsView.vue';const Page=location.pathname.includes('profit-operations')?ProfitPage:UpstreamPage;import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
const router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:Page}]});
createApp(RouterView).use(createPinia()).use(router).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');</script></html>`
process.chdir(frontend)
const server = await createServer({ root: frontend, configFile: false, plugins: [vue(), {
  name: 'channel-operations-offline',
  resolveId(id) { if (id.startsWith('virtual:channel-')) return '\0' + id },
  load(id) { if (id === '\0virtual:channel-fixture') return fixture; if (id === '\0virtual:channel-shell') return shell; if (id === '\0virtual:channel-settings') return settings },
  configureServer(vite) { vite.middlewares.use((req, res, next) => { if (['/admin/channel-operations','/admin/profit-operations'].includes(req.url?.split('?')[0])) { res.setHeader('Content-Type', 'text/html'); void vite.transformIndexHtml('/admin/channel-operations', html).then(value => res.end(value)); return } next() }) }
}], resolve: { alias: [{ find: '@/api/admin/upstreamOperations', replacement: 'virtual:channel-fixture' }, { find: '@/components/layout/AppLayout.vue', replacement: 'virtual:channel-shell' }, { find: '@/stores/adminSettings', replacement: 'virtual:channel-settings' }, { find: '@', replacement: path.join(frontend, 'src') }] }, css: { postcss: { plugins: [tailwind({ config: path.join(frontend, 'tailwind.config.js') }), autoprefixer()] } }, server: { host: '127.0.0.1', port: 4183, strictPort: true }, optimizeDeps: { noDiscovery: true, include: ['vue', 'vue-i18n', 'pinia', 'vue-router'] } })
await server.listen()
const browser=await chromium.launch({headless:true})
try {
 const page=await browser.newPage({viewport:{width:1440,height:1100}}),errors=[],apiRequests=[]
 page.on('pageerror',e=>errors.push(e.message))
 await page.route('**/api/**',route=>{apiRequests.push(route.request().url());return route.abort()})
 for(const kind of ['profit','channel']){
  await page.goto(`http://127.0.0.1:4183/admin/${kind}-operations`,{waitUntil:'networkidle'})
  await page.getByTestId('profit-summary').getByText('33.33%',{exact:true}).waitFor()
  await page.screenshot({path:path.join(output,`${kind}-desktop-synthetic.png`),fullPage:true})
  await page.getByTestId('drill-row').click()
  await page.waitForFunction(()=>globalThis.__upstreamFixture.queries.at(-1).dimension==='account')
  await page.getByTestId('member-drill').waitFor()
  await page.getByTestId('member-drill').click()
  await page.waitForFunction(()=>globalThis.__upstreamFixture.queries.at(-1).dimension==='model' && globalThis.__upstreamFixture.queries.at(-1).account_id===42)
  await page.getByText('vendor-model',{exact:true}).first().waitFor()
  await page.setViewportSize({width:390,height:844})
  await page.screenshot({path:path.join(output,`${kind}-mobile-model-synthetic.png`),fullPage:true})
  const width=await page.evaluate(()=>({body:document.body.scrollWidth,viewport:innerWidth}))
  if(width.body>width.viewport+2)throw new Error(`mobile_overflow_${kind}: ${JSON.stringify(width)}`)
  await page.setViewportSize({width:1440,height:1100})
 }
 await page.evaluate(()=>globalThis.__upstreamFixture.fail())
 await page.getByTestId('refresh-operations').click()
 await page.getByRole('alert').waitFor()
 if(await page.getByTestId('profit-summary').count())throw new Error('failed_query_displayed_as_numeric_success')
 await page.screenshot({path:path.join(output,'query-failure-synthetic.png'),fullPage:true})
 if(errors.length||apiRequests.length)throw new Error(`browser_errors_or_live_requests: ${errors.join('; ')}`)
 await fs.writeFile(path.join(output,'verification.json'),JSON.stringify({commit:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),synthetic:true,actualComponents:['ProfitOperationsView.vue','UpstreamChannelOperationsView.vue','OperationsAnalytics.vue'],liveApiCalls:0,paidRequests:0,browserErrors:errors,queries:await page.evaluate(()=>globalThis.__upstreamFixture.queries)},null,2))
} catch(error){
 for(const page of browser.contexts().flatMap(c=>c.pages())){await page.screenshot({path:path.join(output,'failure-synthetic.png'),fullPage:true});await fs.writeFile(path.join(output,'failure.txt'),String(error)+'\n'+await page.locator('body').innerText())}
 throw error
}finally{await browser.close();await server.close()}
