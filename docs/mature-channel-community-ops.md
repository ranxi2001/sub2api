# 成熟渠道与 QQ 社群运维功能对照（实施中）

冻结基线 `37dc4620a41a68abaeab329358c15821c41e3f7f`。渠道来源 [mizaawa/zayuapi v0.2.219](https://github.com/mizaawa/zayuapi/tree/a71aa9615ccb55a19973908c9f261b5ba25a48c6)，QQ 来源 [DeanZFC/sub2api-custom v0.2.21-custom.4](https://github.com/DeanZFC/sub2api-custom/tree/5876d7d864dae405c2a3b65074fb556f6985adb8)。本清单勾选表示完整功能及验证验收，代码片段或单项测试不能代替完成。

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
| [ ] | 回调 challenge、Ed25519、timestamp、AppID、2MiB 限额 | handler/qq_bot_handler.go、webhook.go | 持久去重/重放窗口，签名失败不能写队列 |
| [ ] | 持久队列、跨实例 claim、崩溃/过期处理 | store.go、service.go、chat_execution.go | 结果未知不重放外部/付费副作用 |
| [ ] | 总配置、模块 CAS、历史、加密、AppID 切换保护 | config.go、config_sections.go、setup.go | 默认禁用，密钥不进响应/审计，版本冲突保留草稿 |
| [ ] | 快速配置与诊断、能力预览/测试 | setup*.go、operations*.go、chat_preview.go | 真实模型测试显式确认；本任务只 mock |
| [ ] | 本站聊天/图片独立专用 key 与额度 | site_ai.go、setup*.go | fork API key 权限、计费、事务与撤销兼容 |
| [ ] | 网站码/邮件绑定、解绑、用户资料入口 | binding*.go、email_binding.go、admin_bindings.go | 本人确认、AppID/群/成员隔离、绑定转移与审计 |
| [ ] | 转账申请/确认/历史 | transfer*.go | 双方身份、额度、账本与余额同事务、幂等 |
| [ ] | 群发现、备注、能力、策略、配额 | group_info.go、group_policies.go | 持久限流/冷却，真实群权限与配置区别 |
| [ ] | 群管理员聊天授权、成员目录/临时摘要 | group_admins.go、members.go、member_hints.go | 聊天权限不赋予资金/站点管理权限 |
| [ ] | Chat Completions / Responses 聊天、有限重试 | model*.go、chat*.go | DNS rebinding/redirect/private IP，已发送请求谨慎重试 |
| [ ] | 独立聊天/图片密钥、生成、编辑、GIF | image*.go、chat_image_tool.go | 禁止跨地址复用凭据，媒体下载 SSRF/体积限制 |
| [ ] | 图片理解、明确引用、图片意图确认/待确认方案 | references*.go、image_intent.go | 成员隔离、来源引用、过期/重复确认 |
| [ ] | 成员历史与上下文、群共享上下文 | group_history.go、group_messages.go、member_context.go | AppID/群/成员隔离及清理策略 |
| [ ] | 自主接话、跟进、预算/队列/投递 | autonomous*.go | 默认关闭，多实例额度/抢占/避免重复投递 |
| [ ] | 防刷屏撤回/禁言、豁免、治理记录 | moderation*.go、spam*.go | 默认关闭，官方角色验证；未知结果不重试 |
| [ ] | 手动群公告、@所有人、取消/状态/分段 | announcements.go | 显式确认、持久幂等、权限/发送失败分类 |
| [ ] | 价格、分组倍率、启停/删除与分时通知 | price*.go | fork canonical 计费解析兼容 |
| [ ] | 渠道状态、被动故障与恢复通知 | channel_status.go、health*.go | 不主动付费探测、不把未知当正常 |
| [ ] | 模型价格、本人今日用量、自然语言站点查询 | prices.go、site_queries.go、usage.go | 本人绑定、公开报价与个人权限隔离 |
| [ ] | 自定义命令、帮助菜单、知识库、命令中心 | custom_commands.go、knowledge.go、help.go、command_centers.go | 配置验证、作用域、输出脱敏 |
| [ ] | 每日随机签到、充值门槛、连续签到奖 | checkin*.go、activities.go | 北京时间、账户级唯一、余额/账本/预算同事务 |
| [ ] | 一次性/周期成长、金额达标日 | growth*.go | 已消耗进度不回退、奖励快照、迟到记录/退款边界 |
| [ ] | 邀请奖、官方邀请事件、入退群记录 | invitations*.go、membership*.go | 重进不重复、双方绑定、每日上限 |
| [ ] | 抽奖、充值次数、保底、余额/次数/实物/自定义奖 | lottery.go、reward*.go | fork 有效充值/退款语义，次数与奖品原子提交 |
| [ ] | 每日 Token/消费排行榜、榜单奖励 | rankings*.go | 隐私、历史规则快照、预算不足保留资格 |
| [ ] | 活动模板、每日硬预算、奖励管理/发放 | activity_templates.go、growth_rewards.go | 多实例账本锁；默认财务副作用关闭 |
| [ ] | 优惠券与支付依赖 | coupons.go、internal/coupon、支付关联 | 完整适配本 fork coupon/payment/ledger；不能删掉来源实际奖励类型 |
| [ ] | 运行总览、群管理、功能连接、活动奖励、成员账户、记录诊断 UI | frontend/views/admin/QQBot*、components/admin/qqbot | 双语、桌面手机、旧地址兼容、功能提示 |
| [ ] | 绑定/转账公开确认页、个人资料卡 | auth/QQBotBindView、QQBotTransferView、ProfileQQBotCard | user/tenant/成员隔离与确认 |

源文件逐一 SHA/额外版权行核对保存在 artifact `source-audit/qqbot-file-provenance.json`（356 blobs 已核固定 Git blob）；这是来源核对，不是已移植/已验收证明。来源模块精确文件名以该 manifest 为准。

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
