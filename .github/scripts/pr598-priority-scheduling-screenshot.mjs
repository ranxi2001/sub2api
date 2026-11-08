// Remote CI only. The actual admin component receives synthetic, offline API data.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'

const root = process.cwd()
const frontend = path.join(root, 'frontend')
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue')
const vue = vueModule.default || vueModule
const tailwind = requireFrontend('tailwindcss')
const autoprefixer = requireFrontend('autoprefixer')
const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const output = process.env.PR598_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })

const fixture = `
import { apiClient } from '/src/api/client.ts'
const config = { error_rate_weight:25,error_rate_window_minutes:10,error_rate_prior_samples:5,oauth_quota_priority:false,oauth_quota_threshold:90,balance_protocols:true,enabled:true,mode:'balanced',group_ids:[11],models:['fixture-model'],window_minutes:60,min_samples:5,target_ttft_ms:3000,latency_percentile:50,latency_measurement:'attributed_v1',latency_penalty_mode:'deprioritize',latency_penalty_minutes:10,max_load_percent:80,min_quality_percent:90,quality_max_age_hours:24,quality_weight:30,latency_weight:25,load_weight:25,cost_weight:20 }
const now='2026-10-30T00:00:00Z'
const candidate={account_id:701,account_name:'Synthetic OAuth account',score:81,tier:'eligible',reasons:[],priority:10,concurrency:8,load_factor:8,load_percent:20,waiting:0,rate:1,revenue:0,theoretical_cost:0,profit:null,margin:null,economics_source:'unknown',profit_samples:0,samples:12,p50_ttft_ms:480,p70_ttft_ms:600,p90_ttft_ms:1450,p95_ttft_ms:2100,p99_ttft_ms:2900,selected_ttft_ms:480,latency_percentile:50,latency_measurement:'attributed_v1',latency_unknown_samples:0,experience_samples:12,experience_p90_ms:2300,slow_samples:1,error_samples:1,quality_passed:11,quality_samples:12,error_rate:0.08,error_successes:11,error_failures:1,error_prior_samples:5,error_window_minutes:10,error_observed_at:now,error_endpoint:'/v1/responses',error_penalty:.94,base_score:86,selection_weight:1.2,bound_groups:1}
const snapshot={at:now,evaluated_at:now,model:'fixture-model',group_id:11,mode:'balanced',latency_percentile:50,latency_measurement:'attributed_v1',min_samples:5,window_minutes:60,config_revision:'synthetic',history_ready:true,history_status:'ready',history_refreshing:false,candidates:[candidate]}
const bucket=(time,selected,p50,p90)=>({account_id:701,bucket:time,samples:6,p50_ttft_ms:p50,p70_ttft_ms:Math.round((p50+p90)/2),p90_ttft_ms:p90,p95_ttft_ms:p90,p99_ttft_ms:p90,mean_ttft_ms:Math.round((p50+p90)/2),selected_ttft_ms:selected,slow_samples:0,error_samples:0})
const latency={config_revision:'synthetic',at:now,model:'fixture-model',group_id:11,bucket_minutes:5,latency_percentile:50,latency_measurement:'attributed_v1',accounts:[{account_id:701,account_name:candidate.account_name,samples:12,p50_ttft_ms:480,p70_ttft_ms:650,p90_ttft_ms:1450,p95_ttft_ms:2100,p99_ttft_ms:2900,selected_ttft_ms:480,latency_percentile:50,latency_measurement:'attributed_v1',slow_samples:1,error_samples:1,error_rate:.08,buckets:[bucket('2026-10-30T00:00:00Z',480,480,1450),bucket('2026-10-30T00:05:00Z',620,620,1600)]}]}
apiClient.defaults.adapter=async request=>{const url=request.url;let data=[];if(url.endsWith('/config'))data=config;else if(url.endsWith('/models'))data={models:['fixture-model']};else if(url.endsWith('/groups/all'))data=[{id:11,name:'Synthetic group'}];else if(url.endsWith('/analysis')){if(request.params.model!=='fixture-model'||request.params.group_id!==11)throw Error('Unexpected analysis scope');data={snapshot:{...snapshot,window_minutes:request.params.minutes},latency}}else if(url.endsWith('/snapshot'))data={snapshot};else if(url.endsWith('/latency'))data=latency;else if(url.endsWith('/sticky-bindings'))data={group_id:11,total:3,by_account:[{account_id:701,count:3}]};else if(url.endsWith('/settings'))data={run_mode:'simple'};else if(url.endsWith('/groups'))data=[];return {data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request}}
globalThis.__pr598Fixture={config,snapshot,latency}
`
const shell = `import { h } from 'vue'; export default {setup(_,ctx){return()=>h('main',{class:'mx-auto max-w-7xl p-4'},ctx.slots.default?.())}}`
const html = `<!doctype html><html lang="en"><meta charset="utf-8"><div id="app"></div><script type="module">
import 'virtual:pr598-fixture';import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';import {createI18n} from 'vue-i18n';import {useAuthStore} from '/src/stores/auth.ts';import View from '/src/views/admin/PrioritySchedulingView.vue';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
const pinia=createPinia(),router=createRouter({history:createWebHistory(),routes:[{path:'/admin/priority-scheduling',component:View}]});const app=createApp(RouterView);app.use(pinia);useAuthStore().user={id:1,email:'synthetic@test.invalid',role:'admin',status:'active',balance:0,concurrency:1};app.use(router).use(createI18n({legacy:false,locale:'en',messages:{en}})).mount('#app')</script></html>`

process.chdir(frontend)
const server = await createServer({
  root: frontend,
  configFile: false,
  plugins: [vue(), {
    name: 'pr598-offline-fixture',
    resolveId(id) { if (id === 'virtual:pr598-fixture' || id === 'virtual:pr598-shell') return `\0${id}` },
    load(id) { if (id === '\0virtual:pr598-fixture') return fixture; if (id === '\0virtual:pr598-shell') return shell },
    configureServer(vite) {
      vite.middlewares.use((req, res, next) => {
        if (req.url?.split('?')[0] === '/admin/priority-scheduling') {
          res.setHeader('Content-Type', 'text/html')
          void vite.transformIndexHtml(req.url, html).then(value => res.end(value))
          return
        }
        next()
      })
    }
  }],
  resolve: { alias: [
    { find: '@/components/layout/AppLayout.vue', replacement: 'virtual:pr598-shell' },
    { find: '@/components/admin/operations/SmartOpsNav.vue', replacement: 'virtual:pr598-shell' },
    { find: '@', replacement: path.join(frontend, 'src') }
  ] },
  css: { postcss: { plugins: [tailwind({ ...theme, content: [path.join(frontend,'index.html'),path.join(frontend,'src/**/*.{vue,js,ts,jsx,tsx}'),path.join(root,'.github/scripts/pr598-priority-scheduling-screenshot.mjs')] }), autoprefixer()] } },
  server: { host: '127.0.0.1', port: 4186, strictPort: true },
  optimizeDeps: { noDiscovery: true, include: ['vue','vue-i18n','pinia','vue-router','axios','@vueuse/core','@tanstack/vue-virtual','chart.js','vue-chartjs','dompurify','qrcode','driver.js'] }
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } })
const errors = [], blocked = []
page.on('pageerror', error => errors.push(error.message))
await page.route('**/*', route => {
  const url = new URL(route.request().url())
  if (url.hostname !== '127.0.0.1') { blocked.push(route.request().url()); return route.abort() }
  return route.continue()
})
try {
  await page.goto('http://127.0.0.1:4186/admin/priority-scheduling', { waitUntil: 'networkidle' })
  await page.getByTestId('latency-panel').waitFor()
  await page.getByTestId('sticky-scope').waitFor()
  await page.getByTestId('priority-snapshot-at').waitFor()
  const css = await page.getByTestId('candidate-table').evaluate(el => ({ overflow: getComputedStyle(el).overflowX, whiteSpace: getComputedStyle(el.querySelector('tbody td')).whiteSpace, width: el.querySelector('table').getBoundingClientRect().width }))
  if (css.overflow !== 'auto' || css.whiteSpace !== 'nowrap' || css.width < 1200) throw Error('Actual project styles were not generated: '+JSON.stringify(css))
  await page.getByTestId('latency-toggle').click()
  await page.getByTestId('bucket-details').locator('summary').click()
  const selectedHeatmapCell = await page.getByTestId('latency-buckets').locator('tbody tr').first().locator('td').nth(1).innerText()
  if (selectedHeatmapCell !== '480') throw new Error(`Configured P50 heatmap should show 480 ms, got ${selectedHeatmapCell}`)
  if (!(await page.getByTestId('sticky-scope-account').innerText()).includes('#701')) throw new Error('Sticky binding scope does not identify the account')
  await page.screenshot({ path: path.join(output, 'priority-scheduling-desktop.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: path.join(output, 'priority-scheduling-mobile.png'), fullPage: true })
  if (errors.length || blocked.length) throw new Error(JSON.stringify({ errors, blocked }))
  await fs.writeFile(path.join(output, 'priority-scheduling-verification.json'), JSON.stringify({
    commit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(),
    syntheticFixture: true, externalRequests: 0, browserErrors: errors
  }, null, 2) + '\n')
} catch (error) {
  await page.screenshot({ path: path.join(output, 'priority-scheduling-failure.png'), fullPage: true })
  await fs.writeFile(path.join(output, 'priority-scheduling-failure.txt'), String(error) + '\n' + JSON.stringify({ errors, blocked }) + '\n' + await page.locator('body').innerText())
  throw error
} finally {
  await browser.close()
  await server.close()
}
