# 仪表盘账号并发预览

入口：管理员仪表盘 `/admin/dashboard` 的“性能指标”。RPM 保持单独一行，底部显示“128 账号并发 / 3.06M TPM”。数字均为虚构数据。

截图由实际 `DashboardView.vue`、项目 Tailwind 样式、Pinia 和 `NewFeatureBadge` 渲染；外层布局、认证和 API 使用离线 fixture。截图只证明组件布局，不代表真实接口、生产环境或完整导航已验收。历史截图固定在原 UI 提交 `8724ef4a62e36332cc294bd33efbf62460ef3332`；按贡献规则移除当前树中的 PR 对比图片，保留历史链接，组件代码和样式未变。后续截图使用 PR 附件。

- [修改前](https://raw.githubusercontent.com/ranxi2001/sub2api/8724ef4a62e36332cc294bd33efbf62460ef3332/docs/screenshots/dashboard-account-concurrency/before-desktop.png)：production `1411d0fc6e1fca51d4b657c21837cd74c87bf84e` 的页面组件。
- [修改后局部](https://raw.githubusercontent.com/ranxi2001/sub2api/8724ef4a62e36332cc294bd33efbf62460ef3332/docs/screenshots/dashboard-account-concurrency/after-performance-inline.png)：账号并发与 TPM 同行。
- [修改后页面](https://raw.githubusercontent.com/ranxi2001/sub2api/8724ef4a62e36332cc294bd33efbf62460ef3332/docs/screenshots/dashboard-account-concurrency/after-desktop-inline.png)：1280 × 800 CSS 像素视口，设备比例 1.5。

本机预览（复用已有依赖时传 `--deps`）：

```bash
node .github/scripts/dashboard-account-concurrency-preview.mjs --deps /path/to/frontend
```

访问 `http://127.0.0.1:4194/`；`?baseline=1` 查看固定基线，`?theme=dark` 查看深色。通过 `browse-with-cdp` 控制的 Chrome 截图，未运行本地测试、类型检查或构建；相关检查交由本 PR 的 CI。
