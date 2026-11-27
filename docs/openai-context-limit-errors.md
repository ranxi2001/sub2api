# OpenAI Responses 上下文超限错误

上游可能以 HTTP 502 返回 `context_length_exceeded`，也可能在 HTTP 200 SSE 中发送同类 `response.failed` / `error`。普通 Responses、透传模式及非流式 SSE 转 JSON 现在将明确识别的上下文超限默认返回为：

```json
{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input exceeds the model context window"}}
```

HTTP 状态为 400。客户端应压缩历史、减少输入，或选择具备足够上下文能力的模型。重复提交同一请求不会增加模型的上下文上限。

沿用已有识别规则，仅检查结构化错误字段和非 JSON 错误文本；JSON 中回显的输入不会触发转换。普通路径的显式错误透传规则仍可覆盖默认响应。已有 compact 回退先于此转换执行；明确上下文超限不会因本次改动触发账号冷却或换号。

compact 心跳已发出时，HTTP 状态无法改成 400，继续以 SSE `response.failed` 和 `context_length_exceeded` 结束，避免在流末尾追加 JSON。其他已经开始的流式路径保持现有行为。

运维记录保留真实上游 HTTP 状态及请求 ID，新生成的规范化 400 归为 `context_limit` 请求限制，不计入上游故障/SLA 失败。未回填历史 502，不变更数据库结构，不新增配置。此修复只改善错误表达和分类，不提升实际模型上下文容量。

Chat Completions 转 Responses 时，上游即使只在错误对象中给出完整提示 `Your input exceeds the context window of this model. Please adjust your input and try again.`，也会返回 HTTP 400 和 `context_length_exceeded`。已有 SSE 错误透传规则仍可覆盖客户端看到的状态、类型和文案；SLA 按实际错误归属计算。若客户端已收到 SSE 响应头，网关发送错误帧和 `[DONE]`。已确认的输入超限归于客户端，排除在 SLA 及账号 HTTP 错误率统计之外。监控保留上游实际 HTTP 状态，普通 502 仍计入服务错误。

若拒绝前上游已计量，明细保留用量和费用。新写入的这类用量行通过 `usage_logs.sla_excluded` 从成功请求数中排除；迁移默认值为 false，旧用量不回填。
