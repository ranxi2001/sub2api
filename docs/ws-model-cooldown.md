# WebSocket 上游模型权限拒绝

原生 WebSocket（ctx_pool / passthrough）及 HTTP/SSE → WebSocket 收到明确的模型不可用事件时，复用账号错误策略记录账号与实际上游模型的冷却。Codex OAuth 的模型权限 400 默认冷却 30 分钟，其他模型仍可调度；管理员配置的状态码策略、池模式和临时规则继续生效。WS 握手的 101 不代表模型请求成功。

同时识别 `error` 和 `response.failed`，包括 Codex 的顶层 `status`。模型冷却使用已经映射的模型名，避免模型别名链被映射两次。客户端断开不取消已有错误的状态记录。独立 `generate:false` prewarm 的 `error` 和 `response.failed` 同样记录冷却，失败后不会继续发送真实生成请求。模型拒绝不计入账号级失败评分，也不触发其他模型的 5xx 质量任务。

ctx_pool 首轮与 HTTP/SSE → WS 只有在没有向客户端输出、请求是自包含的简单文本输入且恢复策略允许时，才交给已有的有限换号流程；不在同一账号重复尝试。请求绑定 private Astra WS anchor、携带 `x-codex-turn-state`，或输入含续接指针、assistant 历史、工具结果、agent/压缩信息或密文时，不在本路径自动换号，也不改写或删除这些历史。HTTP SSE 已刷出事件或旧路径已缓冲进度事件时，禁止本路径重放。

原生 ctx_pool / passthrough 收到无法安全换号的模型错误（包括 `response.failed`）时，下发上游原始模型错误并要求客户端带完整上下文重连。预热后的正式任务属于后续轮，不会重放旧预热包。安全首轮换号耗尽时，客户端收到明确的 `model_not_found` 错误帧和完整重连提示。passthrough 透传错误、记录冷却并要求重连，不增加自动重放。`response.failed` 保留终止事件和已观察到的用量记录；失败作为模型范围的错误传给调度层。

回归入口：`backend/internal/service/openai_ws_model_failure_test.go`、`backend/internal/handler/openai_issue907_failover_test.go` 及 `backend/internal/handler/openai_ws_model_failure_test.go`，使用虚构账号和脚本化 WS 上游，原生入口通过本地 WebSocket 客户端驱动；真实 handler 双账号覆盖 A→B、下一次新请求/陈旧 sticky、其他模型继续使用 A，以及候选耗尽。handler 用例的冷却持久化使用内存仓库替身。`backend/internal/repository/openai_issue907_model_cooldown_integration_test.go` 另用 PostgreSQL、Redis 和独立缓存客户端验证持久化、outbox 与快照传播；需要对应集成环境运行。没有真实供应商调用或生产验收。冷却落库失败仍沿用已有错误记录行为，不保证失败的写入能阻止下一次调度。
