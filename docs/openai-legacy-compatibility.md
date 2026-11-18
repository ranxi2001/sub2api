# OpenAI 旧版兼容模式

针对 [#914](https://github.com/ranxi2001/sub2api/issues/914) 中兼容转换与冻结摘要冲突的请求，管理员可以启用 v2.20 风格的普通转发策略。默认关闭，原来的严格策略与 owner-binding opt-out 行为保持不变。本模式只切换新增的 agent 历史恢复机制，不会降级整个应用到 v2.20.0。

```yaml
gateway:
  openai_legacy_compatibility_mode: true
```

环境变量 `GATEWAY_OPENAI_LEGACY_COMPATIBILITY_MODE=true` 优先于 YAML。四套 Compose 模板均透传此变量；这是启动配置，没有后台页面或请求级覆盖。

## 行为与优先级

开启后，HTTP Responses、WS ctx_pool / passthrough / http_bridge、后续轮与原生压缩不建立新的 agent-owner 恢复上下文，不读取或发布其归属/交付记录，也不执行该机制的 input/controls 冻结、账号绑定、工具来源证明和 no-replay 限制。兼容转换按普通构造流程执行，完整历史使用普通账号池和既有重试策略。

该开关优先于 `disable_encrypted_agent_owner_binding`、`openai_400_omit_encrypted_history_enabled`、`openai_409_omit_encrypted_history_enabled` 以及有损历史重启配置。后几项的已保存值保留，但兼容模式内不执行这些新增的重建、shadow 恢复或省略重试，避免它们重新建立冻结摘要。

普通鉴权、用户/Key/分组/模型准入、额度、并发、计费、请求大小、客户端取消及协议字段校验仍执行。`previous_response_id` 沿用普通传输的粘性与协议处理，HTTP 用户/Key 所属校验保留；不允许借兼容模式读取其他用户的服务端会话。WS 普通构造主动移除续接指针后，不再被新增的 `ws_previous_removed` 检查阻断。

上游拒绝仍按正常错误处理路径返回。跨账号密文兼容、上游私有会话状态和任务连续性由上游实际协议决定；开启本模式不会让任意账号都能解密旧历史。普通重试恢复后，也不再提供新增恢复机制的交付去重保证。独立修正的 JSON Number 解码同时适用于默认模式和兼容模式，防止生图参数转换损坏大整数。

## 切换与回滚

所有接流量的节点统一设置，排空已有 WS 连接后重启；环境变量不会热更新。关闭开关并按同样方式重启即可恢复原策略，无数据库迁移，不清理现有 Redis 恢复记录。兼容模式期间不会新增严格恢复证明，重新开启严格模式后，这些会话可能需要客户端重新建立完整上下文；不承诺跨模式无缝续接。

## 验证范围

回归使用虚构输入与本机 mock 上游，检查 HTTP/WS 实际发送、兼容改写、连续回合、账号池选择、上游错误和用量回执。mock 完成只证明网关行为，不等同于真实供应商接受密文或原客户端任务恢复。上线前需另行固定发布产物并按部署流程验收。
