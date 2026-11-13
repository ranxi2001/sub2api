# Future readable shadow：提交边界与恢复约束

此增量实现未来 HTTP 请求仅回带同一 opaque agent item 时，恢复之前实际观测并成功交付的同任务上下文。它沿用显式开启的有损续聊 Beta 设置；原始未知历史没有可用线索时不能编造。当前可读意图始终优先，不依赖 shadow 存在。

## 第一片范围

真实 HTTP JSON/SSE、隔离真实 Redis、两个独立网关 service 实例。A 收到明确任务并返回工具调用，客户端执行一次内存源码补丁，再把真实调用与结果交回 A。A 随后交付 opaque task。管理员停用 A 后，同一客户端/session 只回带该 opaque item，由第二节点恢复已提交上下文并在合格 B 正常完成。provider 必须收到原任务、已完成 call/result 和实际工具 schema；不能重做工具。

`TestOpenAI409FutureShadowHTTP` 两个正常场景必须走到真实 B 网络发送与客户端 completed，不能用 cache helper 成功替代。`TestOpenAI409FutureShadowBoundaries` 的 26 个场景覆盖不同 user/key/group/model/session/task、TTL 到期、失败下游写、原 owner pending/failed/conflict、设置关闭和当前可读新任务优先。记录在 actual downstream write 之前不能成为已提交 shadow。过期/跨作用域/未知交付不得产生 B 请求，也不能修改原 owner。

实现使用 Redis `openai_readable_shadow:v1:` namespace，scope 含 tenant/user、API key、group、请求 model、session hash、任务关联（完整 opaque item digest）和格式版本；键只含 hash，不能放任务正文。每条最大 64 KiB、TTL 不超过 30 分钟；每个 user/key 跨全部 group/model/session 最多八条，正文总量不超过 512 KiB，原子淘汰旧记录及过期索引。

## 生产接入约束

1. Capture 只能来自网关实际观测的认证请求中的明确意图和配对的已完成工具结果。绑定到同次已成功交付的具体 opaque output，不能维护按 session 取“最近任务”的泛用 fallback。
2. 复用原 generation 的 pending/failed/commit 边界：在最后下游写成功之前不发布可读 shadow；发布失败只能让 shadow 不可用，不能改写 producer owner 或授权重发。
3. Recover 必须重新检查调用者作用域、当前资格、opaque digest/task association、TTL 和全部原 owner delivery fences。先有当前可读任务就用当前请求，不能用旧 shadow 覆盖新意图。不能修改已完成 tool result、恢复为新 tool call 或重放未知副作用。
4. shared store 在同节点/跨节点均有界。无可用 shared store 时不得改用无期限本机 map，不能通过日志保存正文。记录内容只在服务内受限存储中使用，不加入审计/metrics 错误字符串。
5. 本片是 HTTP 首测；WS bridge/native/shared/pooled/dedicated 必须分别接入其实际 final payload 与 downstream terminal，不能借 HTTP 测试替代。

## 远端证据

工作流 **Future readable shadow TDD**（`future-shadow.yml`）现在 PR 自动执行 RED/GREEN。固定 source baseline `84e47f1b57acba77f1cfae08a63e8dd9d32a7991`，disposable worktree 恢复该版本全部生产 Go 文件，保留新回归。RED 必须是两个 leaf 各自的 `FUTURE_SHADOW_B_DISPATCH_MISSING` 与 `encrypted_agent_recovery_readable_intent_missing`，不能接受任意失败、panic/race、skip/no-tests 或编译错误。

```bash
gh workflow run future-shadow.yml --repo ranxi2001/sub2api --ref fix/agent409-readable-continuation -f phase=BOTH
```

生产实现前已读取远端 run `43455666698` artifact `14054563124`：候选 de22、基线 84e47 的两个具名 leaf 均在预期业务断言失败，无运行污染。新候选 GREEN 必须执行主回归 2、边界 26、资源 18、精确任务关联 2，共 48 个 transport leaf，随后执行 session-last-task 生产变异。候选 SHA、baseline SHA、test/production blobs、原始 JSONL 和 validator 自测记录独立上传。生产候选的 GREEN/变异尚未执行，不能用上述 RED 声称成功。

本 checkpoint 另修复既有 Issue805 WS 夹具的基线等待：必须要求整条 owner record 是合法 JSON 数组才视为 A committed；不能仅用 gjson 从 `pending:<token>:[...]` 后的数组取到 account ID。原 B pending 屏障、跨节点等待和原记录字节不变断言保持。该修复待远端执行验证，不能声称 WS 有损重建已实现。

所有动态执行仅限 Actions；没有受控真实供应商账号/实例，真实 A/B 验收仍是监督负责的 blocker。future shadow 的容量/故障/冲突已有生产实现和 transport 回归，等待远端验证。WS bridge/native/shared/pooled/dedicated 的有损重建与 shadow 接入仍是同 PR 必需后续，不能合并或宣称完整功能验收。

## 生产实现与新增反例

shadow 恢复在 HTTP 内容审查、image intent、token 预留和选号之前进行，恢复出的任务仍经过当前准入及最终 long-context 检查。它不是绕过准入的授权。当前可读意图优先，不查询旧任务；只有完整 opaque item digest 对应的已提交记录才可提供旧意图，不能使用 session 最近记录。

共享存储使用 user/key 的 principal hash 作为 Redis Cluster hash tag；在这个预算内，group/model/session/item 都进入精确任务键。每个 principal 最多八条、每条最大 64 KiB，所有 session/model/group 合计不超过 512 KiB 正文，TTL 最多 30 分钟。索引和记录同 slot 原子写入/淘汰，读和重复发布不续期，相同输出的不同内容失效为 conflict，不能覆盖成新授权。没有共享 store 不启用本机后备。

原始请求的可读意图是 capture 前提；实际 native send 的文本及配对 completed call/result 才是保存候选。包含未完成工具或含未知交付的请求不能生成可恢复 shadow。只有真实 downstream terminal 成功后、相应 output owner 提交成功后才发布。记录保留来源的精确 delivery fences，恢复和最后实际发送均再读；缓存失败只损失 future 恢复能力，不改 owner、不重发已交付响应。

独审新增 P2 对应 `TestOpenAI409FutureShadowTaskAssociation`：同一个作用域先真实执行 X 的 patch，再执行独立 Y 的 verification，两个不同 opaque output 均已交付并有 committed owner；另一个节点恢复旧 X，B 必须接收 X 的指令与 patch 结果且不能含 Y 指令/verification。原 unknown-item 负例保留。远端 session-last-task 变异必须因任务内容不匹配而失败，不能用缺 owner/缺 store 失败代替。

`TestOpenAI409FutureShadowResources` 另覆盖 JSON/SSE × 九项：跨141个任务/session 超出八条正文和128条 epoch预算后旧记录淘汰/最新仍能恢复、仅 shadow 写故障/读故障、超大正文不入库、请求或响应含未完成工具不入库、同一 opaque output 对应两条已交付任务时失效，以及回带旧 opaque 后生成同 identity 时失效。故障通过真实 Redis client 的命令 hook 注入，owner、billing 与真实 HTTP 保留；读故障要求先存在真实 committed record，且必须证明 hook 实际执行。

本生产片的 shadow 恢复范围是单个精确 opaque agent item；多条未知 opaque、新 opaque user/media 意图或新未配对工具结果不能借旧快照猜测当前任务。可读请求仍走现有 HTTP 有损重建，不受这个 future-only 范围限制。普通 native HTTP 的共同发送/交付路径可记录上下文。后续独审发现的发布窗口、失效竞态与审计保留已在 [生命周期修复记录](shadow-review-repair-and-ws-tdd.md) 中处理：记录格式升级为2并依赖独立 pending/ready epoch，新增 OAuth shadow 线级回归。新候选全部动态验证、所有 WS 生产入口仍未完成。
