# 成熟渠道与 QQ 社群运维功能对照（已实施，联合验收中）

初始冻结基线 `37dc4620a41a68abaeab329358c15821c41e3f7f`；独审修复阶段按用户新授权通过普通 merge `cc6d40b43` 整合 production `aa0b1abfe5b32c483b32c70fe0c813659ef58083`，保留双方功能、测试和历史。渠道来源 [mizaawa/zayuapi v0.2.219](https://github.com/mizaawa/zayuapi/tree/a71aa9615ccb55a19973908c9f261b5ba25a48c6)，QQ 来源 [DeanZFC/sub2api-custom v0.2.21-custom.4](https://github.com/DeanZFC/sub2api-custom/tree/5876d7d864dae405c2a3b65074fb556f6985adb8)。本清单勾选表示完整功能及验证验收，代码片段或单项测试不能代替完成。

## 原有功能与兼容性

- `account_ops_notifications_v1`：全局开关、邮件、wecom/dingtalk/feishu/custom webhook、加密 URL/签名密钥、冷却、余额/额度规则、手动分组、自动分组。
- `/admin/account-ops`：config、notification-settings、webhooks（含已保存 webhook 测试）、rules/batch、rules/:id、alerts、balance-accounts、threshold-accounts、groups；原有 API 和持久数据保留。
- `account_ops_alerts`、`account_ops_threshold_monitors`、`account_ops_threshold_events`：事件历史、通知送达状态、故障恢复 episode、持久租约及限流。
- `AccountOpsView.vue` / `AutoConfigView.vue`、SmartOpsNav、sidebar 的原有入口及 token guard 继续兼容。
- 当前基线未发现 QQ 独立路由、settings 或 UI；不能将已有 webhook 通知等同于成熟 QQ 社群功能。
- canonical `channels/channel_groups`、`upstream_channels`、账号归属、价格、成本倍率、权限、计费与 API 均保留；新服务商目录独立持久化，不覆盖原 canonical 渠道。

## 渠道来源映射

| 验收 | 能力 | 固定来源 | 目标适配与依赖 |
| --- | --- | --- | --- |
| [ ] | provider CRUD、search、pagination、site types | provider_handler.go / provider_store.go | 独立表、行 revision CAS；替代来源 settings 整体覆盖和进程锁 |
| [ ] | 凭据加密/脱敏与 password/token 会话 | provider_session.go / provider_newapi_auth.go | fork AES-GCM；更换站点/身份不能携带旧凭据 |
| [ ] | metadata-only 连接与真实指标 | provider_remote.go | 余额/今日消费/累计消费/充值 nullable；保留真实 currency；不猜测充值 |
| [ ] | 手动/自动刷新、低余额告警/抑制 | provider_refresh_worker.go | 默认不发真实网络；多实例原子 claim/配置版本 |
| [ ] | 采购成本、登录入口、币种 | UpstreamView.vue / ProviderDialog.vue | 不变更 canonical 财务成本；来源币种或真实换算 |
| [ ] | API Key / Responses 服务商导入 | ProviderAccountImport.vue / ProviderImportPicker.vue / providerImport.ts | 原账号创建 API 与权限保留，刷新站点/密钥与显式选 key |
| [ ] | 创建远端 key、选择/刷新上游 group | provider_keys.go / provider_key_remote.go / ProviderKeyCreateDialog.vue | 显式确认、持久幂等、结果未知不盲目重试 |

## QQ 固定来源完整功能清单

| 验收 | 能力 | 固定来源模块（backend/internal/qqbot，另注除外） | 适配/验证边界 |
| --- | --- | --- | --- |
| [ ] | 回调 challenge、Ed25519、timestamp、AppID、2MiB 限额 | handler/qq_bot_handler.go、client.go、store.go | 持久去重/重放窗口，签名失败不能写队列 |
| [ ] | 持久队列、跨实例 claim、崩溃/过期处理 | store.go、service.go、types.go（ChatExecution） | 结果未知不重放外部/付费副作用 |
| [ ] | 总配置、模块 CAS、历史、加密、AppID 切换保护 | config.go、config_sections.go、setup.go | 默认禁用，密钥不进响应/审计，版本冲突保留草稿 |
| [ ] | 快速配置与诊断、能力预览/测试 | setup*.go、operations*.go、chat_preview.go | 真实模型测试显式确认；本任务只 mock |
| [ ] | 本站聊天/图片独立专用 key 与额度 | setup.go、service/qq_bot_setup.go | fork API key 权限、计费、事务与撤销兼容 |
| [ ] | 网站码/邮件绑定、解绑、用户资料入口 | store.go、email_binding.go、admin_bindings.go | 本人确认、AppID/群/成员隔离、绑定转移与审计 |
| [ ] | 转账申请/确认/历史 | transfer*.go | 双方身份、额度、账本与余额同事务、幂等 |
| [ ] | 群发现、备注、能力、策略、配额 | group_info.go、types.go、config.go、store.go | 持久限流/冷却，真实群权限与配置区别 |
| [ ] | 群管理员聊天授权、成员目录/临时摘要 | group_admins.go、member_directory.go、recent_members.go | 聊天权限不赋予资金/站点管理权限 |
| [ ] | Chat Completions / Responses 聊天、有限重试 | client.go、site_chat.go、site_chat_retry.go、chat_reply.go | DNS rebinding/redirect/private IP，已发送请求谨慎重试 |
| [ ] | 独立聊天/图片密钥、生成、编辑、GIF | image*.go、chat_image_tool.go | 禁止跨地址复用凭据，媒体下载 SSRF/体积限制 |
| [ ] | 图片理解、明确引用、图片意图确认/待确认方案 | image_references.go、message_references.go、image_intent*.go | 成员隔离、来源引用、过期/重复确认 |
| [ ] | 成员历史与上下文、群共享上下文 | group_history.go、group_messages.go、service.go | AppID/群/成员隔离及清理策略 |
| [ ] | 自主接话、跟进、预算/队列/投递 | autonomous*.go | 默认关闭，多实例额度/抢占/避免重复投递 |
| [ ] | 防刷屏撤回/禁言、豁免、治理记录 | moderation*.go、spam*.go | 默认关闭，官方角色验证；未知结果不重试 |
| [ ] | 手动群公告、@所有人、取消/状态/分段 | announcements.go | 显式确认、持久幂等、权限/发送失败分类 |
| [ ] | 价格、分组倍率、启停/删除与分时通知 | price*.go | fork canonical 计费解析兼容 |
| [ ] | 渠道状态、被动故障与恢复通知 | channel_status.go、health*.go | 不主动付费探测、不把未知当正常 |
| [ ] | 模型价格、本人今日用量、自然语言站点查询 | member_insights.go、site_queries.go、site_account_queries.go | 本人绑定、公开报价与个人权限隔离 |
| [ ] | 自定义命令、帮助菜单、知识库、命令中心 | custom_commands.go、site_knowledge.go、help.go、command_centers.go | 配置验证、作用域、输出脱敏 |
| [ ] | 每日随机签到、充值门槛、连续签到奖 | checkin*.go、activities.go | 北京时间、账户级唯一、余额/账本/预算同事务 |
| [ ] | 一次性/周期成长、金额达标日 | growth*.go | 已消耗进度不回退、奖励快照、迟到记录/退款边界 |
| [ ] | 邀请奖、官方邀请事件、入退群记录 | invite.go、service.go、membership_diagnostics.go | 重进不重复、双方绑定、每日上限 |
| [ ] | 抽奖、充值次数、保底、余额/次数/实物/自定义奖 | lottery.go、reward*.go | fork 有效充值/退款语义，次数与奖品原子提交 |
| [ ] | 每日 Token/消费排行榜、榜单奖励 | rankings*.go | 隐私、历史规则快照、预算不足保留资格 |
| [ ] | 活动模板、每日硬预算、奖励管理/发放 | activity_templates.go、growth_rewards.go | 多实例账本锁；默认财务副作用关闭 |
| [ ] | 优惠券与支付依赖 | coupons.go、internal/coupon、支付关联 | 完整适配本 fork coupon/payment/ledger；不能删掉来源实际奖励类型 |
| [ ] | 运行总览、群管理、功能连接、活动奖励、成员账户、记录诊断 UI | frontend/src/views/admin/QQBot*、components/admin/qqbot | 双语、桌面手机、旧地址兼容、功能提示 |
| [ ] | 绑定/转账公开确认页、个人资料卡 | auth/QQBotBindView、QQBotTransferView、ProfileQQBotCard | user/tenant/成员隔离与确认 |

源文件逐一 SHA/额外版权行核对保存在 artifact `source-audit/qqbot-file-provenance.json`（356 blobs 已核固定 Git blob）；这是来源核对，不是已移植/已验收证明。来源模块精确文件名以该 manifest 为准。已移植文件与目标路径见同目录 `mature-community-provenance.json`（386 项）与 `mature-provider-provenance.json`（8 项）；清单中的源码文件名均对应固定来源。服务商 CRUD/UI 采用本 fork 原生接口重新实现，协议辅助函数按 manifest 逐文件适配。

## 必须保留的交付门禁

- [ ] 逐文件 provenance / notices 与 LGPL3 归因。
- [ ] 迁移重放/回滚兼容、真实隔离 PostgreSQL、canonical 数据不变。
- [ ] SSRF、加密、管理员/租户/群成员隔离、重放、幂等、账本、多实例竞争。
- [ ] 同断言冻结基线远程行为 RED / 精确 head GREEN；编译/fixture 失败不算 RED。
- [ ] production 构建、精确 head 全 CI/status。
- [ ] 真实组件合成 fixture、零生产请求的中英桌面/手机 before/after。
- [ ] 原生 PR 图片附件上传、下载 hash 读回；PNG 不进源码。
- [ ] 独立只读 review 由协调者另派；本线程唯一 implementation writer。

本地沿用 `docs/channel-operations.md` 限制；不运行动态 Go、Docker、全量 build/lint/vue-tsc/Wire。不 merge、ready、deploy、release、改生产配置或发送真实消息。

## 当前实现的运行接口（验收状态仍以上表与 PR 精确 head 证据为准）

服务商目录位于 `/admin/channel-operations` 上方，原渠道统计保持可用。`/admin/providers` 支持 GET/POST，`/:id` 支持 GET/PUT/DELETE；PUT/DELETE 必须携带读取时的 revision，DELETE 另需 confirm。字段 `site_type` 为 sub2api/newapi，`auth_mode` 为 token/password；凭据只接收入站，不在读取响应中返回。`/:id/refresh` 是账号资料/消费元数据读取，不是生成测试。上游需要验证码/两步验证时显示具体登录状态，不尝试绕过。

`refresh_interval_seconds=0` 默认禁止后台刷新。显式设置 30～86400 秒后，由具有后台任务职责的实例按 SQL 原子 claim 执行；同一服务商的会话与 refresh token 更新受数据库行锁保护。低余额阈值采用读数来源币种；未知值不作零值或恢复。低余额事件保存于 `mature_provider_alerts`，反复低样本更新同一未结束 episode，不发送外部消息。更新后的登录凭据使用现有固定 AES-GCM 密钥；换站点/账号/认证类型不会继承旧登录。刷新令牌轮换与读数更新在会话锁内持久化。

新增 API Key/Responses 账号时可切换“手动填写/从服务商导入”。`/:id/keys` 和 `/:id/groups` 只返回密钥/分组元数据；创建账号时由服务器读取完整 key 并写入原 canonical 账号创建流程。导入要求完整管理员权限，不对 observer 或 scoped key 开放。原价格、分组、成本、上游渠道绑定、计费行为保留。导入流程不自动启动模型探测。

创建远端 key 使用 `POST /admin/providers/:id/keys`，必须携带 group_id、revision、confirm=true 和 Idempotency-Key。服务端先提交持久 operation 记录，再发送远端创建；成功和失败结果持久重放。进程中断或未知结果不能重发同一操作，管理员需先刷新上游 key 列表核对。token/group/key API 使用校验后的公网 IP 建连，禁止重定向和环境代理。完整凭据不进入上述列表响应、审计附加数据或公开错误文本。

QQ 管理入口 `/admin/qqbot/overview`；配置、快速接入、群管理、活动奖励、绑定、记录保留固定来源页面和旧地址跳转。用户资料页提供绑定卡；公开确认页 `/qqbot/bind`、`/qqbot/transfer`，实际后端确认 API 使用有期 token。回调 `/api/v1/qqbot/webhook` 限制 2MiB、验证 AppID、签名和时间窗；challenge 只签合法短 token。业务消息、活动、通知均由持久队列与来源配置门禁控制，新安装默认关闭。管理 UI 的模型能力测试须用户显式确认，服务器也检查确认字段。本任务所有外部请求与财务副作用只用隔离 fixtures/mock 验证。

QQ 外部模型/平台请求使用禁止 private/特殊用途地址、DNS 固定到 socket、禁止重定向/环境代理的 transport。本系统内部 AI 仅使用服务器接线提供的固定 loopback origin，并验证当前配置的独立聊天/图片专用 key 身份；任意用户输入的内网 URL 没有该例外。图片下载同样校验目标地址及大小。来源中聊天/图片独立 key、群成员上下文、通知、签到、邀请、抽奖、成长、排行、转账和优惠券功能继续适配，全部来源模块已经接线，未验收项不能用开关隐藏后视为完成。

优惠券管理 `/admin/orders/coupons` 与用户 `/coupons` 使用来源固定规则快照；充值选择器沿用本 fork 的支付方式/实例费率范围，优惠调整付款基数和到账额，不覆盖现有费率策略。预留/取消释放/付款消费都有数据库事务；付款后优惠券冲突保留已收款事实并进入管理员审核，不能静默少到账。退款不重新发券，成长/签到的赠额不算有效充值，也不触发充值返利。

## 升级、回滚与证据边界

迁移 345～348 是独立服务商表；350～378 为 QQ/coupon 依赖的新编号迁移，来源旧迁移编号映射记录在逐文件 provenance。未覆盖本 fork 已有迁移。原 `account_ops_notifications_v1`、账号机器人 webhook、canonical channels/upstream_channels/accounts/account_groups、账单、价格和权限数据保持原接口。

应用回退到冻结基线会忽略新表和独立 settings；不能自动撤销回退前已明确执行的远端 key 创建、奖励、转账或优惠券消费。数据清理必须单独迁移；不在此 PR 附可误执行的 DROP/DOWN。现有固定加密密钥不可更换，否则新旧加密凭据都可能失效。本 PR 不包含部署、生产配置修改、PR merge、ready 或 release；feature 分支内的 production 历史整合已获用户授权。

before/after 图只使用真实 Vue 组件与合成数据，不能代表生产验证或完整端到端验收。图片存 artifact，不提交 PNG 源码。61 条原生 PR 图片链接已保留并核对 SHA256：初始8张基线、c58的37张after/compare以及b98的16张差异重拍；b98当前37图另有21张与既有附件逐字节一致。capture SHA不随后续修复变更。独立只读审查和精确最终 head 全 CI 仍是交付门禁，未有证据时保持未勾选。

优惠券的微信支付恢复上下文携带签名的 coupon_id。带券恢复 token 使用独立类型，旧二进制会拒绝该类型，避免回退时忽略优惠并生成全价订单；无券恢复继续使用旧类型。服务器核对请求 coupon 与签名上下文一致，并在创建订单时重新验证归属和有效性。管理员订单详情展示只包含名称/优惠金额/赠额/待审核状态的摘要，需明确确认原优惠利益后手动重试冲突到账。


## 已取得的远程验证与尚未满足的门禁

专用组合回归 [run 43620106126](https://github.com/ranxi2001/sub2api/actions/runs/43620106126) 在 `c58e8e26322c6b1651544ab3bd94e9cf8d825674` 全部通过：provider HTTP/真实 PostgreSQL、完整 QQ 隔离数据库与全迁移链、coupon 财务与 canonical 账号导入兼容、race、前端测试与 production/server/embed 构建、Wire 一致性、真实 UI 渲染。该结果仅证明该提交的检查范围，不代表后续提交已经通过。

后续新增明确拒绝/未知创建结果的 UI 断言、上游 scope 凭据反射断言，均先取得远程旧实现行为 RED，再修复实现。另修复通用 CI 的独立数据库 fixture 与原 securityaudit 空数组 fixture，未削弱已有业务断言。最终结果以 PR 当前 head 的 Checks 与 artifact `STATUS.md` 为准。

历史 Issue805 工作流曾因冻结基线缺辅助脚本而在断言前 exit127，该结果不是行为 RED。新授权的普通 production merge 已引入其已接受的完整 helper/guard/workflow，修复新头必须重新执行。独立只读审查已经给出 F1/F2/F3 阻断；writer 的实现和动态测试不能替代父协调者对最终固定头的独立复审，验收方框保持未勾选。


## 独审 F1/F2/F3 修复边界

F1：生成 POST 一旦可能提交，timeout/DeadlineExceeded/EOF/截断响应、网关5xx和不完整模型输出均停止重放；没有声明上游支持稳定幂等。仅 HTTP trace 未取得连接且错误确认为 dial 失败时允许有界重试。实际已知用量保留，未知不补零。`TestRepairF1SubmittedGenerationIsNotReplayed` 使用真实 siteChatTurn 和 loopback 记录完整 POST 数；确定未提交控制由 `TestRepairF1DefinitelyUnsubmittedDialCanRetry` 覆盖。

F2：优惠券付款在订单行锁内重读持久状态，并与无券订单共用 canonical 恢复谓词：普通 grace、内嵌 BEpusdt 历史窗口和 EasyPay 原始 expires_at+24h；后台不可恢复已取消订单。超窗/不可恢复但已验证付款的订单保留原状态、真实交易号/金额/paid_at，释放未消费券并标记人工审核。重复回调不会履约；管理员明确批准后按原承诺利益处理，可用券在同事务消费，已复用/作废券沿用现有 honor 规则。原 provider/金额/instance 校验和退款边界保留。

F3：每次最终 QQ files/messages 请求在 token 获取之后持有数据库配置共享锁，锁内核对当前 enabled、AppID/凭据、群和相关策略，HTTP 有界20秒；配置保存使用现有独占锁。因此保存返回后旧授权不能再启动新的发送。图片上传与后续消息逐次重新授权；公告已有共享锁保持原路径，避免重复加锁。模型生成本身不持有配置锁，不要求撤回已经在途的消息。真实 PG 双 Service 实例 + model/QQ peer barrier 验证，纯格式单测显式模拟持久授权读取。

修复阶段远程工作流 `mature-review-repairs.yml` 对旧生产 blob 和 clean candidate 分别运行相同断言，逐 case 检查成功/失败标记，拒绝 skip/compile/panic/无测试作为 RED。升级回归读取固定 production 迁移目录验证 fresh/upgrade、多实例迁移、旧版本忽略新表/继续 canonical 写入、再次升级和 checksum 不可变。只证明数据库兼容；旧二进制不包含安全修复，存在未完成机器人任务或付款时不应混用旧执行worker。


## 父复审 R1/R2/R3 补充

R1：`CANCELLED/EXPIRED` 保持真实订单状态；仅已付款且 `coupon.review_required` 的订单显示现有管理员重试入口。拒绝确认不发送请求，接受审核才发送 `confirm:true`，API默认false；服务器限长并严格解析请求，在支付服务重读订单后校验审核确认。保留现有管理员认证、付款/退款检查、人工审核审计和履约幂等，不扩大自动入账。修复沿用原新功能提示ID，不重新公告。

R2：最终发送与配置保存的回归使用三个独立连接池，固定发送者和保存者backend PID，并通过 `pg_locks`、`pg_stat_activity`、`pg_blocking_pids` 确认同一个advisory key的真实共享/独占锁竞争；观察到等待后才释放假QQ对端。轮询时钟仅作观察节奏和失败上界，不能作为成功证明。

R3：普通 moderation 的持久 `started` 只表示不重放的领取记录，不能代替最终授权。禁言和撤回先在锁外取得token，再在共享配置锁内读取当前配置与started操作，校验enabled/群/AppID/AppSecret/sandbox/策略/豁免/有效期，持锁完成实际POST restrict_chat_setting或DELETE messages。撤销时取消操作，结果未知时仍不自动重放。公告、回复、图片上传/消息、自动通知和举报专用执行路径各保留其最终共享锁，配置保存使用同一独占锁；不要求撤回已开始的请求。

修复后候选继续普通整合已发布production：`96b4c2d99` 合并 `999d599f4`、`f0eca1267` 合并 `e90811e27`，新增生产路径逐blob保持一致，无迁移冲突或重编号。旧61原生图片保留，新人工审核before4图源码b98/captureb669已原生上传下载hash一致；新after图以实际生成head记录。父独审未整项clear，所有运行结论以PR精确head和artifact检查点为准。

### Provider session deadline and rotated credentials

Provider metadata and account imports share the same PostgreSQL row-locked
session. Remote work receives the earliest of the caller deadline, 25 seconds
from lock acquisition, and the 35-second transaction deadline minus an 8-second
persistence reserve. Lock waiting consumes the transaction budget; less than one
second of remaining remote budget rejects the operation before authentication.
A timed-out or cancelled import does not create an account. Successfully received
rotated credentials are encrypted and committed while retaining the row lock,
using the transaction context that is detached from browser cancellation.
Database or encryption failures remain errors; this reserve does not promise
recovery from an unavailable database or an upstream rotation whose response was
never received. No background credential replay or generation probe is added.

The focused remote regression uses the real account Create handler, isolated
PostgreSQL and a synthetic TLS provider, including refresh-only/401 renewal,
slow pages, actual row-lock waiting, cancellation and failure. Separate database
read/decrypt and a fresh service verify that the consumed token is not reused.
Signed balance episode coverage includes debt, repeated lows, unknown/failure,
observed recovery and another low episode. Episode deduplication does not claim
external notification delivery verification. Current exact-head results and the
independent review gate are recorded in the PR; prior-head CI is historical.
