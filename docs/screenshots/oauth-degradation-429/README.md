# 降智处置条件预览

CI [43012171595](https://github.com/ranxi2001/sub2api/actions/runs/43012171595) 渲染真实 `OAuthDegradationCard.vue` 组件和项目 CSS，使用虚构配置，没有调用生产 API。截图源码为 `9f0580844f86234223cfa114bca60a54e5dab2c7`，对应入口为自动配置页的“降智后处置”。

- `after.png`：两个条件默认勾选，探针阈值示例 9%，HTTP 阈值 80% / 80%。
- `429-only.png`：取消探针条件，只保留 429 条件。
- `before.png`：旧组件结构（90549fe89）；说明文字使用当前语言资源，不作为旧版文案对照。
- `source.json`：源码与渲染范围。

此证据仅覆盖组件渲染与条件切换，不代表整页导航、真实账号处置或生产验收。
