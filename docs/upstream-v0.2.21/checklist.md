# Upstream v0.2.21 integration checklist

Frozen source: `dab829a4e9b7d1cc307189aea8b0319eb29e29d0..10ba8f9b4dc2e6023d4a9879ab77b21841babd10`; baseline `6fa7fb86eae7c732794450646dc13831dce87ff1`. 82 commits = 44 leaves + 38 merges. Only leaves are imported. Source blob and baseline blob IDs are in [source-map.json](source-map.json).

## Release-note requirements

- [ ] R01: 支持 Claude Fable 5.2 与 Mythos 5.2：包括模型目录、Anthropic 预设、Bedrock 映射、OpenCode 配置和兜底定价（$8/$40 每百万 token，Fast 模式 2 倍）
- [ ] R02: OpenAI 缓存写入计费按 TTL 拆分：渠道、分组、账号统计价卡新增独立的 12h 缓存写入价（GPT-5.6 / GPT-6 系列未配置时默认按输入价 2.5 倍；已配置通用写入价的价卡不受影响），管理员用量详情按实际 TTL 展示
- [ ] R03: Antigravity 账号用量单元格新增「查询」按钮，可跳过缓存强制刷新用量
- [ ] R04: EasyPay 支付实例可单独配置退款成功码（默认 1，可选 0），兼容退款以 code=0 表示成功的易支付网关
- [ ] R05: 请求体内存预算未配置时，自动取进程内存上限的 1/4（取物理内存、cgroup 限制、GOMEMLIMIT 中的最小值），最低 1GiB
- [ ] R06: API 密钥列表不再等近 30 天用量统计返回才渲染，并为用量统计新增覆盖索引，大数据量下 /keys 页面不再长时间停在骨架屏
- [ ] R07: 账号列表刷新时保留现有行和配额数值，不再整表闪烁成骨架屏
- [ ] R08: 系统设置页的保存按钮改为吸附在视口右下角的浮标，长表单不用滚到底部也能保存
- [ ] R09: 余额充值倍率支持最多 6 位小数，便于跟随实时汇率
- [ ] R10: 安全审计任务入队后立即唤醒本进程 worker 认领，不再平均等待约 250ms
- [ ] R11: OpenAI WebSocket 首 token 时间改为遵循管理员配置的 TTFT 口径，与 SSE 保持一致
- [ ] R12: 客户端未提供会话标识时，Responses 会话种子在工具回合中保持稳定，减少粘性会话中途切换账号
- [ ] R13: 内置 Claude Code CLI 版本基线升至 2.1.321
- [ ] R14: 修复 OpenAI 合成的 reasoning effort 未应用 effort 映射规则和上限校验的问题；保留已配置的 GLM 降级语义
- [ ] R15: 修复 OpenAI 显式配置的模型级冷却规则对 overloaded/slow_down 及流内 503 不生效的问题；关键词只匹配实际错误字段
- [ ] R16: 修复 Codex 元数据事件（codex.rate_limits 等）提前提交响应流，导致后续上游过载无法 failover 的问题
- [ ] R17: 修复原生 Responses WebSocket 在后续轮次限流时重放首帧，可能导致任务上下文错乱的问题（现以 1013 关闭，由客户端重连）
- [ ] R18: 修复 OpenAI OAuth 池化 WebSocket 在未声明搜索的连接上发起 web_search 被上游拒绝的问题
- [ ] R19: 修复 Codex Desktop 注入的页面上下文导致首个 Responses 请求返回 400 的问题
- [ ] R20: 修复原生 Codex 生图请求因心跳提前写出 200、掩盖上游错误状态码的问题
- [ ] R21: 修复 /v1/responses 重建函数调用时丢失 encrypted_function_args 与 namespace 的问题
- [ ] R22: 修复 CC Switch 的 reasoning 封装被当作 encrypted_content 发往上游的问题
- [ ] R23: 修复 Anthropic 兼容路径（Chat Completions / Responses）忽略账号自定义错误策略的问题
- [ ] R24: 修复 Gemini 兼容入口非流式响应把思考摘要混入正文的问题
- [ ] R25: 修复 Antigravity 并行工具结果中的图片打断 functionResponse 序列导致上游 400 的问题
- [ ] R26: 修复 Antigravity Pro 账号 Argon 配额与其他 Gemini 配额共用冷却、互相误伤的问题
- [ ] R27: 修复账号信息加载失败时已占用的并发槽位未释放、要等 TTL 过期才恢复的问题
- [ ] R28: 修复刷新令牌并发轮换时同一令牌可被重复使用的问题
- [ ] R29: 修复定价目录哈希与内容版本不一致时，后续同步一直被跳过的问题
- [ ] R30: 修复 Sonnet 5.5 兜底缓存读取价格错误的问题（改为 $0.10 每百万 token）
- [ ] R31: 修复 OpenAI 普通输入免费时 Fast 倍率丢失的问题
- [ ] R32: 修复含非法 UTF-8 的 Responses 请求体被当作上游可重试错误的问题，现直接返回 400

## Every source leaf

- [x] `6b4b37db43443b5e88e424fe3c69602cc3810dfb` — chore: sync VERSION to 0.2.20 [skip ci] (adapted-policy; behavioral verification pending).
- [ ] `c004c962a78014855bdb74c51f750e3fce3407fb` — fix(gateway): auto-size request body memory budget from the memory limit (pending; behavioral verification pending).
- [ ] `03bd718abe9f38fb0455a82631c7e492e4abc693` — fix(sysutil): confine cgroup limit reads to the mount with os.Root (pending; behavioral verification pending).
- [ ] `5184ff55f68908cfd9e7cf5905e11b71157cfb41` — docs(images): explain upstream output format mismatches (pending; behavioral verification pending).
- [ ] `9935268573dd9f87c34ce0b39d69afe94f8cc9e7` — docs(images): align model scope across translations (pending; behavioral verification pending).
- [ ] `2273a5d63f76fce80a9059cb029d2354a1e1004e` — test(openai): isolate orphan precheck allocation test (pending; behavioral verification pending).
- [ ] `4ac1675b56760660d5bac5d6aa9fb2cdf57397b4` — feat(antigravity): query button that refreshes usage past the cache (pending; behavioral verification pending).
- [ ] `fe093d76bd9feedd55387ea4056302384a7ab304` — fix(openai): preserve cache-write TTLs in usage and billing (pending; behavioral verification pending).
- [ ] `7156213cce03b268b3261c59fa6bd47d62e6e6ba` — fix(openai): retain Fast multiplier with free input prices (pending; behavioral verification pending).
- [ ] `eb2da3448404df3bd2dfcce836652f0782206688` — feat(gateway): support Claude Fable and Mythos 5.2 (pending; behavioral verification pending).
- [ ] `ea6b3de8082d5871ce5e90a61f4fe75c8ba6b506` — test: keep Claude Code version fixtures above the built-in floor (pending; behavioral verification pending).
- [ ] `508bb9560f70783ab689996344c23e3772978781` — fix: correct Sonnet 5.5 cache read fallback pricing (pending; behavioral verification pending).
- [ ] `92d35a6947a9ec77257b0258370124f0a2c2b0f8` — fix(pricing): retry sync after catalog hash mismatch (pending; behavioral verification pending).
- [ ] `3e8c2ee3f7c128cad3a95e3b099e9bd348fa2012` — fix(pricing): add Claude Fable and Mythos 5.2 fallback rates (pending; behavioral verification pending).
- [ ] `0578fb160e5b77ecbfd7139b5b7761c39d73fd78` — fix(openai): stop native WS later-turn first-frame replay (pending; behavioral verification pending).
- [ ] `142d22cc4284678a729bce1a28986efcd175126c` — fix(openai-ws): reconnect pooled OAuth sockets before search activation (pending; behavioral verification pending).
- [ ] `9c7380732df8ad4420b8be161504552a52b585a5` — test(openai-ws): explicitly discard deferred close errors (pending; behavioral verification pending).
- [ ] `fab3ec251609ba008e41bd5692668f697152705e` — fix(openai-ws): follow the TTFT mode for WebSocket first token (pending; behavioral verification pending).
- [ ] `e401c58053ce79b68985d1bd47aed49477baaf48` — fix(openai): keep Codex metadata events retry-safe (pending; behavioral verification pending).
- [ ] `ebf01680858d884ea74b77a3cdbeb5ea4164c3ad` — fix(openai): honor explicit model cooldown rules for overloads (pending; behavioral verification pending).
- [ ] `88b7090a35badc3cd3ab616d5ac2dc6783627b3e` — fix(openai): match cooldown rules against actual error fields (pending; behavioral verification pending).
- [ ] `a1395eeaf2d600c7e605257e4a1cb02e46b10517` — fix(openai): preserve other platforms' capacity handling (pending; behavioral verification pending).
- [ ] `f2b0a32b1ffdbf4ac7d6a8df00f30328f3492b03` — fix(apicompat): preserve sealed function call metadata (pending; behavioral verification pending).
- [ ] `552d1ab4bb661f89df4ff267370bf5f578247322` — fix(apicompat): skip foreign CC Switch reasoning envelopes (pending; behavioral verification pending).
- [ ] `85f43b640a5f92dab03599974cb19ee1a5005414` — fix(openai): stabilize Responses session seeds across tool loops (pending; behavioral verification pending).
- [ ] `090c58f663a8e745b0e564518cafe7557e6f92e2` — fix(gateway): honor account error policies in compatibility paths (pending; behavioral verification pending).
- [ ] `36484727c7a634795e2d4f1460d11a6e468c3dc1` — fix(gemini): 兼容入口非流式响应不再把思考摘要混入正文 (pending; behavioral verification pending).
- [ ] `e2ce0b43dc62d2591cf69f7c4c26d5c48cea87b7` — fix(TASK-144): keep Antigravity tool functionResponses contiguous before media (pending; behavioral verification pending).
- [ ] `7796a2d57aa0496cb87594fd8bf7d1246adcbf8d` — fix(antigravity): isolate Pro Argon quota cooldowns (pending; behavioral verification pending).
- [ ] `137316a14c001ea40ab1d99865f5dd88ce03a291` — fix(payment): configure EasyPay refund success codes per instance (pending; behavioral verification pending).
- [ ] `4ac713c4d4b1bdc5e12111477b9e30a7569aa774` — fix(scheduling): release acquired slot when account hydration fails (pending; behavioral verification pending).
- [ ] `7122efee948fc31a086202e75caff1ea2e1562c8` — fix(auth): consume refresh tokens atomically during rotation (pending; behavioral verification pending).
- [ ] `eb4ccd173e35b459661fff4a4f079f6f96fccc56` — fix(openai): apply reasoning mappings to synthesized effort (pending; behavioral verification pending).
- [ ] `6f2faba2935c4e592c78c2f629a3d16f2a7ea09a` — fix(openai): validate synthesized effort after policy mapping (pending; behavioral verification pending).
- [ ] `aa245176ae49d4a37c2b729a633d26968b942b3e` — fix(openai): enforce synthesized effort ceilings and final validation (pending; behavioral verification pending).
- [ ] `b8de5e170006886b21ceee72e10d8cec7132dcdb` — perf(keys): API 密钥列表不再等待 30 天用量统计，并为其加覆盖索引 (pending; behavioral verification pending).
- [ ] `4a22a75b3dad2c77bdeeee1307a63d53e488eb2d` — fix(accounts): retain rows and quota values while refreshing (pending; behavioral verification pending).
- [ ] `edd7db77fe456da8c4d65ea8d611aad7e06fbd31` — fix(payment): support up to 6 decimals for balance recharge multiplier (pending; behavioral verification pending).
- [ ] `5965ac4e93a83d5386dee8981b47f81c1ab3c1b5` — feat(settings): 系统设置保存按钮改为底部吸附浮标 (pending; behavioral verification pending).
- [ ] `b2a695d617d490242a39b5f245cf2a50ae5d9993` — fix(openai): normalize Codex Desktop page context (pending; behavioral verification pending).
- [ ] `cf1ba819b99d141f3fa85b4ce127087e9ac2920c` — fix(openai): preserve native Codex image error statuses (pending; behavioral verification pending).
- [ ] `ffd26af50738d4f31b5fdb0ede1c8cd548e0b879` — test(openai): cover native image routes and proxy deadlines (pending; behavioral verification pending).
- [ ] `63f1172292a5604c7f3d1d04300b68058812d925` — fix(openai): reject invalid UTF-8 Responses bodies (pending; behavioral verification pending).
- [x] `390ba9905ae21cc2fbff3ad82006152b2c6c0772` — fix(securityaudit): 审计任务入队后立即唤醒本进程 worker (implemented; behavioral verification pending).

## Acceptance still pending

- [ ] Full backend compilation and production adapter/unit/integration coverage on exact published head.
- [ ] Frontend lint, typecheck, complete tests and build on exact published head.
- [ ] Critical old-code behavioral RED / current GREEN; compile failures are not RED.
- [ ] Race/resource lifecycle, migration and security gates.
- [ ] New admin feature notice registration, shared dismissal and A→B→C regressions.
- [ ] Real CSS baseline/candidate screenshots, sanitized native PR attachment URLs, hashes and readback.
- [ ] Full pagination of check runs (filter=all), traditional statuses, attempts and raw failure logs.
- [ ] Supervisor independent review; sole writer cannot self-certify it.

No local install, test, lint, typecheck or build commands are permitted for this task. No merge, release, tag, deployment, production mutation or paid requests are authorized.
