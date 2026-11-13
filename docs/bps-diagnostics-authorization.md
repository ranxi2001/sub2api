# BPS 工具参数诊断与授权结果确认

模型生成的工具参数不符合声明 schema 时，BPS 失败终态增加 `tool_arguments`，包含工具名、参数路径、固定规则和原因。参数值、动态对象键、schema 内容及引用 URL 不进入诊断。schema 编译错误仍使用原诊断；合法性判断、工具批次原子性、计费和重试策略不变。

授权连接页面提交回调后清除回调和登录链接并禁止重复提交。网络失败后自动查询一次本次授权结果，也可点击“检查授权状态”。只有成功回包或与本次 session 精确对应的保存标记能显示“凭据已保存”；已有 active 状态、revision 变化或旧授权不作为成功证明。模型目录可用性需另行查看。

`POST /api/v1/admin/accounts/:id/auth-connections/:connection_id/authorization-status` 使用既有管理员权限，body 只接收 `session_id`，返回 `waiting`、`saved` 或 `unconfirmed`。会话标识不进入 URL，查询与 callback 请求体均从管理审计正文中排除。查询不刷新或交换令牌、不修改账号。

保存标记是 session 的带用途前缀 SHA-256 和保存时间，与凭据一起加密并在同一次 CAS 写入。确认期限沿用 OAuth SessionTTL，服务重启不丢失已保存结果；旧凭据、过期标记、已消费但未保存、状态存储不可用均不推断成功。没有数据库迁移；旧二进制可忽略新增加密凭据字段。页面关闭或切换账号清除本地授权材料，刷新页面后需重新开始授权。

参考用户提供的 CPA/BPS 0.5.37 包（ZIP SHA-256 `33babaf75556b04d8c09c707e462a282e4834f8b35feab335e1e7322facd6ca0`）的授权页行为和工具错误结构，在 Sub2API 原架构中独立实现。包不含 Go 业务源码，本改动未加载、嵌入或复制其二进制实现。

回归由 `.github/workflows/bps-diagnostics-auth.yml` 执行：工具校验和流式终态、授权回执与重复 callback、实际 API 客户端组件交互，以及虚构数据的前后页面截图。该验证不代表真实 OAuth 或上游模型验收，也不包含服务端搜索。
