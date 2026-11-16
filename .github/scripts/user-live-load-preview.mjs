// Actual DataTable/UserLiveUsageCell rendering; synthetic rows, no backend API.
// node .github/scripts/user-live-load-preview.mjs /path/to/frontend-with-node_modules
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'
import path from 'node:path'
import fs from 'node:fs/promises'
const root = path.resolve('frontend')
const require = createRequire(path.resolve(process.argv[2] || root, 'package.json'))
const { createServer } = await import(pathToFileURL(path.join(path.dirname(require.resolve('vite/package.json')), 'dist/node/index.js')))
const vueModule = await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')))
const vue = vueModule.default || vueModule
const { default: theme } = await import(pathToFileURL(path.join(root, 'tailwind.config.js')))
let linked = false
try { await fs.access(path.join(root, 'node_modules')) } catch {
  await fs.symlink(path.resolve(process.argv[2], 'node_modules'), path.join(root, 'node_modules'))
  linked = true
}
const entry = `
import {createApp,h} from 'vue'; import {createI18n} from 'vue-i18n'; import {createPinia} from 'pinia';
import Table from '/src/components/common/DataTable.vue';
import Cell from '/src/components/user/UserLiveUsageCell.vue';
import Badge from '/src/components/common/NewFeatureBadge.vue';
import zh from '/src/i18n/locales/zh/index.ts'; import en from '/src/i18n/locales/en/index.ts';
import '/src/style.css';
const params=new URLSearchParams(location.search);const before=params.has('before');
document.documentElement.classList.toggle('dark',params.has('dark'));
const rows=[
{id:101,email:'active@example.test',username:'活跃用户',concurrency:0,current_concurrency:3,rpm_limit:0,current_rpm:18},
{id:102,email:'limited@example.test',username:'已设限额',concurrency:10,current_concurrency:4,rpm_limit:60,current_rpm:45},
{id:103,email:'busy@example.test',username:'达到限额',concurrency:10,current_concurrency:10,rpm_limit:60,current_rpm:61},
{id:104,email:'idle@example.test',username:'空闲用户',concurrency:0,current_concurrency:0,rpm_limit:0,current_rpm:0},
{id:105,email:'unknown@example.test',username:'读取失败',concurrency:10,current_concurrency:0,rpm_limit:60,current_rpm:null}];
const columns=[{key:'email',label:'用户'},{key:'id',label:'ID'},{key:'username',label:'用户名'},...(!before?[{key:'live_usage',label:'并发 / RPM'}]:[]),{key:'status',label:'状态'}];
createApp({render:()=>h('main',{class:'min-h-screen bg-gray-50 p-8 text-gray-900 dark:bg-dark-950 dark:text-gray-100'},[
h('h1',{class:'mb-3 text-xl font-semibold'},'用户管理 · '+(before?'新增列前':'新增列后')),
h('p',{class:'mb-5 text-sm text-gray-500'},'实际表格与单元格组件 · 虚构用户 · 每分钟刷新 · 0 上限显示 ∞'),
h(Table,{columns,data:rows,loading:false},{'header-live_usage':()=>h('span',{class:'flex items-center gap-1'},['并发 / RPM',h(Badge,{noticeId:'user-live-load-initial-announcement',userId:41,compact:true})]),'cell-live_usage':({row})=>h(Cell,{user:row}),'cell-status':()=>h('span','正常')})
])}).use(createPinia()).use(createI18n({legacy:false,locale:'zh',messages:{zh,en}})).mount('#app');`
const server = await createServer({
  root, configFile:false, cacheDir:'/tmp/sub2api-user-load-vite',
  plugins:[{name:'user-load-fixture',resolveId:id=>id==='virtual:user-load'?'\0user-load':null,load:id=>id==='\0user-load'?entry:null,
    configureServer(vite){vite.middlewares.use(async(req,res,next)=>{
      if(req.url.startsWith('/api/')){res.statusCode=503;res.end('Preview has no API');return}
      if(new URL(req.url,'http://127.0.0.1').pathname!=='/')return next()
      res.setHeader('Content-Type','text/html');res.end(await vite.transformIndexHtml(req.url,'<html><head><meta charset="utf-8"></head><body><div id="app"></div><script type="module">import "virtual:user-load"</script></body></html>'))
    })}},vue()],
  resolve:{dedupe:['vue','pinia','vue-i18n'],alias:{'@':path.join(root,'src')}},
  css:{postcss:{plugins:[require('tailwindcss')({...theme,content:[path.join(root,'src/**/*.{vue,ts}'),{raw:entry,extension:'js'}]}),require('autoprefixer')()]}},
  server:{host:'127.0.0.1',port:4198,strictPort:true}
})
await server.listen(); console.log('Preview: http://127.0.0.1:4198')
for (const signal of ['SIGINT','SIGTERM']) process.once(signal,async()=>{await server.close();if(linked)await fs.unlink(path.join(root,'node_modules'));process.exit(0)})
