# API Key 多分组与上游渠道声明

开发基线：`ranxi2001/sub2api` 的 `production`，`ae964909e83d83ae068658a413b84e5961df7b13`（2.12.3）。

用户创建或编辑 Key 时，可在原分组之外选择其他有权使用的分组，最多 20 个。旧 Key 和旧客户端的 `group_id` 继续有效；新客户端用 `group_ids` 提交完整集合。两者同时出现时，`group_id` 必须与数组首项一致。更新时省略 `group_ids` 表示不修改集合；显式提交空数组且不传 `group_id` 表示解绑；单独更新 `group_id` 只修改主分组并保留原有额外绑定；要主动替换完整集合，须显式提交 `group_ids`。

每个新请求先排除停用、无权限、订阅额度不足、模型受限或无可调度账号的分组。在有空闲容量的分组中，优先选择用户专属倍率覆盖后的低倍率分组；token 请求计入高峰倍率，图片和视频使用各自独立倍率。同倍率优先低负载，完全相同则随机分配。所有分组都繁忙时，仍可交给所选分组原有的排队机制。

此处比较的是分组的有效倍率。最终费用仍按所选分组的模型/渠道定价和实际使用量计算，账号附加倍率、模型价格差异等沿用现有计费路径。可用性是请求开始时的快照，最终并发抢占、上游重试和流式行为由现有网关处理。正在执行的请求或已建立的 WebSocket 不因另一分组变便宜而中途改变计费归属；排队和 WS 重新鉴权会核实所选分组仍绑定且有权限。

此版本的自动选择用于普通主站网关；使用区域本地鉴权快照的网关会对多分组请求返回明确的不可用错误，避免绕过其权限与资金边界。生产启用前须评估实际网关部署方式。

## 站点多分组开关 / Site policy

管理员在「系统设置 → 功能开关 → API Key 多分组」设置 `api_key_multi_group_enabled`。默认开启；旧数据库缺失或空值继续开启。只有管理员可保存，普通 public/SSR 设置只公开此 bool，不返回管理员凭据或其他私有设置。

关闭时，用户创建与编辑页隐藏「同时使用其他分组」，只提交主 `group_id`。服务端对显式多个 `group_ids`（包括重复项）或与主组不一致的单项返回 HTTP 400 `API_KEY_MULTI_GROUP_DISABLED`。旧客户端省略 `group_ids` 继续可用：改名、配额等不改集合，单独 `group_id` 仅改主组并保留原有额外绑定；不会把旧主组追加成新绑定。显式单项 `group_ids` 则主动替换集合；显式空数组且省略 `group_id` 才是主动解绑。管理员 Key 编辑支持单个主组（包含一致单项 `group_ids`），也拒绝多项，不增加任何新授权规则。

网关在缓存外读取当前策略。关闭后，新请求只使用数据库 `api_keys.group_id`，额外绑定不参与倍率、容量、排队、模型目录并集或故障转移。请求副本同时屏蔽跨组 fallback，主组内账号重试仍沿用原机制；主组缺失、删除、停用或撤权保持拒绝，不从额外数组补主组。暖 L1/L2 保留完整绑定，开关往返无需清库；配额和历史用量不变。已准入请求不切换计费归属，排队与 WS turn 重新鉴权会拒绝关闭前选中的额外组，要求重新请求。

区域鉴权把这一白名单 bool 放入同一 repeatable-read 事务导出的签名认证快照；区域读取不回源主站数据库。旧快照缺失该字段视为开启，多绑仍由区域原有多组拒绝保护；关闭快照只提供合法主组。更新按既有发布周期传播，过期/断连超出有效期 failclosed。旧二进制严格拒绝含未知字段的新快照，因此区域 reader 应与 publisher 一起升级；现有区域模型服务 503 安全门仍然生效，此开关不会开启区域付费生成。

The administrator switch defaults to enabled, including missing legacy values. Disabled sites reject explicit multi-group submissions with HTTP 400, hide additional-group controls, and authenticate requests against the real primary group only. Omitting `group_ids` preserves existing extra links; explicitly submitting the collection replaces it. No migration or usage rewrite is required. Re-enabling resumes preserved bindings. Auth caches retain configured links while request-local policy views restrict routing; signed regional snapshots carry the same policy and retain their existing expiry and serving gates.

## 查看上游多个分组的倍率

账号管理的「分组」列是本地账号加入的分组。「上游渠道声明」列的数据来自该账号所用上游 Key 的 `/v1/sub2api/billing`，两者并非同一份分组配置。

单分组 Key 继续返回 schema v1。多分组 Key 返回 schema v2：

```json
{
  "object": "sub2api.key_billing",
  "schema_version": 2,
  "billing_scope": "token",
  "billing_mode": "multi_group",
  "groups": [
    {
      "group_id": 1,
      "group_name": "Economy",
      "platform": "openai",
      "status": "active",
      "object": "sub2api.key_billing",
      "schema_version": 1,
      "billing_scope": "token",
      "group_rate_multiplier": 0.18,
      "resolved_rate_multiplier": 0.18,
      "peak_rate_enabled": false,
      "effective_rate_multiplier": 0.18,
      "observed_at": "2026-10-20T00:00:00Z"
    }
  ],
  "min_rate_multiplier": 0.18,
  "max_rate_multiplier": 0.18,
  "observed_at": "2026-10-20T00:00:00Z"
}
```

每个分组沿用 v1 的倍率字段，含可选用户专属倍率和高峰时间/时区。只返回 Key 绑定且用户仍有权使用的分组；停用状态保留显示。顶部范围反映声明中各分组当前 token 倍率，并不承诺某个具体模型或下一次请求的价格。

v2 顶部没有 `resolved_rate_multiplier` 或 `effective_rate_multiplier` 单值，以免旧下游把任意分组的价格同步成固定成本。探测端逐组校验、仅保存白名单字段，展示倍率范围和可展开的分组名称、倍率明细；每组按自己的时区刷新当前高峰倍率。过期或失败标记继续保留，旧明细可供参考。

识别到多分组声明后，停止自动回写单一账号倍率和成本倍率，保留管理员原有设置。保存的自动同步偏好不被改写；以后上游恢复单分组声明时可恢复原同步行为。管理员需要手动编辑倍率时，可关闭原有的同步开关。

上游须实现该多分组声明协议才能提供完整清单。仅升级消费端无法从旧的单分组声明推断其他分组名称和倍率；未支持、失败和过期状态也不会被本地账号分组拼成上游结果。

## 迁移与验证

迁移 `291_api_key_groups.sql` 新增额外分组关联表，保留 `api_keys.group_id` 作为旧客户端绑定。Key 创建/编辑与关联更新使用同一事务；列表筛选、缓存失效和管理员分组迁移覆盖额外绑定。未修改旧迁移、既有用量数据或版本号。

新增后端用例覆盖倍率和容量选择、权限撤销、请求快照隔离、绑定事务回滚、多分组声明脱敏及禁止单值同步；前端覆盖倍率范围、各组明细与过期显示，并将上游声明组件用例加入关键 CI 集合。按仓库约定未在本地运行测试、类型检查、lint 或构建，待 CI 验证。
