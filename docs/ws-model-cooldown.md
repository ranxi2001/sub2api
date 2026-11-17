# WebSocket 上游模型权限拒绝

原生 WebSocket（ctx_pool / passthrough）及 HTTP/SSE → WebSocket 收到明确的模型不可用事件时，复用账号错误策略记录账号与实际上游模型的冷却。Codex OAuth 的模型权限 400 默认冷却 30 分钟，其他模型仍可调度；管理员配置的状态码策略、池模式和临时规则继续生效。WS 握手的 101 不代表模型请求成功。

同时识别 `error` 和 `response.failed`，包括 Codex 的顶层 `status`。模型冷却使用已经映射的模型名，避免模型别名链被映射两次。客户端断开不取消已有错误的状态记录。

ctx_pool 首轮与 HTTP/SSE → WS 只有在没有向客户端输出、请求是自包含的简单文本输入且恢复策略允许时，才交给已有的有限换号流程；不在同一账号重复尝试。输入含续接指针、assistant 历史、工具结果、agent/压缩信息或密文时，不在本路径自动换号，也不改写或删除这些历史。HTTP SSE 加速已刷出控制元数据时，保留既有的禁止重放边界。

原生 ctx_pool 无法安全换号时，下发上游原始模型错误并要求客户端带完整上下文重连。预热后的正式任务属于后续轮，不会重放旧预热包。passthrough 继续透传错误并记录冷却，不增加自动重放。`response.failed` 保留终止事件处理与用量记录。

回归入口：`backend/internal/service/openai_ws_model_failure_test.go`，使用虚构账号和脚本化 WS 上游，原生入口通过本地 WebSocket 客户端驱动。没有真实供应商调用或生产验收。冷却落库失败仍沿用已有错误记录行为，不保证失败的写入能阻止下一次调度。
