# 优先调度共享账号性能

入口：管理员 → 优先调度 `/admin/priority-scheduling`。

截图由 [CI browser job](https://github.com/ranxi2001/sub2api/actions/runs/42983409965) 使用实际页面组件与 CSS、虚构账号和 mock HTTP 接口生成。基线 `6fa7fb86eae7c732794450646dc13831dce87ff1`，候选 `80712d1833a077faee4da72fb0e0926f45c53cfb`；此后测试夹具修复及文档提交不改变截图涉及的运行组件。原始 SHA256 已校验，详见 `manifest.json`。图片保留在 [CI 附件](https://github.com/ranxi2001/sub2api/actions/runs/42983409965/artifacts/13839440932)，按 `CONTRIBUTING.md` 不提交 PR 对比 PNG。

- `before-default-gpt-group.png` 与 `after-default-gpt-group.png`：初次进入时的分组选择前后对比。
- `after-shared-performance.png`：账号性能四项指标与评分列表。
- `after-mobile.png`：移动端筛选及候选列表入口。

布局壳、导航、认证和后台接口为 fixture；不代表真实账号调度或生产验收。
