// Local component preview only: real DashboardView, NewFeatureBadge and project CSS.
// API responses, authentication and the surrounding layout use synthetic fixtures.
// Usage: node .github/scripts/dashboard-account-concurrency-preview.mjs [--repo PATH] [--deps FRONTEND_PATH] [--port 4194]
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const options = {}
for (let i = 2; i < process.argv.length; i += 2) {
  const key = process.argv[i]
  if (!['--repo', '--deps', '--port'].includes(key) || !process.argv[i + 1]) {
    throw new Error('Usage: preview.mjs [--repo PATH] [--deps FRONTEND_PATH] [--port PORT]')
  }
  options[key.slice(2)] = process.argv[i + 1]
}
const root = path.resolve(options.repo || process.cwd())
const frontend = path.join(root, 'frontend')
const deps = path.resolve(options.deps || process.env.SUB2API_PREVIEW_DEPS || frontend)
const port = Number(options.port || process.env.SUB2API_PREVIEW_PORT || 4194)
const baseline = '1411d0fc6e1fca51d4b657c21837cd74c87bf84e'
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid preview port')

await fs.access(path.join(frontend, 'src/views/admin/DashboardView.vue'))
const dependencies = await fs.realpath(path.join(deps, 'node_modules'))
const nodeModules = path.join(frontend, 'node_modules')
let linkedDependencies = false
let cacheDir
let server
let shuttingDown = false

async function cleanup() {
  await server?.close()
  if (cacheDir) await fs.rm(cacheDir, { recursive: true, force: true })
  if (linkedDependencies) {
    const link = await fs.lstat(nodeModules).catch(() => null)
    if (link?.isSymbolicLink() && await fs.realpath(nodeModules) === dependencies) {
      await fs.unlink(nodeModules)
    }
  }
}

async function shutdown(code) {
  if (shuttingDown) return
  shuttingDown = true
  try {
    await cleanup()
  } catch (error) {
    console.error(error)
    code = 1
  }
  process.exit(code)
}

process.once('SIGINT', () => void shutdown(0))
process.once('SIGTERM', () => void shutdown(0))

try {
  const existing = await fs.lstat(nodeModules).catch(error => {
    if (error.code !== 'ENOENT') throw error
    return null
  })
  if (!existing) {
    await fs.symlink(dependencies, nodeModules, 'dir')
    linkedDependencies = true
  }
  cacheDir = await fs.mkdtemp(path.join(os.tmpdir(), 'sub2api-dashboard-preview-'))
  const baselinePath = path.join(cacheDir, 'BaselineDashboard.vue')
  await fs.writeFile(baselinePath, execFileSync('git', ['show', `${baseline}:frontend/src/views/admin/DashboardView.vue`], { cwd: root }))
  const requireDeps = createRequire(path.join(deps, 'package.json'))
  const { createServer } = requireDeps('vite')
  const vueModule = requireDeps('@vitejs/plugin-vue')
  const vue = vueModule.default || vueModule
  const tailwind = requireDeps('tailwindcss')
  const autoprefixer = requireDeps('autoprefixer')
  const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))

  const stats = {
    total_users: 240, today_new_users: 3, active_users: 86, hourly_active_users: 21,
    stats_updated_at: '2026-11-09T04:00:00Z', stats_stale: false,
    total_api_keys: 320, active_api_keys: 298,
    total_accounts: 64, normal_accounts: 61, error_accounts: 0,
    ratelimit_accounts: 2, overload_accounts: 1,
    total_requests: 182400, total_input_tokens: 326000000,
    total_output_tokens: 18400000, total_cache_creation_tokens: 14000000,
    total_cache_read_tokens: 280000000, total_tokens: 344400000,
    total_cost: 420.5, total_actual_cost: 126.15, total_account_cost: 72.8,
    today_requests: 12480, today_input_tokens: 23000000,
    today_output_tokens: 1600000, today_cache_creation_tokens: 1200000,
    today_cache_read_tokens: 18000000, today_tokens: 24600000,
    today_cost: 48.6, today_actual_cost: 14.58, today_account_cost: 8.2,
    average_duration_ms: 1850, uptime: 86400, rpm: 758, tpm: 3060000,
  }
  const prefix = 'virtual:dashboard-account-concurrency-preview/'
  const modules = {
    api: `
      const stats = ${JSON.stringify(stats)};
      export const getUserBreakdown = async () => ({ users: [] });
      export const adminAPI = {
        dashboard: {
          getSnapshotV2: async () => ({ stats, trend: [], models: [], groups: [], users_trend: [] }),
          getUserUsageTrend: async () => ({ trend: [] }),
          getUserSpendingRanking: async () => ({ ranking: [], total_actual_cost: 0, total_requests: 0, total_tokens: 0 }),
          getUserBreakdown,
        },
        accounts: { getConcurrencySummary: async () => ({ current_concurrency: 128 }) },
      };
      export default adminAPI.dashboard;
    `,
    auth: `export const useAuthStore = () => ({ user: { id: 41, role: 'admin' }, isAdmin: true });`,
    app: `export const useAppStore = () => ({ showError: message => console.error('Preview error:', message) });`,
    batchImage: `export const useBatchImageAccess = () => ({ canUseBatchImage: false, refreshBatchImageAccess: async () => false });`,
    router: `export const useRouter = () => ({ push: async () => {} });`,
    layout: `
      import { h } from 'vue';
      export default {
        setup(_, { slots }) {
          return () => h('main', { class: 'mx-auto min-h-screen max-w-7xl bg-gray-50 p-6 dark:bg-dark-950' }, [
            h('p', { class: 'mb-6 text-sm text-gray-500 dark:text-gray-400' }, '本地组件预览 · 全部为虚构数据'),
            slots.default?.(),
          ]);
        },
      };
    `,
    entry: `
      import { createApp, h } from 'vue';
      import { createPinia } from 'pinia';
      import { createI18n } from 'vue-i18n';
      import DashboardView from '/src/views/admin/DashboardView.vue';
      import BaselineDashboard from ${JSON.stringify(`/@fs/${baselinePath}`)};
      import zh from '/src/i18n/locales/zh/index.ts';
      import en from '/src/i18n/locales/en/index.ts';
      import '/src/style.css';
      const params = new URLSearchParams(location.search);
      const locale = params.get('locale') === 'en' ? 'en' : 'zh';
      document.documentElement.lang = locale;
      document.documentElement.classList.toggle('dark', params.get('theme') === 'dark');
      createApp({ render: () => h(params.has('baseline') ? BaselineDashboard : DashboardView) })
        .use(createPinia())
        .use(createI18n({ legacy: false, locale, fallbackLocale: 'en', messages: { zh, en } }))
        .mount('#app');
    `,
  }
  const html = `<!doctype html>
    <html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
    <title>账号并发 · 本地组件预览</title></head>
    <body><div id="app"></div><script type="module">import '${prefix}entry';</script></body></html>`
  const virtualModules = {
    name: 'dashboard-account-concurrency-preview-fixtures',
    enforce: 'pre',
    resolveId(id) {
      return id.startsWith(prefix) ? `\0${id}` : null
    },
    load(id) {
      if (!id.startsWith(`\0${prefix}`)) return null
      const source = modules[id.slice(prefix.length + 1)]
      if (source === undefined) throw new Error(`Unknown preview module: ${id}`)
      return source
    },
    configureServer(vite) {
      vite.middlewares.use(async (req, res, next) => {
        const pathname = new URL(req.url, `http://127.0.0.1:${port}`).pathname
        if (pathname.startsWith('/api/')) {
          res.statusCode = 503
          res.end('Live APIs are disabled in this synthetic component preview')
          return
        }
        if (pathname !== '/' && pathname !== '/index.html') return next()
        try {
          const page = await vite.transformIndexHtml(req.url, html)
          res.setHeader('Content-Type', 'text/html; charset=utf-8')
          res.setHeader('Content-Security-Policy', `default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self' ws://127.0.0.1:${port}`)
          res.end(page)
        } catch (error) {
          next(error)
        }
      })
    },
  }
  const alias = (find, name) => ({ find, replacement: `${prefix}${name}` })
  server = await createServer({
    root: frontend,
    cacheDir,
    configFile: false,
    plugins: [virtualModules, vue()],
    resolve: {
      dedupe: ['vue', 'pinia', 'vue-i18n'],
      alias: [
        alias(/^@\/api\/admin$/, 'api'),
        alias('@/api/admin/dashboard', 'api'),
        alias('@/stores/auth', 'auth'),
        alias('@/stores/app', 'app'),
        alias('@/composables/useBatchImageAccess', 'batchImage'),
        alias(/^vue-router$/, 'router'),
        alias('@/components/layout/AppLayout.vue', 'layout'),
        { find: '@', replacement: path.join(frontend, 'src') },
      ],
    },
    css: {
      postcss: {
        plugins: [tailwind({ ...theme, content: [path.join(frontend, 'src/**/*.{vue,js,ts,jsx,tsx}'), baselinePath, { raw: modules.layout, extension: 'js' }] }), autoprefixer()],
      },
    },
    define: { __INTLIFY_JIT_COMPILATION__: true },
    server: {
      host: '127.0.0.1',
      port,
      strictPort: true,
      fs: { allow: [...new Set([root, deps, dependencies, cacheDir])] },
    },
    optimizeDeps: {
      entries: [],
      include: ['vue', 'vue-i18n', 'pinia', 'chart.js', 'vue-chartjs'],
    },
  })
  await server.listen()
  console.log(`Synthetic DashboardView preview: http://127.0.0.1:${port}/`)
  console.log(`Options: ?locale=en&theme=dark, ?baseline=1 (${baseline}). Stop with Ctrl+C; temporary dependency link and Vite cache are removed.`)
} catch (error) {
  console.error(error)
  await shutdown(1)
}
