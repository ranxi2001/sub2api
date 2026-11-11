# 千问官方缓存与严格会话粘性

在管理员账号页选择「Qwen」，新建和编辑账号均提供「Qwen 缓存与会话」设置。

- **官方 Responses 会话缓存**：开启后通过原生 Responses 转发，出站添加 `x-dashscope-session-cache: enable`，保留 `previous_response_id` 与 `store`。客户端仍需使用官方支持的多轮续接方式；单次请求出现缓存写入不代表下一次一定命中。关闭后恢复该账号原有协议配置。
- **严格会话粘性**：开启后，同一会话绑定该账号。账号满载时等待原账号，账号停用、冷却、模型不可用或同账号重试耗尽时返回错误，不跨渠道故障转移。需要客户端提供稳定的 `session_id` 或 `prompt_cache_key`；缺少可用会话身份时拒绝派发。跨渠道缓存不互通。
- **地址预设**：Token Plan 使用 `https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1`；千问官方按量付费使用 `https://maas.qianwenaiapi.com/compatible-mode/v1`。地址按钮会替换输入框，API Key 必须与计费模式配套。

两个开关默认关闭，旧千问账号继续使用原 Chat Completions 配置。会话缓存只对明确开启的千问 API Key 账号生效，不把同名模型自动视作千问官方 API。现有 OpenAI 账号及分组不会自动迁移。

管理 API 的 credentials 字段为 `qwen_session_cache_enabled`、`qwen_strict_session_affinity`，值为布尔值。更新仍须保留完整非敏感 credentials；省略 API Key 由服务保留，不应将脱敏占位值回传。缓存开关决定原生 Responses 路径，优先于该账号原有的 `api_protocol`；关闭时无须重建原协议配置。

严格绑定沿用 `gateway.openai_ws.sticky_session_ttl_seconds`，默认一小时。到期后可重新分配；带会话身份的千问首次请求均参与 Redis compare-and-swap，只允许一个账号成为所有者，竞争失败的请求释放槽位并报错，避免未开启严格模式的候选账号抢走已建立的严格绑定。共享缓存无法确认所有者时不猜测渠道；不带会话身份的普通千问请求仍按原配置处理。开启严格模式后请求失败率可能上升，这是保持缓存和状态归属的明确取舍。已有同账号重试规则不变，不新增已发送请求的重放。

原先用 OpenAI 平台接入千问的分组，可在完成平台迁移前按分组开启相同的严格策略：

```yaml
gateway:
  openai_scheduler:
    strict_session_affinity_group_ids: [123]
```

示例 `123` 必须替换为目标分组 ID，所有服务该分组的节点须一致。默认空数组，其他分组不变。此兼容项只改变会话调度，不自动更换平台、协议或添加缓存请求头。配置需重启生效；账号开关通过管理 API 保存后走正常缓存更新。无需数据库迁移；回滚代码会恢复旧调度行为，缓存开关不再生效。

用量同时记录缓存读和缓存写，包括 `usage.input_tokens_details.cache_creation_input_tokens` 与 `usage.prompt_tokens_details.cache_creation_input_tokens`。缓存写入可能按上游规则单独计费；本站价卡须与实际协议匹配，本功能不自动调整价格。以服务端 usage 为准，不用请求头已开启代替实际命中验证。

## 参考设计与边界

- [千问官方上下文缓存](https://platform.qianwenai.com/docs/developer-guides/run-and-scale/context-cache)与 [Responses API](https://platform.qianwenai.com/docs/api-reference/chat/openai-responses)：会话缓存请求头、续接方式及缓存计数。
- [New API 渠道亲和设置](https://github.com/QuantumNous/new-api/blob/main/setting/operation_setting/channel_affinity_setting.go)：区分优先与严格会话模式，失败后是否重选是独立策略。本实现复用 Sub2API 现有 Redis 会话绑定，自行编写，未复制其他项目代码。
- [Rfym21/Qwen2API](https://github.com/Rfym21/Qwen2API) 的上下文前缀缓存主要复用网页端上传文件，不等于官方 Token Plan KV 缓存；其跨账号重放也不符合本功能的严格策略。
- [Git-think/Qwen-Proxy](https://github.com/Git-think/Qwen-Proxy) 的轮询/LRU、[wwwzhouhui/qwen3-reverse](https://github.com/wwwzhouhui/qwen3-reverse) 的网页端适配、[oy-paddy/aliyun-model-proxy](https://github.com/oy-paddy/aliyun-model-proxy) 的多账号/模型容错仅作结构参考，不用于替代官方鉴权或改变本功能的账号归属。

本功能不自动把完整历史转换为 `previous_response_id`，不保存用户对话正文，不保证任意第三方兼容服务支持官方缓存。
