# 400 加密历史有损恢复（Beta）

系统设置 → 网关转发行为，在 409 Beta 旁增加独立的 **400 加密历史有损恢复（Beta）**。默认关闭，不随 409 开关自动启用。管理 API 字段为 `openai_400_omit_encrypted_history_enabled`；旧客户端省略字段时保留已保存值，普通请求不能逐请求开启。

原生 HTTP Responses 收到明确的加密拒绝 HTTP 400 时，先走原有的精确 reasoning 恢复。原有恢复无法处理或一次重试未成功，且管理员启用了本功能，才检查一次有损恢复的资格。仅识别现有明确加密错误分类，不因普通 400、未知参数、权限错误或脱敏文案启动省略。

有损恢复仅处理当前可读任务之前的旧密文：旧 agent/消息/工具结果中的加密片段替换为省略提示；旧 reasoning 密文及其回放元数据转为普通历史消息，保留支持的 summary/content 文本，并明确告知内容缺失。未知可读字段或内容形态会停止恢复。当前任务需要非空可读文本；当前及其后的密文、服务端会话指针、item_reference、compaction/compaction_summary 都不处理。工具调用参数、结果、配对 ID、历史顺序及数字精度保留。

这是有损恢复，不是解密、恢复原始推理或供应商兼容保证。可读任务可能仍需要用户补充说明。客户端的本地历史不会被修改；后续请求再次携带旧密文时，可能再次触发恢复并增加延迟和上游用量。

## 发送边界与 409 衔接

- 仅原生 HTTP 的实际 400 响应适用，包括 `stream:true` 请求在流开始前收到的 HTTP 400。HTTP 200 中的 SSE 错误仍由已有精确恢复处理，不触发本 Beta。WebSocket、桥接和透传不在范围内。
- 当前请求已取消、已向客户端输出、存在已保留/已交付的加密输出、账号归属冲突或存储不可用时停止。
- 只允许一次额外的有损尝试，且它不能晚于本次原生 HTTP 的第 3 次发送。最多为初始发送、一次原有精确恢复、一次 Beta；其他逻辑若已占用发送次数，则不再追加 Beta。
- Beta 固定在实际被拒请求的同一账号、模型、配置、认证身份和路线。每次发送仍执行现有权限、额度、账号资格、归属记录和输入完整性校验。账号或凭据/路线变化时停止，不借此轮换账号。
- 409 Beta 已省略历史后，若实际发送又得到符合条件的加密 400，可由独立的 400 开关允许一次新的受限变换。先验证旧快照，再为明确允许的变换创建新的请求上下文和固定快照；旧快照和旧 owner 记录不修改。
- 新上下文继续检查原有归属记录，不把省略的输入登记为成功消费，也不清理 owner 缓存。新输出继续经过现有交付屏障。最终发送若失败、状态不确定或仍被拒绝，不再继续自动改写或重放。

关闭 400 开关后，后续请求恢复原有行为；409 开关保持独立。设置读取失败按关闭处理，无需数据库迁移。

## 后台诊断

沿用「错误详情 → 诊断载荷 → 上游事件」，事件增加可选 `encrypted_history_400`：

- `applied`：已准备有损替换及恢复尝试，不代表上游一定收到第二次请求。
- `after_409`：本次请求此前实际执行过 409 省略，不是 409 开关的全局状态。
- `decision`：固定枚举，包括 `disabled`、`retry_exhausted`、`canceled_or_output_started`、`unsupported_context`、`delivery_unconfirmed`、`history_integrity`、`ownership_unavailable`、`no_eligible_history`、`same_account_retry_scheduled`。

真正安排 Beta 时使用已有 retry 事件，`reason=encrypted_history_400_omitted`。最终错误保留实际上游状态；诊断不会制造上游调用，也不进入客户端模型响应。日志仅包含布尔值和固定决策，不包含密文、正文、摘要或凭据。旧日志缺少字段代表未知。

## 验证

服务回归覆盖 JSON/SSE 请求的 HTTP 400、开关关闭、精确恢复后进入 Beta、拒绝后的次数上限、409 之后的恢复、非加密 400 和 HTTP 200 流内错误不触发。负例覆盖当前密文、指针/压缩状态、未知可读内容、取消/已有输出、未确认交付，以及账号/凭据/路线/输入变更。

真实 handler 回归使用隔离 Redis 与 localhost HTTP 假供应商，检查 standalone 400 与 409→400 的请求次数、同账号恢复、可读历史及工具结果保留、旧 owner 记录不变。有损恢复再遇到 400、503 或连接断开时均停止自动重放。该测试不调用线上模型，也不证明所有供应商接受降级后的历史。

```sh
go test -race -tags=unit ./internal/service ./internal/handler ./internal/handler/admin -run '^Test(OpenAI400|OpenAI409|OmitOpenAIHistorical|ForwardEncryptedContentSSERetry|UpdateSettingsOpenAI400|UpdateSettingsOpenAI409)' -count=1 -timeout=5m
```

handler 测试需要本地 `redis-server`。前端使用设置页回归、i18n 完整性、类型检查，以及新功能提示关闭和持久性的回归；截图脚本展示两个真实 Vue 开关的独立性，使用虚构状态，不代表生产验收。
