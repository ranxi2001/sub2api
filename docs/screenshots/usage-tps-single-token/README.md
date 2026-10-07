# 使用记录：只有 1 个输出 Token 时不再显示 0.0 t/s

截图来自本地前端开发服务器、仅监听本机的 mock API 和无头 Chromium（CDP），管理员「使用记录」页，深色主题，1440 × 1000 视口、2 倍像素。页面使用虚构用户、分组和账号，不含线上账号、凭据或用户数据。

第一行仿照一条真实的中断记录：Claude Code 经 Anthropic API Key 账号流式请求 `claude-opus-5-5`，首字 973ms 后持续输出，21.14s 时上游返回 `Upstream request failed`（`stream usage incomplete: missing terminal event`），只记下 `message_start` 里的输出 1 个 Token。第二行是正常完成的请求，作对照。

修改前基线为 `5ca3cca21eeaf4ca8a694a7f2f8f0ecd9575c549`，从独立 checkout 运行；修改后为同一基线上的本次前端改动。两张图都是实际页面渲染，没有用临时 CSS 或改写 DOM 模拟。

| 修改前 | 修改后 |
| --- | --- |
| [usage-latency-before.png](usage-latency-before.png) | [usage-latency-after.png](usage-latency-after.png) |

## 验收

- 修改前，中断记录显示红色 `0.0 t/s`，延迟色条下段跟着变红，看起来像渠道很慢。
- 修改后，同一记录的平均 TPS 显示灰色 `-`，悬停说明「只记到 1 个输出 Token，算不出速度……」；色条下段沿用总耗时档（绿色）。首字、总耗时、Token 和费用不变。
- 正常记录仍显示 `27.9 t/s`（872 Token ÷ 31.26s），颜色和悬停说明不变。
