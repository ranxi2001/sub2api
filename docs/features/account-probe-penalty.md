# Claude 账号探针惩罚

入口：账号管理 → 编辑一个 Claude（Anthropic）账号 → 探针惩罚。默认关闭，勾选后填写请求最低扣费（站内美元账面额度），例如 `0.001`。配置保存在该账号 `extra.probe_penalty`，不改变分组价卡。

```json
{"probe_penalty":{"enabled":true,"minimum_charge":0.001}}
```

适用于通过 `/v1/messages`、`/v1/chat/completions`、`/v1/responses` 完成的 Claude 按 Token 请求，支持 JSON 与 SSE；普通短请求也适用，不分析请求正文是否“有意义”。结算在所有售价倍率之后取原价与最低金额的最大值，失败或流中断的请求保持既有部分用量计费。后台原始 Token 和采购成本不增加，客户余额、订阅额度、Key 限额与用量账单共用补差后的结算金额，费用详情显示补差。

配置随调度快照同步到本站 gateway，结算复用持久化账单与请求去重。启用账号要求标准运行模式及持久化结算仓库。升级所有接流量节点后再启用；旧二进制不识别该设置。没有数据库迁移，关闭开关即可停止新请求补差，历史账单不回写。

## 外部网关

低于最低金额的请求在标准 `usage.output_tokens`（Chat 为 `usage.completion_tokens`） 返回计费等效 Token，原始响应用量放在 `upstream_usage`，`billing` 中标记 `usage_kind=billing_equivalent` 和补差回执。等效输出量按本站这次请求实际输出 Token 单价向上取整。本站账单仍记录实际观测的 Token；等效量不是模型生成长度。

New API、Sub2API 等按标准 usage 计费的下游可读取增加后的输出量。同样的输入/缓存/输出价卡和倍率下，与本站金额的差额小于一个输出 Token 单价。不同下游定价、重新估算 Token、忽略最终 usage 或按次收费都无法由本站强制统一；需要精确金额时，下游应适配 `billing.probe_penalty.actual_cost`，不要把此金额与等效 Token 重复累加。本功能不承诺未测试的第三方版本兼容。

SSE 正文实时转发，末尾 usage 和成功结束事件在本站持久化结算完成后发送；结算结果未知时返回错误，不自动重新请求模型。响应没有完整 usage、定价不能换算为正的输出 Token 单价或等效输出超过 10 亿 Token 时不能生成等效回执，不声称结算成功。非流式响应使用受限临时文件暂存（最大 64 MiB），流式仅暂存末尾事件（最大 1 MiB）。

管理员新功能提示 ID：`account-claude-probe-penalty-initial-announcement`。仅账号编辑设置显示；点击关闭后在当前浏览器当前管理员的本地存储中保留。


源码对照：New API `8542c9d6490b4330fe18160930f81ae7428bc777` 的 [Claude 适配器](https://github.com/QuantumNous/new-api/blob/8542c9d6490b4330fe18160930f81ae7428bc777/relay/channel/claude/relay-claude.go) 按最终 `message_delta` 的累计用量覆盖，并在 JSON 路径使用 `usage.output_tokens`；这是源码证据，不代表所有部署版本已运行验证。`TestProbePenaltyDownstreamSub2APIChargesEquivalentUsage` 用两个实际 Sub2API handler 串接，核对本站补差后，下一级三个入口读取标准 usage 并按同价卡计费。
