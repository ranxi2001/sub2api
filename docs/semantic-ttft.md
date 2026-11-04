# Responses SSE / WS semantic TTFT

本修复将 OpenAI Responses SSE 的 semantic 首字判定统一到 WS 已使用的
`backend/internal/pkg/openai/response_semantic.go` 中的
`ResponseStartsSemanticOutput(payload, eventType)`，不复制另一套算法。
默认仍为 semantic；显式选择 visible 时保留原判定和行为，不能将默认描述为等待正文。

## 判定口径

- payload 必须是有效 JSON。
- `response.output_item.added/done` 的 `item.type=reasoning` 立即计入，即使 summary 为空。
- 共享 helper 识别的 reasoning/summary typed part 立即计入，即使 text 为空。
- 其它事件仅在 `ResponseContentKind` 返回非空类别时计入，包括非空 reasoning、正文、工具输入或媒体内容。
- unknown、untyped 空 item、created、heartbeat、metadata、metadata.delta 和 empty/null/object delta 不计入。
- completed/done 等 terminal 不单独建立 semantic 首字，即使 terminal snapshot 含正文；非空 refusal 内容按共享内容分类计入，error 不冒充 semantic token。

## 起点与观察边界

各路径原有 `startTime`、每轮起点、queue 边界、总耗时和 TPS 分母均不变。统一的是事件判定，不是所有传输的 timestamp 赋值边界。

| 实际路径 | semantic 首字的观察与赋值边界 |
| --- | --- |
| HTTP ingress → HTTP Responses SSE upstream，native SSE | 保留原边界：guarded 路径在完整 SSE event 结束、执行原有 flush 逻辑后赋值；非 guarded 路径在处理解析出的 data 行时赋值。未启用首输出 timeout 时仍保留原有事件 staging，不因此移动计时。 |
| HTTP ingress → HTTP Responses SSE upstream，passthrough SSE | 保留在处理解析出的 SSE data 行时赋值，后续完整事件刷新独立进行。 |
| WS upstream 的 pooled / passthrough / WS-to-SSE 适配 | 在各自读取 WS frame 后判定，保留各路径原有每轮起点和赋值位置；WS-to-SSE 的客户端实际使用 HTTP SSE。 |
| WS ingress → HTTP bridge → HTTP Responses SSE upstream | 判定来自解析后的 SSE payload；不能仅凭入口是 WS 就称上游为 WS。 |

这些是网关可观察的首个语义事件，不是浏览器接收、渲染首字的 TTFT，也不证明模型计算耗时。完整事件 staging、flush、网络传输和浏览器处理仍可能产生不同的观察时刻。首个符合条件的时刻只记录一次，后续正文不覆盖它；没有符合条件的事件时保持 unavailable，而非补零。

## 交付、重试与历史数据

semantic 指标与原 stream commit/flush/replay eligibility 解耦。原客户端输出分类、首输出 timeout、retry/failover 和错误交付条件继续使用原有交付状态；`forceOutput` 可以要求交付 failed/error，但不能制造 semantic token。空 typed reasoning 被计时，不代表提前刷新暂存内容，也不授权重放已接受的上游工作。usage 解析、计费和 reasoning 内容交付规则不因本修复改变。

`requesttiming.Output` 的 semantic 判定和 Responses priority 首语义采样同步使用共享口径；Chat Completions priority 保留旧分类。其它 Anthropic、Gemini、Chat Completions 协议不在此次口径修改范围。

仅后续记录采用新口径，不重算历史 `first_token_ms` 或聚合；混合历史不可视为统一口径的延迟序列。没有 schema、生产设置或 UI 变更。本轮为本地候选提交，未 push、创建 PR、release 或部署，不声称生产延迟改善。
