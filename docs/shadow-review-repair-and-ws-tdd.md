# Shadow 生命周期修复与下一 WS 具名 RED

本增量优先处理独审 `e593` 的 1 P1 + 3 P2，保留同 PR 未完成的 WS 实现。没有本机动态运行；所有新增回归、RED/GREEN 和变异的执行结果均待远端验证。

## 生产修复

- Shadow 关联增加独立 epoch fence：输出实际暴露前保留带 token 的 pending，真实终端交付和正文发布尝试结束后 CAS 完成。原 owner 路由字节不变。成功写入的关联冲突只撤销 shadow 可用性，不改变原响应；关联 store 不可用时沿用暴露前的存储拒绝边界。
- 首次恢复在准入前对 pending 有界等待（最多三秒），避免客户端看到终端后立刻续聊却遇到正常发布窗口。context 保存精确正文版本和 epoch，最终 native send 重读两者及全部源 owner fences。旧版本正文没有新 epoch 不可授权恢复。
- 复用同一个 opaque identity 会在暴露前切换 epoch；正文写入/失效失败也无法复用旧正文。晚到 publisher 的 token 无法复活已经冲突或更新的关联。正文仍为每个 user/key 八条、64KiB/条、总计512KiB；无正文的 epoch 元数据跨所有 session 最多128条，TTL最多30分钟，两个预算的索引分别原子淘汰且共享各自 principal 的 Redis Cluster slot。
- 恢复的正文仍经过内容审查。provenance 标记禁止把它保存为 Prompt Audit 正文、预览、扫描器引用、处罚证据或传统 moderation 摘录。异步审计对这类请求使用最多八个并发、每项最多128KiB、最多30秒的内存任务；不等待 scanner 才返回准入，不复制到持久 payload 队列。扫描后通过与 Runner 完成路径相同的事务逻辑提交 async 元数据与配置的处罚 hook，处罚失败则事件一起回滚。正常请求保留原配置行为。
- HTTP 的授权 response pointer 在 Beta 关闭、没有 encrypted-agent owner 时也绑定原 producer。只有开启 Beta 且有可读意图的显式重建才能清除指针并换号；原 HTTP 用户所有权校验不变。
- 历史 Issue715 两个 RED 配方移除新 shadow/epoch 依赖；其他已有历史 RED 同步维护，原业务断言与 candidate 测试保留。

## 新证据入口

`shadow-review-repair.yml` 默认执行固定 `e593` 生产 baseline RED 和 candidate GREEN，保留完全相同的测试。原始 JSONL、SHA/blob 和 validator 自测上传为 `shadow-review-repair-{RED|GREEN}-<head>`。

- Publication fences：JSON/SSE × 首发等待、恢复后失效、复用暴露、已有正文时写失败、晚到 publisher，共10条真实跨节点 HTTP/Redis 场景。客户端在 producer 完成前实际读到终端；不能再用等待 handler 返回的串行夹具掩盖窗口。
- Audit provenance：2条真实 HTTP 恢复后审查输入的 provenance 检查；audit persistence：3条真实 PostgreSQL/Redis 场景，验证 blocking allow/risk 与 async 仍评估合成私有段落，但 jobs/events/async payload 无该正文或 scanner quote。
- 后续三项 P2 的 async enforcement：2条具名 PostgreSQL/Redis 回归，配置实际 penalty coordinator。`admission` 将 scanner 持续挡在屏障内，证明准入已经返回；`penalty` 证明 async risk 仍有真实处罚、原 session scope 和 async-mode job/event。两条均检查没有持久正文，并注入 SQL 处罚触发器失败验证事务回滚。RED 固定源为 d023；不改原 e593 的正文泄漏 RED。
- OAuth shadow：12条真实 native final builder → loopback HTTP provider → JSON/SSE client，用固定真实 OAuth URL 构造后仅在 transport 边界重定向；校验 B 身份、账户 session namespace、stream/store 规范、旧 pointer/turn-state 清除与真实 downstream 成败。没有真实供应商网络访问。
- 无 opaque pointer：8条可读 previous/conversation 成功场景，16条严格模式/无意图/跨用户/B禁用边界。A 的第一响应不含 agent_message，真实 owner Redis 为空，但 HTTP response 用户授权存在。严格 baseline 探测如已通过会如实记录 GREEN，不捏造 RED。

新版本必须重跑原48条 shadow及 session-last-task变异；容量夹具扩展为141个任务 identity，独立验证128个 epoch上限和八条正文上限。历史 e593 GREEN/变异成功不等于这些新修复通过。

## 下一 WS 生产增量的精确触发

`ws-readable-continuation.yml` 暂为测试先行：PR 自动只跑固定 `e593` baseline RED。`TestOpenAI409WSReadableContinuation` 分别走 `http_bridge`、`ctx_pool`、`shared`、`dedicated`、`passthrough` 五个实际配置。真实客户端保持同一连接，A 生成 patch，客户端实际执行一次后管理员停用 A；B 必须接续 verification，再正常 terminal，历史 A owner 不改写、已完成工具不再执行。provider 检查最终任务、工具 schema、call/result、B credential 和旧指针缺失。RED 必须五条各自在 `WS_READABLE_B_DISPATCH_MISSING` 与旧 eligibility refusal 失败，panic/编译/skip/任意 EOF 不能冒充业务 RED。

另30条 GREEN 边界准备覆盖 Beta关闭、无可读意图、B禁用、pending/failed owner，以及 B 已实际收帧后断线绝不重放；后者必须实际达到 B 一次，不能以旧 A 拒绝冒充成功。

三项 P2 修复后，ambiguous_B 要求 provider 在完整身份/任务/结果/指针检查后记录接受状态、实际注入断线，并在 gateway handler 自然退出后核对 A/B 身份与每种真实 adapter 的精确发送数。普通负例须匹配对应错误码或政策关闭原因，任意 EOF 不可通过。`TestOpenAI409WSAmbiguousOracleMutations` 使用同一 provider/oracle，实际 HTTP 与 socket 各测试 valid、wrong_credential、old_pointer、missing_result、unrelated_eof；十条自验证不依赖尚未实现的 WS 自动换号。远程证据清单要求全部具名叶子。

复用暴露场景不再用50ms决定顺序：Y 保持在实际下游写之前，consumer 的真实 Redis GET 结果通过显式屏障交给测试。修复版必须读到 pending 且 B 无发送，随后才放行 Y；旧 e593 若读到旧正文，则先放行该 consumer、仍保持 Y，要求它实际完成以暴露错误 B 发送。所有该场景的失败使用 `SHADOW_REUSED_EXPOSURE_LEAK`，JSON/SSE 均走相同顺序。

下一片必须消费具名 RED 并实现实际 per-turn unsent-frame recovery/reselection、每模式最终 payload 与 downstream reserve/commit。跨节点、发送前后竞态、当下权限/quota、WS future-shadow 与 OAuth WS 等剩余门仍需独立补齐。当前并未实现 WS 自动换号，宽泛 Beta 测试匹配到这些先行测试会预期失败；不能删除断言或把测试改为 skip。

受控真实自有上游 A/B 尚无批准凭据/隔离实例，仍为监督验收 blocker。loopback provider 只证明实际 transport 与网关行为，不能替代真实供应商验收。
