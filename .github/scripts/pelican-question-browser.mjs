// Actual Vue controls and project CSS; synthetic question metadata, no upstream calls.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import fs from 'node:fs/promises'
import path from 'node:path'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const baseline = '428e15d4f69f7e72fc8028a42e2158b9e063b1d1'
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vue = requireFrontend('@vitejs/plugin-vue').default
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const output = process.env.PELICAN_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const beforePath = path.join(frontend, 'src/components/admin/account/PelicanFieldsBefore.vue')
await fs.writeFile(beforePath, execFileSync('git', ['show', `${baseline}:frontend/src/components/admin/account/PelicanTestFields.vue`]))
const html = `<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp,ref,h} from 'vue';import {createPinia} from 'pinia';
import Fields from '/src/components/admin/account/PelicanTestFields.vue';
import Before from '/src/components/admin/account/PelicanFieldsBefore.vue';
import Snapshot from '/src/components/admin/account/PelicanQuestionSnapshot.vue';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import '/src/style.css';
const lang=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
createApp({setup(){
 const before=new URLSearchParams(location.search).has('before');
 const cfg=ref({question_kind:'pelican',prompt:'Fixture custom SVG prompt',reasoning_effort:'medium',parallel_count:2});
 const question={id:'pelican-bicycle',version:'svg-motion-v1',round_id:'manual:fixture',source:'builtin:svg-motion',prompt:'创建一个独立 HTML，使用内联 SVG 绘制循环的 2D 动画。鹈鹕骑自行车。',criteria:['长喙与喉囊清晰可辨','两轮、车架及脚踏结构完整','车轮转动与双腿踩踏连续协调','路标向后移动，角色保持在画面内']};
 return()=>h('main',{class:'mx-auto max-w-3xl space-y-5 p-6'},[
  h('p',{class:'text-sm text-amber-700'},'CI fixture · 虚构数据 · 非生产'),
  h(before?Before:Fields,{modelValue:cfg.value,'onUpdate:modelValue':value=>{cfg.value=value}}),
  ...(before?[]:[h(Snapshot,{question}),h(Snapshot)])
 ]);
}}).use(createPinia()).use(i18n).mount('#app');</script>`
process.chdir(frontend)
const server = await createServer({ root: frontend, configFile: false, envFile: false,
 define: { __INTLIFY_JIT_COMPILATION__: true },
 plugins: [vue(), {name:'pelican-fixture',resolveId(id){if(id==='fixture:auth')return '\0'+id},load(id){if(id==='\0fixture:auth')return 'export const useAuthStore=()=>({user:{id:41}})'},configureServer(vite){vite.middlewares.use((req,res,next)=>{if(req.url?.split('?')[0]!=='/')return next();res.setHeader('Content-Type','text/html');void vite.transformIndexHtml('/',html).then(value=>res.end(value))})}}],
 resolve:{alias:[{find:'@/stores/auth',replacement:'fixture:auth'},{find:'@',replacement:path.join(frontend,'src')},{find:'vue-i18n',replacement:'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'}]},
 server:{host:'127.0.0.1',port:4188,strictPort:true},optimizeDeps:{entries:[],include:['vue','pinia','vue-i18n']}
})
await server.listen()
const browser = await chromium.launch({headless:true})
const errors=[], external=[]
try {
 const context=await browser.newContext({viewport:{width:1100,height:900}})
 await context.route('**/*',route=>{if(new URL(route.request().url()).origin!=='http://127.0.0.1:4188'){external.push(route.request().url());return route.abort()}return route.continue()})
 const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message))
 await page.goto('http://127.0.0.1:4188/?before=1',{waitUntil:'networkidle'})
 await page.locator('textarea').waitFor()
 assert.deepEqual(errors,[])
 await page.screenshot({path:path.join(output,'before-zh.png'),fullPage:true})
 for(const lang of ['zh','en']){
  await page.goto('http://127.0.0.1:4188/?lang='+lang,{waitUntil:'networkidle'})
  await page.getByTestId('pelican-mode-select').selectOption('random')
  assert.equal(await page.locator('textarea').count(),0)
  await page.getByTestId('pelican-question-snapshot').first().locator('summary').click()
  await page.screenshot({path:path.join(output,`after-${lang}.png`),fullPage:true})
 }
 await page.setViewportSize({width:390,height:900})
 await page.screenshot({path:path.join(output,'after-mobile-en.png'),fullPage:true})
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
 assert.deepEqual(errors,[]);assert.deepEqual(external,[])
 await fs.writeFile(path.join(output,'evidence.json'),JSON.stringify({head,baseline,fixture:true,errors,external},null,2))
} finally {await fs.writeFile(path.join(output,'browser-errors.json'),JSON.stringify({head,errors,external},null,2));await browser.close();await server.close();await fs.rm(beforePath,{force:true})}
