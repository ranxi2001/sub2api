# 多 TOTP 验证器绑定与恢复

Refs #881。真实授权、登录和 2FA 验收 **NOT EXECUTED**，需要另行明确授权。
本地托管 Worker 的修复不能证明外部 Session Studio 已修复。

## 登录和新增验证器

托管 runtime 在构建时对 `poxiao33/toSub2` 固定提交
`8548397e89bf80e508eda64a87e0d556d43abc84` 应用本仓库的
`tools/reauth-runtime/totp-factor-binding.patch`。应用前校验原文件 SHA256，
使用 zero-fuzz patch；源码漂移会中止构建。未修改或发布上游仓库。

保存了 factor ID 的登录只挑战该 ID，ID 不在返回列表中时立即停止。
旧记录没有绑定时，最多匹配两个不同 TOTP factor，各挑战一次；超过两个时
要求先取得明确绑定。只有 HTTP 400/401 的结构化 `error` / `error.code` 精确命中允许的 OTP
拒绝码才允许尝试下一个 factor；显式非 OTP code 优先于描述文字。只有 message、
重复字段或异常文本中的 OTP 字样均不构成重试依据。
429、网络错误、非 OTP 错误、含糊响应或继续要求 MFA 的 HTTP 200 都停止。
不会靠重复 OTP、无限挑战或删除验证器恢复。factor ID 当前接受 1–256 位
ASCII 字母、数字、下划线和连字符，未知格式保持未决，不作猜测。

新增时，secret、明确返回的 `factor_id` 和 enrollment `session_id` 同时写入
加密 journal 和数据库加密记录，收到持久化确认后才 activate。`session_id`
不是 factor ID；没有返回明确 ID 时，先保留 enrollment 来源，随后由有限登录
匹配取得实际接受的 factor，再原子写入绑定。activation 消耗 OTP 后等待一个
新的时间窗口进行一次验证。绑定写入与恢复使用 ciphertext CAS，不允许替换
另一份密钥或已知 factor。journal 也拒绝覆盖或清除已有来源信息。

成功表示“所选 factor 已验证并保存”，`outcome=selected_factor_verified_no_revocation`。
它不表示旧验证器已撤销；流程没有删除 factor 的请求。两个 factor 都有效时，
不再要求另一个密钥失败。缺少绑定证据时返回 `factor_binding_unconfirmed`；
网络或响应不确定仍保留 `uncertain` 与候选记录。旧检查点的
`new_accepted_old_valid` / `old_accepted_new_valid` 仍可读取，但新流程不再以
这两个结果阻止已验证的新绑定。

## 未决任务与删除后恢复

已有账号仍使用原来的状态、验证和恢复导出接口。重试只接受 `verify_new`
或 `verify_old`，不会重复 enroll。普通重登与 Session 导出均从同一加密记录
解出 secret + factor ID，并把绑定传给本地协议。

旧账号软删除后，管理员可按保留的 task ID 访问快照；观察员禁止访问，响应
使用 `Cache-Control: no-store`。以下是接口契约，不是已执行的生产恢复步骤：

- `POST /api/v1/admin/accounts/totp-recovery-export`：请求
  `task_id`、`expected_email`、`source`（`candidate` / `previous`），可选
  `include_password`。只允许未决任务及匹配的快照邮箱，返回 secret、factor ID
  和恢复状态；默认不返回密码。
- `POST /api/v1/admin/accounts/:id/totp-recovery-import`：请求同样的 task、邮箱和
  source。目标必须是邮箱匹配且尚未保存登录配置的 OpenAI OAuth 账号。
  服务端通过专用 `INSERT ... ON CONFLICT DO NOTHING` 写入完整加密配置；
  已存在或并发先写入的配置返回 409，绝不覆盖赢家。
- 导入选择 `local_worker` 并明确返回 `verified: false`。它准备本地登录配置，
  不执行真实登录，也不改变原任务状态。新账号的普通重登使用恢复绑定。
  原身份仍有未决任务时禁止再次 enroll；导入本身不能清除这个门槛。

恢复导入需要一个通过受支持 OAuth/Session 导入路径建立的目标账号。
如果外部首次登录仍失败，可在另行授权下，使用管理员掌握的有效恢复凭据和
官方登录流程取得 OAuth/Session，再通过已有导入入口建立目标账号，随后使用
上述本地恢复配置接口。不能声称发送给外部 Session Studio 的 `mfa_secret`
携带本地 factor 绑定，或声称外部提供者具备同样的有限匹配能力。

硬删除仍受现有 FK cascade 影响，不承诺数据库任务恢复。journal、journal key
与应用加密 key 都须保留；缺失 key 时不生成新 key 覆盖旧记录。迁移为历史任务
从仍保留的配置补齐邮箱；没有身份快照的历史任务拒绝恢复，不猜测归属。

## 数据兼容与回滚

migration 382 只新增 `binding_verified` 和快照 `login_email`，使用
`IF NOT EXISTS`，重复执行不覆盖已有身份快照。新代码同时读取旧 secret
ciphertext 和带版本的加密绑定记录；所有普通 claim、Session 导出和 TOTP
导出消费者均已适配。新任务每次 claim 清除确认标记，未收到本次绑定确认不能
成功提交；完成时在同一事务内更新登录配置。

**不能只回滚旧 server/worker 二进制**：旧代码不识别加密记录中的版本化
JSON，也不支持绑定确认协议。需要保留支持该格式的服务端/runtime，或另行
审核数据兼容转换；不要清空候选、覆盖 journal 或恢复丢失新凭据的旧数据库
快照来绕过不确定状态。旧 worker 配合新服务端不能完成未确认绑定的任务。

## 验收

本地已运行实际 pinned 模块、完整 CLI、真实 worker HTTP 客户端与加密 journal
的离线 RED/GREEN；上游响应为合成 mock。真实 Go handler/service/repository、
PostgreSQL migration/reload/CAS、完整 API 和 production build 由 PR 的远程 CI
执行。具体 SHA、原始日志及未闭门见 issue881 artifacts handoff；CI 通过也不能
替代尚未授权、尚未执行的真实登录/2FA 与外部 Session Studio 验收。
