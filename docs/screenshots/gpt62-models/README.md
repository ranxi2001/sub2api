# GPT-6.2 模型选择器

页面入口：管理员账号管理 → 编辑 OpenAI 账号 → 模型白名单。

截图使用实际 `ModelWhitelistSelector.vue`、实际语言包与 Tailwind 样式；仅外层预览容器为本地 harness。未连接后台或生产，未使用真实账号或凭据。

- `before.png`：生产基线 `e3f238e3f` 的模型数据，搜索 `gpt-6.2` 没有匹配项。
- `after.png`：源码 `e76af8e73dbdda6a87adf0bbf4de70cbdea374bc`，同样搜索显示 Astra、Astra Pro、Sol 三个独立型号。
- `selected.png`：通过实际组件逐项点击选择三个型号，组件更新事件返回三个原始模型 ID。

浏览器无页面错误。以上验证只覆盖实际组件的显示、搜索、选择及更新事件；不证明整页导航、后台保存、真实供应商能力或生产部署。截图来源与结果见 `manifest.json`。
