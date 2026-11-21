# DeepSeek 输出格式标准化校验

DeepSeek API Key 的原生 `POST /v1/chat/completions` 流式转发保留一层首事件结构校验。部分 OpenAI 兼容上游会先发送 synthetic 空 `delta`，紧接真实 assistant 首块，却没有用空行分隔两个 SSE 事件。严格客户端会把两份 JSON 合并后解析，报 `Extra data`。HTTP 200、后续有文本或最终出现 `[DONE]` 都不足以证明首事件格式正确。

此兼容行为按平台、账号类型、模型和实际事件结构判断，不依赖供应商域名、账号名称或 ID。它随 `production` 维护；供应商未修正前，源码同步、发版及二进制升级均须保留。

## 处理边界

- 仅处理 `deepseek` 平台、`apikey` 账号和 `deepseek-` 前缀模型的原生 Chat Completions 流。
- 仅在流的第一行匹配已知的 synthetic 空块，且下一行紧接同模型、不同 ID 的 assistant 空内容首块时，删除 synthetic 空块。每一行都必须是完整有效的 JSON，检查数据的 object、choices、delta 和 finish_reason 结构。
- 已正确分隔的空块、正常 assistant 首块、合法多行事件、工具数据、错误事件和其他无法确认的结构按原流扫描规则保留。不对所有相邻 `data:` 行强制插入空行，也不删除任意空块或解析失败的事件。
- 保留后续文本、用量和 `[DONE]`。读取错误、EOF 和客户端取消继续交给原有流处理逻辑；不新增重试，不改变计费或账号状态。

本实现修复已知的首事件边界异常，不是任意 SSE/JSON 损坏的通用修复器，也不改变 Responses、Anthropic 或非流式响应协议。供应商恢复标准输出后，该分支自然透传正常事件，无需关闭兼容逻辑。

## 回归与升级

`backend/internal/service/deepseek_chat_sse_test.go` 的 `TestDeepSeekChatSSE*` 用例覆盖多个上游地址、平台及账号类型边界、Flash/Pro、LF/CRLF、正常和异常首事件、工具/错误事件保留、读取错误、取消，以及按完整 SSE 事件解析 JSON 和读取用量。测试属于 `unit` 套件，由主后端 CI 执行。

升级候选必须保留 `streamRawChatCompletions` 的校验入口、实现和回归覆盖；仅保留文件但未接入转发链路不算保留修复。已运行此兼容逻辑的站点不得安装缺失该行为的旧正式产物。

移除兼容分支前，需要在获授权的检查中确认相关上游 Flash/Pro 直连流的首事件 JSON、事件分隔和正常终态均符合预期，并单独评审移除变更。上游声称修复、版本号变化或单次 HTTP 200 均不足以作为移除依据。
