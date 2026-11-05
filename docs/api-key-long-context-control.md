# 单个 API Key 的长上下文开关

管理员在「用户管理 → API Keys → 密钥模型与上下文设置」调整「允许长上下文」。管理员编辑自己的密钥时也有相同入口。已有和新建密钥默认允许，关闭只影响选中的密钥；普通用户的创建、编辑接口不接受该管理字段。

## 关闭后的行为

文本推理请求在转发前进行本地 Token 估算，包含完整会话历史、系统指令、工具定义和工具结果。缓存命中的文本也属于输入上下文，不因命中缓存而豁免。

使用模型价卡中已有的长上下文阈值，保留达到阈值或超过阈值的模型差异；例如部分 OpenAI 模型超过 272,000 Token、部分 xAI 模型达到 200,000 Token 进入长上下文范围。模型没有可识别阈值时采用 200,000 Token 上限。Key 映射、渠道映射和合成路由不会解除限制；关闭账号或分组的长上下文计费也不会解除准入限制。

本地估算复用项目的 Tokenizer，可能与供应商实际用量不同。该开关用于限制长上下文请求，不是费用额度，也不改变现有计费规则。

引用上游保存的历史、会话、缓存或文件时，服务端缺少核对实际上下文所需的完整内容。因此关闭开关的密钥会拒绝这类请求，返回 `context_unverifiable`；客户端需发送完整可见文本，或请管理员开启长上下文。该规则也适用于加密/压缩历史、远程文档和多模态附件，避免将引用字符串的长度当作全部输入。

Anthropic 的 `redacted_thinking` 属于不可见的加密历史，也会被拒绝。工具参数和 Gemini 函数结果中的业务字段（例如 `file_id`）按完整 JSON 文本计数，不作为协议引用；工具结果中的真实文档、图片等附件仍受上述限制。排队心跳已开始 SSE 响应时，最终转发检查会发送协议对应的错误事件，HTTP 状态保留已提交的值。

## 请求与配置

- HTTP 覆盖 Responses（含 compact）、Chat Completions、Messages、Gemini 原生文本生成及对应路由别名；模型发现和 Token 计数入口保留原行为。
- WebSocket 检查首轮和每个后续轮次；发送前再次检查实际载荷，包含程序补充的历史引用和重放内容。有状态会话更新会被受限密钥拒绝。
- 已排队请求或长连接发现开关变化时重新准入；WebSocket 应重新连接。已发送的请求保留其入场配置。
- 图片、音频、视频和 embeddings 独立入口保留原行为；文本推理请求内含附件时仍需接受可见上下文检查。

管理接口为 `PUT /api/v1/admin/api-keys/{id}`：

```json
{
  "allow_long_context": false
}
```

省略字段保留原值，传 `true` 恢复允许。配置持久化在 PostgreSQL `api_keys.allow_long_context`，迁移文件为 `333_api_key_allow_long_context.sql`，默认 `true`。配置成功提交后失效对应鉴权缓存；鉴权及区域快照版本升级为 v33。

部署包含该迁移的新版二进制或 Docker 镜像后，应用启动时自动执行迁移，沿用 `schema_migrations` 的执行记录。区域鉴权部署需重新发布 v33 快照。回退二进制会保留新增列和设置，但旧版本不执行该开关策略。

## 新功能提示

提示 ID 为 `api-key-long-context-control-initial-announcement`，在用户管理的密钥设置入口、管理员自己的密钥编辑入口和弹窗的开关旁共用。点击任一位置后同步隐藏提示，记录保存在当前浏览器、当前管理员的 `localStorage` 中，刷新或重新登录后保留。关闭提示不会改变长上下文开关，也不会影响旧的模型策略公告。

## 验证

本功能使用本地测试数据验证保存与默认值、普通用户写入隔离、缓存和区域快照、HTTP 提前拒绝、模型阈值、隐式历史引用、WebSocket 多轮及配置变化后的重连。实际供应商 Token 数与线上数据库迁移需在部署环境另行验证。

本次定向后端回归通过 936 个顶层测试，含子用例共 1,890 项通过，零失败。`TestAPIKeyConcurrencyUpgradePaths` 因未配置临时 PostgreSQL 的 `SUB2API_UPGRADE_TEST_POSTGRES_URL` 而跳过；SQLite repository 测试已覆盖默认值、更新持久化及事务回滚。在 `backend` 目录执行：

```bash
go test -p 1 -tags=unit \
  ./internal/service ./internal/repository ./internal/handler/... \
  ./internal/server/middleware ./internal/server/routes \
  -run 'APIKey|LongContext|ResponsesWebSocket|Composite|GroupModelAllowlist|TurnAdmission|AdmissionModelRejection|GatewayForwardErrorAlreadyCommunicated|GeminiCachedContentsCreate_Unknown' \
  -count=1
```

CI 回归另覆盖后台账号探针缺少请求 URL、受限 Key 缺少 URL 时的 HTTP/WebSocket 校验、缓存重试、会话引用、API 响应字段及中间件顺序。后台账号探针不绑定客户 Key，保持原有探测行为；客户 Key 关闭开关时仍在最终发送前检查。完整后端单元、集成与并发回归的结果以对应 PR 提交的 CI 为准。

前端定向回归的 7 个文件、113 项测试通过，覆盖弹窗保存/回显/密钥隔离、管理员公告多入口同步、刷新与登录持久性、未来公告及中英文 key 完整性。修改的 12 个前端文件通过 ESLint，`pnpm run build` 通过 i18n 检查、Vue 类型检查和 Vite 构建。前端尚未执行完整测试集；生产数据库升级和真实供应商调用需另行验证。
