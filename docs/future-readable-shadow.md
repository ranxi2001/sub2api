# Future readable shadow：提交边界与恢复约束

这是 mandatory shadow 功能的首个远端 TDD 检查点，生产存储尚未实现。HTTP 候选已能使用请求自带可读意图；此增量专门验证未来仅回带同一 opaque item 时，能否安全恢复之前实际观测的同任务上下文。原始未知历史没有可用线索时不能编造。

## 第一片范围

真实 HTTP JSON/SSE、隔离真实 Redis、两个独立网关 service 实例。A 收到明确任务并返回工具调用，客户端执行一次内存源码补丁，再把真实调用与结果交回 A。A 随后交付 opaque task。管理员停用 A 后，同一客户端/session 只回带该 opaque item，由第二节点恢复已提交上下文并在合格 B 正常完成。provider 必须收到原任务、已完成 call/result 和实际工具 schema；不能重做工具。

`TestOpenAI409FutureShadowHTTP` 两个正常场景必须走到真实 B 网络发送与客户端 completed，不能用 cache helper 成功替代。`TestOpenAI409FutureShadowBoundaries` 的 26 个场景覆盖不同 user/key/group/model/session/task、TTL 到期、失败下游写、原 owner pending/failed/conflict、设置关闭和当前可读新任务优先。记录在 actual downstream write 之前不能成为已提交 shadow。过期/跨作用域/未知交付不得产生 B 请求，也不能修改原 owner。

当前 artifact/实现契约使用 Redis `openai_readable_shadow:v1:` namespace，scope 中必须含 tenant/user、API key、group、请求 model、session hash、任务关联（完整 opaque item digest）和格式版本；键只含 hash，不能放任务正文。每条最大 64 KiB、TTL 不超过 30 分钟；下一生产增量还必须实现作用域内最大记录数/总容量及到期淘汰，不能只靠单条 TTL 宣称总容量有界。

## 生产接入约束

1. Capture 只能来自网关实际观测的认证请求中的明确意图和配对的已完成工具结果。绑定到同次已成功交付的具体 opaque output，不能维护按 session 取“最近任务”的泛用 fallback。
2. 复用原 generation 的 pending/failed/commit 边界：在最后下游写成功之前不发布可读 shadow；发布失败只能让 shadow 不可用，不能改写 producer owner 或授权重发。
3. Recover 必须重新检查调用者作用域、当前资格、opaque digest/task association、TTL 和全部原 owner delivery fences。先有当前可读任务就用当前请求，不能用旧 shadow 覆盖新意图。不能修改已完成 tool result、恢复为新 tool call 或重放未知副作用。
4. shared store 在同节点/跨节点均有界。无可用 shared store 时不得改用无期限本机 map，不能通过日志保存正文。记录内容只在服务内受限存储中使用，不加入审计/metrics 错误字符串。
5. 本片是 HTTP 首测；WS bridge/native/shared/pooled/dedicated 必须分别接入其实际 final payload 与 downstream terminal，不能借 HTTP 测试替代。

## 远端证据

工作流 **Future readable shadow TDD**（`future-shadow.yml`）测试先行阶段 PR 自动只跑 RED。固定 source baseline `84e47f1b57acba77f1cfae08a63e8dd9d32a7991`，disposable worktree 恢复该版本全部生产 Go 文件，保留新回归。RED 必须是两个 leaf 各自的 `FUTURE_SHADOW_B_DISPATCH_MISSING` 与 `encrypted_agent_recovery_readable_intent_missing`，不能接受任意失败、panic/race、skip/no-tests 或编译错误。

```bash
gh workflow run future-shadow.yml --repo ranxi2001/sub2api --ref fix/agent409-readable-continuation -f phase=RED
```

新 workflow 未被默认分支索引时用 PR 自动触发。监督取得有效原始 RED 后，writer 同 thread 立即实现生产 capture/store/recover，将自动阶段切到 BOTH；GREEN 必须执行主回归和 26 个边界。候选 SHA、baseline SHA、test/production blobs、原始 JSONL 和 validator 自测记录独立上传。

本 checkpoint 另修复既有 Issue805 WS 夹具的基线等待：必须要求整条 owner record 是合法 JSON 数组才视为 A committed；不能仅用 gjson 从 `pending:<token>:[...]` 后的数组取到 account ID。原 B pending 屏障、跨节点等待和原记录字节不变断言保持。该修复待远端执行验证，不能声称 WS 有损重建已实现。

所有动态执行仅限 Actions；没有受控真实供应商账号/实例，真实 A/B 验收仍是监督负责的 blocker。future shadow 的总容量、存储故障、冲突及全部 WS gate 尚未完成，不能合并或宣称完整功能验收。
