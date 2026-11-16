# OAuth 首次配置后的 API Key 处置

页面入口：智能运维 → 自动配置。截图来自 CI 对实际 `AutoConfigView.vue` 和项目样式的渲染，使用虚构账号、分组及 API 响应；布局、账号选择器与表单保存为实际组件。认证、导航及与本功能无关的部分子组件使用 stub，不表示生产或真实上游已验收。

- 渲染源码：`70600fa0587a0f4ffaca75c7fe0c460493758b78`。
- 对比基线：`ff1082fd51038f10dcb3a8ecb1d5a738258b0f66`。
- CI：https://github.com/ranxi2001/sub2api/actions/runs/43813431347 。
- `before-layout.png` / `after-layout.png`：首次配置右侧拆出新面板。
- `after-manual-pause.png`：选择虚构 API Key，延迟 5 分钟、暂停 180 分钟。
- `after-group-disable.png`：分组全部 API Key，持续关闭调度。
- `after-english.png` / `after-mobile-dark.png`：英文与 390 px 深色布局。

该渲染之后的空数组兼容修复不改变这些截图中的界面。实际保存交互由截图脚本检查；`manifest.json` 记录渲染来源、边界和浏览器错误情况。
