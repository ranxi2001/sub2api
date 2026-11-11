// Render the real fallback card with fictional accounts; all API/network access is isolated.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdir, writeFile, unlink } from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
const repo = process.cwd(), root = path.join(repo, 'frontend'), output = process.env.EVIDENCE_DIR
const req = createRequire(path.join(root, 'package.json'))
const { createServer } = req('vite'), vueModule = req('@vitejs/plugin-vue')
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const baseline = '8027cb579cce518e7a773da351d009aac2f10b3b', head = git('rev-parse', 'HEAD')
const entry = path.join(root, '.oauth-fallback-preview.js'), errors = []
let before = false, server, browser
await mkdir(output, { recursive: true })
try {
 await writeFile(entry, `
import {mount} from '@vue/test-utils';import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import Card from '/src/components/admin/operations/OpenAIAPIKeyFallbackCard.vue';import '/src/style.css';
const lang=new URL(location.href).searchParams.get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
apiClient.defaults.adapter=async r=>{if(r.method!=='get'||r.url!=='/admin/accounts')throw Error('Unexpected API '+r.url);return {config:r,status:200,statusText:'OK',headers:{},data:{code:0,data:{items:[{id:101,name:'Demo fallback API Key',platform:'openai',type:'apikey',status:'active',schedulable:false,group_ids:[]}],page:1,pages:1,page_size:100,total:1}}}};
mount(Card,{attachTo:'#app',props:{modelValue:{enabled:true,account_ids:[101],metric:'sla',threshold:85,window_minutes:5},groups:[],userId:99999,statusLoading:false,statusFailed:false,status:{oauth_account_count:10,oauth_available_count:8,metric:'sla',metric_value:98,samples:200,window_minutes:5,last_checked_at:'2026-01-01T12:00:00Z',last_metric_checked_at:'2026-01-01T12:00:00Z'}},global:{plugins:[createPinia(),i18n]}});
`, { flag: 'wx' })
 process.chdir(root)
 server = await createServer({ root, configFile: false, envFile: false,
  resolve: { alias: { '@': path.join(root, 'src') } },
  plugins: [{name:'fallback-preview',enforce:'pre',load(id){
   if(before && /\/src\/i18n\/locales\/(zh|en)\/autoConfig\.ts$/.test(id))return git('show',baseline+':'+path.relative(repo,id))
  },configureServer(vite){vite.middlewares.use((r,s,next)=>{
   if(r.url?.split('?')[0]!=='/')return next()
   s.setHeader('Content-Type','text/html');s.end('<!doctype html><meta charset="utf-8"><div id="app" style="max-width:1000px;margin:24px auto"></div><script type="module" src="/.oauth-fallback-preview.js"></script>')
  })}},(vueModule.default||vueModule)()],
  server:{host:'127.0.0.1',port:4197,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n','axios','@vue/test-utils']}
 })
 await server.listen();browser=await chromium.launch({headless:true})
 for(const phase of ['before','after']){
  before=phase==='before';server.moduleGraph.invalidateAll()
  for(const lang of ['zh','en']){
   const page=await browser.newPage({viewport:{width:1150,height:1150},timezoneId:'UTC'})
   page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)})
   await page.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:4197'?route.continue():route.abort())
   await page.goto('http://127.0.0.1:4197/?lang='+lang)
   await page.getByText('Demo fallback API Key',{exact:false}).waitFor()
   await page.getByText('98.00%',{exact:false}).waitFor()
   if(!before)assert.match(await page.locator('#app').innerText(),/OpenAI OAuth SLA/)
   await page.screenshot({path:path.join(output,phase+'-'+lang+'.png'),fullPage:true})
   await page.close()
  }
 }
 assert.deepEqual(errors,[])
 await writeFile(path.join(output,'evidence.json'),JSON.stringify({head,baseline,scope:'Actual fallback card, fictional data, zh/en before/after; no production requests',errors},null,2))
} catch(error) {
 await writeFile(path.join(output,'failure.json'),JSON.stringify({head,errors,error:String(error)},null,2))
 for(const context of browser?.contexts()||[])for(const page of context.pages()){await page.screenshot({path:path.join(output,'failure.png'),fullPage:true});await writeFile(path.join(output,'failure.txt'),await page.locator('body').innerText())}
 throw error
} finally { await browser?.close();await server?.close();await unlink(entry) }
