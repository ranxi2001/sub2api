# Scheduler 发布争用：观测与后续设计

关联 [#711](https://github.com/ranxi2001/sub2api/issues/711)。分析基线为
`production@dc1e1b072faa53ec2e6f0925be39d5d024ec1687`。
本文包含已落地的分阶段日志与退休分组批次优化；后半部分是尚未实现的设计与验收门槛。
这些增量不修改账号锁粒度、Redis 发布位置或请求 fallback，不能据此关闭 #711。

## 当前一致性边界

- `account_codex_snapshot.go:WithLatestSchedulerAccounts` 在 PostgreSQL 事务内按全局顺序一次获取账号 advisory lock，重新读取完整账号，再执行发布回调。
- `openai_codex_snapshot_contract.go` 的全量重建使用锁内完整账号，防止旧查询恢复已禁用状态、旧凭据、旧 BPS/质量状态。请求反馈单账号发布保留既有非 usage 字段合并语义。
- `scheduler_cache.go` 的 bucket epoch 保护退休、重开与版本激活；账号详情、调度 metadata 是跨 bucket 的共享键，bucket epoch 不能代替账号级新鲜度保护。
- `SetSnapshotByAccountIDs` 可复用同一批次已写入的成员，不能据此假设首次完整发布也已无锁。
- `sched:acc` 仍是请求 hydration 的完整账号来源。直接删掉 credentials/extra 会破坏后续实际请求与部分调度判断。

[#712](https://github.com/ranxi2001/sub2api/pull/712) 已减少取锁 SQL 往返，仍有 N 个账号锁、锁内重读及锁内 Redis 发布。

## 新增发布时序日志

`[Scheduler] account publication timing` 在传入 ID 列表非空的发布结束后生成一条结构化记录；空列表沿用无事务回调，不增加日志。
失败及总耗时至少 1 秒使用 WARN；其他成功路径使用 DEBUG，沿用现有日志级别配置。
不增加查询、重试、采样任务或生产管理开关。

| 字段 | 口径 |
| --- | --- |
| `phase` | 最后进入的阶段：`begin`、`lock`、`read`、`publish`、`commit`；正常返回为 `complete` |
| `failure_kind` | `none`、`error`、`deadline` 或 `canceled`；不输出原始错误 |
| `requested_count` / `lock_count` | 传入 ID 数量 / 去重且过滤非正数后的锁数量；不输出 ID 值 |
| `owns_transaction` | 本方法是否创建事务；false 时事务可能由外层持有 |
| `elapsed_ms` | 本方法进入到内部提交/回滚清理结束的时间；不含外部事务结束时间 |
| `lock_sql_attempted` / `lock_sql_ms` | 是否执行锁 SQL及其客户端耗时，包含数据库执行、锁等待、网络及 driver 时间 |
| `lock_acquired` / `post_lock_ms` | 所有目标锁是否成功获得；完整锁集合获得后到本方法清理结束的时间 |
| `read_attempted` / `read_ms` | 是否开始锁内 fresh read 及其客户端耗时 |
| `publish_attempted` / `publish_ms` | 是否开始发布回调及其耗时，包含编码、Redis 写入与 bucket 激活等回调工作 |

`lock_sql_ms` 不能直接称为 PostgreSQL 纯锁等待时间。多个账号的锁在单条 SQL 中逐个获得，`post_lock_ms` 也不是完整持锁时间：第一把锁可能在 SQL 返回前已持有；外部事务还可能在本方法返回后持锁。取锁失败可能已经获得部分锁，不能把 `lock_acquired=false` 解读为从未持锁。

阶段未开始时毫秒值为 0，必须结合对应 `*_attempted` 判读；毫秒取整为 0 不等于没有执行或没有消耗。
日志只增加计数、阶段、布尔值和耗时，不含账号 ID、凭据、SQL、回调错误内容或请求正文。
bucket 重建沿用现有日志维度，并补充 `load_ms`、`publish_ms`、`elapsed_ms` 和输入 `size`；输入大小不等于最终发布成员数。该耗时从成功获得 bucket 锁后开始，不包含前面的 bucket 锁请求。

排查时在相同时间窗口、相同节点分别比较以下指标：

1. `phase=lock` 失败或 `lock_sql_ms` 高：结合只读 `pg_stat_activity`/`pg_locks` 的 advisory wait 证据，区分网络与真实锁等待。
2. `read_ms` 高：检查完整账号/分组读取和数据库往返；不得通过打印完整账号定位。
3. `publish_ms`/`post_lock_ms` 高：检查序列化与 Redis 阶段；网络慢 Redis 会继续占用 PostgreSQL 临界区。
4. bucket 加载耗时、主 outbox 水位、retry 待处理量与最老事件年龄一起看；主水位前进不能证明 retry 已排空。

本次未增加持久化指标或 retry 深度采集器，未采集线上耗时，不声称任何生产性能改善幅度。

## 退休分组：减少一次 bucket 准备内的重复协调

在 `production@b677bb176ae66e67ed09919756c10f1b0ae2ac0f`，严格 outbox
处理延迟到达的 `account_changed` 时，已删除账号会通过旧分组 ID 重建全部平台。
若该分组已经退休，`prepareBucketWriteTasks` 为每个退休 bucket 再次调用
`prepareGroupLifecycle`。每次都获取生命周期租约、fresh 查询分组、读取 bucket 注册表，
并退休全部 canonical 与已登记历史 bucket；同组 B 个退休 bucket 重复 B 轮。

现在仅在一次 `prepareBucketWriteTasks` 内记住**已成功完成**的 inactive/missing 协调。
相同分组后续仍然退休的 bucket 不再重复整组协调；该分组的权威读取、注册表读取和租约
获取由 B 次变为 1 次，全组退休由 B 轮变为 1 轮。这是源码调用次数的变化，未测量线上收益。

每个 bucket 仍先捕获当前 epoch，成功 capture 优先于之前的 inactive 判断，
并发 reopen 后仍可生成写任务；之后的 retire/reopen 仍由原 token fencing 拒绝旧写入。
失败（包括租约释放失败）不缓存，首个错误仍向上传递；active 协调不复用。
记录不跨调用、事件或 retry，后续调用重新读取权威状态。首次 inactive 判断之后发生的
状态变化继续由既有事件与全量对账处理，不新增 revision 或事件可靠性保证。

本增量进度（勾选表示源码/接线完成，不表示测试已执行）：

- [x] 生产严格 outbox 路径复用同次准备内完成的 inactive/missing 整组退休。
- [x] 确定性回归源码覆盖 missing/inactive、主事件/retry、历史 bucket、失败后重试、下一调用激活、并发 reopen 与旧 token fencing。
- [x] 专项 CI 选择新回归与生命周期守卫，并新增固定旧 blob 的 expected-RED；原真实 PG18/双 Redis adapter 集成检查继续运行。
- [ ] 当前候选 SHA 的远端 GREEN、行为 RED 与真实 adapter 集成证据（本轮禁止本地动态验证，尚未发布）。

回归位于 `backend/internal/service/scheduler_snapshot_retired_group_batch_test.go`；
远端命令为 `go test -race -tags=unit ./internal/service -run '^TestSchedulerRetiredGroupBatch' -count=1 -v`。
此优化没有减少活跃大分组的全量成员重建，也没有实现真正的增量成员更新。

## 后续方案：账号 revision 与 Redis CAS（尚未实现）

### 1. 先建立可证明的账号修订号

单账号发布必须具备数据库事务内递增、随账号状态一起提交的 revision。
不要用 `updated_at`、进程时钟、bucket 版本或先取全局 sequence 的数值当作账号提交顺序；并发事务可能以不同顺序提交。

优先评估账号行 `scheduler_revision` 在同一行锁下递增的方案，并逐一覆盖 Ent 与 raw SQL 更新、usage CAS、分组绑定、代理变化、软删除和硬删除。
分组/代理等关联表修改需在同一事务内推进受影响账号 revision；仅给 Account UPDATE 加 trigger 不够。
删除必须留下带新 revision 的持久 tombstone，使旧 reader 不能在 Redis 清理或 TTL 后复活账号。
DDL、回填锁开销、revision 溢出策略以及硬删除后的 tombstone 生命周期需要在独立迁移 PR 中确定。

### 2. 账号级 CAS 先运行，才移出 PostgreSQL 锁

使用独立的 versioned Redis namespace，原子更新 revision、投影、详情版本引用及 tombstone。
Lua 拒绝旧 revision；同 revision 的重试必须是幂等相同载荷，不允许同版本不同内容静默覆盖。
大整数必须用固定宽度十进制字符串比较，避免 Lua number 的 53 位整数精度边界。

usage 已有逐字段 observation version，要保留该语义。现有请求反馈会合并尚未提交到数据库的非 usage 状态，不能简单贴上当前数据库 revision 后覆盖整行。
先明确这些临时反馈是独立的运行时覆盖层，还是先持久化再发布；两者都须测试失败、乱序和超时行为。
在这一边界实现前，不能删除 `WithLatestSchedulerAccounts` 的账号锁。

独立 Redis 之间不共享本地 CAS 状态。每个实例仍须通过可靠 outbox 获取已提交 revision 并推进各自缓存；不能假设节点 A 的 CAS 会同步拒绝节点 B 的陈旧缓存。

### 3. 投影与详情 hydration 同步改造

增加只读调度投影查询，以实际候选判断消费的字段为白名单，禁止包含原始 token、代理密码或全量 Extra。
模型映射、质量/BPS 条件、倍率与用量窗口等必须有明确投影，不能因裁掉 Extra 而改变候选资格。
选中账号后再按投影引用的 revision 获取详情；详情不存在、版本不符或变为禁用/删除时重新读取权威状态并重新选择，不能盲用旧凭据。
保留批量获取能力，避免重新引入 N 次数据库或 Redis 往返。

完成账号级 CAS 与详情读取契约后，全量加载的 revision 随投影一次读出，先结束数据库事务，再写 Redis；bucket 激活仍独立校验 epoch。
旧 revision 拒绝不等于 Redis 失败：前者读取更新状态并处理成员，后者保留旧 active 指针并入持久 retry。

### 4. 增量成员更新最后接入

`account_changed` 的新旧分组、平台、mixed 模式和删除状态决定需要增加/删除的 bucket 成员。
增量写同样校验账号 revision 与 bucket epoch；跨 bucket 失败保留可重试进度，不得只提交一半即确认 outbox。
保持现有顺序契约，并覆盖重排、停用、跨组迁移、退休/重开；有界后台全量对账作为修复手段，不能让每条事件重新触发全量查询。

## 迁移与回滚门槛（尚未实现）

- 先部署兼容读写二进制，revision 回填与新 namespace 对账完成后才启用新 writer。
- 混合旧 writer 时继续使用当前账号锁和旧路径；不能只因新二进制已部分部署就锁外发布。
- 启用新路径前，两个独立 Redis 都必须完成新 namespace 初始化并验证 tombstone/revision。
- 回退旧二进制前先停止新路径、排空在途发布并重建旧 namespace；不能把“二进制可回退”写成缓存格式天然兼容。
- 迁移 PR 应单独说明 DDL 对旧代码的兼容性及 tombstone 保留期；本次观测 PR 不含迁移、配置或缓存格式变化。

## #711 关闭验收

以下均为待完成条件，不能用日志单测、退休分组批次优化或 #712 的历史 GREEN 替代：

- [ ] 明确单账号状态修订顺序、临时请求反馈、usage 逐字段版本和删除 tombstone 的契约。
- [ ] Redis 原子 CAS 对旧 revision、同版本冲突、硬删除、Redis 重启、TTL/清理和旧 writer 均 fail closed。
- [ ] 投影查询不携带凭据/完整 Extra；真实调度选择与详情 hydration 在所有平台一致。
- [ ] Redis 发布离开 PostgreSQL 账号临界区；失败保留旧快照，后台 retry 和请求 fallback 可验证。
- [ ] 大分组增量更新覆盖停用、删除、迁组、跨平台/mixed、退休/重开、乱序重放和部分写失败。
- [ ] PG18 + 双独立 Redis + 合成大分组 + 持续变更 + 受控数据库/Redis 网络延迟下，测量锁 SQL 次数、真实 advisory wait、临界区、重建时间、outbox/retry 水位及请求结果。
- [ ] 与同构旧版对照，CI 保存 exact SHA、数据规模、延迟注入值、窗口、分母和原始脱敏指标，不能只给一张绿色截图。
- [ ] 生产灰度经单独授权后记录版本、窗口、流量与故障恢复；不能将本地或 CI 合成结果写成生产 503 已消失。
