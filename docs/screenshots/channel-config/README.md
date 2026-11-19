# 渠道配置截图

2026-11-19，通过 Windows Chrome CDP 渲染实际 Vue 组件。数据来自 `.github/scripts/upstream-channel-entities-screenshot.mjs` 的合成 Axios adapter，未访问生产或真实供应商。AppLayout 使用简单预览容器，因此截图不证明生产侧栏、管理员认证或后端接口已验收。

- `before-profit.png`：生产基线 `5a51997112d0f93c8ec06747748680a9d7e8e4a7` 的原利润页，旧智能运维导航中三个入口独立。
- `management.png`：新统一入口默认展开渠道管理，利润和账号运维尚未挂载。
- `profit.png`：展开利润面板，保留原汇总、时间筛选和渠道/账号/模型下钻。
- `accounts.png`：再展开账号面板，展示账号规则和通知渠道；空账号列表是合成数据场景。
- `mobile-dark.png`：390px 宽度、深色主题，保留账号面板；页面 `scrollWidth=390`，表格在自身容器横向滚动。

浏览器回读：默认进入利润 API 调用 0 次，展开利润后 1 次，展开账号后仍为 1 次；利润汇总/表格、账号规则 DOM 均存在，最后回读无页面 `role=alert`。这仅验证面板加载互不触发利润请求；数据库缓存正确性和耗时由 PostgreSQL CI 用例验证。

截图源代码是本目录提交中的实现，截图未后期合成或贴字。原利润页使用单独基线 worktree 和相同合成数据渲染。
