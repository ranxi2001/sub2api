# WS 后续回合的上游拒绝

API-key `passthrough` 的上游 `error` / `response.failed` 事件及未完成回合的关闭帧，由实际 socket 读取路径标记为 `upstream_transport`，归属 `provider`。这里表示错误返回位置，不证明供应站内部根因。

Ops 保留业务状态、回合、`wire_status=101`、`phase=upstream_event/read_upstream`。关闭帧另保留 `upstream_close_code`；已交付的错误事件优先，同回合后续关闭不重复记错。`upstream_status_code` 为空，因为 WS 业务拒绝不是 HTTP 409 响应。当前回合执行状态未知，不能把缺少发送证据记成 `request_written=false`。

仅从关闭原因识别固定 `encrypted_agent_recovery_owner_unavailable` 代码，记录为业务 409；其他关闭记为 502。不存储原始关闭原因，避免将供应站的内部诊断或任务内容复制到 Ops。已有事件错误保留其业务字段。

上述错误不授权换号、重发首轮或第二轮。409 省略历史 Beta 的发送前恢复边界不变。合成测试覆盖第二轮工具结果与响应锚点保留、正常完成、事件加关闭、仅关闭，以及未知关闭原因脱敏；不代表已经复现或修复第三方供应站的 owner 状态丢失。

关联：[#905](https://github.com/ranxi2001/sub2api/issues/905)。线上原因仍需获准的合成多轮任务与供应站直连对照；勿重放客户任务或将其他会话成功当作原任务已恢复。
