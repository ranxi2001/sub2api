// Render the actual user UsageView, Select and error table with project CSS.
// Only API responses and unrelated layout/charts use fictional fixtures.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [repoArg, outputArg] = process.argv.slice(2)
const repo = resolve(repoArg), root = join(repo, 'frontend'), output = resolve(outputArg)
const req = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const sha = git('rev-parse', 'HEAD'), baseline = '3137257829f0cc48466911aa4a249dd03f3c088f'
mkdirSync(output, { recursive: true })
const scratch = mkdtempSync(join(output, 'vite-'))
const entryName = `.http2-filter-preview-${process.pid}.js`, entry = join(root, entryName)
const origin = 'http://127.0.0.1:4298'
const errors = [], captures = [], requests = []
let before = true, server, browser
try {
  writeFileSync(entry, `
import {mount} from '@vue/test-utils';
import {h} from 'vue';
import {createPinia,setActivePinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import {useAppStore} from '/src/stores/app.ts';
import View from '/src/views/user/UsageView.vue';
import '/src/style.css';
const locale=new URL(location.href).searchParams.get('locale')||'en';
await loadLocaleMessages(locale);i18n.global.locale.value=locale;
const pinia=createPinia();setActivePinia(pinia);
useAppStore().cachedPublicSettings={allow_user_view_error_requests:true};
window.filterRequests=[];
const rows=[
{id:833,created_at:'2026-11-13T14:00:00Z',model:'example-reset-model',inbound_endpoint:'/v1/responses',status_code:200,category:'transport_error',platform:'openai',message:'Upstream connection reset by peer. Content may be incomplete. Do not automatically retry the same request.',key_name:'Demo key',key_deleted:false,stream:true,request_type:2},
{id:834,created_at:'2026-11-13T14:00:00Z',model:'example-http-model',inbound_endpoint:'/v1/responses',status_code:502,category:'upstream',platform:'openai',message:'Synthetic upstream HTTP refusal',key_name:'Demo key',key_deleted:false,stream:false,request_type:1}
];
apiClient.defaults.adapter=async request=>{
window.filterRequests.push({method:request.method,url:request.url,params:structuredClone(request.params||{})});
let data;
switch(request.url){
case '/keys':data={items:[{id:833,name:'Demo key'}],total:1,page:1,page_size:100,pages:1};break;
case '/groups/available':data=[];break;
case '/usage':data={items:[],total:0,page:1,page_size:20,pages:0};break;
case '/usage/stats':data={total_requests:0,total_input_tokens:0,total_output_tokens:0,total_cache_tokens:0,total_tokens:0,total_cost:0,total_actual_cost:0,average_duration_ms:0,endpoints:[],upstream_endpoints:[],endpoint_paths:[]};break;
case '/usage/dashboard/models':data={models:[]};break;
case '/usage/dashboard/snapshot-v2':data={trend:[],groups:[]};break;
case '/usage/errors':{
const selected=rows.filter(row=>(!request.params?.category||row.category===request.params.category)&&(request.params?.status_code==null||row.status_code===request.params.status_code));
data={items:selected,total:selected.length,page:1,page_size:20,pages:1};break;
}
default:throw Error('Unexpected fixture API '+request.url);
}
return {config:request,status:200,statusText:'OK',headers:{},data:{code:0,data}};
};
window.preview=mount(View,{attachTo:'#app',global:{plugins:[pinia,i18n],stubs:{
AppLayout:{setup(_, {slots}){return()=>h('main',{class:'min-h-screen bg-gray-50 p-6 text-gray-900'},slots.default?.())}},
UsageStatsCards:true,ModelDistributionChart:true,GroupDistributionChart:true,EndpointDistributionChart:true,TokenUsageTrend:true,TrafficPanel:true,UsageTable:true
}}});
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({
    root, configFile: false, cacheDir: scratch,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'http2-filter-preview', enforce: 'pre', load(id) {
      const clean = id.split('?')[0]
      if (before && !id.includes('?') && clean === join(root, 'src/views/user/UsageView.vue')) return git('show', baseline + ':frontend/src/views/user/UsageView.vue')
    }, configureServer(vite) {
      vite.middlewares.use((request, response, next) => {
        if (request.url === '/' || request.url?.startsWith('/?')) {
          response.setHeader('Content-Type', 'text/html')
          response.end('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/' + entryName + '"></script>')
        } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
      })
    } }, vue()],
    server: { host: '127.0.0.1', port: 4298, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'vue-router', 'axios', '@vue/test-utils', 'chart.js', 'marked', 'dompurify'] }
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1440, height: 1050 }, timezoneId: 'UTC' })
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
  await page.route('**/*', route => {
    if (new URL(route.request().url()).origin === origin) return route.continue()
    errors.push('Unexpected external request'); return route.abort()
  })
  for (const state of ['before', 'after']) {
    before = state === 'before'
    server.moduleGraph.invalidateAll()
    for (const locale of ['en', 'zh']) {
      await page.goto(origin + '/?locale=' + locale, { waitUntil: 'networkidle' })
      await page.getByRole('button', { name: locale === 'en' ? 'Error Requests' : '错误请求', exact: true }).click()
      const categoryLabel = locale === 'en' ? 'Category' : '分类'
      const statusLabel = locale === 'en' ? 'Status' : '状态码'
      const control = label => page.locator('.input-label').filter({ hasText: new RegExp('^' + label + '$') }).locator('..').getByRole('button').first()
      await control(categoryLabel).click()
      await page.getByRole('option', { name: locale === 'en' ? 'Transport error' : '传输错误', exact: true }).click()
      await page.getByText('example-reset-model', { exact: true }).waitFor()
      await control(statusLabel).click()
      const optionLabel = locale === 'en' ? '200 — Stream failed after headers' : '200 — 响应头发送后流失败'
      const http200 = page.getByRole('option', { name: optionLabel, exact: true })
      if (before) {
        if (await http200.count()) throw Error('Baseline unexpectedly offers HTTP200')
      } else {
        await http200.waitFor()
      }
      const dropdownName = `${state}-status-options-${locale}.png`
      await page.screenshot({ path: join(output, dropdownName), fullPage: true })
      captures.push({ name: dropdownName, componentSha: before ? baseline : sha, locale })
      if (before) {
        await page.getByRole('listbox').press('Escape')
      } else {
        await http200.click()
        await page.waitForFunction(() => window.filterRequests.some(r => r.url === '/usage/errors' && r.params.category === 'transport_error' && r.params.status_code === 200))
        await page.getByText('example-reset-model', { exact: true }).waitFor()
        if (await page.getByText('example-http-model', { exact: true }).count()) throw Error('Filtered HTTP200 result includes HTTP502 fixture')
        const selectedName = `after-filtered-${locale}.png`
        await page.screenshot({ path: join(output, selectedName), fullPage: true })
        captures.push({ name: selectedName, componentSha: sha, locale })
        await control(statusLabel).click()
        await page.getByRole('option', { name: '502', exact: true }).click()
        await page.waitForFunction(() => window.filterRequests.some(r => r.url === '/usage/errors' && r.params.category === 'transport_error' && r.params.status_code === 502))
      }
      const observed = await page.evaluate(() => window.filterRequests)
      if (observed.some(r => r.url === '/usage' && (r.params.status_code !== undefined || r.params.category !== undefined))) throw Error('Error filters leaked into normal usage requests')
      requests.push({ state, locale, observed })
    }
  }
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ sha, baseline, captures, requests, errors,
    boundary: 'Real user UsageView, Select, error table, API modules, Axios parameter construction and CSS; synthetic API responses; unrelated layout/charts stubbed. Baseline UsageView cannot choose200; candidate chooses transport+200 and submits both filters. No production traffic.' }, null, 2))
  if (errors.length) throw Error(errors.join('\n'))
  console.log('PASS real UsageView transport+HTTP200 filtering and API arguments at ' + sha)
} finally {
  await browser?.close(); await server?.close()
  rmSync(entry, { force: true }); rmSync(scratch, { recursive: true, force: true })
}
