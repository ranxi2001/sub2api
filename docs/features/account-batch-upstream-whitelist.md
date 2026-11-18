# 批量同步账号上游模型白名单

管理员在账号列表显式勾选账号，再打开“批量编辑”，可在模型白名单区域点击“批量同步上游模型”。跨页勾选的 ID 会一起冻结在弹窗打开时的选择中；后续列表刷新不会增删本轮账号。没有勾选账号的筛选结果预览不提供此操作。

该操作使用每个账号已保存的连接和凭据，立即保存各自的上游模型白名单。其他表单修改仍需单独提交。现有“同步最新支持模型”继续填充共同的手工编辑列表。启动上游同步会取消勾选共同模型限制，防止随后提交表单覆盖各账号刚同步的结果。

每个账号显示独立结果。最多同时提交两个账号；某个账号失败不阻止其他账号。成功时移除失效模型、保留仍指向有效上游模型的别名；空目录、未验证目录和受保护的账号策略保留旧白名单。失败账号可以单独重试。连接异常时可能无法确认服务端是否已经保存，重试前应核对账号。

“停止排队”及关闭弹窗会停止尚未启动的账号。已经提交的请求仍可能完成并保存，界面不会声称将其撤销。上一轮已提交请求结束前，新打开的弹窗不能开始另一轮同步。

## API 与安全合同

复用 POST /api/v1/admin/accounts/:id/models/sync-upstream，显式传入：

```json
{"apply": true, "whitelist_only": true}
```

whitelist_only 要求 apply=true 且 automatic=false，省略该字段时保留原同步行为。请求经过管理员认证；observer 不得 apply。每个请求只处理 URL 中的正整数账号 ID。

白名单模式复用上游目录读取、代理、凭据、模型 ID 归一化、订阅隐藏模型策略及现有 model_mapping 条件更新。它不写能力元数据、目录观测、质量状态或自动同步冷却；只有 model_mapping、账号更新时间和既有调度失效通知会变化。发现期间账号凭据/代理/状态有变化时不覆盖并发编辑。

Vertex 复用现有有上限的分页读取；其他目录如果仍带未消费的分页标记，就保留旧白名单并报告未验证目录。混合 BPS/native 账号要求相关目录完整成功，避免部分来源失败导致错误删除。没有 generation、付费探针或自动业务重试。

无数据库迁移。旧后端不认识 whitelist_only，因此必须配套部署本 PR 的前后端。回滚代码不恢复已经保存的白名单。

## 验证边界

- BatchUpstreamWhitelist.spec.ts 挂载真实弹窗，使用 Axios adapter 的虚构响应，覆盖冻结选择、双并发、失败重试、停止、关闭与重入、普通表单互斥以及提示持久化。
- AccountsView.bulkEdit.spec.ts 覆盖列表跨页勾选和筛选预览边界。
- account_handler_batch_whitelist_test.go 使用真实 Gin handler/service 和管理员认证；存储/上游边界使用虚构实现。
- upstream_batch_whitelist_test.go 覆盖目录归一化、别名、隐藏策略、分页、部分 BPS 来源失败和并发编辑。
- account_handler_batch_whitelist_integration_test.go 在临时 PostgreSQL 上运行真实 handler → service → repository 写入，再通过 GET 逐字段核对两个不同成功目录、失败账号和未选中账号。
- UI 取证脚本通过 Vite 渲染原组件与真实 CSS，浏览器禁用外部网络。截图和带 SHA/哈希的 manifest 作为 PR 附件与 CI artifacts 保存，不进入源码。

管理员新功能提示 ID 为 account-batch-upstream-whitelist-v1，仅展示于上述批量操作入口。关闭记录遵循当前浏览器、当前管理员的 localStorage 边界。
