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
const output = process.env.CHANNEL_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const fixture = `
let revision=8,failed=false;
const members=[{id:42,name:'合成账号 A',platform:'openai',status:'active',schedulable:true,concurrency:5,upstream_id:3,eligible:true}];
const upstreams=[{id:3,name:'合成上游 Alpha',members:1},{id:4,name:'合成上游 Beta',members:0}];
const channel={id:7,name:'合成渠道 · 多上游',description:'离线 UI 验收',status:'active',group_ids:[11],model_pricing:[],model_mapping:{},billing_model_source:'channel_mapped',restrict_models:false,account_stats_pricing_rules:[],created_at:'2026-10-27T00:00:00Z',updated_at:'2026-10-27T00:00:00Z'};
globalThis.__channelFixture={mutations:[],fail:()=>{failed=true}};
export async function list(){return {items:[channel],total:1}}
export async function getById(){return {...channel}}
export async function create(body){return {...channel,...body}}
export async function update(id,body){Object.assign(channel,body);return {...channel}}
export async function remove(){}
export async function catalog(){return structuredClone({revision,upstreams,upstream_total:upstreams.length,accounts:members,total:members.length})}
export async function mutate(id,body){if(body.revision!==revision)throw {status:409,reason:'CHANNEL_OPERATIONS_CONFLICT'};globalThis.__channelFixture.mutations.push(body);revision++;if(body.action==='detach')members[0].upstream_id=0;if(body.action==='create_upstream')upstreams.push({id:5,name:body.name,members:0});return {revision}}
export async function snapshot(query){
const observed=new Date(Date.now()-300000).toISOString();
const metrics={requests:1604,samples:1320,unknown_samples:88,duration_samples:1578,p50_ms:820,p90_ms:1900,p95_ms:2450,duration_p90_ms:4300,latest_sample_at:observed};
return {scope:query,from:query.from,until:query.until,model_scope:query.model_scope,
usage:failed?{status:'query_failed',data:null,queried_at:new Date().toISOString(),error:'合成查询失败：未伪装成零数据'}:{status:'ready',queried_at:new Date().toISOString(),data:{source:'usage_logs.channel_id / persisted first_token_ms',summary:metrics,total:3,rows:[{...metrics,requests:1260,samples:1040,unknown_samples:32,duration_samples:1244,dimension_id:3,name:'合成上游 Alpha'},{...metrics,requests:320,samples:280,unknown_samples:32,duration_samples:310,p50_ms:640,p90_ms:1200,p95_ms:1400,dimension_id:4,name:'合成上游 Beta'},{...metrics,requests:24,samples:0,unknown_samples:24,duration_samples:24,p50_ms:null,p90_ms:null,p95_ms:null,dimension_id:0,name:'未分配上游'}],lineage:[{public_model:'public-alias',recorded_model:'persisted-model',upstream_model:'provider-model',model_mapping_chain:'public-alias→routing-model→provider-model',samples:1604}],lineage_total:1,error_rate:null}},
quality:{status:'ready',queried_at:new Date().toISOString(),data:{source:'scheduled_test_results: frozen config / judgment',scope:'current account groups',summary:{rounds:19,passed:16,degraded:2,inconclusive:1,latest_sample_at:observed},rows:[{id:100,account_id:42,plan_id:8,measured_model:'provider-model',observed_at:observed,probes:2,passed_probes:1,expected:2,outcome:'degraded',round_complete:true,round_probes:2,round_expected:2,actions:['failure_counted:1/2']},{id:99,account_id:42,plan_id:8,measured_model:'provider-model',observed_at:new Date(Date.now()-3900000).toISOString(),probes:2,passed_probes:2,expected:2,outcome:'passed',round_complete:true,round_probes:2,round_expected:2,actions:['passed']}],has_more:false}},
errors:{status:'ready',queried_at:new Date().toISOString(),data:{source:'ops_error_logs',scope:'current accounts',records:17,upstream_5xx:5,latest_sample_at:observed,rows:[{error_type:'upstream',error_phase:'forward',status_code:502,records:17}],bucket_total:1,error_rate:null}},
capacity:{status:'ready',queried_at:new Date().toISOString(),data:{source:'accounts',scope:'current configuration',accounts:6,schedulable:5,configured_concurrency:30}}};}
`
const settings = `export const useAdminSettingsStore=()=>({requestCaptureEnabled:false})`
// SmartOpsNav reads the signed-in admin. Keep its identity synthetic and avoid
// pulling the real auth store's API barrel into this isolated fixture graph.
const auth = `export const useAuthStore=()=>({isAdmin:true,isObserver:false,user:{id:1,role:'admin'}})`
const shell = `import {h} from 'vue';export default {setup(_,ctx){return ()=>h('main',{class:'mx-auto max-w-7xl p-4 text-gray-900'},ctx.slots.default?.())}}`
const html = `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';import {createI18n} from 'vue-i18n';
import Page from '/src/views/admin/ChannelOperationsView.vue';import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
const router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:Page}]});
createApp(RouterView).use(createPinia()).use(router).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');</script></html>`
process.chdir(frontend)
const server = await createServer({ root: frontend, configFile: false, plugins: [vue(), {
  name: 'channel-operations-offline',
  resolveId(id) { if (id.startsWith('virtual:channel-')) return '\0' + id },
  load(id) { if (id === '\0virtual:channel-fixture') return fixture; if (id === '\0virtual:channel-shell') return shell; if (id === '\0virtual:channel-settings') return settings; if (id === '\0virtual:channel-auth') return auth },
  configureServer(vite) { vite.middlewares.use((req, res, next) => { if (req.url?.split('?')[0] === '/admin/channel-operations') { res.setHeader('Content-Type', 'text/html'); void vite.transformIndexHtml('/admin/channel-operations', html).then(value => res.end(value)); return } next() }) }
}], resolve: { alias: [{ find: '@/api/admin/channelOperations', replacement: 'virtual:channel-fixture' }, { find: '@/api/admin/channels', replacement: 'virtual:channel-fixture' }, { find: '@/components/layout/AppLayout.vue', replacement: 'virtual:channel-shell' }, { find: '@/stores/adminSettings', replacement: 'virtual:channel-settings' }, { find: '@/stores/auth', replacement: 'virtual:channel-auth' }, { find: '@', replacement: path.join(frontend, 'src') }] }, css: { postcss: { plugins: [tailwind({ config: path.join(frontend, 'tailwind.config.js') }), autoprefixer()] } }, server: { host: '127.0.0.1', port: 4183, strictPort: true }, optimizeDeps: { noDiscovery: true, include: ['vue', 'vue-i18n', 'pinia', 'vue-router'] } })
await server.listen()
const browser = await chromium.launch({ headless: true })
const failures = [], consoleErrors = [], apiRequests = []
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } })
  page.on('pageerror', error => failures.push(error.message))
  page.on('console', message => { if (message.type() === 'error') consoleErrors.push(message.text()) })
  await page.route('**/api/**', route => { apiRequests.push(route.request().url()); return route.abort() })
  await page.goto('http://127.0.0.1:4183/admin/channel-operations?channel_id=7', { waitUntil: 'networkidle' })
  await page.getByTestId('usage-section').getByText('1,604', { exact: true }).first().waitFor()
  await page.getByRole('link', { name: 'AI 运维', exact: true }).waitFor()
  await page.screenshot({ path: path.join(output, 'desktop-synthetic.png'), fullPage: true })
  await page.getByText('持久化模型谱系（最多 50 组）', { exact: false }).click()
  await page.getByText('public-alias', { exact: true }).waitFor()
  await page.screenshot({ path: path.join(output, 'model-lineage-synthetic.png'), fullPage: true })
  await page.getByRole('button', { name: '解除归属', exact: true }).click()
  if (await page.evaluate(() => globalThis.__channelFixture.mutations.length)) throw new Error('mutation_before_confirmation')
  await page.getByRole('button', { name: '确认执行', exact: true }).click()
  await page.getByRole('status').filter({ hasText: '已保存' }).waitFor()
  await page.getByRole('dialog').waitFor({ state: 'hidden' })
  const mutations = await page.evaluate(() => globalThis.__channelFixture.mutations)
  if (mutations.length !== 1 || mutations[0].expected_upstream_id !== 3 || mutations[0].revision !== 8 || !mutations[0].confirm) throw new Error('invalid_confirmed_mutation')
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: path.join(output, 'mobile-synthetic.png'), fullPage: true })
  const width = await page.evaluate(() => ({ body: document.body.scrollWidth, viewport: innerWidth }))
  if (width.body > width.viewport + 2) throw new Error('mobile_page_horizontal_overflow')
  await page.evaluate(() => globalThis.__channelFixture.fail())
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await page.getByText('合成查询失败：未伪装成零数据').waitFor()
  if (await page.getByTestId('usage-section').locator('dl').count()) throw new Error('query_failure_displayed_as_numeric_data')
  await page.screenshot({ path: path.join(output, 'query-failure-synthetic.png'), fullPage: true })
  if (failures.length || apiRequests.length) throw new Error('browser_errors_or_unexpected_api_requests: ' + failures.join('; '))
  await fs.writeFile(path.join(output, 'verification.json'), JSON.stringify({ commit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), synthetic: true, actualComponent: 'ChannelOperationsView.vue', confirmedMutations: mutations, liveApiCalls: 0, paidRequests: 0, browserErrors: failures, mobileWidth: width }, null, 2))
} catch (error) {
  await fs.writeFile(path.join(output, 'browser-failures.json'), JSON.stringify({ browserErrors: failures, consoleErrors, unexpectedApiRequests: apiRequests }, null, 2))
  await fs.writeFile(path.join(output, 'failure.json'), JSON.stringify({ error: String(error), browserErrors: failures, apiRequests }, null, 2))
  for (const page of browser.contexts().flatMap(context => context.pages())) {
    await page.screenshot({ path: path.join(output, 'failure-synthetic.png'), fullPage: true })
    await fs.writeFile(path.join(output, 'failure.txt'), String(error) + '\n' + await page.locator('body').innerText())
  }
  throw error
} finally { await browser.close(); await server.close() }
