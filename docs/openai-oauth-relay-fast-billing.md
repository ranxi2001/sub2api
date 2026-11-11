# 可信 OAuth 中转的 Fast 计费

当本站通过 API Key 接入自营 Sub2API，再由自营站的 Codex OAuth 账号处理请求时，自营站可能保留出站 `priority` 并按 Fast 计费，但仍透传 OAuth 的 `service_tier=default` 回包。本站原有的普通 API Key 规则会因此降为普通档，两站账单不一致。

为已核实这项约定的自营中转，可通过现有管理员账号 API 在**实际凭据账号**的 `extra` 中显式保存：

```json
{
  "openai_oauth_relay_fast_billing": true
}
```

这是后端兼容选项，当前没有新增后台表单控件。使用正常管理认证读取 `/api/v1/admin/accounts/:id`，保留完整原 `extra` 并合入该字段后 PUT 同一路径，再 GET 回读。`extra` 更新为替换语义，不应只上传上面这一项而丢失原配置；不要上传 `credentials` 或输出它们。复制得到的 API Key 账号各自保存该选项。现有 Spark 影子仅允许 OAuth 父账号，沿用原有 OAuth 计费约定；本选项不会新增 API Key 影子支持。精简调度缓存也保留该选项。

默认关闭，仅 OpenAI API Key 账号且字段为 JSON 布尔值 `true` 时启用，不根据站名、URL 或供应商名称自动判断。只有最终出站档位是 `priority` / `fast`，响应档位是 `default` 时保留请求的 Fast 档位。该选项不会开启 Fast 请求，不会把未指定档位的请求升档，也不修改原始回包或宣称实际加速已验证。

| 出站档位 | 回包档位 | 启用后的计费档位 |
| --- | --- | --- |
| priority / fast | default | 保留出站 Fast |
| priority | flex / standard | 按回包降价 |
| 未指定 / default | default | 普通档 |
| ultrafast | default | default，UltraFast 不在兼容范围 |
| priority | ultrafast | priority，不自动升档 |

现有 `openai_response_tier_billing_enabled=true` 表示按实际响应档位结算，其优先级高于本兼容选项；不要同时开启这两种互相冲突的上游约定。普通 API 供应商仍沿用原有降价保护。混合 OAuth 与官方 API、或会在内部主动过滤 Fast 的中转，不能仅凭名称启用此选项，否则可能按高于其实际处理档位收费。

Fast 单价继续由模型／分组价卡决定。需要固定 2 倍时设置目标分组文本价卡的 `fast_multiplier=2`；分组基础倍率与账号统计成本倍率仍各自独立。`free_openai_fast=true` 仍让客户按普通档付费。该兼容选项只决定结算档位，现有统一计费路径负责用量、余额、Key 配额和幂等扣费。

生效时审计日志为 `billing.service_tier_resolved`，`tier_source=trusted_oauth_relay_request`，同时保留 `requested_tier`、`response_tier`、`billed_tier`、账号及请求 ID。日志不记录凭据或正文。

无需数据库迁移，不重算或补扣历史账单。回滚时关闭该字段即可恢复普通 API 回包降价规则；旧二进制忽略此字段。上线需升级实际承接请求的所有节点，再只为已确认的凭据账号启用。代码合并或保存字段均不能替代新请求的用量与实扣核验。
