普通 Anthropic API Key 的 Messages 请求在签名整流已启用、模型协议允许且上游返回匹配的 HTTP 400 时，可进入既有历史整流重试。显式透传账号不执行这种 400 请求体降级重试。

第一阶段将可见 thinking 转为普通文本、移除 redacted thinking，并保留历史回答及工具结构。若后续仍是匹配的工具签名错误，第二阶段还可能将 tool_use/tool_result 转为带工具标识的文本。这些降级会改变历史表达方式；上游是否接受整流后的请求仍取决于实际响应。

两个重试过滤器以 `json.Number` 保留 JSON 数字，避免将大整数、小数、指数或嵌套数值先转换为 `float64`。保留的工具参数以及二阶段工具文本中的 JSON 数字均不应因解码而舍入；二阶段重建请求时也保留顶层扩展中的数字。此保证针对数字值及其 JSON 数字表示，不承诺整个请求的空白、对象键顺序或字节完全相同。无效的待解码 JSON 仍返回原请求，不将只解码成功的前缀当作有效请求。

显式关闭 API Key 子开关、关闭总开关，或已保存的旧 JSON 缺少 API Key 字段时，均继续保持关闭。未保存配置时，回退二进制可以恢复旧默认；如果已显式保存 `apikey_signature_enabled:true`，旧二进制仍会读取 true，回滚不会自动关闭该选项，需要另行关闭。

离线回归定义见 [两阶段数字与历史契约](../backend/internal/service/claude_apikey_signature_numbers_test.go) 和 [开关与透传契约](../backend/internal/service/claude_apikey_signature_retry_test.go)。mock 回归不能替代线上供应商验收，也不表示该修复已经部署。
