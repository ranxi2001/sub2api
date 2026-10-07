# 渠道状态 V3 布局调整

基线为 owner fork `production` 的 `5ca3cca21eeaf4ca8a694a7f2f8f0ecd9575c549`（2.10.0）。截图于 2026-10-08 在本地 Vite + Chrome CDP 环境生成，使用实际 `ChannelStatusV3View`、`AppLayout` 和 V3 组件。演示入口通过 Axios adapter 返回内存中的模拟数据：GPT 5 个组件、Claude 3 个组件、60 格状态、无置顶组件。名称参考反馈场景，用户、余额、倍率、状态与可用率均为演示值。没有连接生产 API、数据库或模型服务。

## 调整

- 移除用户页 1680px 最大宽度，沿用 AppLayout 页面边距，与鹈鹕测智一致；内容和页脚相对侧栏右侧的主区域对齐。
- 组件文字从 12px 提升为 14px，桌面状态条从 10px 提升为 20px，手机为 16px；放大刷新、历史导航和事件按钮。
- 分类增加浅色背景、边框和内边距，增加组件之间的留白。1280px 起双列，较窄屏幕单列；奇数个分类的最后一项仍占满整行。
- 手机上的名称和可用率分行，长名称可以换行，图例在手机上也可见。

## 截图

| 场景 | 修改前 | 修改后 |
| --- | --- | --- |
| 2560 × 1440 宽屏 | [before-wide.png](before-wide.png) | [after-wide.png](after-wide.png) |
| 390 × 844 手机，整页 | [before-mobile.png](before-mobile.png) | [after-mobile.png](after-mobile.png) |
| 1440 × 1000 桌面 | — | [after-desktop.png](after-desktop.png) |
| 1024 × 1000，侧栏展开，整页 | — | [after-tablet.png](after-tablet.png) |
| 深色 | — | [after-dark.png](after-dark.png) |
| 英文 | — | [after-en.png](after-en.png) |
| 键盘聚焦异常状态格 | — | [after-tooltip.png](after-tooltip.png) |

## 验证

- 在 2560px 宽屏下，系统卡片宽度从 1680px 增至 2240px，高度从约 333px 增至约 549px（同一份模拟数据）。
- 在 320、390、1024、1440、2560px 宽度，以及 1440px 侧栏收起时，`scrollWidth` 均等于视口宽度；V3 内容中心与 `main` 中心偏差为 0px。原页面同样水平居中，主要问题是额外限宽和内容密度，改动不做偏移补偿。
- 检查中文、英文、浅色、深色，分类折叠正常；聚焦红色状态格后浮层可见且位于视口内。
- `make test-frontend`：lint、类型检查、62 个关键测试文件 / 1003 个测试通过。
- `pnpm --dir frontend run build`：通过；有 Browserslist 数据陈旧及部分 chunk 超过 500kB 的提示。
- `git diff --check`：通过。

这是本地模拟数据下的布局验收，不代表生产部署或真实 API 集成验收。
