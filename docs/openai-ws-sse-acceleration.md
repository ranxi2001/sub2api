# OpenAI OAuth HTTP 流式 WS 加速

账号编辑页的「HTTP 流式 WS 加速」默认关闭。开启后，普通 OpenAI OAuth 账号的 HTTP `POST /v1/responses` 流式请求可以使用已有的上游 WebSocket v2 连接池，下游仍接收 SSE。`response.created`、`response.in_progress` 等前置事件不再等待首 token，收到后即刷出；首 token 指标仍由内容事件决定。

需要为后续新建账号统一设置时，可在[OAuth 首次 WS 配置](oauth-initial-ws.md)中选择 WS 模式和加速勾选状态；已有账号仍通过账号编辑调整。

## 使用条件

1. 编辑普通 OpenAI OAuth 账号，将 WS mode 设为「上下文池」，关闭「自动透传」，开启「HTTP 流式 WS 加速」并保存。
2. 网关已有的 `gateway.openai_ws.enabled`、`oauth_enabled`、`responses_websockets_v2` 必须开启，账号及全局 `force_http` 不能开启。`mode_router_v2_enabled=true` 不是加速的必要条件：开启时账号模式须解析为 `ctx_pool` 或 `passthrough`；关闭时 legacy resolver 仍须通过 `IsOpenAIResponsesWebSocketV2Enabled()` 账号布尔资格检查，字段全部缺失时不启用。后台保存模式时会同步对应布尔值。
3. 客户端使用 HTTP Responses 且 `stream: true`。非流式、`/responses/compact`、Messages 兼容桥接及携带非空 `previous_response_id` 的请求继续使用原传输。

数据库字段为 `accounts.extra.openai_oauth_ws_sse_acceleration`，仅布尔值 `true` 启用，不需要数据库迁移。连接数、超时、账号代理和会话隔离沿用已有 WS 配置。自动透传账号继续走自己的 HTTP 转发链；已绑定的 OAuth 出站传输插件优先，不会被这个开关绕过。API Key、setup-token、影子账号、Agent Identity、PAT 和 BPS 协议不由此开关切换传输。

## 错误与回退

WebSocket 握手失败且状态允许回退，或本地在任何 WS 写入前发现 payload 超限时，允许回退一次 HTTP；均要求请求未取消且下游尚未写出。认证、权限和限流错误保留原错误处理。请求写入失败、等待首事件超时或断连都不能证明上游未执行，因此加速路径不会自动重连重放，也不会因此回退 HTTP。元数据已发出后，后续错误在当前流内结束，不拼接另一请求。

`codex.rate_limits` 和 `codex.response.metadata` 两类控制事件在下游尚未提交时短暂缓冲，最多8个事件、共64KiB；收到其他事件时按原顺序释放，超限则保守输出。`response.created`、工具事件及未知事件保持原有提前输出规则。这项缓冲只延后 HTTP 提交，不代表上游没有接收或执行请求，也不允许据此重放。

加速请求已发送、但在推理终态之前收到关闭帧（包括1000）或读取失败时，返回固定错误码 `upstream_stream_incomplete`。仅收到上述控制元数据且尚未提交响应时，返回 HTTP 502 JSON；已经提交 SSE 时，保留原 HTTP 状态并追加单个 `response.failed`，Ops按逻辑502计失败，不把它当成上游返回的HTTP502。失败终态保留已知响应ID和客户端模型，序号继续已观察的事件顺序。客户端取消沿用原有取消与有界排空路径，不改判为上游失败。

诊断保存关闭码、事件/Token/终态计数、请求已发送与下游已提交状态，不记录原始关闭reason、事件正文或密钥。没有用量证据时不生成零Token用量记录；已经观察到的Token或图片信息作为未完成的部分结果保留。上游为何提前关闭仍需独立诊断；关闭加速可恢复该账号后续HTTP请求的原路径，但不保证供应商故障恢复。

提前发出元数据意味着客户端更早收到事件，也意味着不能继续保留首 token 前的无感换请求窗口。它不保证模型生成速度提高，也不改变 token 计费。关闭账号开关即可恢复原 HTTP 路径；不必安装或执行外部插件包。

## 实现来源与验证边界

功能需求参考 `openai-ws-sse-accelerator.s2plugin` 0.2.2 的 manifest 和配置页面，使用本仓库现有 WS 转发器实现；未复制插件二进制或逆向代码。与该包声明的「首事件前失败回退」相比，本实现将回退限于允许的握手失败或本地写入前 payload 超限，避免把上游尚未返回事件误判为请求尚未执行。

回归测试使用离线模拟连接，覆盖路由开关、元数据立即刷出、首 token/用量记录、握手回退和发送后的失败。测试结果不代表真实 OpenAI 网络的性能测量或生产启用状态。

## 界面验证

使用本地 Vite、Chrome 和虚构 OAuth 账号验证：修改前仅有 WS mode；修改后显示默认关闭的新开关，开启并保存时，模拟更新 API 收到布尔值 `openai_oauth_ws_sse_acceleration: true`。前端测试另覆盖重新打开和关闭后的保存行为。截图仅展示本地模拟页面，不是生产配置。

| 修改前 | 修改后（已开启） |
| --- | --- |
| [修改前账号传输设置](https://github.com/ranxi2001/sub2api/blob/297e9201a2f6d6fe70073b370a5b71f49579accf/docs/screenshots/ws-sse-acceleration/before.png) | [新增 HTTP 流式 WS 加速开关](https://github.com/ranxi2001/sub2api/blob/297e9201a2f6d6fe70073b370a5b71f49579accf/docs/screenshots/ws-sse-acceleration/after.png) |

## 相关文档

- [OpenAI Responses WebSocket 模式（WS mode）](openai-ws-mode.md)：模式、账号字段、连接池和 1013 排障。
