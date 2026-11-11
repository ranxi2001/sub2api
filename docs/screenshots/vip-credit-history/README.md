# VIP 自动垫付充值记录截图

PR #799 的 CI 组件预览，来源提交 `194b6b022447bc4c222eef38d3f90971c5f771e1`。
前后对比使用真实 `UserBalanceHistoryModal.vue` 与项目 CSS，接口为虚构数据。
修复前组件来自 `87d01c39ee4cda151534812b172e7acdf959a50e`。

- `zh-before.png` / `en-before.png`：旧列表缺少垫付记录。
- `zh-after.png` / `en-after.png`：完整列表显示管理员充值、待收款与已收款垫付，金额及前后余额统一两位小数。
- `zh-filtered.png` / `en-filtered.png`：筛选 VIP 自动垫付。
- `evidence.json`：CI 记录的来源信息。

页面入口为管理员用户列表的充值记录弹窗。本预览只验证组件、CSS 与模拟筛选交互，不代表生产环境验收。

CI: https://github.com/ranxi2001/sub2api/actions/runs/43041852502
