# 加密代理任务续接与恢复边界

本页说明 #727 已合入源码的归属保护，关联 [Issue #715](https://github.com/ranxi2001/sub2api/issues/715)。源码合并、Release 和实例部署分别核实；不能只凭版本号认为当前实例已启用这些路径，也不能把离线回归当作真实供应商的格式兼容证明。

## 保留任务后再判断故障

`unsupported_encrypted_agent_message` 表示某一层不能消费代理任务的加密格式。它不等同于 reasoning 的 `invalid_encrypted_content`，也不证明故障来自 BPS；先确定同一故障请求的端点、实际通道和部署 SHA。

保留原会话、完整代理项及已有工具结果，不把删除密文、发送空任务或新建会话当作无损恢复。已有工具结果可能对应真实写操作；未确认执行和交付结果前，不反复提交。更换账号、模型、分组、API Key 或传输不能凭空建立兼容证据。不要为了续接恢复已撤销的凭据或绕过权限限制。

## 哪些续接已有保护

归属按 API Key、分组、会话、实际转发模型和完整代理项摘要隔离，并核对实际账号、认证配置及传输。普通 sticky、HTTP 200 或收到早期事件都不算完成证据。

| 路径或状态 | 当前源码行为与证据边界 |
| --- | --- |
| 原生 HTTP Responses，普通 OpenAI OAuth / API Key | 已完整消费或生成、且最终完成结果交付成功的代理项记录归属；后续完整项回原兼容路由。覆盖 JSON、SSE 聚合与流式终态重建；不证明供应商以后必然接受自身输出。 |
| WS ingress：`ctx_pool`、`passthrough`、`http_bridge` | 三种 adapter 已接入发送前校验、首次交付屏障和完成提交；不同 adapter/HTTP wire 分域。现有 socket 业务夹具为 API Key，未据此声明真实 OAuth 或所有供应商已验收。 |
| `http_bridge` 的已知完整代理项 | 显式 `store=true` 且带 `previous_response_id` 时保留客户端完整历史与该锚点，发送前仍核对权威 owner 与 response 归属。普通历史重建规则不能替代这项校验。 |
| 首次完整产物已可见，producer 尚未确认交付 | 先预留 pending；consumer 在整批最多 3 秒内等完成提交。pending 不授予成功证据，超时保守拒绝，不自动换号。 |
| 重启 / 多应用节点 | 配置共享 Redis adapter 且记录仍有效时共享归属。Redis 记录过期、淘汰、丢失或存储不可用时，不能依靠本机 sticky 补造证据。进程内 fallback 不提供跨节点保证。 |
| Chat/BPS 等不兼容桥的能力目录 | generated/live/fetched/pinned 目录按真实通道限制能力。目录约束减少创建不兼容任务，不能解密旧任务或替用户迁移既有会话。 |
| HTTP ingress → upstream WS、legacy/native 压缩端点、managed credentials | #727 未完成这些路径的完整归属契约，不从其他路径的通过结果推断兼容。 |
| 未知旧密文或缺少原文/兼容路由 | 没有无损自动恢复保证。首次遇到且没有归属记录的项仍走普通准入，不代表被判为兼容；真实上游仍可能拒绝。 |

该保护不新增工具执行重试。已知代理任务携带工具结果时，模糊失败不自动换号重放；客户端主动重交仍是新请求，网关不提供外部工具的全局 exactly-once 事务。新旧 WS 节点混部或回滚旧二进制会失去完整保护，必须核实所有接入节点的实际 SHA。

## 按错误采取下一步

以下 `encrypted_agent_recovery_*` 是本地兼容拒绝分类，不等同于全账号池失效。HTTP 响应未提交时返回 409 JSON；已提交 Responses SSE 时通过 `response.failed` 表达失败，不能把其 HTTP 200 当作成功。WS ingress 目前可能以 1008 和保留任务的通用提示结束，不能承诺客户端总能取得同名 JSON 错误码。

| 错误后缀 | 下一步 |
| --- | --- |
| `delivery_pending` | 等当前 producer 请求结束，核实最终完成/失败及交付结果；交付不确定时不重交工具结果。一直 pending 时由管理员检查完成提交与共享存储，不能靠删除记录解除。 |
| `store_unavailable` | 管理员检查该实例的共享兼容存储可达性与状态；保留现有记录，不清缓存、不换号绕过拒绝。可达性恢复不代表已丢失的归属记录自动恢复。 |
| `owner_unavailable` | 核对原账号的权限、可调度性及可用性。现有配额/并发/权限规则继续生效，其他账号成功不证明能消费这个任务。 |
| `owner_conflict` | 检查同一任务是否对应冲突的路由证据；保留上下文，由管理员定位，不随机挑一个账号，不重放已有工具结果。 |
| `route_changed` | 核对原兼容路由与当前有效授权配置；不恢复已失效凭据，不降低权限门。原路由不可恢复时，本保护不能保证无损续接。 |
| `wire_changed` | 核对实际端点、adapter、模型及认证/组织/项目等出站协商信息；不要把 HTTP 的归属当作 WS 的证据，或仅改客户端别名认为已恢复。 |

## 继续 #715 所需的证据与验收

原报告缺少同一故障请求的实例 SHA、端点和实际通道，不能判定 #727 已覆盖原故障。由管理员经 SSH 只读查询，以一个故障为单位关联以下元数据；公开 issue 仅保留脱敏结论：

- 精确时间和时区、请求关联号、实例和实际构建 SHA、客户端版本、请求端点、stream 与 effective adapter。
- 同接入续接或跨接入、是否为修复前已有任务、是否存在完整代理项、工具结果与 previous-response 锚点。仅报告存在性及类型，不导出密文或业务历史。
- 网关/上游状态、最终协议事件、下游是否已提交及交付、尝试数；本地恢复分类及共享归属的 miss/pending/committed/conflict/failed 状态。未核实项明确标未知，不从邻近请求推定。
- 归属与原账号/模型/分组/会话是否一致，以及凭据或 adapter 是否变化。仅比较一致性，不公开凭据、密文摘要、内部账号标识或 Redis 原始值。

不为补证据发送付费请求、切号探测旧密文、清记录或重启服务。拿到真实失败路径后，在隔离 CI 添加该路径的最小合成业务链：首次生成 → 实际消费完整任务与工具结果 → 同节点/跨节点两轮续接；覆盖 pending、交付失败、取消、route/wire 变化及存储丢失，断言不丢任务、不空发、不重复执行。未知旧密文、压缩及 managed 路径须分别建立契约和回归，不能靠放宽现有校验合并验收。

既有测试入口为 `openai_issue715_recovery_test.go`、`openai_issue715_delivery_race_test.go`、`openai_issue715_ws_test.go`、service recovery/delivery tests 和 Redis adapter tests；远程工作流为 `.github/workflows/issue715-agent-recovery.yml`。新增改动默认交 CI，不在生产构建或通过真实任务试错。
