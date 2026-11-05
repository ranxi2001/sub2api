# Scheduler outbox 失败补偿

单条事件处理失败时，writer 先将 PostgreSQL 中的原始事件完整复制到 `scheduler_outbox_retries`，复制提交成功后才允许主 outbox 的 watermark 越过该事件。复制失败时仍保留原始事件，只推进此前成功的连续前缀，并按 5 秒至 5 分钟指数退避。不会以全量重建返回成功作为丢弃失败事件的依据。

补偿 worker 与主 outbox worker 独立运行，每轮最多领取一条到期事件。领取使用 PostgreSQL `FOR UPDATE SKIP LOCKED` 和 3 分钟租约，handler 沿用 2 分钟预算；完成和延后重试均要求租约仍有效且 generation 一致。generation 来自全局序列，任务删除后同一事件重新入队也不会复用旧 owner。进程退出、连接中断或完成确认失败后，任务保留，租约过期可由其他 writer 重新领取。失败任务不设置丢弃次数上限，重试间隔最大 5 分钟。

事件可以重复执行、晚于后续事件执行。补偿发布在现有共享发布锁内读取完整的最新账号状态，并按最新调度资格、平台和分组成员关系过滤旧查询候选；普通请求反馈保留原合并语义。补偿不复用同批查询或成员 ID；主 outbox 与补偿遇到 bucket busy/fenced 时均保留事件，避免旧重建持锁期间的新成员事件被当成完成。已退休的分组须在现有生命周期租约内重新确认当前权威状态，不能按旧事件直接重开。`last_used` 的 Redis Lua 只接受较大的时间戳。持久化唯一键为原事件 ID，重复复制不覆盖事件内容、重试次数或租约。原 outbox 的正常清理不会删除补偿副本。

该机制恢复事件消费进度，不会使持续不可用的数据库、Redis 或永久超时的大分组立即恢复。失败补偿可能长期存在；不能只根据主 outbox watermark 已追平认定所有账号变更已完成传播。日志使用事件 ID、类型、重试次数和退避时间，不打印 payload。主要日志包括 `outbox event deferred durably`、`outbox retry handle failed`、`outbox retry recovered`；出现 `outbox retry persistence failed` 时，主队列保持原有安全阻塞语义。

只读统计示例（在目标数据库中执行，记录主机、统计截止时间和时区；不查询 payload）：

```sql
BEGIN READ ONLY;
SET LOCAL statement_timeout = '3s';
SET LOCAL work_mem = '4MB';
SELECT NOW() AS observed_at,
       COUNT(*) AS pending,
       COUNT(*) FILTER (WHERE next_retry_at <= NOW()
                         AND (lease_until IS NULL OR lease_until <= NOW())) AS due,
       MIN(created_at) AS oldest_event_at,
       MAX(attempts) AS max_attempts
FROM scheduler_outbox_retries;
COMMIT;
```

## 升级与回滚

迁移 `334_scheduler_outbox_retries.sql` 只新增队列和到期索引，不改账号、分组、计费或旧事件。只读 gateway 不运行主队列或补偿 worker；替代仓储未实现持久补偿接口时保留成功前缀推进和有界退避，不跳过失败事件。

旧二进制不消费补偿表。因此回滚前必须先在没有新增失败的窗口让新 writer 排空补偿表，并核对 `pending=0`；不能只检查主 watermark。若故障导致补偿无法排空，应保留支持该队列的修复版本，或先停止全部 writer、备份数据库，将每条补偿的 `event_type/account_id/group_id/payload` 作为新的 outbox 事件在同一数据库事务中重新入队，确认插入条数一致后才删除对应补偿。重新入队使用新序列 ID 和当前创建时间；不得沿用低于已提交 watermark 的旧 ID。完成恢复前不得直接删表或清空补偿。

本说明描述源码行为与回滚条件；不代表已完成生产故障注入或上线验收。
