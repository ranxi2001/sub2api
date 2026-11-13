# HTTP 可读任务重建：远端 TDD 检查点

本检查点只交付下一片回归与远端 RED 脚手架，生产代码仍是 `96ba36a85edab665ad5a035788fc07a40fd18762`。按任务约定，监督取得命名业务 RED 后再续接同一 writer 实现生产改动；不能把测试存在或 RED 成功视为功能完成。

## 本片目标

已授权的有损模式应支持无人值守编码循环：同一客户端把已有可读任务和已完成工具结果继续交给网关，管理员停用 A 后自动选择合格 B。不能要求用户点击确认、换客户端会话、手工重试或重新输入。无法迁移的旧 opaque 历史可以省略；只有没有任何可读意图的请求才不能重建任务。权限、额度、停用状态及未决交付仍独立校验。

`TestOpenAI409HTTPTaskRebuild` 有 36 个场景：API-key/OAuth × JSON/SSE × 无指针但最后一个 agent 仍 opaque / previous_response_id / conversation × 完成 / B 拒绝 / 写失败。回归通过真实 localhost HTTP 组合客户端、生产 handler、调度、转发和 provider；使用真实隔离 Redis 与生产 owner Lua。OAuth 的固定 ChatGPT URL 只在最终 HTTPUpstream 发送边界重定向到 localhost，body 和认证头不改写。夹具明确拒绝其他目的地和代理，不能向真实上游外呼。

正常路径中，A 返回一次 `apply_patch`，客户端修改一个内存中的 Go 源文件；A 停调度后 B 返回一次 `run_tests`，客户端解析实际修改后的 AST 并计算 `Sum(2,3)`；随后 B 交付最终 completed。所有后续请求只附加实际收到的 output 和实际执行的工具结果，不新增用户指令，不重试，原 session 标识固定。两个工具各执行一次。B 必须收到最新可读任务、system 指导、工具参数和匹配结果；替换请求不含 A 的 provider pointer/ciphertext。第三轮同时包含已交付 A/B opaque 历史，不能再次因混合历史拒绝正常续接。

新 B 输出继续接受实际下游写前 pending、写后 commit、写失败 failed 的检查。A owner 不变；上游拒绝不在账号池重放；成功上游用量仍进入 RecordUsage。模拟工具只操作内存数据，不运行外部命令，不代表真实供应商完成了真实仓库编码。

`TestOpenAI409HTTPRebuildBoundaries` 有 20 个场景：API-key/OAuth × JSON/SSE × pointer-only 无任务 / opaque-only 无任务 / pending / failed unknown delivery / B 也停用。它们要求明确错误、无完成伪装、无 B generation、owner 及交付屏障保留。该套在 GREEN 阶段执行，不用尚未执行的边界假设充当 RED。

## 远端运行

新增工作流 **HTTP readable task rebuild TDD**（`http-task-rebuild.yml`）。本测试先行检查点的 PR 自动执行 RED；监督也可在发布后手动指定阶段：

```bash
gh workflow run http-task-rebuild.yml --repo ranxi2001/sub2api --ref fix/agent409-readable-continuation -f phase=RED
```

新增 workflow 如未被默认分支索引，以 PR 自动触发为准。生产实现检查点必须把自动阶段切换为 BOTH，且监督必须取得同一生产候选 SHA 的 GREEN 与边界结果，才能声称 HTTP gate 完成。工作流已有 `phase=GREEN` / `phase=BOTH` 入口。

RED 的固定 source baseline 是 `96ba36a85edab665ad5a035788fc07a40fd18762`：创建 disposable worktree，恢复所有变更的后端生产 Go blobs，保留候选测试与已修 fixture，核对 production diff 为零。原干净 candidate checkout 留给 GREEN。两侧使用同一命令：

```bash
cd backend
go test -race -json -tags=unit ./internal/handler -run '^TestOpenAI409HTTPTaskRebuild$' -count=1 -timeout=5m
```

RED 必须是 36 个命名场景均在 `HTTP_REBUILD_B_DISPATCH_MISSING` 断言失败，且各自输出有明确的旧 owner/HTTP pointer/wire refusal code。构建失败、无测试、skip、panic、race、crash 或任意其他非零退出都不算业务 RED。GREEN 要求全部通过，并额外执行 20 个边界场景。候选 SHA、source baseline、测试 blob、原始 JSONL 和验证器自测日志分别记录在 Actions artifact。

旧 Issue805 RED 只新增排除此处的后续 Beta fixture，以保持其旧 baseline 下的 helper 依赖相容；原 Issue805 业务测试与断言不变。该排除仅发生于其 disposable RED，其他候选与本 TDD workflow 保留新测试。

## 仍需实现和验收

本片生产工作包括：可读任务判定取代最后 agent 位置启发式；body、选号参数和 recovery context 的 provider pointer 一致重建；原生 OAuth 的 system/tool/ID 转换与最终严格摘要对齐；同客户端连续工具回合中对已交付 A/B 历史的有损处理，且每个 pending/failed/conflict 必须完整检查。不能靠清 owner、放宽最终 digest 或假 200 达成。

future shadow/安全摘要是后续独立增量。可实现方案是把已观测的明确任务意图与任务关联标识按 tenant/user、API key、group、model、session 和版本隔离，使用有界 TTL 和容量；记录来源与交付状态，内容不进日志。只有可证明与当前 opaque 任务关联的已提交意图才能用于重建，不能把“同 session 上一条任务”无条件套给未知新任务。摘要/意图记录不能覆盖 generation/tool 的 pending 或 unknown 屏障，也不能授权重做副作用。历史从未留下线索时仍准确报告缺失。本 HTTP 检查点不实现该存储。

WS HTTP bridge、native/shared/pooled/dedicated、future shadow、真实自有 A/B 上游验证和最终独立 review 都保留为同 PR 必需 gate。HTTP mock 不能替代 WS、真实认证/财务系统或真实供应商验收。
