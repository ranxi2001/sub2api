# OpenAI Responses WebSocket 模式（WS mode）

WS mode 控制 OpenAI Responses 请求在网关与上游之间如何传输。它同时涉及两件不同的事：

1. **客户端 WS ingress**：客户端用 WebSocket 连接 `/v1/responses`，网关再把 turn 转发到上游。
2. **HTTP 流式 WS 加速**：客户端仍用 HTTP/SSE，但网关用上游 WebSocket 连接池承载请求，提前发出 `response.created` 等前置事件。

第 2 项只对普通 OAuth 账号的首轮流式请求生效，单独由 `openai_oauth_ws_sse_acceleration` 控制，详见 [OpenAI OAuth HTTP 流式 WS 加速](openai-ws-sse-acceleration.md)。本文主要说明第 1 项的模式、开关、连接池和排障。

## 开关层级

WS mode 的最终行为由全局配置和账号字段共同决定。`mode_router_v2_enabled` 控制是否按账号模式分发；关闭时仍会检查 legacy WS 资格。

### 全局配置

| 配置 | 默认 | 作用 |
| --- | --- | --- |
| `gateway.openai_ws.enabled` | `true` | 上游 WS 总开关；关闭后协议解析器选择 HTTP/SSE |
| `gateway.openai_ws.oauth_enabled` | `true` | 是否允许 OAuth 账号使用 WS |
| `gateway.openai_ws.apikey_enabled` | `true` | 是否允许 API Key 账号使用 WS |
| `gateway.openai_ws.responses_websockets_v2` | `true` | v2 协议开关；v2 优先于 v1 `responses_websockets` |
| `gateway.openai_ws.force_http` | `false` | 强制协议解析器选择 HTTP/SSE；不会自动启用 WS ingress 的 HTTP 桥接 |
| `gateway.openai_ws.mode_router_v2_enabled` | `false` | 启用账号级 `off` / `ctx_pool` / `passthrough` / `http_bridge` 模式分发 |
| `gateway.openai_ws.ingress_mode_default` | `ctx_pool` | 新会话默认模式，仅在 v2 路由开启时生效 |

`enabled=false` 或 `force_http=true` 不会让客户端 WS 静默变成 HTTP：`ctx_pool` / `passthrough` 在无法使用上游 WS v2 时会失败；HTTP 客户端继续走 HTTP/SSE。显式 `http_bridge` 是独立的 WS ingress 转 HTTP 路径。

`mode_router_v2_enabled=false` 时，不按账号模式区分 `passthrough` / `http_bridge` 等路径；只有通过全局协议开关和 `IsOpenAIResponsesWebSocketV2Enabled()` 账号布尔资格检查后，WS ingress 才按 legacy `ctx_pool` 处理。账号布尔字段全部缺失时不具备该资格。后台保存模式时仍会同步启用布尔值，因此不能笼统认为模式设置完全无影响。对应环境变量为 `GATEWAY_OPENAI_WS_MODE_ROUTER_V2_ENABLED=true`；修改全局配置或环境变量后需要重启网关。

### 账号字段

OAuth 与 API Key 使用不同字段，不能混用：

| 账号类型 | 启用字段 | 模式字段 |
| --- | --- | --- |
| OAuth / setup-token | `accounts.extra.openai_oauth_responses_websockets_v2_enabled` | `accounts.extra.openai_oauth_responses_websockets_v2_mode` |
| API Key | `accounts.extra.openai_apikey_responses_websockets_v2_enabled` | `accounts.extra.openai_apikey_responses_websockets_v2_mode` |

兼容字段（在分类型字段缺失时回退）：

- `accounts.extra.responses_websockets_v2_enabled`
- `accounts.extra.openai_ws_enabled`

v2 模式路由的解析优先级：分类型模式字符串 → 分类型布尔值 → 兼容布尔值 → 全局默认。布尔值 `true` 等价于 `ctx_pool`，`false` 等价于 `off`。后台会将历史值 `shared` / `dedicated` 显示并保存为 `ctx_pool`。

账号级强制 HTTP 字段为 `accounts.extra.openai_ws_force_http`；为 `true` 时该账号不使用上游 WebSocket。

### 后台选项

账号编辑页的「WS mode」下拉框写入分类型模式字段，同时将对应启用布尔值设为「模式不是 `off`」：

| 选项 | 值 | 行为 |
| --- | --- | --- |
| 关闭 | `off` | 禁用该账号的 WS |
| 上下文池 | `ctx_pool` | 复用上游 WS 连接池，推荐用于 OAuth |
| 透传 | `passthrough` | 直接把客户端 WS 会话转发到上游 |
| HTTP 桥接 | `http_bridge` | WS ingress 转 HTTP 上游，下游仍按 WS 返回 |

## 四种模式

### `ctx_pool`（上下文池）

- 网关从上游 WS 连接池取连接并复用；客户端会话在整个生命周期内持有一条上游连接。
- 适合 OAuth 账号、需要低开销多轮会话的客户端。
- 同时是「HTTP 流式 WS 加速」的推荐模式：HTTP 客户端也能使用该连接池，提前收到前置事件。
- 连接数、空闲裁剪和并发系数见下文「连接池与 1013」。

### `passthrough`（透传）

- 客户端 WS 帧直接透传到上游，网关不按 `ctx_pool` 的会话池语义重新排队。
- 客户端需要自己处理 WS 会话、重叠 `response.create` 和取消语义；重叠请求会被拒绝。
- 适合明确要求原生 WS 语义、且上游 WS 可用的客户端。

### `http_bridge`（HTTP 桥接）

- 客户端仍用 WS ingress，但网关把 turn 转成 HTTP 请求发往上游，再把 SSE 事件包装成 WS 事件返回。
- legacy 无状态续轮通常移除 `previous_response_id`，按连接内缓存的 input 和工具调用项重建上下文，不保证包含完整的 assistant 输出历史；smart stateful 会话保留该锚点。加密代理项的归属保护也会保留该锚点，并在发送前核对账号、实际传输和 response 归属；对已知完整代理项，客户端显式 `store=true` 时还会保留客户端完整历史，避免 legacy 重建重复拼接任务。不能仅凭任意或跨连接的 response ID 恢复完整对话；完整边界见[加密代理任务续接](openai-agent-continuation.md)。
- 失败后的跨账号重放仍受限制：需要可重建的 input，工具输出须有配对的调用上下文，且不能含账号绑定的加密内容或 `item_reference`；当前 turn 已向下游输出非心跳事件后不会因此切账号重放。
- 确认上游 HTTP 可用、WS 不可用时可显式选用；仅凭 1013 / “no available account” 不能确定故障来源。
- 它不是原生 WS 加速；上游仍走 HTTP/SSE，延迟特性以 HTTP 路径为准。

### `off`（关闭）

- v2 模式路由下，该账号会被 WS ingress 选号排除；若转发层遇到 `off`，会以 `1008`（策略拒绝）关闭连接。网关不会把该客户端 WS 请求自动改为 HTTP。
- HTTP 客户端仍可使用该账号的 HTTP/SSE 路径；legacy 路由的账号资格按前述布尔字段判断。

## OAuth 账号推荐配置

### 只用 WS ingress

1. 全局：`enabled=true`、`oauth_enabled=true`、`responses_websockets_v2=true`、`force_http=false`、`mode_router_v2_enabled=true`。
2. 账号：WS mode 选择「上下文池」，保存后对应
   `openai_oauth_responses_websockets_v2_enabled=true`、
   `openai_oauth_responses_websockets_v2_mode=ctx_pool`。
3. 确认账号不是 `error`、`schedulable=false`，且没有开启 SIWC、Copilot SDK 或 Excel BPS all-models。

### 同时启用 HTTP 流式 WS 加速

可沿用上述配置；`mode_router_v2_enabled=true` 不是加速本身的必要条件，legacy resolver 通过账号布尔资格及全局 WS v2 检查时也能启用。在此基础上：

1. 勾选账号的「HTTP 流式 WS 加速」，即
   `openai_oauth_ws_sse_acceleration=true`。
2. 关闭「自动透传」；该加速路径要求 `IsOpenAIPassthroughEnabled()==false`。
3. 只对 HTTP 客户端、`POST .../responses`、`stream=true`、且**没有** `previous_response_id` 的首轮请求生效；非流式、`/responses/compact`、Messages 兼容桥接和 chained turn 继续走原传输。

「WS mode」下拉框和「HTTP 流式 WS 加速」是两个独立开关：前者控制客户端 WS ingress / 上游传输，后者只控制 OAuth 的 HTTP 首轮流式加速。

## WS 不可用或模式未生效的条件

### WS mode 不生效

以下条件会影响模式分发或 WS 可用性：

- `mode_router_v2_enabled=false`：不按模式分发，须先通过 legacy 账号布尔资格检查，再使用 `ctx_pool` 路径。
- 全局 `gateway.openai_ws.force_http=true`，或账号 `openai_ws_force_http=true`。
- OAuth 账号开启 SIWC（`openai_siwc=true`）。
- 账号开启 Copilot SDK（`openai_copilot_sdk=true`）。
- Excel BPS 已开启且没有配置 `openai_excel_bps_models` 模型范围（all-models 模式）。
- 账号 `status=error`、`schedulable=false`，或没有可用并发槽位。

### HTTP 流式 WS 加速额外不生效

该项只适用于普通 OAuth 账号，且要求：

- 客户端使用 HTTP Responses，`POST .../responses`，`stream=true`；
- 请求没有 `previous_response_id`（只覆盖首轮）；
- 不是非流式、`/responses/compact` 或 Messages 兼容桥接；
- 自动透传关闭（`IsOpenAIPassthroughEnabled()==false`）；
- 没有插件把该 OAuth 账号路由到自己的传输链；
- 账号不是影子账号、Agent Identity、Personal Access Token 或 BPS 协议账号。

这些条件只影响“HTTP 流式 WS 加速”，不一定会关闭客户端 WS ingress；WS ingress 的可用性按账号类型、全局开关和上游能力判断。

## 连接池与 1013

`ctx_pool` 的关键配置：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `max_conns_per_account` | `128` | 每个账号的上游 WS 连接上限 |
| `min_idle_per_account` | `4` | 每账号保留的空闲连接 |
| `max_idle_per_account` | `12` | 空闲连接裁剪上限 |
| `dynamic_max_conns_by_account_concurrency_enabled` | `true` | 按账号并发动态计算连接上限 |
| `oauth_max_conns_factor` / `apikey_max_conns_factor` | `5.0` | 动态上限系数 |
| `pool_target_utilization` | `0.7` | 目标利用率 |
| `queue_limit_per_conn` | `64` | 单连接排队上限 |
| `max_ingress_connections_per_api_key` | `64` | 单个 API Key 的活跃 WS ingress 会话上限 |

`ctx_pool` 下，一个客户端会话在生命周期内持有一条上游连接。活跃会话数接近 `max_conns_per_account`（或动态上限）时，新会话可能收到：

```text
1013 upstream websocket is busy
```

在 `ctx_pool` 的连接获取路径中，该关闭消息对应获取超时或连接队列满，不足以单独证明连接池耗尽。先核对实际模式、底层错误、握手状态和 `queue_wait_ms`；确认是本地容量限制后，再调整 `max_conns_per_account` / 动态系数、活跃会话数或 `ingress_inter_turn_idle_timeout_seconds`。

上游也可能返回 1013，例如：

```text
close frame: status = StatusTryAgainLater
reason = "no available account"
```

仅凭这个关闭码和 reason 不能归因于上游供应商的账号池：网关本地选号失败也会发送 `1013 no available account`，并发、RPM 或准入失败也可能返回 1013。握手 `101` 后首个 turn 立即关闭仍不足以区分。先结合 `openai.websocket_account_select_failed`、`conn_reader_loop_closed` 等日志确认关闭发生在哪一侧；若确认由上游返回，再：

1. 找上游确认该 key 是否开通 WS、WS 账号池是否有容量；
2. 暂时把该账号改成 `http_bridge`，让 WS ingress 走上游 HTTP；
3. 或把该账号从 WS 调度中排除，改用有 WS 容量的上游。

握手 `101` 只能证明端点接受 Upgrade，不能证明该 turn 能成功；以后续 close frame / 首事件为准。

## 排障

| 现象 | 先检查 | 处理 |
| --- | --- | --- |
| 下拉框选了 `ctx_pool`/`http_bridge` 但行为没变 | `mode_router_v2_enabled`、账号启用布尔值和全局协议开关 | 要按模式分发需开启 v2 路由并重启；legacy 仍检查账号 WS 资格 |
| 面板显示「流式」而不是「WS」 | 客户端是否真的用 WS ingress；`usage_logs.openai_ws_mode` | HTTP 客户端即使使用上游 WS 加速也仍是 HTTP ingress；客户端另行重试 HTTP 是独立请求 |
| 有 WS 连接但 0 成功 | 本地选号日志及 `conn_reader_loop_closed` 的 close status/reason | 先确认 1013 来源；确认上游 WS 不可用且 HTTP 可用后再考虑 `http_bridge` |
| 报 `1013 upstream websocket is busy` | 模式、底层获取错误或握手状态、`queue_wait_ms` | 确认本地容量限制后再扩池或减少会话 |
| 大输出请求首 token 很高 | 是否 WS ingress、输出 token 数、上游是否增量流式 | 按实际事件和耗时判断传输差异；不要只凭输出大小推断 `http_bridge` 更快 |
| 账号明明 active 但仍不走 WS | SIWC / Copilot SDK / Excel BPS all-models / `openai_ws_force_http` / 账号状态 | 关闭冲突开关或换账号 |
| HTTP 流式加速不生效 | 是否首轮、是否 HTTP、`stream=true`、是否有 `previous_response_id`、自动透传是否关闭 | 该开关只覆盖首轮流式请求 |

常用观测：

- `usage_logs.openai_ws_mode`：客户端是否使用 WS ingress；它**不表示**上游最终协议，`http_bridge` 时上游是 HTTP。
- `ingress_ws_protocol_confirm`：`account_type`、`transport`、`ws_host`、`ws_path`、`ws_mode`。
- `ingress_ws_upstream_connected`：`conn_reused`、`conn_idle_ms`、`queue_wait_ms`、`preferred_conn_id`。
- `conn_reader_loop_closed`：上游 close status / reason，需结合本地选号和连接获取日志判断故障来源。
- `ingress_ws_turn_chain` / `ingress_ws_next_turn_chain`：多轮会话的 turn 编号和 `previous_response_id` 链。

## 验证边界

本文说明的是当前代码中的开关、默认值和日志字段，不构成生产性能结论。WS 连接是否成功应以实际 turn 的首事件和 close frame 为准；握手成功不代表请求成功。排障时使用脱敏的账号、连接 ID 和 close reason，不要记录请求正文、凭据或真实用户数据。

## 相关文档

- [OpenAI OAuth HTTP 流式 WS 加速](openai-ws-sse-acceleration.md)
- [OpenAI SIWC](openai-siwc.md)
- [Copilot SDK Codex](copilot-sdk-codex.md)
- [OpenAI HTTP 阶段超时](openai-http-phase-deadlines.md)
