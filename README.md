<div align="center">

<img src="assets/logo-icon.png" alt="Sub2API Logo" width="128" />

# Sub2API

[![Go](https://img.shields.io/badge/Go-1.27.3-00ADD8.svg)](https://golang.org/)
[![Vue](https://img.shields.io/badge/Vue-3.4+-4FC08D.svg)](https://vuejs.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15+-336791.svg)](https://www.postgresql.org/)
[![Redis](https://img.shields.io/badge/Redis-7+-DC382D.svg)](https://redis.io/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED.svg)](https://www.docker.com/)


**tosky.io 维护的 Sub2API 独立分支**

按需同步上游更新，扩展实用功能，独立发布生产版本。

[平台](https://tosky.io/) · [使用文档](https://tosky.io/docs/) · [版本下载](https://github.com/ranxi2001/sub2api/releases) · [问题反馈](https://github.com/ranxi2001/sub2api/issues)

中文 | [English](README_EN.md) | [日本語](README_JA.md)

</div>

## 社区交流群

欢迎正在部署、使用或维护 Sub2API 的朋友加入 QQ 群「中转技术交流」（群号 **1004036018**），交流部署实践、协议兼容、出口代理和功能改进。也欢迎参与问题复现、测试、文档补充和 Pull Request，一起维护这个独立分支。

<table>
<tr>
<td align="center" width="50%">
  <img src="assets/community/qq-relay-tech-group.png" alt="QQ 群：中转技术交流，群号 1004036018" width="71%" />
  <br />
  加入 QQ 群「中转技术交流」
</td>
<td align="center" width="50%">
  <img src="assets/community/coffee-reward.png" alt="支付宝和微信赞赏码：如果帮到大家可以打赏咖啡" width="100%" />
  <br />
  如果帮到大家可以打赏咖啡！
</td>
</tr>
</table>

二维码长期有效。需要长期留档、报告问题或讨论具体改动时，请使用 [Issues](https://github.com/ranxi2001/sub2api/issues)。

## 快速开始

新人部署、官方版替换、Excel / BPS 配置和质量运维步骤，请阅读[新人入门文档](docs/新人入门.md)。

账号检测、失败处置和自动恢复，请阅读[质量运维](docs/account-quality-operations.md)；BPS OAuth 登录、工具兼容和 403 恢复配置，请阅读[Excel / BPS 协议](docs/excel-bps.md)。

2.19.2 新增默认关闭的账号性能监控，支持指标阈值和独立暂停；启用、人工解除与区域传播边界见[账号性能监控](docs/account-performance-monitor.md)。

已经托管账号或完成部署，想在 Codex 中使用？请阅读[新人入门：使用 Sub2API 接入 Codex](docs/新人入门-使用Sub2API.md)，按步骤创建 API Key、配置 Cockpit Tools 并验证调用。


## 本仓库的维护方向

本仓库是 `ranxi2001/sub2api` 的独立生产 fork，基于 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 按需同步。应用功能、上游修复、Release 和生产验证都以 `production` 分支为准；不会用上游默认分支或 tag 直接覆盖本 fork 的生产历史。

- **DeepSeek 与 Codex 适配**：支持 Responses 到 Chat Completions 的转换、工具调用历史和上下文压缩兼容。配置模型映射后，可通过切换 API Key 分组使用 DeepSeek，沿用客户端配置。[操作教程](https://tosky.io/docs/?doc=deepseek-switch-group)
- **账号质量运维**：维护定时答题检测、判题记录、失败处置和自动恢复，支持按模型隔离及全部检测模型失败时移出指定分组。检测结果以实际回答和判题证据为准，不以票据形态判断质量。
- **Mihomo 出口管理**：维护订阅、动态代理、节点状态和账号业务出口；支持从订阅节点或动态代理单条、批量添加为静态代理，再给账号分别选择固定节点。[操作说明](docs/mihomo-static-node-proxies.md)
- **Excel / Basispoints**：维护模型级 BPS 路由、内嵌图片 HTTPS 中转、磁盘和并发保护、结构化输出校验，以及工具历史和 transport 恢复。路由遵循账号与模型配置；不支持的托管工具可按策略回退原生 Codex，或保持 BPS 并省略工具，具体边界见 [BPS 配置说明](docs/excel-bps.md)。
- **上游修复维护**：持续跟踪上游 Codex、Responses、工具调用、密文恢复和限流修复；先确认与本 fork 的行为差异，再按提交级别移植并补充回归测试。

已有代理任务报 `unsupported_encrypted_agent_message` 或 `encrypted_agent_recovery_*` 时，先保留任务及工具结果，按[加密代理任务续接与恢复边界](docs/openai-agent-continuation.md)核对实际通道和恢复条件。
- **独立发布与升级**：使用 `ranxi2001/sub2api` 的 Release、安装资源和容器镜像。版本变更见 [更新说明](https://github.com/ranxi2001/sub2api/releases)；Release 成功不代表生产服务已经部署，线上状态需要单独验证。

贡献代码时，请在独立分支中说明影响的请求路径、账号类型、配置默认值和兼容边界。涉及生产分支的修复应先进入原 PR head，通过 CI 后再合并；不要提交 Token、OAuth 导出、ticket、代理凭据或生产配置。

## 项目概述

Sub2API 是一个 AI API 网关平台，用于分发和管理 AI 产品订阅的 API 配额。用户通过平台生成的 API Key 调用上游 AI 服务，平台负责鉴权、计费、负载均衡和请求转发。

## 核心功能

- **多账号管理** - 支持多种上游账号类型（OAuth、API Key）
- **API Key 分发** - 为用户生成和管理 API Key
- **精确计费** - Token 级别的用量追踪和成本计算
- **智能调度** - 智能账号选择，支持粘性会话
- **并发控制** - 用户级、账号级和 API Key 级并发限制，支持可配置的 Key 等待队列
- **速率限制** - 可配置的请求和 Token 速率限制
- **内置支付系统** - 支持 EasyPay 易支付、支付宝官方、微信官方、Stripe，用户自助充值，无需独立部署支付服务（[配置指南](docs/PAYMENT_CN.md)）
- **USDT / USDC 加密货币支付** - 内置 BEpusdt 引擎，无需外部网关 API 或 Token；支持钱包管理、故障补扫、多网络充值与订阅购买，保留外接网关模式和幂等入账（[配置指南](docs/BEPUSDT_PAYMENT.md)）
- **管理后台** - Web 界面进行监控和管理
- **外部系统集成** - 支持通过 iframe 嵌入外部系统（如工单等），扩展管理后台功能

### OpenAI API Key 的 UltraFast 档位计费

账号新增/编辑页提供默认关闭的 **UltraFast 档位计费** 开关。开启后，按该账号所有有价 token 模型的上游响应 `service_tier` 结算；`ultrafast` 使用标准单价×6，包含缓存读写，再应用有效账号/分组倍率。响应未返回档位时按请求档位回退，该开关不强制上游加速。

客户端明确请求 `ultrafast` 且最终仍按该档位结算时，关闭此开关也按6倍收费。开关控制是否采用上游响应升档，不豁免客户端请求的 UltraFast 价格。配置字段、响应降档、长上下文和计费范围见 [UltraFast 账号计费说明](docs/openai-ultrafast-billing.md)。

### Anthropic API Key 客户端模拟与计费

创建或编辑 Anthropic API Key 账号时, 可开启 "模拟 Claude Code 客户端", 为要求 Claude Code 客户端的上游补齐缺失的请求头, system 身份句和会话信息. 原有 system 指令及缓存断点会保留, count_tokens 仅补请求头. 此选项与 "抹除 Claude Code 身份" 互斥, 同时存储为开启时以抹除身份为准. 身份句可能影响模型自述.

管理员可单独选择 "按模型估算扣除身份句 token". 默认关闭, 按上游用量正常计费. 开启后, 仅在网关实际补入身份句时估算扣除约 14-24 个输入或缓存 token, 不修改返回客户端的 usage. 型号别名, 上游分词器和混合缓存会影响估算准确度; 需要按上游原始用量结算时, 保持此开关关闭.

对应账号 extra 配置为 `anthropic_mimic_claude_code` 和 `anthropic_mimic_claude_code_deduct_tokens`, 均默认 `false`. 关闭模拟或开启抹除身份时, 页面同时清除估算扣减设置.

### 首次初始化管理员

全新自动安装时，`ADMIN_EMAIL` 和 `ADMIN_PASSWORD` 留空会生成随机登录邮箱和密码，可从首次启动日志中的 `Generated admin` 获取。显式指定时，邮箱必须符合登录格式，密码必须为 8–72 字节。已有管理员或已有用户的部署跳过创建与此项校验。详细配置见 [部署说明](deploy/README.md)。

## API Key 并发等待队列

当 API Key 设置了大于 `0` 的 `concurrency_limit` 时，达到上限后的新请求会在原连接上等待空闲槽位。默认值 `0` 不增加 Key 级并发限制。等待策略是全局配置，进程启动时读取：

```yaml
gateway:
  api_key_queue:
    max_waiting: 5
    timeout_seconds: 30
```

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `GATEWAY_API_KEY_QUEUE_MAX_WAITING` | `5` | 每个受限 Key 允许额外等待的请求数；`0` 关闭 Key 排队。 |
| `GATEWAY_API_KEY_QUEUE_TIMEOUT_SECONDS` | `30` | 单个请求最长等待秒数，必须为正整数。 |

- 等待名额按 Key 独立计算，不保证 FIFO；`concurrency_limit: 0` 的 Key 不进入队列。
- 队列用于 HTTP/SSE、OpenAI Responses WebSocket 每轮请求和 Live 创建的 Key 准入，与用户级、账号级等待限制独立。
- 等待期间复核 Key、用户及当前请求的模型/能力权限。Key 换组、平台或计费模式变化返回可重试的 `503` / `API_KEY_GROUP_CHANGED`。WebSocket 鉴权/权限失败以 `1008` 关闭，容量或临时服务错误以 `1013` 关闭。
- 关闭排队时达到上限返回 `429` / `gateway_concurrency_limit`；队列满返回 `429` / `api_key_queue_full`；等待超时返回 `429` / `api_key_queue_timeout`。
- 两个配置值必须为整数；负数、小数、非法字符串或超出范围会阻止启动。即使关闭排队，超时也必须为正数。
- 修改 Compose `.env` 后需要重建容器以更新环境变量。调大等待时间时，需要确认客户端及反向代理的首字节超时。
- 升级自动应用 `237_add_api_key_concurrency_limit.sql`，旧 Key 默认为 `0`。回退二进制不会撤销数据库新增字段。

### 中转 API Key 的上游 429

- OpenAI 平台普通 API Key 收到上游 429 时，使用管理端已有的“429 兜底冷却”设置（默认启用、5 秒），不再把透传的 ChatGPT/Codex 周窗口、`resets_at` 或使用率快照写成整个 Key 的配额状态。关闭该设置时不新增本地兜底冷却，上游错误仍按原有响应与故障转移逻辑处理。
- OAuth / Setup Token 保留真实配额窗口；Ollama Cloud、国产供应商和 OpenCode Go 的已识别专用配额逻辑，以及池模式、自定义错误策略和显式临时停调规则，仍按原有优先级处理。
- 管理端测试 API Key 时，不再用透传的 ChatGPT 429 重置时间更新 Key 的调度状态。升级不会自动清理此前已写入的长冷却；核实状态后可使用管理端恢复功能处理。

## 技术栈

| 组件 | 技术 |
|------|------|
| 后端 | Go 1.27.3, Gin, Ent |
| 前端 | Vue 3.4+, Vite 5+, TailwindCSS |
| 数据库 | PostgreSQL 15+ |
| 缓存/队列 | Redis 6.0+ |
| API 协议 | OpenAI Responses / Chat Completions、Anthropic Messages、Gemini、SSE、WebSocket |
| 调度与可靠性 | 粘性会话、并发控制、限流、故障转移、模型隔离与冷却 |
| 观测与运维 | 结构化日志、请求分段耗时、健康检查、Mihomo 出口与 systemd |
| 交付与质量 | Docker Compose、GitHub Actions、Go 单元测试、前端类型检查与构建 |

## 贡献与协作

欢迎围绕协议兼容、账号调度、质量运维、支付计费、管理后台和运维观测提交改进。高质量贡献应尽量保持边界清晰，并在 PR 中说明请求路径、状态变化、兼容性影响和验证证据。

开始前请阅读 [贡献指南](CONTRIBUTING.md)：包含最小复现、日志脱敏、开发环境、验证命令和 PR 流程。[提交 Issue](https://github.com/ranxi2001/sub2api/issues/new/choose) 时可选择 Bug、功能建议、文档或使用问题表单；提交 PR 时按模板填写行为变化和实际验证结果。

建议按以下方式提交：

- **协议与网关**：补充请求/响应样例，覆盖流式终止、工具调用、重试和上游错误映射。
- **调度与账号**：说明候选筛选、粘性状态、并发占用、冷却窗口和故障转移行为，避免改变幂等语义。
- **后台与配置**：同步前后端类型、默认值、权限边界和迁移兼容性。
- **运维与部署**：提供离线或 mock 验证，不在 PR 中写入 Token、ticket、代理凭据或生产配置。
- **验证与审查**：列出实际运行的测试、构建或脚本检查；未运行的检查不要标记为通过。

感谢已合并 PR 的贡献者（按本仓库已合并 PR 数量降序排列，数量相同时按 GitHub 用户名排序）：

<p>
  <a href="https://github.com/ranxi2001"><img src="https://avatars.githubusercontent.com/u/77790009?v=4" width="56" height="56" alt="Onefly" title="Onefly" /></a>
  <a href="https://github.com/akihitohyh"><img src="https://avatars.githubusercontent.com/u/79531840?v=4" width="56" height="56" alt="akihitohyh" title="akihitohyh" /></a>
  <a href="https://github.com/blackdm666"><img src="https://avatars.githubusercontent.com/u/67053678?v=4" width="56" height="56" alt="老黑" title="老黑" /></a>
  <a href="https://github.com/psyche314"><img src="https://avatars.githubusercontent.com/u/180074435?v=4" width="56" height="56" alt="psyche314" title="psyche314" /></a>
  <a href="https://github.com/akayedi"><img src="https://avatars.githubusercontent.com/u/90334896?v=4" width="56" height="56" alt="akayedi" title="akayedi" /></a>
  <a href="https://github.com/loserzero-7"><img src="https://avatars.githubusercontent.com/u/177290228?v=4" width="56" height="56" alt="loserzero-7" title="loserzero-7" /></a>
  <a href="https://github.com/289989142"><img src="https://avatars.githubusercontent.com/u/63082228?v=4" width="56" height="56" alt="289989142" title="289989142" /></a>
  <a href="https://github.com/AI8888-SHOP"><img src="https://avatars.githubusercontent.com/u/297756662?v=4" width="56" height="56" alt="AI8888-SHOP" title="AI8888-SHOP" /></a>
  <a href="https://github.com/damian2848"><img src="https://avatars.githubusercontent.com/u/55676325?v=4" width="56" height="56" alt="damian2848" title="damian2848" /></a>
  <a href="https://github.com/Cd1s"><img src="https://avatars.githubusercontent.com/u/135119404?v=4" width="56" height="56" alt="Cd1s" title="Cd1s" /></a>
  <a href="https://github.com/danvilig"><img src="https://avatars.githubusercontent.com/u/230506945?v=4" width="56" height="56" alt="danvilig" title="danvilig" /></a>
  <a href="https://github.com/EdmundMad0309"><img src="https://avatars.githubusercontent.com/u/122854730?v=4" width="56" height="56" alt="EdmundMad0309" title="EdmundMad0309" /></a>
  <a href="https://github.com/LeeSssong"><img src="https://avatars.githubusercontent.com/u/37948462?v=4" width="56" height="56" alt="LeeSssong" title="LeeSssong" /></a>
  <a href="https://github.com/QQQHHHDDD"><img src="https://avatars.githubusercontent.com/u/51820811?v=4" width="56" height="56" alt="QQQHHHDDD" title="QQQHHHDDD" /></a>
  <a href="https://github.com/spake404"><img src="https://avatars.githubusercontent.com/u/123435269?v=4" width="56" height="56" alt="spake404" title="spake404" /></a>
  <a href="https://github.com/stl25"><img src="https://avatars.githubusercontent.com/u/317681732?v=4" width="56" height="56" alt="stl25" title="stl25" /></a>
  <a href="https://github.com/wuwu131452011"><img src="https://avatars.githubusercontent.com/u/165636850?v=4" width="56" height="56" alt="wuwu131452011" title="wuwu131452011" /></a>
  <a href="https://github.com/abcgoodwei"><img src="https://avatars.githubusercontent.com/u/35887090?v=4" width="56" height="56" alt="abcgoodwei" title="abcgoodwei" /></a>
  <a href="https://github.com/alanbulan"><img src="https://avatars.githubusercontent.com/u/140389107?v=4" width="56" height="56" alt="alanbulan" title="alanbulan" /></a>
  <a href="https://github.com/ambi-vip"><img src="https://avatars.githubusercontent.com/u/46626203?v=4" width="56" height="56" alt="ambi-vip" title="ambi-vip" /></a>
  <a href="https://github.com/ape-ming"><img src="https://avatars.githubusercontent.com/u/30568607?v=4" width="56" height="56" alt="ape-ming" title="ape-ming" /></a>
  <a href="https://github.com/BeterXie"><img src="https://avatars.githubusercontent.com/u/30471270?v=4" width="56" height="56" alt="BeterXie" title="BeterXie" /></a>
  <a href="https://github.com/buluw"><img src="https://avatars.githubusercontent.com/u/45087912?v=4" width="56" height="56" alt="buluw" title="buluw" /></a>
  <a href="https://github.com/Coke165"><img src="https://avatars.githubusercontent.com/u/259744923?v=4" width="56" height="56" alt="Coke165" title="Coke165" /></a>
  <a href="https://github.com/Even1997"><img src="https://avatars.githubusercontent.com/u/95899304?v=4" width="56" height="56" alt="Even1997" title="Even1997" /></a>
  <a href="https://github.com/InCerryGit"><img src="https://avatars.githubusercontent.com/u/36690780?v=4" width="56" height="56" alt="InCerryGit" title="InCerryGit" /></a>
  <a href="https://github.com/Lanyang1176"><img src="https://avatars.githubusercontent.com/u/187152708?v=4" width="56" height="56" alt="Lanyang1176" title="Lanyang1176" /></a>
  <a href="https://github.com/Mickey0811"><img src="https://avatars.githubusercontent.com/u/49522921?v=4" width="56" height="56" alt="Mickey0811" title="Mickey0811" /></a>
  <a href="https://github.com/mracry"><img src="https://avatars.githubusercontent.com/u/112537993?v=4" width="56" height="56" alt="mracry" title="mracry" /></a>
  <a href="https://github.com/sylarchen1389"><img src="https://avatars.githubusercontent.com/u/43306063?v=4" width="56" height="56" alt="sylarchen1389" title="sylarchen1389" /></a>
  <a href="https://github.com/Terry1321"><img src="https://avatars.githubusercontent.com/u/41000037?v=4" width="56" height="56" alt="Terry1321" title="Terry1321" /></a>
  <a href="https://github.com/yuanyuan19"><img src="https://avatars.githubusercontent.com/u/120552623?v=4" width="56" height="56" alt="yuanyuan19" title="yuanyuan19" /></a>
  <a href="https://github.com/ZASENJC"><img src="https://avatars.githubusercontent.com/u/65362072?v=4" width="56" height="56" alt="ZASENJC" title="ZASENJC" /></a>
  <a href="https://github.com/zhoumooooo"><img src="https://avatars.githubusercontent.com/u/271002711?v=4" width="56" height="56" alt="zhoumooooo" title="zhoumooooo" /></a>
</p>

---


## ⚠️ 重要提醒

使用本项目前，请务必仔细阅读以下内容：

- **🚨 服务条款风险**：使用本项目可能违反 Anthropic 等上游服务商的服务条款。请在使用前仔细阅读相关服务商的用户协议，由此产生的一切风险由用户自行承担。
- **⚖️ 合规使用**：请在符合您所在国家或地区法律法规的前提下使用本项目，严禁将其用于任何违法违规用途。
- **📖 免责声明**：本项目仅供技术学习与研究使用，作者不对因使用本项目导致的账户封禁、服务中断、数据丢失或其他任何直接或间接损失承担责任。
- **🚫 无商业授权**：本项目从未授权任何个人或组织基于本项目开展任何形式的商业化运营。任何以本项目名义或基于本项目从事的商业行为均与本项目及其开发者无关，由此产生的一切纠纷、损失和法律责任由行为主体自行承担。

## 许可证

本项目基于 [GNU 宽通用公共许可证 v3.0](LICENSE)（或更高版本）授权。

Copyright (c) 2026 Wesley Liddick

---

<div align="center">

**如果觉得有用，请给个 Star 支持一下！**

</div>
