# API Key 输入上下文限制

管理员在用户密钥列表或自己的密钥编辑页打开“密钥模型与上下文设置”，为当前 Key 设置“输入上下文上限（Token）”。普通用户的创建、编辑接口不接受此设置。

| 设置 | 行为 |
| --- | --- |
| 正整数 | 只对当前 Key 使用该输入上限，优先于“允许长上下文”开关和模型价卡阈值。上游模型自身限制仍生效。 |
| 留空或 `0` | 沿用原规则：允许长上下文时不做此项本地限制；关闭时按模型长上下文阈值检查，缺少阈值时默认 200,000 Token。 |

范围是 `0` 至 `2147483647` 的整数。管理接口 `PUT /api/v1/admin/api-keys/:id` 使用字段 `context_token_limit`：省略或 `null` 保留已有值，`0` 清除独立上限。值持久化于 PostgreSQL `api_keys.context_token_limit`；程序启动时执行嵌入的 `341_api_key_context_token_limit.sql`，已有 Key 默认为 `0`。“允许长上下文”字段和原有 `333_api_key_allow_long_context.sql` 保持兼容；鉴权缓存与区域快照升级到 v34，区域节点需刷新快照。旧二进制忽略新增上限，回滚时需评估该限制失效。

检查按完整输入的本地保守估算执行，包括历史、缓存文本和工具定义；转发前会重新检查模型转换、映射和注入后的正文。超限请求在发往模型服务前终止。排队请求或持久 WebSocket 的设置变更需要重新进入鉴权，避免沿用旧上限。它是单次请求输入上限，不是累计用量或输出长度上限，也不改变计费。

应用限制时，引用历史、远程附件、加密摘要等缺少完整可见文本的请求延续原有拒绝规则。独立上限不豁免这些内容；本地估算与上游实际 Token 数可能不同。

## 客户端错误与 Codex

对外不返回管理员开关、Key 策略、具体配置上限或内部计数原因。OpenAI HTTP 请求返回 `400`：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "context_length_exceeded",
    "message": "Your input exceeds the context window of this model. Please adjust your input and try again."
  }
}
```

已开始的 Responses SSE 流和 WebSocket 使用标准 `response.failed` 事件，错误位于 `response.error`，包含同一 `code` 与通用文案。WebSocket 发出事件后关闭连接。Anthropic 和 Gemini 入口保留各自协议的错误结构，文案同样不包含内部策略。

Codex 可把此标准错误识别为上下文窗口耗尽；这不保证所有客户端版本都在本次失败请求中立即压缩重试。建议在客户端将 `model_auto_compact_token_limit` 配置在服务端上限以下，并为系统提示、工具和估算差异留出余量。即使客户端发起压缩，请求和后续摘要仍受同一 Key 的限制，尤其是不可计量的加密摘要；没有为压缩端点放行或伪造 usage。

## 管理员新功能提示

独立上限使用新 ID `api-key-context-token-limit-initial-announcement`，共用于用户管理密钥入口、管理员自己的密钥编辑入口、弹窗上限字段。点击任一标签同步隐藏，使用当前浏览器、当前管理员的 localStorage 保存关闭状态。原模型策略和长上下文开关的公告 ID 保留。

## 验证范围

定向回归覆盖整数边界、正数覆盖与 0 继承、Key 隔离、普通用户写入隔离、数据库读取和更新掩码、鉴权与区域快照、变更后的重新准入，以及 HTTP/SSE/WebSocket 标准错误和无上游发送。前端回归覆盖保存、清空、重新打开、切换 Key、新功能公告多入口同步及刷新/重新登录持久性。真实 Codex 客户端自动压缩、生产数据库迁移与真实供应商调用未作为本地测试执行，CI 结果以对应提交为准。
