// Render the actual settings card with project CSS and synthetic data only.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
const root=process.cwd(), frontend=path.join(root,'frontend')
const requireFrontend=createRequire(path.join(frontend,'package.json'))
const {createServer}=requireFrontend('vite')
const vueModule=requireFrontend('@vitejs/plugin-vue'),vue=vueModule.default||vueModule
const {chromium}=createRequire(path.join(process.env.PLAYWRIGHT_ROOT,'package.json'))('playwright')
const output=path.resolve(process.env.OAUTH_DEGRADATION_SCREENSHOT_DIR)
const baseline='90549fe89' // Before the independent HTTP trigger and condition switches.
const component='frontend/src/components/admin/operations/OAuthDegradationCard.vue'
const previous=path.join(frontend,'src/components/admin/operations/OAuthDegradation429Before.vue')
await fs.mkdir(output,{recursive:true})
await fs.writeFile(previous,execFileSync('git',['show',`${baseline}:${component}`],{cwd:root}))
const html=`<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp,ref,h} from 'vue';import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import '/src/style.css';
import {defaultOAuthDegradationConfig} from '/src/utils/oauthDegradation.ts';
const {default:Card}=new URLSearchParams(location.search).has('before')?await import('/src/components/admin/operations/OAuthDegradation429Before.vue'):await import('/src/components/admin/operations/OAuthDegradationCard.vue');
await loadLocaleMessages('zh');i18n.global.locale.value='zh';
createApp({setup(){const cfg=ref({...defaultOAuthDegradationConfig(),enabled:true,threshold_percent:9,limit_models:false});return ()=>h('main',{class:'mx-auto max-w-4xl p-5'},[h('p',{class:'mb-3 text-sm text-gray-500'},'CI 组件预览 · 虚构配置 · 非生产'),h(Card,{modelValue:cfg.value,'onUpdate:modelValue':value=>cfg.value=value,userId:41})])}}).use(createPinia()).use(i18n).mount('#app');</script>`
process.chdir(frontend)
const server=await createServer({root:frontend,configFile:false,envFile:false,define:{__INTLIFY_JIT_COMPILATION__:true},plugins:[vue(),{name:'429-fixture',configureServer(vite){vite.middlewares.use((req,res,next)=>{if(req.url?.split('?')[0]!=='/')return next();res.setHeader('Content-Type','text/html');void vite.transformIndexHtml('/',html).then(v=>res.end(v))})}}],resolve:{alias:{'@':path.join(frontend,'src')}},server:{host:'127.0.0.1',port:0}})
let browser
try {
 await server.listen()
 browser=await chromium.launch({headless:true})
 const page=await browser.newPage({viewport:{width:1100,height:1000}})
 const errors=[];page.on('pageerror',e=>{errors.push(e.message);console.error(e.message)})
 const url=`http://127.0.0.1:${server.httpServer.address().port}`
 await page.goto(url+'/?before');await page.locator('[data-testid="degradation-threshold"]').waitFor()
 await page.locator('[data-testid="degradation-action"]').screenshot({path:path.join(output,'before.png')})
 await page.goto(url);await page.locator('[data-testid="degradation-429-enabled"]').waitFor()
 assert.equal(await page.locator('[data-testid="degradation-probe-enabled"]').isChecked(),true)
 assert.equal(await page.locator('[data-testid="degradation-429-enabled"]').isChecked(),true)
 assert.equal(await page.locator('[data-testid="degradation-429-threshold"]').inputValue(),'80')
 await page.locator('[data-testid="degradation-action"]').screenshot({path:path.join(output,'after.png')})
 await page.locator('[data-testid="degradation-probe-enabled"]').uncheck()
 assert.equal(await page.locator('[data-testid="degradation-threshold"]').count(),0)
 await page.locator('[data-testid="degradation-action"]').screenshot({path:path.join(output,'429-only.png')})
 assert.deepEqual(errors,[])
 await fs.writeFile(path.join(output,'source.json'),JSON.stringify({head:execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim(),baseline,component,scope:'Actual Vue component, project CSS, synthetic configuration; no production API'},null,2))
} finally {await browser?.close();await server.close();await fs.rm(previous,{force:true})}
