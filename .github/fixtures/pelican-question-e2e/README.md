# Issue #795 隔离应用 E2E：手动、账号定时与分组定时

Refs #795。本目录只提供 CI fixture，不修改应用业务代码，不表示完整验收、真实模型兼容性或经验难度校准完成。逐场景状态由 `acceptance.json` 声明；脚本即使成功，也固定输出 `full_acceptance_complete: false`。

PR #832 的六个真实成功结果和请求身份门禁已由父级接受，仅代表该增量。当前 successor 从 `dfc0219a9e66a19642eb73875e4914e540b49575` 开始，保留原有场景，再增加同账号/分组计划的第二 nominal round；待当前 head 的远程运行证据，不能用旧 SHA 的成功代替。`pelican-detail-identity.mjs` 按真实请求 URL 将响应绑定到 PG result/plan，拒绝整包交换。独立合成门禁与真实应用结果分开记录。

第二轮只根据源码 `pelican_questions.go` 的 bank version、SHA256 前八字节 big-endian 与 modulo 公式选择已到期且题目不同的微秒时间，写入 fixture 计划的 `next_run_at`。不注入题目、不修改抽题算法，不保证普通随机轮次必不重复。远程 oracle 回归另以 #832 两份真实 PG 问题身份校验公式。真实 RunDue/Claim 的持有租约、Finish 释放、last_run_at 和已推进 next_run_at 均读取限定投影；旧/新结果按各自冻结快照关联 provider marker、API 请求 URL 和真实 DOM。
## 本增量的运行链

GitHub `pull_request`（base 为 `production`，包含 fixture/script/workflow 变动）即可触发本分支新工作流，Draft 不跳过。`workflow_dispatch` 只是工作流进入默认分支后的备用入口，不能假定新分支已经可 dispatch。父协调者负责 publication 和精确 head/check 回读。

`APP_SHA` 与 `HARNESS_SHA` 均显式取 PR head（dispatch 时取该事件 SHA），分别 checkout，记录 SHA/tree。`PARENT_BASE_SHA` 记录本轮父协调者提供的整合基线，绝不作为永久固定的被测旧应用 SHA。正常根 Dockerfile 构建嵌入前端的真实应用；浏览器/pg 依赖、镜像和 fixture CA 也全部在运行隔离前准备。运行期间 Compose 一次性内部网络上的应用、PostgreSQL、Redis、假上游和浏览器均无第二网络、host networking、宿主 Docker socket 或发布端口。宿主 CI 的 Docker inspect 只投影网络/端口/挂载/特权配置，不输出容器环境变量。

真实 AUTO_SETUP、迁移及虚构管理员正常登录后，SQL 只初始化两个 `anthropic/apikey` 账号和测试分组/关联，不插入题目、计划或结果。手动测试通过真实页面按钮、prepare API、handler/service 和上游 HTTP 请求执行，A/B 各两样本；输出唯一标记与上游去认证后的题面记录关联，再核真实 DOM 和 localStorage 刷新恢复。分组页面通过真实创建表单保存**禁用**计划并核对 API/PG，这一步不执行调度，后续分组定时场景再经真实 UI 启用。没有调用任何 `/run` 来代替 RunDue。

账号定时增量从真实账号页的 Scheduled tests → Add Plan 创建 A/B 随机计划，各两个样本。年周期 Cron 避免创建期间自动到期；该阶段唯一 SQL 写入是绑定两份计划 ID、账号 ID/fixture 名称和未执行条件，原子地将两者 `next_run_at` 对齐到同一个已到期 UTC 微秒 T，其他计划字段与结果不写入。

脚本等待真实每分钟 runner。唯一外部替身 fake-upstream 暂停这四份回复（每份最多 30 秒），使脚本可以读到两份有效 `running_until`、已推进的到期时间及四份真实 provider capture，同时确认 PG 尚无结果。90 秒内必须观察到两份 Claim，否则失败；释放 provider 回复后最多等 30 秒，要求真正落库四份成功结果、唯一 marker、同一 `scheduled:T` / question / actual_prompt。此能力只延迟外部提供商回复，不调用或替换应用执行器。

真实历史窗口发起账号过滤的 history 和 result detail API；脚本只监听这些响应，对照限定 PG 字段与 provider marker，逐一点击对应卡片、展开题目快照并核对 iframe，截图文件名包含 account/result ID。不读取登录 token 或直接发起待验 API 请求。新场景的代码已实现，仍须在 successor PR 的实际候选 SHA 上远程执行；#809 的历史结果不能证明本增量通过。

分组定时阶段复用先前真实创建的禁用计划：经真实编辑表单设置年周期 Cron，等计划行显示新 Cron 后点击 Enable。以实际 PUT 响应、启用后的 DOM 和限定 PG 规则投影对账，然后仅对这个 fixture plan/group 更新一次 `next_run_at`。仍等待真实分钟 tick/RunDue/Claim，并复用 provider 最多30秒的回复等待来观察有效运行租约、推进后的到期时间及两份真实请求；释放回复后要求两份真实 PG 结果和成功轮次的 lease 完成。每份结果按实际 `account_id` 与 capture alias 对账，允许两份都选中 A 或 B，不断言路由均匀。

页面保持在真实 `PelicanTestsView`，等待其15秒轮询取得本轮 list，随后点击按 result ID 标记的行，监听实际 detail 响应，核对组名、模型、实际账号、成功状态、展开的 question/actual_prompt 和 iframe marker。按 group plan/result ID 保存截图与受限投影。不写结果、调用 `/run` 或注入 auth/store；这个分组的首次 nominal T 不是同一计划的第二轮 T2。CI 浏览器总等待上限为1080秒，job仍为45分钟；本轮新代码未远程验证。

## HTTPS fixture 与原 HTTP 建议的差异

当前 `AccountTestService.validateUpstreamBaseURL` 在白名单启用时调用 `ValidateHTTPSURL`，不会读取 `allow_insecure_http` 来放行 HTTP。为保持真实校验启用，fixture 使用 `https://fake-upstream:4199`，临时生成仅含 fixture SAN 的证书，并仅在 CI fixture 镜像中加入该 CA。所有三类 URL host 白名单只列 `fake-upstream`，允许隔离私网，保持 HTTP 禁止。未关闭 TLS 校验、白名单或改动业务代码。该选择与 audit 中的 `http://` 提议不同，须在独审中确认；它不改变任何生产配置。

假上游只接受模型发现和 messages SSE，额外的健康、去认证 capture 读取及单次 refusal 控制端点仅供 fixture。可选 pricing 刷新明确返回 503，真实应用使用打包的 fallback 文件；未知路径/输入记录为拒绝并使浏览器验收失败。网页本身仅允许 app origin；provider 只能由应用调用，Node fixture 控制读取不伪装成浏览器请求。主防外呼边界是所有运行容器的 Docker internal 网络，虚构 key 不是防外呼措施。

## Successor 的编辑、失败和恢复门禁（待本 head 远程运行）

同计划 T2 后，通过实际账号 Scheduled tests 编辑表单与分组编辑表单，将随机规则改为不同的 custom prompt、样本数改为1。核对 PUT、PG，刷新页面后读取真实 list API，再打开编辑器核对保存值。原计划的 last_run_at 不应被 UI 编辑改写，账号 B 的计划应保持不变。再次打开旧历史，逐条按原 PG result/plan、请求 URL、provider marker 校验旧题目、版本、actual_prompt、样本数和 DOM；不使用当前计划覆盖旧结果。随后经同一真实表单恢复 random 模式，保留新样本数。

账号和分组各运行一次确定 refusal，再各运行一个有效 nominal round。refusal 是实际假提供商 SSE 的 stop_reason=refusal/category，附带唯一 partial-output marker；它是真实 service 的模型输出失败，不是编译 RED、网络拦截或预先写入的 failed 行。账号 A/B 并发时只拒绝一个实际到达的请求，不预设到达顺序或拒绝账号；按 marker/alias 精确绑定实际失败样本。分组拒绝应保留失败答案而不 failover。每轮都观察 Claim、原 last_run_at 保持、Finish 释放、last_run_at 前进及 next_run_at 已推进，检查旧结果不变。

账号失败卡片通过真实 history/detail 展开，核对冻结快照与失败文本。分组原 UI 失败行没有 preview 按钮，仅从实际 list 请求核对 snapshot/身份，并核对 DOM failed/error；不伪造该 UI 不提供的 detail 请求或失败快照 DOM。下一有效轮次要求相同计划再次实际执行成功并保留之前失败历史。

## 剩余验收

- [ ] 当前 successor exact-head 的远程原始日志、每个 attempt、真实 PG/provider/API/DOM 产物；静态实现不是通过，#832 六份结果也不替代本轮。
- [ ] 下载并检查当前真实渲染截图，原生附件上传/字节回读（可用时）；无 PNG 入源码。
- [ ] 父级独立冻结 diff 复审及全部检查验收。
- [ ] 原始产品/完整题库判据和专家或经验难度可比性。没有实际模型调用，fixture SVG 只证明工程链路，`full_acceptance_complete:false` 保留。

## 证据与清理

CI artifact 预期包含应用/harness SHA/tree、显式未完成清单、命名场景结果、网络结构投影、仅限 fixture 题面的 provider captures（无认证头/全量 body）、真实手动请求/题面投影、本轮 localStorage 结果投影、必要的实体/计划 PG 投影、浏览器错误/拒绝清单和截图。账号定时新增 `account-scheduled-created/due/wait/claimed/results.json`、按账号的 history/detail/DOM 投影与按结果命名的截图；SQL 不导出响应全文，只取 fixture marker、ID、status、pelican_config、到期与租约字段。比较 `actual_prompt` 的边界是本 fixture 的 UTF-8 文本经过真实 service TrimSpace 后的发送值，不能外推任意输入。

分组定时新增 `group-scheduled-enabled/due/wait/claimed/results/api-dom.json` 与 `group-scheduled-<planID>-<resultID>.png`；实际选中账号按 fixture ID/name 和 provider alias 关联，SQL 只读 attempts 数量，不导出完整尝试错误。尚未执行编辑后旧快照、第二轮、确定失败历史或失败 lease 验收，成功轮次 lease 完成不能代替它们。

不导出 auth token、Cookie、完整 HAR、数据库全表、凭据列、原始应用日志或真实用户数据。不把整个运行目录当附件。fixture 密码/key/CA 都是本轮自建假值，不能用到任何其他环境。失败时也留存上述受限产物，`always()` 销毁容器、网络和一次性卷，artifact 保留七天。父协调者首次远程运行前需审查此增量，发布后仍须按原 brief 继续同一 PR 的未完成范围。

## 证据目录权限

浏览器与假上游容器使用 CI runner 的 UID/GID，保持 `cap_drop: ALL` 和只读根文件系统。证据目录由同一 runner 创建，因此无需增加 DAC_OVERRIDE 或放宽目录权限。网络结构投影核对这两个容器的实际用户；浏览器启动失败也写入场景结果。

## 首次登录与支付 SDK

全新 fixture 正常完成引导退出与管理员声明确认，均由真实 UI 操作，不注入 localStorage 或伪造认证状态。登录 redirect 直接进入账号页，避免立即整页跳转取消初始化请求。隔离 job 同时发现 Airwallex SDK 被公共 vendor-misc 提前加载；本 PR 将该 SDK 独立分包，仍仅由现有支付页动态 import，账号页不应请求外部支付脚本。

管理员确认前已发出的页面读取会被真实合规中间件拒绝。脚本保留首次初始化阶段的错误列表，确认持久化后正常刷新页面，再对鹈鹕场景执行浏览器错误零值断言；不声称首次安装全过程无控制台错误。

分组计划场景从固定页面地址进入，再使用真实表单创建计划；不依赖当前管理员可定制的侧栏菜单展开/隐藏状态，也不声称已验收侧栏导航。

账号菜单保留原有滚动时关闭行为；harness 先滚动定位并等待绘制稳定，再打开菜单。仅菜单已消失时允许至多两次重新打开，次数写入 `menuReopens`；生成请求不会重试。
