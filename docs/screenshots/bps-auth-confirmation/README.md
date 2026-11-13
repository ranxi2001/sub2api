# 授权结果确认页面

CI 从 `afd362e7684bed025859c7fed2a30c710b99b860` 渲染实际 OpenAIAuthConnections.vue；旧页面固定到 `0b9cbfbabb2d0ff03adc7f3f5f65b9b4edaa029b`。使用虚构账号、mock API 和 synthetic callback，不代表真实 OAuth 已验证。

[CI 截图产物](https://github.com/ranxi2001/sub2api/actions/runs/43485282730/artifacts/14067392027)包含：

- before.png：原授权链接与回调入口。
- unconfirmed.png：模拟提交回包丢失，隐藏回调输入并允许查询状态。
- saved.png：查询确认本次保存后，清除授权材料并提示模型可用性需另行检查。

CI 页面断言 callback 总提交次数为 1。前端源码在 production 同步中未变化，相关版本记录见 evidence.json。截图按仓库约定保留在 CI 产物中，源码不存放 PR 对比 PNG。
