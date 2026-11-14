import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const repo = resolve('.'), root = join(repo, 'frontend'), output = process.env.EVIDENCE_DIR
const { createServer } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')))
const { default: vue } = await import(pathToFileURL(join(root, 'node_modules/@vitejs/plugin-vue/dist/index.mjs')))
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const baseline = 'ea1f3a28d', head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const entryName = `.account-alert-preview-${process.pid}.js`, entry = join(root, entryName)
const cacheDir = mkdtempSync(join(tmpdir(), 'account-alert-ui-')), origin = 'http://127.0.0.1:4298'
let before = true, server, browser
const errors = []
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import {createApp,h,ref} from 'vue';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import Rules from '/src/components/admin/operations/AccountOpsRuleList.vue';
import Global from '/src/components/admin/operations/AccountOpsGlobalSettings.vue';
import Dialog from '/src/components/admin/operations/AccountOpsBatchRuleDialog.vue';
import '/src/style.css';
await loadLocaleMessages('zh');i18n.global.locale.value='zh';
if(location.search.includes('dark'))document.documentElement.classList.add('dark');
const accounts=ref([
{account_id:1,account_name:'示例余额账号',platform:'openai',type:'apikey',balance:null,unit:'USD',balance_status:'stale',probe_enabled:false,last_known_balance:120,received_at:'2026-01-01T08:00:00Z',usage_windows:[]},
{account_id:2,account_name:'示例号池',platform:'openai',type:'apikey',balance:null,unit:'USD',balance_status:'unknown',probe_enabled:false,received_at:null,usage_windows:[]}
]);
const groups=accounts.value.map(a=>({id:'demo-'+a.account_id,name:a.account_id===1?'示例站点':'示例号池',site:'https://example.test',provider:'sub2api',mode:'manual',account_ids:[a.account_id]}));
const config={enabled:true,recipient:'',balance_low:true,weekly_quota:true,cooldown_minutes:60,balance_thresholds:accounts.value.map(a=>({account_id:a.account_id,enabled:true,threshold:50,unit:'USD',notify_alert:true,notify_recovery:true}))};
window.__requests=[];
apiClient.defaults.adapter=async c=>{
window.__requests.push({method:c.method,url:c.url,data:c.data});
let data;
if(c.method==='get'&&c.url.endsWith('/settings'))data={enabled:true,interval_minutes:30};
else if(c.method==='put')data={};
else if(c.method==='post')data={account_id:1,snapshot:{balance:{status:'ok'}}};
else throw Error('Unexpected mock request');
return {config:c,status:200,statusText:'OK',headers:{},data:{code:0,data}};
};
const selected=ref([]);
createApp({setup(){return()=>h('main',{class:'p-6 space-y-5'},[
h('p',{class:'text-sm text-gray-500'},'虚构数据 · 实际规则组件预览'),
h(Rules,{accounts:accounts.value,groups,config,loading:false,ready:true,error:'',onBatchEdit:a=>selected.value=a,onRefresh:()=>accounts.value[0]={...accounts.value[0],probe_enabled:true,balance_status:'ok',balance:118,last_known_balance:undefined}}, {'global-settings':()=>h(Global,{config})}),
h(Dialog,{show:selected.value.length>0,accounts:selected.value,config,onClose:()=>selected.value=[]})
])}}).use(createPinia()).use(i18n).mount('#app');
`)
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'account-alert-preview', enforce: 'pre', load(id) {
      const file = 'frontend/src/components/admin/operations/AccountOpsRuleList.vue'
      if (before && id === join(repo, file)) return execFileSync('git', ['show', `${baseline}:${file}`], { encoding: 'utf8' })
    }, configureServer(vite) { vite.middlewares.use((req, res, next) => {
      if (req.url === '/' || req.url?.startsWith('/?')) { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module" src="/'+entryName+'"></script></body></html>') }
      else if (req.url === '/favicon.ico') { res.statusCode=204;res.end() } else next()
    }) } }, vue()], server: { host: '127.0.0.1', port: 4298, strictPort: true }, optimizeDeps: { entries: [], include: ['vue','pinia','vue-i18n','axios'] } })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1360, height: 1000 }, timezoneId: 'UTC' })
  page.on('pageerror', e => errors.push(e.message))
  await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
  const load = async query => { await page.goto(origin+'/'+query, { waitUntil:'networkidle' });await page.getByTestId('account-ops-rules').waitFor(); assert.equal(await page.getByTestId('account-ops-rules').evaluate(el=>getComputedStyle(el).borderTopWidth),'1px','project Tailwind styles must be generated') }
  await load('');await page.screenshot({path:join(output,'before.png'),fullPage:true})
  before=false;server.moduleGraph.invalidateAll()
  await load('');await page.screenshot({path:join(output,'after.png'),fullPage:true})
  assert.equal(await page.getByTestId('failure-settings').getAttribute('open'),null)
  assert.match(await page.getByTestId('balance-status-1').textContent(),/自动采集未开启/)
  await page.getByTestId('refresh-balance-1').click()
  await page.getByTestId('balance-status-1').getByText('118.00 USD',{exact:true}).waitFor()
  const calls=await page.evaluate(()=>window.__requests)
  assert.deepEqual(calls.map(c=>c.method),['get','put','post'])
  await page.getByTestId('edit-rule-demo-1').click()
  await page.getByTestId('batch-rule-threshold').waitFor()
  assert.equal(await page.getByTestId('batch-rule-threshold').inputValue(),'50')
  await page.waitForFunction(()=>{
    const panel=document.querySelector('.modal-content'), overlay=document.querySelector('.modal-overlay')
    return panel && overlay && getComputedStyle(panel).opacity==='1' && getComputedStyle(overlay).opacity==='1' && !overlay.classList.contains('modal-enter-active')
  })
  await page.getByTestId('batch-rule-threshold').fill('55')
  assert.equal(await page.getByTestId('batch-rule-threshold').inputValue(),'55')
  await page.screenshot({path:join(output,'edit.png'),fullPage:true})
  await page.getByTestId('cancel-batch-rules').click()
  await page.locator('[role="dialog"]').waitFor({state:'hidden'})
  await page.setViewportSize({width:390,height:844});await load('?dark')
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true)
  await page.screenshot({path:join(output,'mobile-dark.png'),fullPage:true})
  assert.deepEqual(errors,[])
  writeFileSync(join(output,'evidence.json'),JSON.stringify({head,baseline,scope:'actual rule components, fictional data and mocked APIs; not production verification',calls,errors},null,2))
} finally {
  await browser?.close();await server?.close();rmSync(entry,{force:true});rmSync(cacheDir,{recursive:true,force:true})
}
