# OpenAI 流式首事件与内容计时

OpenAI 流式使用记录区分首事件、首语义、首推理和首文字。首个事件可能只是 `response.created`，不代表模型已经开始输出正文。已有 `first_token_ms`、TPS 和计费口径保留，历史记录不回算。

## 查看与计时范围

启用请求耗时详情后，点击使用记录的延迟单元格，在详情的最终出口内容时序中查看：

| 字段 | 含义 |
| --- | --- |
| `sse_event_first_ms` | 宿主写出并 flush 首个完整、非心跳 SSE data 事件 |
| `semantic_first_ms` | 现有共享判定器识别到首个语义进展 |
| `reasoning_first_ms` | 首个明确推理事件 |
| `visible_text_first_ms` | 首个可见正文文字 |

时间均相对应用入口，属于最终出口 attempt。未知显示 `—`，不能从全局事件或前一失败 attempt 补值。HTTP、可信插件和原生 WS 的采集来源分别标注；逻辑 WS 响应有独立生命周期，不使用连接握手或写请求的耗时替代内容时序。旧记录未包含最终出口标记时保留旧采集范围，缺失项继续未知。

flush 只能证明应用侧写出，不能证明远端客户端已经收到。首事件早而正文晚，应结合上游生成时间和代理缓冲逐段检查。插件 gRPC 响应头和原生 WS 握手不作为远端 TCP 首字节证据，相应 `response_byte_first_ms` 保持缺失。

原生 OAuth WS→SSE 加速沿用现有账号开关、连接池和安全 fallback：只有可证明未提交请求的失败才允许切换传输，不能把“尚未收到首事件”解释为“上游没有执行”。新增观测不改变选路或自动启用加速。

## 可选的可信插件元数据提前交付

默认关闭。YAML：

```yaml
gateway:
  openai_plugin_sse_early_events_enabled: false
```

环境变量为 `GATEWAY_OPENAI_PLUGIN_SSE_EARLY_EVENTS_ENABLED`。Compose 示例已显式映射，改为 `true` 后重建应用容器；二进制部署修改启动环境/YAML 后按既有方式重启服务。它是宿主启动配置，不是插件页面热配置。

开启后还需同时满足：

- 账号为 OpenAI OAuth，请求实际选中出站插件 `local.sub2api.openai-ws-sse-accelerator`。
- 插件兼容当前宿主、成功启用，安装签名状态为 `trusted`。
- 响应为 HTTP 200、`text/event-stream`，并声明 `X-Sub2api-Plugin-Preflight-Sse: 1`。
- 宿主 PluginManager 在成功调度边界赋予私有 context 来源标记；普通上游伪造同名响应头不能获得能力。

完整空行边界闭合的 `response.created/queued/in_progress/metadata` 可提前写出并 flush；未知事件、心跳、`[DONE]` 和不完整事件不会因这个开关获得新资格。元数据提交不解除语义首输出期限，也不伪造首语义或正文。

该插件响应提前提交后，后续事件也在完整边界闭合后交付，避免将半个上游事件与本地错误拼接。每个暂存事件上限为 8 MiB，超过后结束当前流，不自动重放；正常语义输出后同样适用。需要单事件大于此上限的插件应保持此开关关闭，不能把多个 data 行用作绕过限额的方式。

响应提前提交后，故障切换窗口会收窄。后续超时、EOF 或读取失败在当前 SSE 内结束，不跨账号重放或拼接另一条响应；客户端断连沿用既有用量收集。这是交付行为变化，启用前应在隔离候选实例验收两条宿主 SSE parser。

本功能不附带第三方插件包或发布者公钥，不放宽兼容版本与签名校验。研究时收到的 v0.2.2 包 manifest 为 `>=0.2.4 <0.3.0`，不能直接用于 2.x 宿主；修改 manifest 会破坏签名，需要发布者交付兼容范围正确的有效签名包。`allow_unsigned` 不会使签名状态变为 `trusted`。

## 验收与回滚

CI 使用受控时钟和 mock 验证 created 100ms、推理 300ms、正文 800ms 的独立计时，以及最终 attempt 隔离、分片/多行/半事件、EOF/读取错误/超时、伪造来源、断连和单次终态。该覆盖不等于真实插件或生产链路验收。实际启用后需另行测量直连、域名与真实下游的完整事件和正文到达时间；反代仍需允许流式交付。

将宿主开关改回 `false` 并按既有发布流程重启，可恢复插件原暂存策略，观测继续保留。本功能沿用 timing JSON，无数据库迁移；完整二进制回滚无需恢复历史 timing 数据。在途 SSE 按现有发布方式排空。

## 实现来源

起点为用户提供的 `SUB2API_SSE_PATCH_AB_MIGRATION_20261106.md`，附录 A/B 摘要分别为：

```text
A f1799eb45e3acbf4da30fb01be53ce72e96c8d3ecd3aa842f4fe3c95a57eb138
B 824c547bb8b0a3b6617f227bf087f6bc355e2bf7a5ab8907ff52ac2ebd71a1fa
```

在 production 基线 `3077b050730c322969e04568f384ae5008e7cebb` 上适配，补充原生 WS 逻辑 attempt、完整事件边界、最终出口投影和新功能提示。交付方提供的发布提交及生产测试结论不是本 PR 的独立运行证据。未复制插件二进制、重新签名或引入第二套连接池。
