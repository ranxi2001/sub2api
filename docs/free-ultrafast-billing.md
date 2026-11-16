# 免费 UltraFast

在 OpenAI 或 Composite 分组的新建／编辑弹窗中，可独立开启 **免费 UltraFast**。默认关闭。

开启后，已结算为 `service_tier=ultrafast` 的 OpenAI 请求向客户按 Standard 单价计费，包含输入、输出、缓存读取和缓存写入。用户专属倍率、分组倍率、模型附加倍率、长上下文及其他原有定价规则继续适用；“免费”仅免除 UltraFast 档位溢价，并非请求免单。

- 管理 API 字段：`free_openai_ultrafast`。创建时省略为 false；更新时省略不修改，显式 false 关闭。
- 此开关不设置或改写上游 `service_tier`，不改变路由、重试或供应商收费。
- `free_openai_fast` 仍只覆盖 Priority/Fast，两项开关互不替代。两项都开时分别按各自档位生效。
- 上游响应档位能否提升结算档位，继续由账号 `openai_response_tier_billing_enabled` 决定。此开关不隐式开启账号响应计费。
- 用量记录保留 `service_tier=ultrafast` 和原档位 `total_cost`；余额与 Key 配额扣除 Standard 重新计算的 `actual_cost`。历史 `customer_cost_details.free_ultrafast=true` 保存客户价格明细，用户与管理员用量详情使用该请求快照。
- 已有历史记录不重新计费。没有客户价格快照的旧折扣记录显示未知客户明细，不从六倍总价反推。

迁移 `381_group_free_openai_ultrafast.sql` 新增默认 false 的非空布尔列。旧二进制可以忽略该列，但回退会停止兑现免费 UltraFast；回滚前先关闭该开关并处理仍在执行的请求。不要在滚动升级尚有旧进程时启用新开关。

新功能提示 ID：`group-free-ultrafast-initial-announcement`。新建与编辑入口共用 ID，任一入口关闭后，在同一浏览器当前管理员的两个入口一同隐藏；其他功能提示不受影响。

验证由 CI 执行：独立计费开关、响应档位边界、个人倍率、缓存与长上下文、DTO／鉴权缓存、数据库读写、表单保存与提示关闭。UI 截图来自实际 GroupsView 的合成 API 场景，保存在 PR 对应的 `free-ultrafast-ui-<head>` Actions 附件；不代表生产推理验收。
