# HTTP 可读任务重建：生产候选与远端 TDD

测试检查点 `5d8d14b960c2beda3627ee514ee09eead32a771a` 已在 [run 43441932069](https://github.com/ranxi2001/sub2api/actions/runs/43441932069) 取得固定 `96ba36a85edab665ad5a035788fc07a40fd18762` 生产源码的业务 RED：36 个 leaf 均命中 B 未发送的指定断言，30 个 owner_unavailable、6 个 OAuth http_previous_response_unsupported，无运行污染。当前在此 RED 后实现生产候选及独审 oracle 修复；候选 GREEN 和新增 mutation 仍须 Actions 执行，不视为已验收。

## 本片目标

已授权的有损模式应支持无人值守编码循环：同一客户端把已有可读任务和已完成工具结果继续交给网关，管理员停用 A 后自动选择合格 B。不能要求用户点击确认、换客户端会话、手工重试或重新输入。无法迁移的旧 opaque 历史可以省略；只有没有任何可读意图的请求才不能重建任务。权限、额度、停用状态及未决交付仍独立校验。

`TestOpenAI409HTTPTaskRebuild` 有 36 个场景：API-key/OAuth × JSON/SSE × 无指针但最后一个 agent 仍 opaque / previous_response_id / conversation × 完成 / B 拒绝 / 写失败。回归通过真实 localhost HTTP 组合客户端、生产 handler、调度、转发和 provider；使用真实隔离 Redis 与生产 owner Lua。OAuth 的固定 ChatGPT URL 只在最终 HTTPUpstream 发送边界重定向到 localhost，body 和认证头不改写。夹具明确拒绝其他目的地和代理，不能向真实上游外呼。

正常路径中，A 返回一次 `apply_patch`，客户端修改一个内存中的 Go 源文件；A 停调度后 B 返回一次 `run_tests`，客户端解析实际修改后的 AST 并计算 `Sum(2,3)`；随后 B 交付最终 completed。所有后续请求只附加实际收到的 output 和实际执行的工具结果，不新增用户指令，不重试，原 session 标识固定。两个工具各执行一次。B 必须收到最新可读任务、system 指导、工具参数和匹配结果；替换请求不含 A 的 provider pointer/ciphertext。第三轮同时包含已交付 A/B opaque 历史，不能再次因混合历史拒绝正常续接。

新 B 输出继续接受实际下游写前 pending、写后 commit、写失败 failed 的检查。A owner 不变；上游拒绝不在账号池重放；成功上游用量仍进入 RecordUsage。模拟工具只操作内存数据，不运行外部命令，不代表真实供应商完成了真实仓库编码。

`TestOpenAI409HTTPRebuildBoundaries` 有 20 个场景：API-key/OAuth × JSON/SSE × pointer-only 无任务 / opaque-only 无任务 / pending / failed unknown delivery / B 也停用。它们要求明确错误、无完成伪装、无 B generation、owner 及交付屏障保留。该套在 GREEN 阶段执行，不用尚未执行的边界假设充当 RED。

provider 现在必须先收到客户端的两个工具声明及完整 schema 才发出任何工具；原生可选 description/strict 默认值可变化，参数 schema 不可丢失或改变。`TestOpenAI409HTTPToolDefinitionOracle` 的 8 个场景在测试专用最终传输层删 tools 或改变 schema，要求真实 provider 返回拒绝且不输出工具、无 B owner/usage grant。

`TestOpenAI409HTTPMixedDeliveryFences` 的 48 个场景先产生两个已交付 A item 和一个已交付 B item，再将 A 或 B 的真实 Redis owner record 置为 pending/failed/conflict，测试正反 opaque item 顺序。必须在额外 generation 前拒绝，所有 Redis record 值逐字节不变。反序包含“已完成 B → 已完成 A-extra → fenced A”，专门识别遇 owner 差异就停止扫描的错误。

两个远端 disposable source mutant 分别把实际生产扫描缩成最新 key，或在 owner 差异处截断保留 key 列表并 break。它们用不变的真实 HTTP oracle 运行，必须在 `HTTP_REBUILD_FENCE_BYPASSED` 断言失败并证明错误地产生了 `coding-task-complete`；任意其他失败不能算 mutation 被检出。原干净 candidate 不修改。

## 远端运行

工作流 **HTTP readable task rebuild TDD**（`http-task-rebuild.yml`）现已自动执行 BOTH；监督也可在发布后手动指定阶段：

```bash
gh workflow run http-task-rebuild.yml --repo ranxi2001/sub2api --ref fix/agent409-readable-continuation -f phase=BOTH
```

新增 workflow 如未被默认分支索引，以 PR 自动触发为准。必须取得同一生产候选 SHA 的 GREEN、边界和 mutation 结果，以及独立审查，才能声称 HTTP gate 完成。

RED 的固定 source baseline 是 `96ba36a85edab665ad5a035788fc07a40fd18762`：创建 disposable worktree，恢复所有变更的后端生产 Go blobs，保留候选测试与已修 fixture，核对 production diff 为零。原干净 candidate checkout 留给 GREEN。两侧使用同一命令：

```bash
cd backend
go test -race -json -tags=unit ./internal/handler -run '^TestOpenAI409HTTPTaskRebuild$' -count=1 -timeout=5m
```

RED 必须是 36 个命名场景均在 `HTTP_REBUILD_B_DISPATCH_MISSING` 断言失败，且各自输出有明确的旧 owner/HTTP pointer/wire refusal code。构建失败、无测试、skip、panic、race、crash 或任意其他非零退出都不算业务 RED。GREEN 要求 36+20+8+48 个场景全部通过，随后两个独立 source mutant 必须被指定业务断言检出。候选 SHA、source baseline、测试 blob、原始 JSONL、source diff/hash 和验证器自测日志分别记录在 Actions artifact。

旧 Issue805 RED 只新增排除此处的后续 Beta fixture，以保持其旧 baseline 下的 helper 依赖相容；原 Issue805 业务测试与断言不变。该排除仅发生于其 disposable RED，其他候选与本 TDD workflow 保留新测试。

## 仍需实现和验收

本片生产候选已包含：可读任务判定取代最后 agent 位置启发式；body、选号参数和 recovery context 的 provider pointer 一致重建；原生 OAuth 的明确 system/tool 规范化与独立预期摘要；同客户端连续工具回合中已交付 A/B 历史的有损处理；所有原 key 的交付检查和最终发送前重查。原 input 快照不改写，最终校验另覆盖 tools/schema 与有效 instructions。还需当前候选的全部远端 GREEN、mutation 与独立审查，不靠清 owner、任意重置 digest 或假 200 达成。

future shadow/安全摘要是后续独立增量。可实现方案是把已观测的明确任务意图与任务关联标识按 tenant/user、API key、group、model、session 和版本隔离，使用有界 TTL 和容量；记录来源与交付状态，内容不进日志。只有可证明与当前 opaque 任务关联的已提交意图才能用于重建，不能把“同 session 上一条任务”无条件套给未知新任务。摘要/意图记录不能覆盖 generation/tool 的 pending 或 unknown 屏障，也不能授权重做副作用。历史从未留下线索时仍准确报告缺失。本 HTTP 检查点不实现该存储。

WS HTTP bridge、native/shared/pooled/dedicated、future shadow、真实自有 A/B 上游验证和最终独立 review 都保留为同 PR 必需 gate。HTTP mock 不能替代 WS、真实认证/财务系统或真实供应商验收。

## 84e47 独审后的修复候选

独审提出一个 P1 和三个 P2，修复候选保留这些执行 gate：未归属 pointer 的 discard waiver 与真实 ownership 分开，任何设置翻转/读取故障导致未完成重建时必须零 upstream send；缺省 instructions 的存在状态也受摘要保护，OAuth 默认指令只能在原快照校验后由受控规则产生；合法嵌套 tool choice 和无工具 parallel 控制使用明确的派生快照；全空白 text-part 数组视为占位，opaque/media/未知 part 仍不能借用旧任务。

新增实际 HTTP `TestOpenAI409HTTPUnownedPointerMustDetach` 两场景和 `TestOpenAI409HTTPControlNormalization` 八场景。工作流 `HTTP review counterexamples on 84e47` 在独立旧源码 checkout 对相同测试验证具体错误发送/规范化失败；候选 GREEN 同样执行全套。service integrity 测试另覆盖在 OAuth preparation **之前**插入/替换 instructions 的反例以及确切默认指令正例。新增执行结果仍待当前 head 的 Actions 和独审，不能视为上述 findings 已动态关闭。
