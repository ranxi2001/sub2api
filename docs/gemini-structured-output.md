# Gemini Chat Completions 结构化输出

Gemini 平台账号通过 `/v1/chat/completions` 接收 OpenAI 兼容的 `response_format`。网关保留原始 Schema，并在 Gemini 请求构造完成后写入 `generationConfig`，避免中间协议转换丢失输出约束。

| 客户端格式 | Gemini 请求 |
| --- | --- |
| 未指定、`null` 或 `{"type":"text"}` | 保持原有文本输出行为 |
| `{"type":"json_object"}` | `responseMimeType: "application/json"` |
| `{"type":"json_schema","json_schema":{"name":"result","schema":{...}}}` | 上述 MIME 类型，加原始 `responseJsonSchema` |

`json_schema.schema` 必须是 JSON 对象。Schema 属性顺序、数字字面量和 `additionalProperties` 等约束保留原样，不经过工具 Schema 清洗函数。流式和非流式使用同一映射；Code Assist 的请求包装也保留原始 Schema 字节。

以下情况在发送上游之前返回 HTTP `400`、`invalid_request_error`：

- `response_format` 不是对象、缺少合法 `type`，或使用未知类型。
- `json_schema` 缺少对象类型的 `schema`。
- JSON 输出同时携带工具，或映射后的上游模型为图片输出模型。当前兼容路径拒绝这些组合，而不是静默删除格式约束；这不是对所有 Gemini 模型能力的判断。

如果排队心跳已提交 SSE 响应头，HTTP 状态无法再修改；此时发送 `invalid_request_error`（code 为 `invalid_response_format`）流内错误和 `[DONE]`，同样不调用上游。客户端不能仅凭流式 HTTP 200 判断成功。

这是请求参数传递修复。具体模型和供应商对 JSON Schema 子集的支持仍需核实；网关不会将不支持的约束自动改为 prompt，也不会因为输出不符合业务要求而自动重试。客户端应在完整响应组装后执行自己的 Schema 和业务校验，并处理拒绝、截断或取消等终态。

范围仅为 Gemini 平台的 Chat Completions 入口；Anthropic Messages、Responses、Antigravity 和 Gemini 原生入口沿用各自现有行为。本变更不增加配置、数据库迁移或生产请求采集。

来源：[上游 PR #8662](https://github.com/Wei-Shaw/sub2api/pull/8662)、[Issue #4940](https://github.com/Wei-Shaw/sub2api/issues/4940)。
