# HTTP 409 的可读历史续接（首增量）

管理员开启现有 Beta 设置 `openai_409_omit_encrypted_history_enabled` 后，HTTP Responses 在发送前因已交付历史的原账号不可调度或路线变化被拒绝，可以省略旧密文并选择其他合格账号。默认仍为 `false`；它与进程配置 `disable_encrypted_agent_owner_binding` 的完整密文跨账号尝试是两个独立选项。本次没有新增设置、迁移或公告 ID。

本次修复一个发送前仍返回 409 的缺口：历史省略冻结了 input 摘要，但后续 `normalizeOpenAIAPIKeyStoreFalseReasoningReplay` 会删除没有密文的 reasoning，`normalizeOpenAIResponsesReasoningContentReplay` 会删除可读 reasoning.content。最终 `prepareOpenAIAgentRecoveryAttempt` 检测到 input 变化并以 `wire_changed` 拒绝。

在显式省略路径中，现在先把 reasoning.summary 和 reasoning.content 的可读文本按原顺序转换为带来源说明的 assistant 历史；旧 reasoning 密文改为明确的缺失标记，同时移除其原账号的 reasoning item ID 和输出状态。之后才冻结摘要。普通消息、system/user 指令、工具调用及已完成结果、call_id 关联和大整数保持原值。未知 reasoning 结构拒绝转换，不猜测其内容。普通非降级请求继续使用原有 normalizer。

发送前完整性比较仍是严格摘要比较，不允许转发器丢弃可读内容、修改工具参数/结果或调整历史顺序。新 B 输出仍使用原有的先 reserve、成功下游写后 commit 机制。A 的 owner 记录不改写；拒绝、未确认交付和存储故障不会获得跨账号重放许可。原有不含正文的 `openai.encrypted_history_409_omitted` 日志继续标记一次请求中的降级，不增加逐项日志或客户端通知事件。

## 首片范围与后续 gate

| 场景 | 本增量状态 |
| --- | --- |
| 原生 API-key HTTP JSON / SSE，同一 session，A 停调度后 B 可读续接 | 已编写真实 localhost HTTP + Redis 回归，动态结果待 Actions |
| summary-only / 可读 content + 密文 / store=true 历史 | 转换后发送，回归检查可读内容及一次生成 |
| B 拒绝、B 输出 reserve-before-write / commit-after-write、下游写失败 | 同一 HTTP 回归含对应断言，待远端验证 |
| 默认关闭、当前 opaque 任务、pointer-only、pending/unknown delivery、篡改 input | 既有拒绝保护保留，相关回归加入远端检查 |
| 原生 OAuth HTTP（含 system 提升、工具名和 ID 变换） | reasoning 转换共用代码；完整 HTTP 验收未完成，不能用 API-key 回归替代 |
| 有可读原始指令/可信摘要的当前 opaque 任务 | 本次仍按现有规则拒绝；需同 thread 后续实现及 gate |
| previous_response_id / conversation + 足够可读历史重建 | 本次仍拒绝；后续须一起清除 body、选号参数和上下文里的 provider pointer，pointer-only 仍需拒绝 |
| WS HTTP bridge、native、pooled shared、pooled dedicated | 均未实现/未验收本模式；HTTP 测试不覆盖 WS |
| 真实供应商 A/B 兼容 | 未执行；由监督使用获准自有账号另做有界验证 |

## 远端验证

工作流 **Readable continuation 409 RED GREEN**（`readable-continuation-409.yml`）可由 PR 自动触发，也可由监督在发布候选分支后手动触发：

```bash
gh workflow run readable-continuation-409.yml --repo ranxi2001/sub2api --ref fix/agent409-readable-continuation
```

首次新增的 workflow 若尚未被默认分支索引，以 PR 自动触发为准。工作流使用同一候选 SHA 的两个独立 checkout。RED 仅恢复固定生产提交 `dbed7697aa56e76c6eb48c4914fa441496b67bdd` 的 `backend/internal/service/openai_409_omit_history.go`，保留候选 HTTP 测试；GREEN 保持干净候选树。两侧执行同一 `TestOpenAI409ReadableContinuationHTTP`，分别覆盖 18 个 JSON/SSE 场景。证据校验要求 RED 的六个正常续接场景明确因 `encrypted_agent_recovery_wire_changed` 失败，GREEN 全部通过；编译失败、no-tests 和 skip 都不能算 RED。日志与代码/测试 blob 身份保存在 Actions artifact 中。

夹具只使用本地虚构 provider、隔离 Redis、虚构身份和内存账本工具。handler 的认证/计费依赖使用现有测试替身，不能据此声称验证了真实权限、余额或供应商协议。GREEN 另运行既有省略、完整性、当前任务、交付和权限保护的定向回归。所有动态执行仅限 Actions；本地仅做静态检查。
