> 产品定义已纠正：当前「智能运维 → 渠道运维」按账号有效上游 base URL 自动发现，模型仅作过滤与下钻；本页以下内容是 #573 旧手工分类 API 的兼容记录，不是当前产品操作指南。当前只读渠道与独立利润页面请看 [上游渠道与利润运维](upstream-profit-operations.md)。旧分类数据不迁移成真实 URL，也不参与新页面归属。

# 智能运维 · 渠道运维

## 目标与现有实体

站点管理员从渠道总览进入上游和账号，管理运营归属，比较持久化请求延迟与实测质量轮次。渠道继续使用 `channels`、`channel_groups`、`ChannelService` 及 `/admin/channels` CRUD；分组仍只属于一个渠道，账号仍通过 `account_groups` 参与路由。现有渠道定价、模型映射、归属与质量编辑器继续有效。

仓库没有独立供应商实体；账号的 credentials 中的地址不是可公开的上游目录，不能用 URL 自动分组。新增 `channel_operation_upstreams`（渠道内的人工名称）和 `channel_operation_members`（渠道、现有账号、上游），只表达运维分类，不成为调度输入。一个账号可在多个 canonical 渠道中，但每个渠道最多属于一个运维上游。没有自动探测、网络目标、密钥或计费操作。

## 管理与并发

- 保留 `/admin/channels/pricing` 与所有旧路由。在智能运维下新增 `/admin/channel-operations`。
- 渠道创建/编辑复用既有 API；基本字段编辑不提交价格、映射、分组等未编辑字段。完整配置打开既有渠道编辑页。
- 上游新增、改名、删除与账号分配提供真实持久化 API。候选账号仅来自该渠道 canonical 分组，搜索和分页由服务端处理；已归属但分组后来改变的账号仍显示为失效成员，可明确解除。
- 新增账号归属必须校验账号未删除且仍在渠道分组。禁止把其它渠道的上游用于本渠道。改派必须同时提供先前上游、版本与确认；不自动抢占。
- 渠道行锁串行化该渠道归属变更；独立单调递增 revision 拒绝旧页面。删除上游与解除归属须明确确认。删除上游先解除运维关联，绝不删除账号或改变账号分组、凭据与状态。
- 删除渠道沿用 canonical 删除语义，级联仅删除渠道配置及运维分类，账号与分组实体保留。UI 明示渠道定价/映射影响并二次确认。
- 所有 API 位于既有管理员认证和审计中间件之下；不提供凭据导出。名称是纯文本，不接受端点配置。

## 数据与统计口径

- 用量直接读取 `usage_logs.channel_id`，时间为 `[from,to)`，最多 31 天。旧记录缺少 channel_id 时不猜测归属。公共模型筛选使用 `COALESCE(NULLIF(requested_model,''),model)`；已记录模型使用 `model`；上游模型使用 `upstream_model`。不会把 capability 当 ingress 端点，不复用优先调度快照或已知错误的模型查询。
- 模型谱系显示 public / recorded / upstream 与原始 mapping_chain。`model` 不能保证等于 routing model；没有可靠持久化字段时不声称已知 routing model。
- TTFT 是持久化 `first_token_ms` 的历史转发口径，仅流式且有 token/费用证据的请求；展示 p50/p90/p95、有效/未知数量。总耗时独立用 `duration_ms`；NULL/非正数不当作零。此口径不是端到端或纯上游网络延迟。
- 上游统计按当前人工运维归属回看历史，不能声称是请求发生时的上游路由；改派仅重分组展示，不改写 usage_logs 的历史 channel/model 字段；未分配账号保留单独桶（upstream_id=-1 下钻），渠道总数不因未分配而丢失。当前配置并发只是容量上限，不是实时负载。
- 质量从 `scheduled_test_results` 的冻结配置、judgment 和 round_id 读取，按 plan+round+model 展示和计数。沿用 canonical `ScheduledTestService.ListQualityHistory` 的冻结 ModelIDs 数量 × ParallelCount 完整性规则，模型筛选在完整性校验之后执行；所有配置模型及其并发样本保存完整后，才判断各模型行。模型行通过不等于整个计划通过；通过仍要求该模型全部 correct+success；降级需明确 incorrect 且 answer_mismatch/state_degraded；缺失 judgment/冻结并发数量/轮次 ID、并发轮未完成、传输失败与 skipped 为不确定。质量不等于每个请求的智能水平，错误不自动当降智。
- 质量没有请求 group/channel 维度：按明确登记的运维成员或当前渠道分组账号范围选择（成员被质量处置移除分组后仍保留历史）；分组筛选仅取当前仍属于该分组的账号，明确标记为当前账号范围。模型筛选只匹配结果快照模型，快照缺失不回填当前计划模型；它不与公开别名自动等价，选择 public/recorded 筛选时质量为范围不支持。
- 分别返回 usage/quality 查询状态、查询时间、最新样本时间、来源、窗口和范围限制。成功无样本与查询失败不同；失败不显示成 0。分页统计不能截断汇总分母。错误计数直接读取 ops_error_logs（不读正文/头/原始错误详情），按明确登记的运维成员/当前渠道账号归属和实际日志 group_id 筛选并排除 count_tokens；该表无历史 channel_id，范围明确标注。缺少全请求分母，错误率保持不支持，5xx 不当作降智。
- 质量历史可进入已有质量计划编辑器（选中 plan），不新建默认付费任务。

## 升级与共享边界

仅新增关系表、索引及 channels 的独立 operations revision；不改 Ent schema、Wire 或现有迁移。旧版本忽略新表/字段，升级默认没有上游分类，所有用量仍在未分配桶。回退应用无需回退数据；后续删除关系表须独立迁移。

优先 observation 修复文件（priority_scheduling*、attempt observer、网关、usage 写入和 request timing）不修改。必要的共享接入只有 admin routes、sidebar、router；本功能独立文件实现，最终报告记录实际重叠。本地禁止 Go 编译/动态测试、Docker、全量构建、lint、vue-tsc 与 Wire；远端精确 HEAD CI 验证 Go/PostgreSQL/前端构建。

## 验收清单（按最终提交核验；实际进度及证据见 PR）

- [ ] canonical 渠道 CRUD 完成，旧配置与旧入口兼容；删除不删除账号。
- [ ] 上游 CRUD 与账号搜索/分页/分配/改派/解除可操作；跨渠道、失效账号、旧 revision 与未确认破坏操作被拒绝。
- [ ] 渠道总览 → 上游 → 账号下钻；时间、分组、模型身份筛选正确。
- [ ] p50/p90/p95 和有效/未知样本，未归属桶、总耗时和配置容量有真实来源。
- [ ] 实测质量按轮聚合，有通过/降级/不确定历史与原质量编辑器集成。
- [ ] 数据源、时效、当前归属重分类与质量分组限制可见；空数据、失败与加载状态独立。
- [ ] Go HTTP/SQL 与 PostgreSQL 边界回归测试通过远端精确 HEAD CI。
- [ ] 定向前端交互测试与远端类型/构建通过；浏览器桌面/移动端人工验收。
- [ ] 迁移升级、重复应用、旧数据、删除与并发冲突验收。
- [ ] 中文 Draft PR、Agent 声明、精确 SHA、检查证据和剩余门禁完整。

## API 与人工验收路径

| API | 用途与边界 |
| --- | --- |
| `GET/POST /admin/channels`、`GET/PUT/DELETE /admin/channels/:id` | 原 canonical 渠道 CRUD；基本编辑仅提交 name/description/status；完整价格/分组/映射沿用原编辑器 |
| `GET /admin/channels/:id/operations` | `search/page/page_size` 账号分页；`membership=all/assigned/unassigned`；上游独立 `upstream_search/upstream_page`；返回 revision/total/upstream_total |
| `POST /admin/channels/:id/operations` | action 为 create_upstream/update_upstream/delete_upstream/assign/detach；revision 必填；改派、解除、删除须 confirm，并校验 expected_upstream_id |
| `GET /admin/channels/operations/snapshot` | from/until RFC3339，最长31天；channel_id/upstream_id/account_id/group_id；model_scope=public/recorded/upstream 与 model 精确匹配；dimension=channel/upstream/account；page/page_size；quality_before 游标 |

快照返回 scope（实际应用的筛选，独立于尚未应用的表单）、from/until、usage/quality/errors/capacity。各区块分别返回 status、data、queried_at；失败 data 为 null，不能转成全零。错误记录分组最多20组、模型谱系最多50组，均返回总组数并标注截断；其汇总分母不截断。快照总读取预算25秒、各SQL至多8秒；不会因页面刷新启动探测。

1. 新建渠道 → 打开原完整配置编辑器绑定分组 → 返回渠道运维。确认原有定价/模型映射仍可编辑，基本编辑不会重写它们。
2. 新建两个上游 → 搜索候选账号 → 选择目标上游（支持搜索、分页或精确ID）→ 首次分配。再次改派时取消确认应无写入；确认后刷新读回新归属。
3. 同开两个页面，先在一页改名/分配，再在另一页提交旧 revision；应返回409，并刷新供重新确认。跨渠道上游和渠道外账号应拒绝。
4. 从渠道总览下钻上游（含未分配桶）、账号，分别切换 public/recorded/upstream 精确模型、分组与时间。验证分页前后 summary 分母不变，public 别名不应被当作 routing 模型。
5. 查看质量模型轮历史，核对完整通过、明确降级、NULL judgment、传输失败和部分持久化轮次。点击计划 ID 定位原质量页中的计划，再打开原编辑器；不会执行探测。
6. 错误计数只代表当前账号范围的留存日志；核对错误查询失败、空数据和无完整分母状态。容量只代表当前配置。
7. 删除上游确认后仅解除分类；删除渠道确认后仅移除 canonical 渠道配置与关联。账号、分组实体、凭据和原 account_groups 保留。

自动化证据：`channel_operations*_test.go` 包含实际 HTTP/SQL、认证/observer、并发/版本、错误/质量/模型/时间/分页与迁移重放；前端定向 Vitest 验证真实页面操作；精确HEAD远端工作流另产出合成数据桌面/移动截图和确认写入记录。合成截图不替代真实 PostgreSQL 回归，不代表生产验证。


## Agent 与实施边界

实施 Agent 为 OpenAI Codex CLI v0.164.0，gpt-6.1-sol / xhigh，来自本任务原始启动记录；父协调者 Hermes 为 gpt-6.1-sol，effort 未知（用户提供）。唯一实施者，无子 Agent。独立核对紧急评分修复 #572 后，两 PR 实际修改文件交集为空；本功能不包含该修复的实现或迁移。

## 父审查回归验收

- 客户端错误采用 api/client.ts 的顶层 status 契约。409 回归通过真实 Axios adapter 与拦截器归一化，不能用旧 response.status 假对象替代。
- 创建渠道与导航统一等待渠道切换完成：清除上游/账号、成员/上游/统计分页、质量游标和账号显示模式；真正切换渠道时清空渠道所属分组筛选。时间/模型条件保留；同渠道基本编辑不清空当前下钻和分页。
- [ ] 当前提交真实拦截器409、新建渠道清理、同渠道编辑保留回归。
- [ ] 多模型missing-other-model完整性回归及当前SHA全部push/PR CI审查。


多模型审查样例：冻结计划为 A+B、parallel_count=1。仅保存 A 的正确结果时，即使筛选 A，也显示不确定且 round_complete=false、round_probes=1、round_expected=2；现有 canonical 计划历史同样不能通过。B 的传输失败结果也保存后，A 可显示为模型行通过、B 仍不确定，不能声称整个计划通过。两者都正确时 canonical 计划和两个模型行才都通过。部分/NULL/缺元数据规则保留，未发起补测或自动付费探针。
