# 智能运维：移除凭证守护与打票流程

移除「凭证守护」「打票流程」的菜单、页签、显示开关、页面和路由。旧 `/admin/token-guard`、`/admin/harvest-flow`、`/admin/astra-gateway` 不再提供页面。

旧凭证守护的后台巡检、自动修复、通知、手动重登、状态/事件仓储和管理接口全部移除，旧设置中的 `enabled` 不再启动任务。2FA 初次登录拆到独立 `OpenAIReloginService`，配置入口位于「凭证运营 → 重登服务」。Session Studio Worker 与首次登录共同读取这份配置。2FA 成功后只返回短期内存中的 token，不向旧全局配置写入明文密码/TOTP；账号导入后的加密登记仍由凭证运营完成。

打票流程的汇总接口、事件内存环与事件落库、手动/并行控制台、节点列表/重置接口、专属覆盖率统计移除。已集成到 OpenAI 网关的票据采集、验证、注入、失效处理、出口绑定与账号调度保留在网关模块；Mihomo、IP 管理、网关借票后端和凭证运营继续独立工作。移除界面不会自动修改账号上的票据、代理和现存网关设置。

## 接口与升级

- 删除 `/api/v1/admin/account-ops/token-guard/*`。
- 首次登录改用 `/api/v1/admin/account-ops/relogin/two-fa-login`（POST）及 `/:id`（GET/DELETE），要求凭证运营已启用加密；不再接受 `credential_target=guard`。
- 服务配置使用 `/api/v1/admin/account-ops/relogin/config`（GET/PUT），仅管理员可用，响应禁止缓存；配置写入不记录审计请求体。
- 删除账号接口中的 `codex-harvest-flow`、`codex-harvest-controls`、`codex-harvest-nodes`、`codex-harvest-nodes/reset`、`:id/codex-skip-harvest` 和 `:id/manual-harvest`。
- 无数据库迁移。已发布迁移、旧状态/事件表和旧设置保留以兼容历史数据库，不再由被删除功能读写。新重登配置不存在时，仅读取旧配置的地址和请求头；新配置读取失败会报错，不退回旧地址。
- 回滚旧版本会恢复旧功能，并可能按旧 `enabled` 配置启动巡检；新模块中保存的重登地址不会自动同步回旧配置。回滚前须核对旧开关和服务地址。

源码合并不代表已部署。生产升级应使用后续发布产物，升级前后不执行真实登录或收费模型请求作为自动验证。
