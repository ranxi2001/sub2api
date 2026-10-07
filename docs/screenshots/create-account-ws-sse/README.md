# 新增账号的 HTTP 流式 WS 加速开关

页面入口：管理后台 → 账号 → 添加账号 → OpenAI → OAuth 或 2FA，向下滚动到 WS mode 后的加速开关。

这些截图来自实际 `CreateAccountModal.vue`、子组件、中文翻译及项目样式的本地 Chromium 渲染。API 和登录状态使用 mock，分组和代理列表为空，没有真实账号、凭据或生产请求；未验证完整后台导航、OAuth 授权或真实账号创建。

- 修改前组件：`1c28a9f5a1e9fbc00eb4f25c7f0e77a81c8e45fd`。
- 修改后组件：`133aff7d85121eaa9e70e471967924d0e86d46f2`。后续 lint 修复未改变组件源码。
- `entry.png`：OpenAI OAuth 新增入口。
- `before.png`：修改前的 WS 配置区，没有加速开关。
- `oauth-off.png`：新增开关默认关闭。
- `oauth-on.png`：点击后的启用状态；保存该值仍需 WS 模式和全局开关允许，截图不代表上游加速已生效。
- `two-fa-on.png`：切换为 2FA 后保留加速选项。
- `dark.png`、`mobile.png`：深色及 390px 窄屏下的配置区。窄屏中既有 WS mode 选择器横向裁切，不属于本次新增开关的修复范围。

浏览器断言检查了默认 `aria-checked=false`、点击后为 `true`、2FA 状态可展示，并阻止外部请求。未发生页面异常或 HTTP 错误。组件测试另行覆盖创建请求参数、切换账号类型与表单重置。

截图来源与 mock 边界详见 `manifest.json`。
