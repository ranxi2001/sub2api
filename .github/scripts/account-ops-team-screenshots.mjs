import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'
const root = resolve('frontend'), out = process.env.EVIDENCE_DIR
const { createServer } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')))
const { default: vue } = await import(pathToFileURL(join(root, 'node_modules/@vitejs/plugin-vue/dist/index.mjs')))
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const baseline = 'ce45c5a85f2a68544994ac146f3f809f31308e1a'
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const origin = 'http://127.0.0.1:4309', entry = join(root, '.account-ops-team-preview.js')
const original = new Map(['AccountOpsEmailDialog.vue', 'AccountOpsRuleList.vue'].map(name => [join(root, 'src/components/admin/operations', name), execFileSync('git', ['show', `${baseline}:frontend/src/components/admin/operations/${name}`], { encoding: 'utf8' })]))
async function settleDialog(page) {
 await page.waitForFunction(() => {
  const panel=document.querySelector('.modal-content'), overlay=panel?.parentElement
  if(!panel||!overlay||overlay.className.includes('modal-enter'))return false
  const bounds=panel.getBoundingClientRect()
  return Number(getComputedStyle(overlay).opacity)===1 && Number(getComputedStyle(panel).opacity)===1 && bounds.width>0 && bounds.height>0 && bounds.top>=0 && bounds.bottom<=innerHeight
 })
}
let before = true, server, browser
mkdirSync(out, { recursive: true })
try {
 writeFileSync(entry, `
import {createApp,h,ref} from 'vue';import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';
import Rules from '/src/components/admin/operations/AccountOpsRuleList.vue';
import Email from '/src/components/admin/operations/AccountOpsEmailDialog.vue';
import Groups from '/src/components/admin/operations/AccountOpsGroupDialog.vue';
import '/src/style.css';
const language=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(language);i18n.global.locale.value=language;
if(location.search.includes('dark'))document.documentElement.classList.add('dark');
const before=location.search.includes('before');
const accounts=[1,2].map(id=>({account_id:id,account_name:'示例供应商 Key '+id,platform:'openai',type:'apikey',provider_id:before?undefined:7,balance:35,unit:'USD',balance_status:'ok',probe_enabled:true,received_at:'2026-01-01T08:00:00Z',usage_windows:[]}));
accounts.push({account_id:3,account_name:'示例临时 OAuth',platform:'openai',type:'oauth',oauth_lifecycle:'temporary',balance:null,unit:'%',balance_status:'unknown',received_at:null,usage_windows:[]});
accounts.push({account_id:4,account_name:'示例长期 OAuth',platform:'openai',type:'oauth',oauth_lifecycle:'long_term',balance:null,unit:'%',balance_status:'unknown',received_at:null,usage_windows:[]});
const groups=[{id:'manual-provider-7',name:'示例供应商',provider_id:before?undefined:7,provider:'provider',site:'https://supplier.example.test',mode:'manual',account_ids:[1,2]},...accounts.slice(2).map(a=>({id:'manual-oauth-'+a.account_id,name:a.account_name,provider:'manual',site:'',mode:'manual',account_ids:[a.account_id]}))];
const config={enabled:true,recipient:before?'ops@example.test':'ops@example.test, oncall@example.test',email_name:'值班团队',balance_low:true,weekly_quota:true,cooldown_minutes:60,groups,balance_thresholds:[1,2].map(account_id=>({account_id,enabled:true,threshold:50,unit:'USD',notify_alert:true,notify_recovery:true}))};
apiClient.defaults.adapter=async c=>({config:c,status:200,statusText:'OK',headers:{},data:{code:0,data:c.url.includes('/providers')?{items:[{id:7,name:'示例供应商'}],total:1}:config}});
const email=ref(false),group=ref(false);
createApp({setup(){return()=>h('main',{class:'p-6 space-y-4'},[
 h('p',{class:'text-sm text-gray-500'},'虚构数据 · 实际账号运维组件'),
 h('button',{'data-testid':'open-email',class:'btn btn-primary',onClick:()=>email.value=true},'配置收件邮箱'),
 h('button',{'data-testid':'open-groups',class:'btn btn-secondary',onClick:()=>group.value=true},'关联供应商'),
 h(Rules,{accounts,groups,config,userId:42,loading:false,ready:true,error:''}),
 h(Email,{show:email.value,config,smtpConfigured:true,userId:42,onClose:()=>email.value=false}),
 h(Groups,{show:group.value,groups,accounts,onClose:()=>group.value=false})
])}}).use(createPinia()).use(i18n).mount('#app');
 `)
 process.chdir(root) // Tailwind resolves the project theme relative to cwd.
 server = await createServer({ root, configFile: false, server: { host: '127.0.0.1', port: 4309, strictPort: true }, resolve: { alias: { '@': join(root, 'src') } }, plugins: [{ name: 'baseline-components', enforce: 'pre', load(id) { if (before && original.has(id)) return original.get(id) }, configureServer(vite) {
  vite.middlewares.use((req, res, next) => {
   if (req.url?.split('?')[0] !== '/preview') return next()
   res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/.account-ops-team-preview.js"></script></html>')
  })
 } }, vue()] })

 await server.listen(); browser = await chromium.launch({ headless: true })
 const captures = []
 for (const language of ['zh', 'en']) for (const phase of ['before', 'after']) {
  before = phase === 'before'; server.moduleGraph.invalidateAll()
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  await context.route('**/*', route => route.request().url().startsWith(origin) ? route.continue() : route.abort())
  const page = await context.newPage(), errors = []
  page.on('pageerror', e => { errors.push(e.message); console.error('preview page error:', e.message) })
  await page.goto(`${origin}/preview?${phase}&lang=${language}`, { waitUntil: 'networkidle' })
  await page.getByTestId('account-ops-rules').waitFor()
  await page.screenshot({ path: join(out, `${phase}-${language}-rules.png`), fullPage: true, animations: 'disabled' })
  await page.getByTestId('open-email').click(); await page.locator('#account-ops-email').waitFor(); await settleDialog(page)
  await page.screenshot({ path: join(out, `${phase}-${language}-email.png`), fullPage: true, animations: 'disabled' })
  if (phase === 'after') {
   if (await page.locator('textarea#account-ops-email').count() !== 1) throw new Error('multiple-email editor missing')
   await page.reload({ waitUntil: 'networkidle' })
   await page.getByTestId('open-groups').click()
   await page.getByTestId('ops-provider-select').first().evaluate(el => { el.closest('details').open = true })
   await page.getByTestId('ops-provider-select').first().focus()
   await page.getByTestId('ops-provider-select').first().locator('option').filter({ hasText: '示例供应商' }).waitFor({ state: 'attached' })
   await settleDialog(page)
   await page.screenshot({ path: join(out, `after-${language}-provider-link.png`), fullPage: true, animations: 'disabled' })
  }
  if (errors.length) throw new Error(errors.join('\n'))
  captures.push({ phase, language, source: before ? baseline : head }); await context.close()
 }
 writeFileSync(join(out, 'source.json'), JSON.stringify({ head, baseline, captures, scope: 'Actual Vue components with synthetic data; all external requests blocked' }, null, 2))
} finally { await browser?.close(); await server?.close(); rmSync(entry, { force: true }) }
