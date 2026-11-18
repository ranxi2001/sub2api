# 上游 v0.2.23 完整引入

- fork 基线：`657bb38d141901abcbeef2f178d9234109d37167`，版本 `2.22.0`。
- 上次完整同步：PR #900；上游 v0.2.22 `b0fa9f3e6eeab1968a53e1289b69daa4bb40ca2b`。
- 目标：Wei-Shaw/sub2api v0.2.23，annotated tag `ce19a640eab5c4b5ef1827c7c1fcfce49569411a`，源码 `5af9afb588c4b014b3df9db7d4073af1f8ae01b6`。
- 补齐本地浅历史后，merge base 为 `efe9aab1e4ec89a42ba45e8dac20e882c5409a6a`。
- 完整范围 20 个普通提交、12 个合并提交。普通提交按拓扑/上游合入顺序全部 `cherry-pick -x`，保留作者与主题；两个已覆盖改动保留空提交。不重复重放合并提交。
- `git cherry -v origin/production upstream-v0.2.23 upstream-v0.2.22` 的 20 项均为 `+`，不能据此忽略已做过局部修改的文件；逐文件核对发现 fork 已包含安全依赖、工具链和部分 GPT-6.2 支持。

## 普通提交清单

| 上游源 SHA | 引入 SHA | 主题与覆盖说明 |
| --- | --- | --- |
| `15871f904056f548dd874a1d069ffc6cdf777b78` | `0dde7b7b6b802c3bfcb3ea9c682258d1c703fb54` | chore: sync VERSION to 0.2.22 [skip ci] |
| `6107cf17487fe5cb0bd179457ede6b1fe8f7271e` | `da89d16b95cf40decc135d34f6ad5fab8aa88e30` | feat(openai): support GPT-6.2 Sol and Astra |
| `2d20707f16dcd5d2d692a9535370dbbdb9b8b6f0` | `6961e492f9fa0aec8e936668d1386021e20da150` | fix(openai): honor GPT-6.2 Ultrafast entitlements by Pro plan |
| `d867299264691b7158451ed547d94657d5da0237` | `7118de0093ecbd3e12935ba03a6e70516b08773b` | perf(openai): DeepSeek reasoning 占位与 Ollama Cloud reasoning 回填改为单遍改写请求体 |
| `2c678449640ca67e0958b3e2a8e0c98acb4fd1e7` | `a25c19e74ddeb9816226c3ccd52ef17a9f6bb0a2` | perf(openai): 原生 Chat Completions 流式透传在等待上游前才 flush |
| `8a1315e8ccb60bf4389f6a436deb5447445ac560` | `72533d9c67b6feee3d82644a541a6c2f7c6c4435` | test(billing): reuse rate resolver and isolate heap checks |
| `b36cb244f32ddeb03f3fd11c52f70aea7f9776f5` | `86c29959c2ab60069ac75bd63b892a160120a015` | fix(channels): support configurable Ultra Fast pricing |
| `189b336f47133dabe68ad0fa5d2c35a128210c73` | `2a123d71edbbdca9c3c338898079cb6ed73c6755` | fix(openai): enforce stream idle deadlines and pre-output failover |
| `7f6673ead54a1949a0db990eda789f72bbfd6dae` | `e9bc30d2ead3e07e21ffe6a52f9052813d9999a0` | fix(apicompat): preserve Anthropic cache creation TTL details |
| `efc58486d88a5f43078dcd16bd0b05b33c75ed3e` | `506673853a466885307457bf30f6cab79b82a4cf` | fix(usage): preserve authoritative cache creation totals |
| `e6bde6b3c642058a4f384bf7100d510ea385029b` | `92fbdd9956825ccd662a2940e5aff10f104b9aa0` | fix(billing): retain native cache creation accounting |
| `7b879c56a6002ae985fa535a33939d6f4602c97a` | `fee1fb41b8ff9a0e00b90bf778ece4cee92190de` | chore(deps): update follow-redirects security patch；空提交，安全依赖/Go 1.27.4 已在基线覆盖 |
| `bcb0512132081c920da966e1e23c66389e948dcc` | `3b3dfe3fae7d57ad4f4e92a2348fd2d5c0ceb473` | fix(billing): account for unclassified cache creation tokens and TTL overrides |
| `9e4c03d2fee29211d9026a5cbfbdfe0b4af92448` | `1260e5159dc41c152abf4fad71aa6c02160464b6` | fix(usage): reconcile prompt totals across cache-only updates |
| `71532bf91cfe79f65f1943bdb32e896cd087238b` | `9fb8c894edfe7afebb235444ac441a733715dd8f` | fix(usage): preserve compatible cache snapshot semantics |
| `4656364337fdadb4d4a8250f082fefe3445be041` | `e28da1b4aeb38adc0c1a4935fde8262141a4d791` | test(usage): assert incomplete cache streams and isolate allocation guard |
| `202e23b2b8ae5984591bda227e4ccb3bf3c2951a` | `4943f678a5322c531f8a03fe70872f1063e6efcf` | chore(build): upgrade Go toolchain to 1.27.4；空提交，安全依赖/Go 1.27.4 已在基线覆盖 |
| `23c3eb7f8b569233023b3332424a7788da1e1fbb` | `c113fbd17642cf8fcda106191609b23fc767a3d2` | fix(usage): align cache usage across Anthropic conversion boundaries |
| `82395b0b5adf8dabd0c78d4009367f49ff161d0f` | `23a955918baab1e3f64f66c54e9ee703440b6eaf` | test(usage): distinguish EOF before and after client output |
| `29809eef099843ccc35c1b01991f56362a3dc9b5` | `c8d94821b23e9eb46e13509ee9364a987af62b33` | fix(openai): bound pre-output idle failover to the first-output switch budget |

## 合并提交核对

对范围内全部 merge 执行 `git show --remerge-diff`。唯一非空项是 `661770bf3`，包含缓存 5m/1h 与 30m/12h 字段组合、账单投影及组合回归；独有改动已补入。

| 上游 merge | 处理 |
| --- | --- |
| `a4d3103cd5a075505da604a3d2d5243cbc51f4ea` | 自动重合并无独有差异 |
| `45632495f593697393e6b52b3aabdab9eb1dd9ac` | 自动重合并无独有差异 |
| `69d2fae91318359e4e25c0171eaf70364dad3629` | 自动重合并无独有差异 |
| `4e10912b4eeb2754bd078217388838f9172cce04` | 自动重合并无独有差异 |
| `9e9ca6fe95d3d20ca9d099b0c681e336e8d812c4` | 自动重合并无独有差异 |
| `19d4cb222a24cecde72ba20045f7eb24cf2cf9a5` | 自动重合并无独有差异 |
| `c91c594cc8d080c46880da317942e3cb2fde9813` | 自动重合并无独有差异 |
| `9e8913876f566898e7dbd11a4efce37efe0e1017` | 自动重合并无独有差异 |
| `b25dbe3e4b203ae955a94d06138946797dcf88e7` | 自动重合并无独有差异 |
| `661770bf34c1b8b5470a7ce7d67a192feb0c37f5` | 4 文件存在组合修正；辅助函数/结构体已在 leaf 冲突整合保留，TTL 投影与新回归补于 `b00c428d8` |
| `2354c78288d05ec817b4f31a1cf5ebe231e36ba2` | 自动重合并无独有差异 |
| `5af9afb588c4b014b3df9db7d4073af1f8ae01b6` | 自动重合并无独有差异 |

## fork 适配与审查重点

- 上游版本提交先完整引入，独立 fix 恢复 `2.22.0`；本次范围没有赞助变更，README 与 owner Release 地址保持基线。
- GPT-6.2 Sol/Astra 采用上游 Codex 元数据、参数验证与 Pro 权益识别；保留 fork 的 `gpt-6.2-astra-pro` 独立模型、价卡和 12h 缓存价。两套测试分别保留，原 fork 测试移至 `openai_gpt62_fork_test.go`。
- fork 默认 Ultra Fast 固定 6 倍，不采用上游按日期 3→6 倍促销，也不为 GPT-6.2 默认价卡自动新增 272k 长上下文加价。新增渠道 `ultrafast_multiplier` 为可空正数，显式配置优先；现有 Free Fast/Free UltraFast 和 GPT-6.1 Sol 订阅阈值保留。上游日期/长上下文测试按这一明确差异调整断言，保留前后时点、账户类型与 resolver 双路径。
- 清理上游新增的重复 GPT-6.2 JSON 价卡，避免后一个同名对象覆盖 fork 的 12h 配置；不改变其他模型的价格。
- 缓存明细引入时保留 `CacheReadKnown` 与媒体观测、BPS 缓存写入按普通输入计费、请求级定价时点、区域计费和 journal 路径。Anthropic 首事件的输出量保留；终态嵌套 usage 优先，同时继承媒体信息。
- 原 fork 内存测试已使用独立进程与负控；保留其隔离方式，接入上游 resolver 复用与 fixture KeepAlive。
- Go 1.27.4 与依赖修复已覆盖；保留 fork CI 分片、嵌入收款模块 replace、release owner 地址。
- 新功能提示 `channelUltrafastPricing` 接入渠道创建/编辑/批量编辑共用 `PricingEntryCard`，补入已有 A→B→C、多入口和持久化回归参数集。
- 292 ticket/harvest 身份、加密历史、DeepSeek compaction、账号调度文件没有被上游替换；共享请求体、usage 与 SSE 代码的修改仍需最终 CI 回归验证。代码差异审查不等同生产验收。

## 迁移与验证边界

新增 `252_channel_pricing_ultrafast_multiplier.sql`，添加可空字段和正数约束；旧记录 NULL 继续模型默认倍率。二进制回退不自动移除数据库字段/约束。未连接生产数据库，未发真实模型请求，未发布或部署。

已执行：提交范围/来源/merge 差异检查、Go 格式化、`git diff --check`。本地测试、lint、类型检查与构建未运行，交由 PR CI。最终 head 的 CI 状态和实际 UI 证据以 PR 正文为准。

CI 首轮发现缺少 GPT-6.2 描述解析 helper；补入上游旧依赖 `bea67f412` 中的 `codexInstructionsTemplate`，仅补当前 GPT-6.2 调用所需 helper，不改 fork 的 GPT-6.1 Sol 默认 instructions 策略。

补齐 fork 渠道价卡导入/导出和批量编辑的 Ultra Fast 字段保留，沿用正数校验；增加往返与批量提交回归。为上游渠道视图测试补充 fork 已有的管理员 store fixture。

前端 CI 发现 Ultra Fast 帮助文案与 fork 规则不一致：恢复 `service_tier=ultrafast` 说明，将上游促销价改为本站固定 6 倍及渠道覆盖说明，保留新增套餐权益信息。新增渠道倍率视图回归已纳入 Makefile 的 CI 关键用例。定位 CI 后，后端模型/TTL/渠道倍率/转换边界定向用例通过；前端 6 文件、120 项定向用例通过。

最终 CI 追加修正：

- 保留 GPT-6.2 显式 priority 缓存写入价；上游模型策略不可将已配置的写入价强制翻倍，原 Free Fast 账单回归作为验收。
- 分组 tier 与套餐 helper 仍放在 fork 的独立文件；用量投影和原计费包装器独立保留，避免历史 RED 恢复旧网关文件时丢失新调用依赖。
- OAuth 429 与 Issue715 历史 RED 只向经过完整 blob 核对的旧 `OpenAIUsage` 添加缺失的零值存储字段；移除新增字段后必须逐字节还原旧文件。源级自测验证原字节、字段唯一性和脏文件拒绝。没有跳过、删除或弱化历史业务断言，候选 GREEN 不使用该补丁。

完整单元回归补充：原生 Anthropic→Responses/Chat 缓冲转换现在复用既有终态检查；仅有缓存 usage 后 EOF 返回 502 并保留 usage，不再伪装成功。保留 stop_reason 兼容收尾、HTTP/2 reset 特殊处理与语义错误路径。上游对应测试保留，错误码按 fork 的 `stream_incomplete` 契约适配。

隔离 Pelican E2E 在两个运行中都仅记录一次 `/api/v1/auth/me` 的 `net::ERR_ABORTED`，其余业务场景通过。导航 helper 增加请求清空后的 250ms 静默窗口，覆盖 load 后延迟发起的认证请求，仍使用同一个 20s 总截止时间、保留全部 requestfailed 记录。9 项导航回归通过；完整 E2E 以最终 PR 检查为准。

E2E 分组历史回读的监听在导航排空后安装，避免把旧页面轮询响应当作新页面结果；新增 goto/reload 两个旧响应负控，11 项导航回归通过。
