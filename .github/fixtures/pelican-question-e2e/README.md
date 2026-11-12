# Issue #795 隔离应用 E2E：首个待审增量

Refs #795。本目录只提供 CI fixture，不修改应用业务代码，不表示完整验收、真实模型兼容性或经验难度校准完成。逐场景状态由 `acceptance.json` 声明；脚本即使成功，也固定输出 `full_acceptance_complete: false`。

## 本增量的运行链

GitHub `pull_request`（base 为 `production`，包含 fixture/script/workflow 变动）即可触发本分支新工作流，Draft 不跳过。`workflow_dispatch` 只是工作流进入默认分支后的备用入口，不能假定新分支已经可 dispatch。父协调者负责 publication 和精确 head/check 回读。

`APP_SHA` 与 `HARNESS_SHA` 均显式取 PR head（dispatch 时取该事件 SHA），分别 checkout，记录 SHA/tree。`PARENT_BASE_SHA` 记录本轮父协调者提供的整合基线，绝不作为永久固定的被测旧应用 SHA。正常根 Dockerfile 构建嵌入前端的真实应用；浏览器/pg 依赖、镜像和 fixture CA 也全部在运行隔离前准备。运行期间 Compose 一次性内部网络上的应用、PostgreSQL、Redis、假上游和浏览器均无第二网络、host networking、宿主 Docker socket 或发布端口。宿主 CI 的 Docker inspect 只投影网络/端口/挂载/特权配置，不输出容器环境变量。

真实 AUTO_SETUP、迁移及虚构管理员正常登录后，SQL 只初始化两个 `anthropic/apikey` 账号和测试分组/关联，不插入题目、计划或结果。手动测试通过真实页面按钮、prepare API、handler/service 和上游 HTTP 请求执行，A/B 各两样本；输出唯一标记与上游去认证后的题面记录关联，再核真实 DOM 和 localStorage 刷新恢复。分组页面通过真实创建表单保存**禁用**计划并核对 API/PG，尚不执行调度。没有调用任何 `/run` 来代替 RunDue。

## HTTPS fixture 与原 HTTP 建议的差异

当前 `AccountTestService.validateUpstreamBaseURL` 在白名单启用时调用 `ValidateHTTPSURL`，不会读取 `allow_insecure_http` 来放行 HTTP。为保持真实校验启用，fixture 使用 `https://fake-upstream:4199`，临时生成仅含 fixture SAN 的证书，并仅在 CI fixture 镜像中加入该 CA。所有三类 URL host 白名单只列 `fake-upstream`，允许隔离私网，保持 HTTP 禁止。未关闭 TLS 校验、白名单或改动业务代码。该选择与 audit 中的 `http://` 提议不同，须在独审中确认；它不改变任何生产配置。

假上游只接受模型发现和 messages SSE，额外的健康、去认证 capture 读取及单次 refusal 控制端点仅供 fixture。可选 pricing 刷新明确返回 503，真实应用使用打包的 fallback 文件；未知路径/输入记录为拒绝并使浏览器验收失败。网页本身仅允许 app origin；provider 只能由应用调用，Node fixture 控制读取不伪装成浏览器请求。主防外呼边界是所有运行容器的 Docker internal 网络，虚构 key 不是防外呼措施。

## 未完成的完整验收范围

- [ ] A/B 账号随机定时计划由真实 UI 创建，各两个样本；只用限定 SQL 对齐同一 UTC 微秒 nominal time T，等待真正 runner/Claim。
- [ ] 四个账号结果的 PG 投影、去认证 payload、history list/detail API 和真实展开 DOM/iframe 按同一 plan/result ID 对账。
- [ ] 分组计划真实 RunDue/Claim、结果 PG → list/detail API → `PelicanTestsView` 同 ID 对账；不假设分组均匀路由 A/B。
- [ ] 经真实浏览器编辑计划后，旧 PG/API/UI 快照保持不变。
- [ ] 第二 nominal time T2 按现有哈希公式选到另一题，等待真实 tick，新旧记录共存；不能修改生产抽题算法。
- [ ] arm 单次 refusal 后真正产生 failed 持久化结果、失败历史及 lease 释放；现有 refusal server 能力尚未被此增量测试。
- [ ] 对结果/租约/到期字段进行限定 SQL 投影，与 API、capture 和场景 ID 关联。目前只有初始化实体与禁用分组计划投影，没有定时结果 SQL 证据。
- [ ] 远程 Actions 首次运行、raw job log 定向修正、精确 head/check 回读。没有运行本地动态检查。
- [ ] 隔离链路真实截图上传 PR 原生附件并回读；现有组件 job 截图仍只是组件证据，不能混同此 job。无 PNG 提交到源码。
- [ ] 父协调者独审、完整技术验收与产品/经验难度证据。fixture 的简单 SVG 不代表题目作答合格或难度相当。

## 证据与清理

CI artifact 预期包含应用/harness SHA/tree、显式未完成清单、命名场景结果、网络结构投影、仅限 fixture 题面的 provider captures（无认证头/全量 body）、真实手动请求/题面投影、本轮 localStorage 结果投影、必要的实体/计划 PG 投影、浏览器错误/拒绝清单和截图。比较 `actual_prompt` 的边界是本 fixture 的 UTF-8 文本经过真实 service TrimSpace 后的发送值，不能外推任意输入。

不导出 auth token、Cookie、完整 HAR、数据库全表、凭据列、原始应用日志或真实用户数据。不把整个运行目录当附件。fixture 密码/key/CA 都是本轮自建假值，不能用到任何其他环境。失败时也留存上述受限产物，`always()` 销毁容器、网络和一次性卷，artifact 保留七天。父协调者首次远程运行前需审查此增量，发布后仍须按原 brief 继续同一 PR 的未完成范围。
