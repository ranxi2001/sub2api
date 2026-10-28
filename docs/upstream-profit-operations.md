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

## 当前页面与 API

- `/admin/channel-operations` 使用新的 `UpstreamChannelOperationsView`，调用 `GET /api/v1/admin/upstream-operations/snapshot`。
- `/admin/profit-operations` 使用独立 `ProfitOperationsView`，调用 `GET /api/v1/admin/profit-operations/snapshot`。两个入口在 SmartOpsNav、侧栏及 router 中独立存在，共享只读分析组件和归属层。
- 两个 API 均需管理员认证；observer 不得通过这两个全局统计入口越过账号范围。只有 GET，不新建任务或保存配置。旧 #573 的模型渠道手工分类 API 保留兼容，不参与新归属，也不被新页面调用。
- 参数 `from/until` 为带时区 RFC3339，使用 `[from, until)`，最多31天。初始和“近24小时”从同一个时间点减24小时；翻页/排序沿用同一个已应用窗口。`dimension=upstream/account/model`，`model_scope=public/recorded/upstream`；模型精确匹配，不把模型当渠道 ID。`sort=requests/standard_charge/request_income/estimated_cost/estimated_profit/margin/name`，`direction=asc/desc`；`page>=1`，`page_size<=100`。
- 费用、用量、质量与错误从同一 repeatable-read 只读事务读取；金额和延迟由 SQL 聚合并分页，无 account_groups 多对多 join 扩大收费行数。账号配置投影只取端点/非秘密模式及成本估算字段，不读取 api_key/token/header 值；服务响应不返回 credentials。
- 当前自动发现独立于 usage：没有历史请求的账号仍可发现上游和下钻当前成员。发现列表、当前成员、历史对比共用已应用页码并分别显示总数；费用汇总不受分页或前100个来源详情展示上限影响。具体账号可用 account_id 查看。

## 可证明的上游边界

- 普通 OpenAI、Anthropic、Gemini、TypeSafe 和多协议 API 账号复用当前 forwarding getter/default provider profile。scheme/host 转小写，保留显式端口、path 大小写和转义边界，不将 `/tenant-A/v1` 与 `/tenant-B/v1` 或不同端口并为一组。普通拼接按客户端 trim-right 语义处理尾斜杠；Antigravity upstream 透传只 trim 一个尾斜杠，与实际 builder 一致。
- Antigravity upstream 的静态 `base_url` 可归属；OAuth fallback / Vertex ServiceAccount / Bedrock 等无法可靠还原单一 API base 的账号保留未知，不编造域名。
- 普通 OpenAI OAuth/SetupToken 使用代码定义的官方 Codex base；SIWC、Prism、BPS 等动态适配路径保留 `oauth_dynamic_endpoint`。Anthropic OAuth 官方 base 和已启用的自定义中继遵循原 getter。Grok 读取当前站点默认模式；文本/媒体可能走不同 base 时保留 `multiple_effective_bases`。
- 多协议 `api_base_urls` 候选若规范化后不同，整笔历史记录保留 `multiple_effective_bases`，不因相同 host、模型或当前 API protocol 就伪造过去选择。当前没有账号多 key endpoint override schema；只发现单个账号 api_key 以及分协议 base overrides。影子账号没有凭据归属快照，明确 `shadow_parent_unresolved`。
- 含 userinfo/query/fragment、非法 URL 及包含秘密的筛选 URL 都拒绝/进入未知，不把已剥离秘密的 URL 冒充确定上游。软删账号在窗口有 usage 时保留估算来源；物理缺失账号为 `account_missing`。没有 URL 历史快照，响应明确 snapshot 请求数为0。

## 金额和诊断边界

保存的 `actual_cost` 不是现金入账；尤其订阅和 simple mode 不能声称实际收款。成本是保存的历史统计基数乘当前独立估算倍率。零倍率保留为显式零成本；没有倍率使用默认0.1但明确计数，无效倍率/非有限金额不回落成0。任一成本缺失时整体成本/利润/毛利率为NULL，另保留已覆盖部分和计数；零收入时毛利率NULL，亏损保留负数。汇总毛利率由金额总额计算，不平均行百分比。标准或收费金额不完整时相应总额也为NULL。

账号 `updated_at` 只证明当前账号配置更新时间，不能当作成本倍率精确编辑时间；来源详情和汇总均如此标注。历史 `account_stats_cost` 缺失时回退保存 `total_cost` 并计数，不使用当前模型价卡重新计价。缓存、按次、图片/视频、服务等级等已在历史费用管线中计算，统计不再另算。没有逐笔币种字段，仅以系统现有 USD 计费单位呈现，不合并无法比较的外部采购账单。

TTFT 使用原持久化转发口径，不宣称纯上游语义延迟。无计量证据的零额记录单独计数；不能从它们判断支持/成功。质量复用冻结计划×轮次×模型的完整性判定，缺少其他配置模型不能通过。错误只统计留存 `ops_error_logs`，5xx 不等于降智，不编造失败率。各独立区块查询失败有明确状态；费用读取失败直接非2xx，页面不显示成功0。容量只展示当前配置，与实时负载/历史容量区分。

## 实测记录与实施范围

- RED head `739fe7cac`：真实 service/handler 断言和前端页面断言失败，非编译错误；远端 run `40646353497`。
- `25edb9703` 专项 run `40647035692`：同一 PostgreSQL 用例在 RED scaffold 上实际 requests=0、expected=3 失败，恢复实现后通过；该 head 的 service/handler、完整前端构建通过。
- `f198ee39d` 专项 run `40647201513`：追加零值/亏损/默认和无效倍率、半开区间、分页和 SQL 质量/错误状态通过。后续 head 的完整结果以 PR 记录及证据目录为准，不沿用旧SHA结果。
- 本地仅定向单 worker Vitest、gofmt、脚本语法和 `git diff --check`；未运行本地 Go 动态/编译、Docker、完整前端 build/typecheck/lint。生产 SSH/DB、真实付费请求、merge/tag/release/deploy 均未执行。
- CI 首轮安全扫描发现基线 `golang.org/x/net v0.60.0` 的 GO-2026-6860；升级至 v0.62.0 并匹配必要的 x/crypto/sys/term/text/tools/sync 版本，校验和来自 Go checksum database，实际模块/安全验证在远端执行。
- 只取消本 feature 分支已经被新提交替代的旧 CI，不操作其他分支。延迟兜底工作树未读写；priority_scheduling、gateway、usage writer、账本、支付、结算和迁移未修改。
