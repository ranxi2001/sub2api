# MiniMax-H3 视频转发

支持提供 OpenAI Videos JSON 协议的 MiniMax-H3 中转服务。此入口不是 MiniMax 原生视频 API，需确认上游支持 `POST /v1/videos`、任务查询及内容重定向；协议参考：[中转服务说明](https://movoapi.top/docs/minimax-h3-api)。

## 配置

1. 创建 MiniMax 按量付费 API Key 账号，填写对应的自定义 Base URL 与上游密钥。已有 Coding Plan 文本入口不变，视频任务不会选择 Coding Plan 账号。
2. 把账号加入 MiniMax 分组，在分组创建或编辑页面勾选“开启 MiniMax-H3 视频模型”。默认未开启，未配置价格时拒绝创建视频。
3. 选择按秒或按次计费，填写美元基础单价。按次计费还需指定每次时长，范围为 1–15 秒；请求时长不符会在提交上游前返回 400。
4. 最终费用为基础费用乘以当前分组倍率。按秒模式先乘请求秒数；按次模式收取一次基础费用。首版所有分辨率使用同一单价，不使用独立视频倍率。输入框支持 `0.07`、`0.005` 等小数，零价格表示免费。

例如按秒单价 `$0.07`、时长 5 秒、分组倍率 `0.5`，基础费用为 `$0.35`，用户扣费为 `$0.175`。按次单价 `$0.20`、每次 5 秒、分组倍率 `0.5`，用户扣费为 `$0.10`。

## 请求与结果

视频使用 Sub2API 入站端点 `/v1/videos`，创建请求为 `POST /v1/videos`。下游客户端应请求 Sub2API 站点地址，并使用绑定 MiniMax 分组的下游密钥。

使用绑定该分组的下游密钥，提交 JSON：

```http
POST /v1/videos
Authorization: Bearer <DOWNSTREAM_API_KEY>
Content-Type: application/json

{
  "model": "MiniMax-H3",
  "prompt": "A puppy and a kitten playing on a rug.",
  "seconds": "5",
  "size": "1344x768",
  "metadata": { "ratio": "16:9" }
}
```

`seconds` 必须为 `"1"`–`"15"` 的整数字符串。每次请求生成一个视频，`n` 可省略或设为 `1`。使用中转服务提供的其他视频型号时，在账号的模型映射里添加具体条目，例如 `h3-video → relay-video-v2`。上游型号须支持上述视频协议，画幅和其他扩展字段按原 JSON 转发。

使用创建请求返回的 `id` 或 `task_id`，携带同一密钥访问：

- `GET /v1/videos/{id}`：查询任务，保留上游 JSON 响应。
- `GET /v1/videos/{id}/content`：上游就绪后返回 HTTPS 输出地址的 302 重定向。不要把 API Key 发送到输出文件域名。

任务绑定创建者、下游密钥及上游账号；其他密钥不能查询。提交请求遇到上游拒绝或连接异常时不自动切换账号重试，避免重复创建付费任务。响应不明确时先排查已有任务，不要自动重发 POST。

## 计费与边界

- 创建受理、排队、生成中及失败状态不记成功用量。首次查询到 `completed` 且 `metadata.url` 或 `url` 提供有效 HTTPS 输出地址，或取得有效内容重定向时结算一次。
- 按提交时的基础报价及请求秒数结算，成功查询时使用当前分组倍率；上游返回或 MP4 实际时长不覆盖请求秒数。基础报价快照保留 24 小时，客户端应在此期间完成查询；本功能没有后台主动轮询结算。
- 状态查询与内容查询共用一次性计费认领和稳定账单 ID，重复查询不会重复扣费。
- 使用记录写入视频数量、请求时长、基础费用、分组倍率和实际扣费。分辨率字段当前使用中转默认值 `768p`，不作为本功能的计价依据。
- 功能只对 MiniMax 分组视频入口生效，Grok、Composite 和其他平台保留原路由与计价；无数据库迁移。回退到不支持 H3 的版本前应先完成在途 H3 任务。

## 定向验证

```bash
cd backend
go test -tags=unit ./internal/service ./internal/handler ./internal/server/routes -run 'MiniMaxH3|Grok.*(Video|Media)|Seedance|Video.*(Cost|Pric|Billing)|NormalizeVideo|Inflight' -count=1
```

```bash
pnpm --dir frontend exec vitest run src/views/admin/__tests__/MiniMaxH3VideoPricing.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
```

用例覆盖协议透传、价格与时长校验、分组路由隔离、任务归属、完成后单次结算、内容重定向和小数输入。真实生成会产生上游费用，需另外授权；离线测试不创建视频。
