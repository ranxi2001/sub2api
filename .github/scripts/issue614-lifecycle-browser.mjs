// Execute only on GitHub's synthetic runner. No application backend/upstream.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const output = process.env.ISSUE614_BROWSER_DIR
await fs.mkdir(output, { recursive: true })
const fixture = `
import { apiClient } from '/src/api/client.ts'
const now = new Date().toISOString(), until = new Date(Date.now() + 2000).toISOString()
const policy = {account_id:7,state:'PENALIZED_SHADOW',evidence_id:'synthetic-immutable-evidence',evidence_at:now,expires_at:until,policy_revision:'original-policy',observed_value:16000,threshold:15000,percentile:90,mode:'stop',rule:'legacy shadow; v2 trigger contract pending'}
window.__offline = false
window.__lifecycleRequests = []
apiClient.defaults.adapter = async request => {
  window.__lifecycleRequests.push({url:request.url,params:request.params})
  if(window.__offline) throw new Error('synthetic offline')
  const p=request.params, scope={group_id:p.group_id==='ungrouped'?null:Number(p.group_id),model:p.model,endpoint:p.endpoint,request_type:Number(p.request_type),measurement_schema:'ingress_alias_v1/legacy_forward',statistics_spec:'synthetic-statistics-v1'}
  let data={scope,server_now:new Date().toISOString(),policy_revision:'current-policy',mode:'shadow',last_error:'',attempted_at:now,statistics:{scope,generation:4,watermark:6,observed_at:now,window_start:now,window_end:now,policy_revision:'current-policy',signals:{}},accounts:[
    {account_id:7,account_name:'Synthetic disabled account',configuration:'ADMIN_DISABLED',admission:'BLOCKED',reasons:['account_not_schedulable'],content:'VALID',freshness:'FRESH',refresh:'IDLE',signal:{samples:20,latency_percentile:70,selected_ttft_ms:null,p90_ttft_ms:42500,latency_unknown_samples:1},policy},
    {account_id:8,account_name:'Synthetic unknown account',configuration:'ENABLED',admission:'NOT_EVALUATED',reasons:['shadow_only'],content:'NO_OBSERVATIONS',freshness:'UNKNOWN',refresh:'IDLE',signal:null,policy:null}]}
  if(request.url.endsWith('/decisions'))data={items:[{sequence:1,decision_id:'synthetic-decision',parent_decision_id:'synthetic-parent',trace_id:'synthetic-trace',scope,policy_revision:'decision-policy',statistics_generation:3,watermark:5,selected_account_id:7,at:now,phase:'SELECTED',allowed:false,reason:'synthetic restriction',mode:'shadow'}]}
  if(request.url.endsWith('/policy-events'))data={items:[{sequence:1,kind:'OBSERVED_SHADOW',event_at:now,recorded_at:now,policy}]}
  if(p.model==='slow-old-scope')await new Promise(resolve=>setTimeout(resolve,800))
  return {data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request}
}
`
const html = `<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module">
import 'virtual:lifecycle-fixture';import {createApp} from 'vue';import {createPinia} from 'pinia';import {createI18n} from 'vue-i18n';import {useAuthStore} from '/src/stores/auth.ts';import View from '/src/components/admin/PriorityLifecyclePanel.vue';import en from '/src/i18n/locales/en/index.ts';import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
const app=createApp(View),pinia=createPinia();app.use(pinia);useAuthStore().user={id:1,email:'synthetic@test.invalid',role:'admin',status:'active',balance:0,concurrency:1};app.use(createI18n({legacy:false,locale:new URLSearchParams(location.search).get('lang')||'en',messages:{en,zh}})).mount('#app')</script></html>`
process.chdir(frontend)
const server = await createServer({
  root: frontend, configFile: false,
  plugins: [vue(), {
    name: 'issue614-synthetic-fixture',
    resolveId(id) { if (id === 'virtual:lifecycle-fixture') return '\0' + id },
    load(id) { if (id === '\0virtual:lifecycle-fixture') return fixture },
    configureServer(vite) { vite.middlewares.use((req,res,next) => {
      if(req.url?.split('?')[0] !== '/lifecycle') return next()
      vite.transformIndexHtml(req.url,html).then(value=>{res.setHeader('Content-Type','text/html');res.end(value)}).catch(next)
    }) },
  }],
  resolve: { alias: { '@': path.join(frontend, 'src') } },
  css: { postcss: { plugins: [requireFrontend('tailwindcss')(),requireFrontend('autoprefixer')()] } },
  server: { host: '127.0.0.1', port: 0 },
  optimizeDeps: { noDiscovery: true, include: ['vue','vue-i18n','pinia','axios','vue-router','@vueuse/core'] },
})
await server.listen()
const browser = await chromium.launch({headless:true}), results=[], errors=[], blocked=[]
let activePage,activePrefix='startup'
try {
  for(const lang of ['zh','en']) for(const viewport of [{width:1440,height:1000},{width:390,height:844}]) {
    const page=await browser.newPage({viewport})
    activePage=page;activePrefix=lang+'-'+viewport.width
    page.on('pageerror',error=>errors.push(String(error)))
    await page.route('**/*',route=>{
      if(new URL(route.request().url()).hostname!=='127.0.0.1'){blocked.push(route.request().url());return route.abort()}
      return route.continue()
    })
    await page.goto('http://127.0.0.1:'+server.httpServer.address().port+'/lifecycle?lang='+lang)
    await page.getByTestId('lifecycle-model').fill('synthetic-public')
    await page.getByTestId('lifecycle-group').fill('11')
    await page.getByTestId('lifecycle-select').click()
    await page.getByTestId('lifecycle-account-7').waitFor()
    if(!(await page.getByTestId('lifecycle-account-7').innerText()).includes('ADMIN_DISABLED'))throw new Error('disabled account missing')
    if((await page.getByTestId('lifecycle-account-7').innerText()).includes('42500'))throw new Error('P90 substituted for absent P70')
    const prefix=lang+'-'+viewport.width
    await page.screenshot({path:path.join(output,prefix+'-current.png'),fullPage:true})
    await page.getByTestId('lifecycle-tab-decisions').click();await page.getByTestId('lifecycle-decision').waitFor()
    await page.screenshot({path:path.join(output,prefix+'-decisions.png'),fullPage:true})
    await page.getByTestId('lifecycle-tab-events').click();await page.getByTestId('lifecycle-event').waitFor()
    await page.screenshot({path:path.join(output,prefix+'-timeline.png'),fullPage:true})
    await page.getByTestId('lifecycle-tab-current').click();await page.getByTestId('lifecycle-account-7').waitFor()
    await page.evaluate(()=>{window.__offline=true;window.dispatchEvent(new Event('offline'))})
    await page.getByTestId('lifecycle-error').waitFor()
    if(!(await page.getByTestId('lifecycle-account-7').innerText()).includes('STALE'))throw new Error('offline last-good not stale')
    await page.screenshot({path:path.join(output,prefix+'-offline.png'),fullPage:true})
    await page.evaluate(()=>{window.__offline=false;window.dispatchEvent(new Event('online'))})
    await page.getByTestId('lifecycle-error').waitFor({state:'hidden'})
    await page.getByTestId('lifecycle-model').fill('slow-old-scope');await page.getByTestId('lifecycle-select').click()
    await page.getByTestId('lifecycle-model').fill('new-scope');await page.getByTestId('lifecycle-select').click()
    await page.waitForTimeout(1000)
    if(!(await page.getByTestId('lifecycle-scope').innerText()).includes('new-scope'))throw new Error('older scope response replaced selection')
    const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth+2)
    if(overflow)throw new Error('page horizontal overflow')
    results.push({lang,viewport,disabledVisible:true,noP70Substitution:true,offlineLastGood:true,scopeGeneration:true})
    await page.close()
  }
  if(errors.length||blocked.length)throw new Error(JSON.stringify({errors,blocked}))
  await fs.writeFile(path.join(output,'evidence.json'),JSON.stringify({head:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),results,errors,blocked},null,2))
} catch(error) {
  if(activePage) {
    await activePage.screenshot({path:path.join(output,activePrefix+'-failure.png'),fullPage:true}).catch(()=>{})
    await fs.writeFile(path.join(output,activePrefix+'-failure.json'),JSON.stringify({error:String(error),errors,blocked,body:await activePage.locator('body').innerText(),requests:await activePage.evaluate(()=>window.__lifecycleRequests)},null,2))
  }
  throw error
} finally {await browser.close();await server.close()}
