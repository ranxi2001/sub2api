// CI only: unmodified complete application entry, project Vite/Tailwind/plugins.
// A separate test page exercises real NewFeatureBadge/store announcement epochs.
import { writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'

const repo = resolve('.'), output = process.env.EVIDENCE_DIR
if (!output || process.env.GITHUB_ACTIONS !== 'true') throw Error('Isolated CI only')
const baselineRoot = resolve(process.env.BASELINE_FRONTEND_ROOT || '.pr838-baseline/frontend')
const noticeKeys = process.env.EVIDENCE_SCENARIO === 'gateway-models'
  ? { A: 'crossPlatformGatewayAccounts', B: 'gatewayModelSync', C: 'fixture-gateway-models-future-announcement-C' }
  : { A: 'vipAutoCredit', B: 'tempUnschedStreamCountWindow', C: 'fixture-pr838-future-announcement-C' }
mkdirSync(output, { recursive: true })
const { build } = await import(pathToFileURL(join(repo, 'frontend/node_modules/vite/dist/node/index.js')))
for (const mode of ['before', 'after']) {
  const root = mode === 'before' ? baselineRoot : join(repo, 'frontend')
  const extra = join(root, 'pr838-notices.html'), entry = join(root, '.pr838-notices.js')
  try {
    if (mode === 'after') {
      writeFileSync(extra, '<!doctype html><html><meta charset="utf-8"><div id="app"></div><script type="module" src="/.pr838-notices.js"></script></html>', { flag: 'wx' })
      writeFileSync(entry, `
import {createApp,h} from 'vue';import {createPinia} from 'pinia';
import Badge from '/src/components/common/NewFeatureBadge.vue';
import {NEW_FEATURE_NOTICES as ids} from '/src/constants/newFeatureNotices.ts';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import '/src/style.css';
async function mountNotices(){
await loadLocaleMessages('en');i18n.global.locale.value='en';
// Future C is deliberately a test-only registered ID, not a product announcement.
const keys=${JSON.stringify(noticeKeys)};
const registry={A:ids[keys.A],B:ids[keys.B],C:keys.C};
if(!registry.A||!registry.B)throw Error('Requested notice IDs must exist in the actual candidate registry');
const p=new URLSearchParams(location.search), phase=p.get('phase')||'a',user=Number(p.get('user'));
if(!Number.isSafeInteger(user)||user<1)throw Error('Fictional fixture administrator required');
const entries=phase==='a'?['A']:phase==='b'?['A','B','B']:['A','B','B','C'];
createApp({render:()=>h('main',{class:'p-6 space-y-4'},[
h('h1',{},'CI fixture · actual badge/store · simulated announcement epochs'),
...entries.map((id,i)=>h('section',{'data-testid':'notice-'+id+'-'+i},[h('span',{},id),h(Badge,{noticeId:registry[id],userId:user})]))
])}).use(createPinia()).use(i18n).mount('#app');
}
void mountNotices();
`, { flag: 'wx' })
    }
    process.chdir(root)
    await build({ root, configFile: join(root, 'vite.config.ts'), build: {
      outDir: join(output, mode + '-dist'), emptyOutDir: true,
      rollupOptions: { input: mode === 'before' ? join(root, 'index.html') : { app: join(root, 'index.html'), notices: extra } },
    } })
  } finally {
    process.chdir(repo)
    if (mode === 'after') { rmSync(extra, { force: true }); rmSync(entry, { force: true }) }
  }
}
