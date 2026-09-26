<template>
  <div class="mt-3 space-y-3 rounded border p-4 dark:border-dark-600">
    <p class="text-sm" role="status">{{ text('内核状态', 'Kernel status') }}: {{ status?.phase || '—' }} · {{ status?.nodes || 0 }} {{ text('个节点', 'nodes') }} · {{ status?.dynamic_proxies || 0 }} {{ text('个动态代理', 'dynamic proxies') }}</p>
    <div v-for="pool in warmPools" :key="pool.source" class="rounded bg-gray-50 p-3 text-xs dark:bg-dark-700" data-testid="bps-warm-pool">
      <p class="font-medium">{{ pool.source }} · Warm IP pool</p>
      <p>{{ text('就绪', 'Ready') }} {{ pool.ready }} / {{ text('目标', 'Target') }} {{ pool.target }} · {{ text('探测中', 'Checking') }} {{ pool.checking }} · {{ text('冷却中', 'Cooling') }} {{ pool.cooling }}</p>
      <p>{{ text('目标按已启用账号的并发数汇总；不足时复用就绪出口，用户请求不探测冷节点。', 'Targets follow enabled account concurrency. Ready exits are reused when scarce; user requests never probe cold nodes.') }}</p>
      <p v-if="pool.target > 0 && pool.ready === 0" class="text-amber-700 dark:text-amber-400">{{ text('暂无通过初筛的出口，后台正在预热；不会退回直连。', 'No qualified exits yet. Background warming continues; direct fallback is disabled.') }}</p>
      <p v-for="(count, reason) in pool.failure_reasons" :key="reason">{{ reason }}: {{ count }}</p>
    </div>
    <p v-if="error || status?.error" class="text-sm text-red-600" role="alert">{{ error || status?.error }}</p>
    <div class="flex gap-2">
      <button type="button" class="btn btn-secondary" :disabled="pending" @click="refresh">{{ text('检测状态', 'Check status') }}</button>
      <button v-if="status && !status.installed" type="button" class="btn btn-primary" :disabled="pending || status.busy || !status.supported" @click="operate('install')">{{ text('检测并安装', 'Install kernel') }}</button>
      <button v-else-if="!status?.running" type="button" class="btn btn-secondary" :disabled="pending || status?.busy || !status?.nodes" @click="operate('start')">{{ text('启动内核', 'Start kernel') }}</button>
    </div>
    <template v-if="status?.installed">
      <label class="block text-sm" for="mihomo-subscriptions">{{ text('机场订阅地址（每行一个）', 'Subscription URLs (one per line)') }}</label>
      <textarea id="mihomo-subscriptions" v-model="subscriptions" class="input w-full" rows="3" autocomplete="off" spellcheck="false" />
      <p class="text-xs text-gray-500">{{ text('已保存', 'Saved') }} {{ status.subscriptions }} {{ text('个订阅；地址不回显，留空保留。支持 Clash/Mihomo YAML 订阅。', 'subscriptions; URLs are hidden. Leave empty to keep. Requires Clash/Mihomo YAML.') }}</p>
      <input type="file" accept=".txt,text/plain" :aria-label="text('导入订阅 TXT', 'Import subscription TXT')" @change="importFile" />
      <label class="flex items-center gap-2 text-sm"><input v-model="append" type="checkbox" />{{ text('追加到已保存订阅（不勾选则替换）', 'Append to saved subscriptions (otherwise replace)') }}</label>
      <button type="button" class="btn btn-primary" :disabled="pending || status.busy" @click="operate('apply')">{{ text('保存并应用', 'Save and apply') }}</button>
      <label class="block text-sm" for="mihomo-dynamic-proxies">{{ text('动态代理（每行一个）', 'Dynamic proxies (one per line)') }}</label>
      <label class="block text-sm" for="mihomo-dynamic-protocol">{{ text('无协议前缀时使用', 'Default protocol for entries without a prefix') }}</label>
      <select id="mihomo-dynamic-protocol" v-model="dynamicProtocol" class="input">
        <option value="http">HTTP</option><option value="https">HTTPS</option><option value="socks5">SOCKS5</option><option value="socks5h">SOCKS5H</option>
      </select>
      <p class="text-xs text-gray-500">{{ text('支持以下四种格式；带协议前缀时优先使用该协议。', 'Supports the four formats below; an explicit protocol prefix takes precedence.') }}</p>
      <pre class="text-xs whitespace-pre-wrap">hostname:port:username:password
username:password:hostname:port
username:password@hostname:port
hostname:port@username:password</pre>
      <textarea id="mihomo-dynamic-proxies" v-model="dynamicProxies" class="input w-full font-mono text-xs" rows="5" autocomplete="off" spellcheck="false" :placeholder="text('http://用户名:密码@主机:端口\n也支持省略 http://', 'http://user:pass@host:port\nThe http:// prefix may be omitted')" />
      <p class="text-xs text-gray-500">{{ text('已保存', 'Saved') }} {{ status.dynamic_proxies || 0 }} {{ text('个动态代理。应用动态代理会替换失败的机场订阅，只保留这些动态节点。', 'dynamic proxies. Applying dynamic proxies replaces remote subscriptions and keeps only these nodes.') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="pending || status.busy" @click="operate('apply_dynamic')">{{ text('应用动态代理', 'Apply dynamic proxies') }}</button>
      <button v-if="status.running" type="button" class="btn btn-secondary ml-2" @click="$emit('ready', status.endpoint)">{{ text('设为打票代理', 'Use for ticket harvesting') }}</button>
      <p class="text-xs text-gray-500">{{ text('应用成功后点击“设为打票代理”，再保存系统设置。', 'After applying, select Use for ticket harvesting and save system settings.') }}</p>
      <MihomoCountryFilter v-if="status.nodes" :filter="status.country_filter" :codes="status.country_codes || []" :nodes="status.node_states || []" :busy="pending || status.busy" @save="operate('country_filter', $event)" @scan="operate('country_scan')" />
      <details v-if="status.node_states?.length">
        <label class="my-2 flex items-center gap-2 text-sm"><input type="checkbox" :checked="status.use_once" :disabled="pending || status.busy" @change="operate(status.use_once ? 'once_off' : 'once_on')" />{{ text('打票节点用后移出（需手动恢复）', 'Retire each harvest node after use (manual recovery)') }}</label>
        <summary class="cursor-pointer text-sm">{{ text('节点管理', 'Manage nodes') }}</summary>
        <p class="my-2 text-xs text-gray-500">{{ text('检测仅测试网络连接，不调用模型。失败或停用节点需手动恢复。', 'Tests network connectivity only. Failed or disabled nodes require manual recovery.') }}</p>
        <div class="max-h-64 overflow-auto">
          <div v-for="node in status.node_states" :key="node.name" class="flex items-center gap-2 py-1 text-xs">
            <span>{{ node.display_name || node.name }}</span><span>{{ node.state }}</span>
            <span :title="node.country_checked_at ? new Date(node.country_checked_at).toLocaleString() : ''">{{ node.dynamic ? text('动态出口 · 地区随连接变化', 'Dynamic exit · region may change') : node.country_code || text('地区未知', 'Unknown region') }}</span>
            <button v-if="!node.dynamic" type="button" class="btn btn-secondary btn-sm" :disabled="pending || status.busy" @click="operate('country_probe/' + node.name)">{{ text('检测地区', 'Check region') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="pending || status.busy || !status.running" @click="operate('probe/' + node.name)">{{ text('检测', 'Test') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="pending || status.busy || node.state === 'country_excluded'" @click="operate((node.state === 'enabled' ? 'disable/' : 'recover/') + node.name)">{{ node.state === 'country_excluded' ? text('地区已排除', 'Region excluded') : node.state === 'enabled' ? text('停用', 'Disable') : text('恢复', 'Recover') }}</button>
          </div>
        </div>
      </details>
    </template>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'
import MihomoCountryFilter from './MihomoCountryFilter.vue'
import type { CountryFilter, CountryNode } from './mihomoCountry'
const { locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
defineEmits<{ ready: [endpoint: string] }>()
interface WarmPoolStatus { target: number; ready: number; checking: number; cooling: number; failure_reasons?: Record<string, number> }
interface Status { bps_warm_pool?: WarmPoolStatus; bps_ip_warm_pool?: WarmPoolStatus; installed: boolean; running: boolean; busy: boolean; supported: boolean; phase: string; error?: string; nodes: number; subscriptions: number; dynamic_proxies?: number; endpoint: string; use_once?: boolean; node_states?: CountryNode[]; country_filter?: CountryFilter; country_codes?: string[] }
const status = ref<Status>(); const subscriptions = ref(''); const dynamicProxies = ref(''); const append = ref(false); const pending = ref(false); const error = ref('')
const warmPools = computed(() => [
  { source: 'Mihomo', pool: status.value?.bps_warm_pool },
  { source: text('IP 管理', 'IP Management'), pool: status.value?.bps_ip_warm_pool }
].flatMap(item => item.pool ? [{ source: item.source, ...item.pool }] : []))
const dynamicProtocol = ref('http')
function dynamicProxyLines(): string[] {
  return dynamicProxies.value.split(/\r?\n/).map(s => s.trim()).filter(Boolean)
    .map(s => s.includes('://') ? s : `${dynamicProtocol.value}://${s}`)
}
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
async function refresh() {
  if (timer) clearTimeout(timer)
  try { status.value = (await apiClient.get<Status>('/admin/system/mihomo')).data; error.value = '' }
  catch { error.value = text('无法读取内核状态', 'Cannot read kernel status') }
  if (!disposed && (status.value?.busy || warmPools.value.some(pool => pool.target > 0))) timer = setTimeout(refresh, status.value?.busy ? 1500 : 5000)
}
async function operate(action: string, countryFilter?: CountryFilter) {
  pending.value = true; error.value = ''
  try {
    status.value = (await apiClient.post<Status>('/admin/system/mihomo', { action, subscriptions: action === 'apply' ? subscriptions.value.split(/\r?\n/).filter(s => s.trim()) : [], dynamic_proxies: action === 'apply' || action === 'apply_dynamic' ? dynamicProxyLines() : [], append: append.value, ...(action === 'country_filter' ? { country_filter: countryFilter } : {}) })).data
    if (action === 'apply') subscriptions.value = ''
    if (action === 'apply' || action === 'apply_dynamic') dynamicProxies.value = ''
    await refresh()
  } catch (cause: unknown) {
    const message = (cause as { message?: string })?.message
    error.value = message || text('操作未提交，请检查服务状态后重试', 'Operation was not accepted; check service status and retry')
  }
  finally { pending.value = false }
}
async function importFile(event: Event) {
  const input = event.target as HTMLInputElement; const file = input.files?.[0]
  if (!file) return
  if (file.size > 256 * 1024) { error.value = text('文件不能超过 256 KiB', 'File must be at most 256 KiB'); return }
  subscriptions.value = await file.text(); input.value = ''
}
onMounted(refresh)
onUnmounted(() => { disposed = true; if (timer) clearTimeout(timer) })
</script>
