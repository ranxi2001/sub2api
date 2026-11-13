# HTTP 的自动有损可读续接

管理员开启现有 Beta 设置 `openai_409_omit_encrypted_history_enabled` 后，HTTP Responses 可以从可用的最新 user 任务、显式 instructions 或结构化任务摘要自动重建请求，省略不能迁移的旧 opaque 历史，并选择合格账号建立新的 upstream 会话。无需客户端确认、重开会话或重新输入。默认仍为 `false`；它与进程配置 `disable_encrypted_agent_owner_binding` 的完整密文跨账号尝试是两个独立选项。没有新增设置、迁移或公告 ID。

本次修复一个发送前仍返回 409 的缺口：历史省略冻结了 input 摘要，但后续 `normalizeOpenAIAPIKeyStoreFalseReasoningReplay` 会删除没有密文的 reasoning，`normalizeOpenAIResponsesReasoningContentReplay` 会删除可读 reasoning.content。最终 `prepareOpenAIAgentRecoveryAttempt` 检测到 input 变化并以 `wire_changed` 拒绝。

重建不再把最后一个 agent_message 的位置视为任务可恢复性的依据。现有可读任务在旧 opaque agent 之前也能继续；全部没有可读意图时返回明确的 422 缺失错误，不编造任务或返回假成功。空白 user 占位不覆盖此前任务；仅 system 通用指导和 agent 路由 envelope 不作为任务。较晚的完全 opaque user 任务不能借用此前不相关摘要。

旧 agent/reasoning/compaction 的可读内容仍带来源说明，已识别的密文标记为不可用；未知历史 reasoning 结构保留成可读 JSON，而不是作为可执行协议节点发出。工具参数、已完成结果和关联不能因此丢弃。previous_response_id / conversation 同时从 body、映射后的 forwardBody、选号参数和 recovery context 清除。已有异租户 response 归属和 store error 继续拒绝；只有无归属记录的旧指针才可在显式模式下丢弃，且仅使用调用者提供的可读意图。

最终发送仍精确校验 input，并新增 tools/schema、tool_choice、parallel_tool_calls 和有效 instructions 的摘要保护。原生 OAuth 在验证原快照后，仅派生 system 指导保留/提升、原生工具声明格式和保留名映射的预期快照；原快照不改写，普通 input 过滤器不能再次丢数据。最终 wire 必须匹配派生预期且无旧 provider pointer。

已完成 A/B 记录的账号差异和 pending/failed/conflict 分开处理。所有原 opaque item 都要扫描，重建后保留原 key 引用，在实际发送前再次读取交付屏障，不清除或覆写 A owner。新 B 输出继续先 reserve、成功下游写后 commit；拒绝和未知交付不能触发自动重放。沿用不含正文的 `openai.encrypted_history_409_omitted` 审计标识，不增加阻断客户端的通知。

## 首片范围与后续 gate

| 场景 | 本增量状态 |
| --- | --- |
| 原生 API-key HTTP JSON / SSE，同一 session，A 停调度后 B 可读续接 | 已编写真实 localhost HTTP + Redis 回归，动态结果待 Actions |
| summary-only / 可读 content + 密文 / store=true 历史 | 转换后发送，回归检查可读内容及一次生成 |
| B 拒绝、B 输出 reserve-before-write / commit-after-write、下游写失败 | 同一 HTTP 回归含对应断言，待远端验证 |
| 默认关闭、无任何可读意图、pointer-only、pending/unknown delivery、篡改 input/tools | 独立保护与回归保留；无意图无法重建，不扩大成所有 opaque 历史拒绝 |
| 原生 OAuth HTTP（system/tool/ID 与最终发包） | 已有生产候选与独立实际网络回归，候选 GREEN 待 Actions |
| 有可读原始任务/结构摘要的 opaque 历史 | 已实现候选；同客户端 A→B→B 受控工具循环待远端 GREEN |
| previous_response_id / conversation + 足够可读任务 | 已实现联合重建；不要求完整无损历史，候选 GREEN 待 Actions |
| 混合 A/B fence、工具定义丢失的 mutation | 48 个混合 fence、8 个工具 wire mutation、2 个 source mutant 验证待 Actions |
| future shadow/摘要 | 必须在同 PR 后续实现 tenant/key/group/model/任务关联/TTL；本片不存储客户任务正文 |
| WS HTTP bridge、native、pooled shared、pooled dedicated | 均未实现/未验收本模式；HTTP 测试不覆盖 WS |
| 真实供应商 A/B 兼容 | 未执行；由监督使用获准自有账号另做有界验证 |

## 远端验证

工作流 **Readable continuation 409 RED GREEN**（`readable-continuation-409.yml`）可由 PR 自动触发，也可由监督在发布候选分支后手动触发：

```bash
gh workflow run readable-continuation-409.yml --repo ranxi2001/sub2api --ref fix/agent409-readable-continuation
```

首次新增的 workflow 若尚未被默认分支索引，以 PR 自动触发为准。原 reasoning 工作流仍使用固定生产提交 `dbed7697aa56e76c6eb48c4914fa441496b67bdd` 的 omission 和相应生产调用者，保留候选测试；GREEN 保持干净候选树。两侧执行同一 `TestOpenAI409ReadableContinuationHTTP`，覆盖 18 个 JSON/SSE 场景。新的任务/指针/OAuth 工作流及已取得的旧源码业务 RED 见 [HTTP 重建 TDD](http-readable-task-rebuild-tdd.md)。后续每个候选均须重新取得对应 SHA 的 GREEN；测试存在、编译失败、no-tests、skip、panic/race 或旧版本成功都不算验收。

夹具只使用本地虚构 provider、隔离 Redis、虚构身份和内存账本工具。handler 的认证/计费依赖使用现有测试替身，不能据此声称验证了真实权限、余额或供应商协议。GREEN 另运行既有省略、完整性、当前任务、交付和权限保护的定向回归。所有动态执行仅限 Actions；本地仅做静态检查。
