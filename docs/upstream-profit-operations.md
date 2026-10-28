# 上游渠道与利润运维

基线 `9620d315f72a7dbf04fa10fca02970e1e410805e`，纠正 #573 的产品定义：渠道身份是账号实际使用的上游 base URL，模型仅作过滤与下钻；不依赖模型定价 `channels` 或手工分类。

## 已核对的口径

- `usage_logs.total_cost`：请求当时持久化的客户标准收费基数，已包含当次模型、缓存、按次/图片等定价结果。不得用当前价卡重算。
- `usage_logs.actual_cost`：请求收费金额；持久化计费链路会规范化该金额。订阅记录表示套餐用量计价，不能声称本次现金收款；simple mode 等未扣费记录需要明确限制。
- `account_stats_cost`：请求时计算并保存的账号统计定价基数；缺失时现有评分使用 `total_cost`。它不是采购付款。
- `account_rate_multiplier`：账号统计/配额倍率快照，与采购成本独立，利润运维不乘此倍率。
- 优先调度利润评分：`SUM(actual_cost) - SUM(COALESCE(account_stats_cost,total_cost)) × account.CostMultiplier()`。`extra.cost_multiplier` 是独立估算倍率，默认 `0.1` 是既有调度默认估算，不能称实际采购价。旧利润准入门使用 `accounts.rate_multiplier`，不能混同于这个评分。
- 当前 usage schema 没有采购成本倍率、完整上游 base URL 或币种快照。旧记录上游归属和采购成本只能按读取时账号配置估算，并保留配置更新时间；不修改 usage 或账本，不伪造历史快照。
- 当前账号 API key 只有单个 `api_key` 字段；发现了分协议 `api_base_urls`，需要处理多端点不确定性，不能复制一笔收入到多个候选上游。

## 验收（实现与实测尚在进行）

- [ ] 独立利润运维入口、只读 API、账号/上游对比、模型下钻、时间/排序/SQL分页、公式/来源/覆盖范围。
- [ ] 渠道运维按有效上游 URL → 账号 → 模型，自动发现；保留用量/延迟/质量/留存错误能力。
- [ ] URL 默认/端口/路径/大小写/秘密/多端点/官方OAuth/无法解析/已删除边界。
- [ ] 零收入/零成本/亏损/缺失或无效倍率/旧记录/按次与缓存/总额毛利率/半开窗口/查询失败回归。
- [ ] 精确 head 远端 Go/PostgreSQL、前端完整验证和浏览器证据。

本机禁止 Go 动态测试/编译、Docker、全量前端 build/typecheck/lint；仅 gofmt、静态检查、离线脚本和定向前端测试。动态 Go/PostgreSQL 与完整验收由精确 head CI 执行。证据目录：`/data/development/artifacts/sub2api-upstream-profit-operations/`。
