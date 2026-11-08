# 队列重登身份校验与诊断

关联 [Issue #730](https://github.com/ranxi2001/sub2api/issues/730)。适用于 Codex 队列的直接 credentials 回写和服务端绑定 callback 交换；浏览器普通账号更新是另一条路径，不能当作同条件对照。

## 身份来源

候选身份只取本次 OAuth token。账号 ID 优先使用 ID/access token `https://api.openai.com/auth.chatgpt_account_id`，用户 ID 优先使用 `chatgpt_user_id` / `user_id`；邮箱取顶层 `email` 或 profile claim。两个 token 都有明确字段时必须相同，任一冲突拒绝。缺少明确字段时保留历史 ID token 的 `sid` / `sub` 回退，无法据此匹配原账号时继续拒绝。stored credentials、worker 顶层 metadata 和登录邮箱配置不补齐候选身份。

这是在已有可信 worker 与服务端 token exchange 边界内解析 claim，不新增 JWT 签名验证，也不把字段匹配等同新鲜上游鉴权成功。所有原存储的 account/user/email 仍要求候选非空且相同；凭据快照 hash、worker ownership、任务终态及原子 CAS 不变。

静态确认的旧失败条件：ID token 没有明确 workspace，`sid` 是新会话标识，access token 带有原 workspace 时，旧路径只使用 `sid` 并拒绝。这是合成回归条件，尚未证明是线上 #730 每个失败样本的原因。

## 不含身份原文的诊断

失败任务和回调错误仅保存固定格式：

```text
reauth_identity_failed reason=account_id_missing account=100 user=111 email=111
```

每组三位依次代表 stored_present、present、matches；示例为虚构状态。API 的 `diagnostic` 同时提供 `stage=credentials_validation`、固定 reason 和 `identity.account/user/email` 的三个布尔值。reason 区分 account_id/user_id/email 的 missing 与 mismatch、token 间冲突、token 无效或凭据不完整。没有邮箱、真实身份 ID、token、密码、TOTP 或原始 payload。旧错误无法追溯还原这些信息。

已有凭证运营诊断标签允许展示这些固定 reason，未新增控制项或页面布局。unknown 和任意 provider 文本仍不会直接显示。失败记录沿原预算核算器按 task 计一次，不自动解锁耗尽预算或把写入成功视为恢复；必须是成功任务后的新鲜鉴权探测通过。

## workspace 与引擎

本地 Codex worker 继续通过 `OPENAI_CODEX_EXPECTED_WORKSPACE` 接收目标；Session Studio 在原 `action/auth_mode/email/password/mfa_secret` 请求上，仅有目标时新增 `expected_workspace`，无目标保留原请求。远端服务必须支持此可选字段；客户端无法证明远端实际选择了目标，最终仍由 token 身份守卫校验，不在失败时切引擎或重试登录。

这项远端协议扩充需要与实际 Session Studio 服务验收；远端可能拒绝未知字段或忽略提示，不能因客户端发送成功声称选中了正确 workspace。旧服务不支持时应先对齐服务合同，保留失败，不能关闭身份校验作为绕行方式。

## 验证与关闭条件

提交中的 CI 使用虚构 JWT、离线 worker 响应和临时 PostgreSQL，检查明确 access 身份优先、冲突拒绝、缺字段、脱敏、自动/手动共用路径、CAS 及预算。测试本地未运行，以当前 PR head 的 CI 日志为准。

Issue 关闭还需要实际故障链路证据：自然发生或另行授权的有界自动重登，关联其 task/attempt、有效引擎、白名单诊断、授权成功、原子凭据回写，以及随后新鲜鉴权通过。生产部署、真实登录和预算恢复独立授权。本变更本身不自动关闭 Issue，不声称历史失败已全部恢复。

无数据库迁移；诊断保存在现有 error_message。旧 worker 仍可提交 credentials；新 worker 的 Session Studio workspace 字段需远端配合。旧二进制不能提供新增诊断与双 token 解析，仍沿用原身份拒绝。重登运行包需随正式发布更新，不能只替换应用二进制后宣称 worker 协议已升级。
