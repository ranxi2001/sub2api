<template>
  <details class="card" @toggle="onToggle">
    <summary class="cursor-pointer px-6 py-4 font-semibold">{{ text('Serverless 管理', 'Serverless management') }}
      <span class="ml-2 text-xs font-normal text-gray-500">{{ text('地区 → Pod（可选）', 'Region → Pod (optional)') }}</span>
    </summary>
    <div class="space-y-5 border-t border-gray-100 p-6 dark:border-dark-700">
      <p class="text-sm text-gray-500">{{ text('管理已有请求 Pod，不创建或扩缩容。默认关闭；未知地区走主站。IP 国家识别结果在 Redis 缓存 24 小时，命中时不再请求地区服务；识别失败缓存 1 分钟。', 'Manage existing request Pods without creating or scaling them. Disabled by default; unknown regions use the primary. IP country results are cached in Redis for 24 hours without another provider lookup on a cache hit; failed lookups are cached for one minute.') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="notice" role="status" class="text-sm text-green-600">{{ notice }}</p>
      <div class="flex flex-wrap items-center gap-4">
        <label class="flex items-center gap-2"><input v-model="draft.enabled" type="checkbox" :disabled="busy || !loaded" data-testid="serverless-enabled" />{{ text('开启地区分流', 'Enable region routing') }}</label>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="refresh(false)">{{ text('刷新状态', 'Refresh status') }}</button>
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy || !loaded" @click="save">{{ text('保存 Serverless 配置', 'Save Serverless configuration') }}</button>
      </div>
      <div class="space-y-3 rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-700">
        <label class="flex flex-wrap items-center gap-3">{{ text('绑定范围', 'Binding scope') }}
          <select v-model="draft.affinity_scope" class="input w-auto" :disabled="busy || !loaded" data-testid="serverless-affinity-scope">
            <option value="api_key">{{ text('API Key（默认）', 'API Key (default)') }}</option>
            <option value="api_key_ip">{{ text('API Key ＋ 客户端 IP', 'API Key + client IP') }}</option>
          </select>
        </label>
        <p class="text-xs text-gray-500">{{ text('同一 Key 需要按使用者地区分流时，选择 Key ＋ IP。每个绑定最后使用后保留 24 小时。切换范围会使用独立绑定；IP 改变可能更换 Pod，不能用于依赖节点内状态的会话。', 'Choose Key + IP when one key is used from multiple regions. Each binding lasts 24 hours after last use. Changing scope uses separate bindings; changing IP may select another Pod, so do not use this for sessions with node-local state.') }}</p>
        <label class="flex items-center gap-2"><input v-model="draft.fallback_to_primary" type="checkbox" :disabled="busy || !loaded" data-testid="serverless-primary-fallback" />{{ text('已绑定 Pod 不可用时回主站', 'Use primary when a bound Pod is unavailable') }}</label>
        <p class="text-xs text-gray-500">{{ text('默认关闭，失联时保留绑定并返回错误。开启后仅在转发前探测失败或实例失联时将绑定改为主站；不会重试已发送请求或已开始的流。回主站后保持绑定，不自动切回。', 'Off by default: preserve the binding and return an error. When enabled, move the binding to the primary only if the instance is missing or its probe fails before forwarding. Sent requests and started streams are never retried. A fallback binding stays on the primary without automatic failback.') }}</p>
      </div>
      <p class="rounded-lg bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-950/30 dark:text-amber-300">{{ text('停用分流或 Pod 只阻止新绑定；已有绑定继续使用原 Pod。首次启用或更改绑定范围前，先结束存量 BPS/WS 会话。', 'Disabling routing or a Pod stops new bindings; existing bindings remain. Finish existing BPS/WS conversations before first enabling routing or changing binding scope.') }}</p>

      <section class="space-y-3">
        <h3 class="font-medium">{{ text('自动注册的 Pod', 'Automatically registered Pods') }}</h3>
        <p class="text-xs text-gray-500">{{ text('心跳就绪不等于入口可达；先确认上报地址，再保存并检查连接。跨集群请使用可达的 HTTPS 或受信隧道地址。', 'A ready heartbeat does not prove ingress connectivity. Approve the reported endpoint, save, then check connectivity. Across clusters, use a reachable HTTPS or trusted tunnel endpoint.') }}</p>
        <p v-if="!pods.length" class="text-sm text-gray-500">{{ text('暂无 Pod；按下方接入说明配置已有 gateway。', 'No Pods yet. Configure an existing gateway using the connection instructions below.') }}</p>
        <div v-for="pod in pods" :key="pod.id" class="rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-700">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="font-medium">{{ pod.id }} <span class="text-xs text-gray-500">{{ pod.region || '—' }} · {{ pod.version || '—' }}</span></span>
            <span :class="pod.ready ? 'text-green-600' : 'text-amber-600'">{{ pod.ready ? text('心跳就绪', 'Heartbeat ready') : text('未就绪 / 失联', 'Not ready / offline') }}</span>
          </div>
          <p class="mt-1 break-all text-xs text-gray-500">{{ pod.endpoint }} · {{ new Date(pod.at * 1000).toLocaleString() }}</p>
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || approved(pod)" @click="approve(pod)">{{ approved(pod) ? text('地址已确认', 'Endpoint approved') : text('确认此地址', 'Approve endpoint') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || !approved(pod)" @click="probe(pod.id)">{{ text('检查连接', 'Check connection') }}</button>
            <span v-if="probes[pod.id] !== undefined" class="text-xs">{{ probes[pod.id] ? text('检查时可达', 'Reachable at last check') : text('检查未通过', 'Check failed') }}</span>
          </div>
        </div>
        <div v-for="policy in draft.pods" :key="policy.id" class="flex flex-wrap items-center gap-3 text-sm">
          <label class="flex items-center gap-2"><input v-model="policy.enabled" type="checkbox" :disabled="busy" />{{ policy.id }} · {{ text('接收新绑定', 'Accept new bindings') }}</label>
          <code class="break-all text-xs text-gray-500">{{ policy.endpoint }}</code>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="removePod(policy.id)">{{ text('移除规则引用', 'Remove policy') }}</button>
        </div>
      </section>

      <section class="space-y-3">
        <div class="flex items-center justify-between"><h3 class="font-medium">{{ text('地区 → Pod 池', 'Region → Pod pool') }}</h3>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || !loaded" @click="addRegion">{{ text('添加地区', 'Add region') }}</button>
        </div>
        <div v-for="(region, index) in draft.regions" :key="index" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-700">
          <div class="flex flex-wrap items-center gap-3">
            <label class="text-sm">{{ text('国家代码', 'Country code') }}<input v-model="region.country" maxlength="2" class="input ml-2 w-24" placeholder="US" :disabled="busy" /></label>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="draft.regions.splice(index, 1)">{{ text('删除地区', 'Remove region') }}</button>
          </div>
          <div class="flex flex-wrap gap-4">
            <label v-for="pod in draft.pods" :key="pod.id" class="flex items-center gap-2 text-sm"><input v-model="region.pod_ids" :value="pod.id" type="checkbox" :disabled="busy" />{{ pod.id }}</label>
          </div>
        </div>
      </section>

      <section>
        <h3 class="mb-2 font-medium">{{ text('今日分流（UTC）', 'Routing today (UTC)') }}</h3>
        <div class="overflow-x-auto"><table class="w-full text-left text-sm">
          <thead><tr><th>Pod</th><th>{{ text('地区', 'Region') }}</th><th>{{ text('原因', 'Reason') }}</th><th>{{ text('请求', 'Requests') }}</th><th>{{ text('HTTP 错误', 'HTTP errors') }}</th></tr></thead>
          <tbody><tr v-for="row in statRows" :key="row.key"><td>{{ row.pod }}</td><td>{{ row.country }}</td><td>{{ reasonLabel(row.reason) }}</td><td>{{ row.requests }}</td><td>{{ row.errors }}</td></tr></tbody>
        </table></div>
        <p class="mt-2 text-xs text-gray-500">{{ text('统计为入口完成的 HTTP/WS 请求；流已开始后的业务错误不一定反映在 HTTP 状态中。', 'Counts cover completed ingress HTTP/WS requests. Errors after streaming starts may not appear in the HTTP status.') }}</p>
      </section>
      <details class="text-sm">
        <summary class="cursor-pointer">{{ text('Pod 接入说明', 'Pod connection instructions') }}</summary>
        <p class="mt-2">{{ text('主站与 Pod 共用 PostgreSQL/Redis 和至少 32 字节的 RUNTIME_SERVERLESS_SECRET，使用 Secret 注入，不填入页面。Pod 额外配置以下环境变量，并允许主站访问 /internal/serverless/probe 与业务路由：', 'The primary and Pods share PostgreSQL/Redis and a RUNTIME_SERVERLESS_SECRET of at least 32 bytes, injected through a Secret, not this form. Configure these additional Pod variables and allow the primary to reach /internal/serverless/probe and gateway routes:') }}</p>
        <pre class="mt-2 overflow-x-auto rounded bg-gray-50 p-3 text-xs dark:bg-dark-800">RUNTIME_ROLE=gateway
RUNTIME_SERVERLESS_ID=nerd-us-1
RUNTIME_SERVERLESS_REGION=US
RUNTIME_SERVERLESS_ENDPOINT=https://pod.example.com</pre>
        <p class="mt-2 text-xs text-gray-500">{{ text('ID 必须全局唯一；地址应直达该 Pod，不要指向会随机负载均衡的入口。仅同步文本接口和 Responses WebSocket 参与分流；异步任务、管理和支付保持主站。', 'IDs must be globally unique. The endpoint must identify this Pod, not a random load balancer. Routing covers synchronous text APIs and Responses WebSockets; asynchronous tasks, management and payments remain on the primary.') }}</p>
      </details>
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'

interface Policy { id: string; endpoint: string; enabled: boolean }
interface Region { country: string; pod_ids: string[] }
interface Config { enabled: boolean; affinity_scope?: 'api_key' | 'api_key_ip'; fallback_to_primary?: boolean; pods: Policy[]; regions: Region[] }
interface Pod { id: string; boot: string; endpoint: string; region: string; version: string; ready: boolean; at: number }
interface Snapshot { config: Config; pods: Pod[]; stats: Record<string, string> }
const { locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const loaded = ref(false), busy = ref(false), error = ref(''), notice = ref('')
const draft = ref<Config>({ enabled: false, affinity_scope: 'api_key', fallback_to_primary: false, pods: [], regions: [] })
function normalizeConfig(config: Config): Config {
  return { ...config, affinity_scope: config.affinity_scope || 'api_key', fallback_to_primary: config.fallback_to_primary ?? false }
}
const pods = ref<Pod[]>([]), stats = ref<Record<string, string>>({}), probes = ref<Record<string, boolean>>({})
let disposed = false
onBeforeUnmount(() => { disposed = true })
const statRows = computed(() => {
  const rows = new Map<string, {key: string; pod: string; country: string; reason: string; requests: number; errors: number}>()
  for (const [key, value] of Object.entries(stats.value)) {
    const [pod, country, reason, metric] = key.split('|')
    if (!pod || !country || !reason || !['requests', 'errors'].includes(metric ?? '')) continue
    const id = [pod, country, reason].join('|')
    const row = rows.get(id) ?? { key: id, pod, country, reason, requests: 0, errors: 0 }
    if (metric === 'requests') row.requests = Number(value)
    if (metric === 'errors') row.errors = Number(value)
    rows.set(id, row)
  }
  return [...rows.values()]
})
function reasonLabel(reason: string) {
  const labels: Record<string, string> = {
    region: text('地区命中', 'Region match'), binding: text('已有绑定', 'Existing binding'),
    default: text('默认主站', 'Default primary'), unhealthy_fallback: text('无健康 Pod，回主站', 'No healthy Pod; primary'),
    binding_unavailable: text('绑定实例不可用', 'Bound instance unavailable'),
    binding_fallback: text('绑定实例失联，已回主站', 'Bound instance unavailable; moved to primary'),
  }
  return labels[reason] ?? reason
}
async function action(fn: () => Promise<void>) {
  if (busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try { await fn() } catch { if (!disposed) error.value = text('操作失败，请检查配置、集群密钥和网络后重试。', 'Operation failed. Check configuration, cluster credentials and connectivity.') }
  finally { if (!disposed) busy.value = false }
}
async function refresh(replace: boolean) {
  await action(async () => {
    const data = (await apiClient.get<Snapshot>('/admin/serverless')).data
    if (disposed) return
    pods.value = data.pods; stats.value = data.stats
    if (replace || !loaded.value) draft.value = normalizeConfig(data.config)
    loaded.value = true
  })
}
function onToggle(event: Event) { if ((event.target as HTMLDetailsElement).open && !loaded.value) void refresh(true) }
async function save() {
  await action(async () => {
    const config: Config = JSON.parse(JSON.stringify(draft.value))
    config.regions.forEach(r => { r.country = r.country.trim().toUpperCase() })
    const { data } = await apiClient.put<Config>('/admin/serverless', config)
    if (!disposed) { draft.value = normalizeConfig(data); notice.value = text('已保存。新规则在数秒内生效；相同绑定范围内继续使用已有绑定。', 'Saved. New rules take effect within seconds; existing bindings remain within the same scope.') }
  })
}
function approved(p: Pod) { return draft.value.pods.some(v => v.id === p.id && v.endpoint.replace(/\/$/, '') === p.endpoint) }
function approve(p: Pod) {
  const existing = draft.value.pods.find(v => v.id === p.id)
  if (existing) { existing.endpoint = p.endpoint; existing.enabled = false }
  else draft.value.pods.push({ id: p.id, endpoint: p.endpoint, enabled: false })
  notice.value = text('已加入草稿，需保存后才能检查连接。确认就绪后再启用新绑定。', 'Added to draft. Save before checking connectivity, then enable new bindings.')
}
function removePod(id: string) {
  if (!window.confirm(text('移除后，已有绑定会返回错误，或按已保存的回退设置迁到主站。确认该 Pod 已排空？', 'After removal, existing bindings fail or move to the primary according to the saved fallback policy. Confirm that this Pod is drained?'))) return
  draft.value.pods = draft.value.pods.filter(p => p.id !== id)
  draft.value.regions.forEach(r => { r.pod_ids = r.pod_ids.filter(p => p !== id) })
}
function addRegion() { draft.value.regions.push({ country: '', pod_ids: [] }) }
async function probe(id: string) {
  await action(async () => {
    const { data } = await apiClient.post<{ ready: boolean }>('/admin/serverless/pods/' + encodeURIComponent(id) + '/probe')
    if (!disposed) probes.value[id] = data.ready
  })
}
</script>
