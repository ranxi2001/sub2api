# OpenAI Responses 上下文超限错误

上游可能以 HTTP 502 返回 `context_length_exceeded`，也可能在 HTTP 200 SSE 中发送同类 `response.failed` / `error`。普通 Responses、透传模式及非流式 SSE 转 JSON 现在将明确识别的上下文超限默认返回为：

```json
{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input exceeds the model context window"}}
```

HTTP 状态为 400。客户端应压缩历史、减少输入，或选择具备足够上下文能力的模型。重复提交同一请求不会增加模型的上下文上限。

沿用已有识别规则，仅检查结构化错误字段和非 JSON 错误文本；JSON 中回显的输入不会触发转换。普通路径的显式错误透传规则仍可覆盖默认响应。已有 compact 回退先于此转换执行；明确上下文超限不会因本次改动触发账号冷却或换号。

compact 心跳已发出时，HTTP 状态无法改成 400，继续以 SSE `response.failed` 和 `context_length_exceeded` 结束，避免在流末尾追加 JSON。其他已经开始的流式路径保持现有行为。

运维记录保留真实上游 HTTP 状态及请求 ID，新生成的规范化 400 归为 `context_limit` 请求限制，不计入上游故障/SLA 失败。未回填历史 502，不变更数据库结构，不新增配置。此修复只改善错误表达和分类，不提升实际模型上下文容量。
