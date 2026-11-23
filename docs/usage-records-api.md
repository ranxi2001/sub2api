# 用 API Key 按时间段读取用量明细

在用户面板的“使用记录”打开“用量 API”，可复制本站完整地址，curl 和可运行的 Python 3 示例。每把 Key 只能读取归属于这把 Key **且属于其当前用户**的记录。每行的 `id` 用于去重，`request_id` 用于关联原请求。查询免费。

## 请求和分页

`GET /v1/usage/records` 支持 `Authorization: Bearer sk-...`，`x-api-key` 和 `x-goog-api-key` 请求头。沿用网关的 Key，用户，IP 和分组访问规则。余额不足，额度耗尽或 Key 到期后仍可查询，已删除或停用的 Key 会被拒绝。

| 参数 | 规则 |
| --- | --- |
| `start_time` | 必填，包含此时刻。使用带 `Z` 或明确时区偏移的 RFC3339 时间（小数秒最多 9 位），或整数 Unix 秒。 |
| `end_time` | 必填，不含此时刻。原始终点须晚于起点。先将未来终点截到服务器当前时间，再校验有效跨度最多 7 × 24 小时。全在未来时返回空页，起止时间均等于服务器当前时间。 |
| `limit` | 每页 1 到 1000 条，默认 500。 |
| `cursor` | 首页省略。后续页原样传回上一页给出的游标，使用同一把 Key、同一时间窗和同一格式。 |
| `format` | 默认 `json`，或使用 `csv`。 |

```bash
curl -sS --fail-with-body -G "$SUB2API_BASE/v1/usage/records" \
  -H "Authorization: Bearer $SUB2API_KEY" \
  --data-urlencode 'start_time=2026-11-20T13:00:00+08:00' \
  --data-urlencode 'end_time=2026-11-20T14:00:00+08:00'
```

`SUB2API_BASE` 填站点网关根地址。`--data-urlencode` 会保留偏移量里的加号。数据按 `(created_at, id)` 升序返回。第一页固定实际终点，后续页沿用原始参数或响应中的 UTC 起止时间。

```json
{
  "object": "list",
  "data": [{
    "id": 42, "request_id": "client:example", "created_at": "2026-11-20T05:12:00.123456Z",
    "model": "example-model", "request_type": "stream", "stream": true,
    "input_tokens": 120, "output_tokens": 30, "cache_read_tokens": 0, "cache_creation_tokens": 0,
    "image_count": 0, "total_cost": 0.002, "actual_cost": 0.001, "rate_multiplier": 0.5,
    "duration_ms": 730, "first_token_ms": 95
  }],
  "has_more": false, "next_cursor": null,
  "start_time": "2026-11-20T05:00:00Z", "end_time": "2026-11-20T06:00:00Z"
}
```

`model` 是用户面板展示的模型，历史行沿用面板的回退规则。`total_cost` 为标准价，`actual_cost` 为用户用量行的实际计费金额，金额单位为 USD。订阅消费和简易模式按各自的记账规则处理。`rate_multiplier` 为该行的倍率快照。`duration_ms` 和 `first_token_ms` 缺少测量值时为 `null`。可选字段有 `reasoning_effort`，`inbound_endpoint`，`ip_address`，`billing_mode`。管理员允许用户查看上游响应模型且有值时，另有 `upstream_response_model`。

每把 Key 每分钟最多 60 次，同一用户的所有 Key 合计每分钟最多 120 次，翻页也计次。429 错误消息区分 Key 级和用户级。 参数或游标无效时返回 `400`，鉴权或访问被拒绝时返回 `401` 或 `403`，超限时返回 `429` 及 `Retry-After` 秒数。查询或限流存储暂时不可用时返回 `503`。参数错误响应形如 `{"type":"error","error":{"type":"invalid_request_error","message":"..."}}`。鉴权失败沿用对应网关中间件的错误体。

## CSV

为同一请求添加 `--data-urlencode 'format=csv' -D usage.headers -o usage.csv`。每页是完整的 UTF-8 BOM CSV。响应头 `X-Has-More: true` 表示还有下一页，用 `X-Next-Cursor` 的值继续请求。末页返回 `X-Has-More: false` 并省略游标头。合并多页时逐页按 `utf-8-sig` 解码并只保留一行表头。

列名及顺序与用户面板 CSV 一致，金额按面板的 `toFixed(8)` 舍入。 方括号中的两列由首页的可见性开关决定，布局写入签名游标。后续开放的列留到下次导出。原可见列关闭或其权限读取失败时返回 `409`，请丢弃本轮部分 CSV，用同一窗口从首页重读。

```text
Time, API Key Name, Model, [Upstream Response Model], Reasoning Effort,
Inbound Endpoint, IP Address, Type, Billing Mode, Input Tokens, Output Tokens,
Cache Read Tokens, Cache Creation Tokens, [Cache Rate], Rate Multiplier,
Billed Cost, Original Cost, First Token (ms), Duration (ms)
```

`Billing Mode` 默认使用英文，`Accept-Language: zh` 对应中文面板的单元格值。需要 `id`，`request_id` 或图片数时选 JSON。

中文计费模式逐字沿用 `frontend/src/i18n/locales/zh/admin/resources.ts` 的值，包括 `按量`、`按次`、`按次(图片)` 和 `按次(视频)`。

启用区域鉴权快照的节点返回 HTTP 503，错误码为 `REGIONAL_USAGE_RECORDS_UNAVAILABLE`。仅启用区域调度预览的节点返回通用 HTTP 503。这两种情况请使用主站地址查询。沿用主库鉴权的区域资金模式可以正常查询。

## 每小时对账

[可运行的 Python 3 示例](../frontend/src/components/usage/usage_records.py) 使用标准库。建议每小时第 10 分钟运行，定时任务使用固定的 SQLite 路径。

| 环境变量 | 说明 |
| --- | --- |
| `SUB2API_USAGE_URL` | 独立脚本必填，填本站最终完整 HTTPS 地址，包含 `/v1/usage/records`。页签复制的脚本已填入本站默认地址。所有重定向都会中止导入。 |
| `SUB2API_KEY` | 必填，填要对账的 API Key。水位按完整端点地址和 Key 的 SHA-256 指纹隔离，水位表只保存指纹。 |
| `SUB2API_USAGE_DB` | SQLite 文件路径，默认是当前目录下的 `usage-records.sqlite3`。已有记录继续按站点和行 id 去重。 |
| `SUB2API_USAGE_OVERLAP_HOURS` | 非负整数，默认 2 小时。已有完整导入水位时从水位减去该时长开始。 |
| `SUB2API_USAGE_START` | 可选整数 Unix 秒，指定该站点和 Key 首次建档时的起点，默认当前 UTC 整点往前 1 小时。 |

- 首次在 14:10 UTC 运行时读取 `[13:00, 14:00)`。若 15:10 漏跑，16:10 从 14:00 水位向前重叠 2 小时，读取 `[12:00, 16:00)`。跨度超过 7 天时自动拆成相邻的半开窗口。每段读完末页后，水位与末页一起提交。中途退出会从上个完整水位恢复。
- 翻页期间才入库、排序位置早于当前游标的记录，本轮后续页会跳过。补拉时以同一窗口从首页重读，省略 `cursor`，并按 `id` 去重。遇到较长的计费积压，请重读所有受影响的小时。
- 普通请求在转发结束后构建计费行时写 `created_at`。长流可以跨小时。异步队列可能推迟构建或落库，#559 恢复已暂存的计费事件时沿用原时间，较早的行可在翻页经过该位置后才出现。Live 会话在关闭时写入会话开始时间，批量图片使用结算时刻。没有固定的最迟入库时间，短重叠窗口只覆盖日常延迟。
- 只查询已持久化的 `usage_logs`。未写用量行的转发失败，未计量请求及单纯的错误日志均不出现。已写入的零费用行仍然出现，包括免费 Live 会话和部分策略拒绝。简易模式也可只记录用量。
- 普通持久化计费在同一事务确认扣费和用量行，恢复重试沿用同一事件。`actual_cost` 和 `total_cost` 保存该行金额。退款和冻结余额释放单独记账。面板 `settled-debits` 读取已确认的钱包扣款凭证，不回写用量行。因此本接口适合请求成本对账，计算钱包现金流需另取钱包凭证。
- 历史长度受后台“请求日志保留天数”控制。设为 0 时跳过按天自动清理。已被保留策略或手动清理删除的记录查不到。

## 将一行对应到自己的请求

同时保存响应头 `X-Sub2api-Usage-Id` 和 `X-Request-Id`，任一值匹配 `request_id` 即可对账。普通 HTTP 转发使用生成请求响应头 `X-Sub2api-Usage-Id`，按原值匹配 `request_id`。该值为 `client:` 加响应头 `X-Client-Request-ID`。Claude，Gemini 原生生成和 Antigravity 普通生成均沿用此次请求的上下文。收到头后仍需等到用量行写入。

| 路径或计费模式 | 对账标识 |
| --- | --- |
| 普通同步图片，`/v1/images/{generations,edits}/async` | 异步任务保留提交时的上下文。保存提交响应的用量 ID。轮询头只标识本次查询。 |
| OpenAI Responses WebSocket，含 HTTP 入口经 WebSocket 桥转发 | 上游提供轮次响应 ID 时优先以它记账，可从对应响应事件保存该 ID。一个握手可包含多轮。 |
| OpenAI HTTP 因 `cyber_policy` 失败且写入用量行 | 该行使用 `X-Request-Id`，缺少该头时使用本地请求 ID。 |
| OpenAI Live | 会话关闭时以通话哈希记一行。 |
| 批量图片 | 使用 `batch_image_capture:<batch_id>`。 |
| 独立网页搜索，Grok 音频和实时会话 | 使用 `web_search:`，`grok_audio:`，`grok_realtime:` 操作 ID。普通生成中附带的搜索沿用该生成请求的计费路径。 |
| 异步视频 | 视频计费使用稳定的 `grok-video:<task_id>` 形式，保存任务 ID。 |
| Gemini 显式缓存 | 创建和延长存储的费用分别使用 `gcache:create:`，`gcache:patch:` 操作 ID。 |
| 区域资金计量 | 使用账本的独立事件 ID 和完成时间。 |

少数历史或内部任务缺少客户端上下文，可使用 `local:`，上游原值或 `generated:`。当响应没有提供对应操作标识时，使用记录 ID 去重，保留原始任务 ID，UTC 时间，模型和用量供核对。
