# Qwen 万相视频

原生 Qwen 分组可通过 `POST /v1/videos` 提交 `wan3.0-video` 异步任务。管理员在分组启用媒体生成，在系统价卡中选择 video 模式，以 480p、720p、1080p 三档配置官方每秒基价，并在模型白名单中放行。Qwen API Key 账号使用 `credentials.qwen_video_protocol` 选择 `dashscope`（直连原生接口）或 `openai`（连接提供本接口的 Sub2API 上游）。对返回 OpenAI 风格状态但缺少计费用量的供应商，可以选择 `fixed_duration`，它仅接受固定输出时长且拒绝输入参考视频；查询成功后按提交时长结算。使用对应服务地域、支持服务端调用的凭据和该上游已有的模型映射。

请求示例：

```json
{"model":"wan3.0-video","prompt":"一只蓝色小球在白色桌面上滚动","seconds":2,"resolution":"480p","aspect_ratio":"16:9"}
```

支持 2–30 秒、三档分辨率、音频开关，以及通过 `media` 数组提供 HTTPS 首尾帧、参考图片、视频或音频。创建返回本站任务 ID。用原 API Key 调用 `GET /v1/videos/{id}`，完成状态包含成片 URL 和计费秒数。`GET /v1/videos/{id}/content` 在完成后返回临时 HTTPS 重定向。客户端访问成片 URL 时只向网关发送业务 Key。

完成查询沿用现有视频任务归属、去重和入账流程。费用以提交时的分辨率单价乘上游回传的输入与输出合计秒数，再应用客户视频倍率。小数秒保留在费用中，现有整数用量时长字段向上取整。轮询或下载触发结算；不轮询的任务沿用网关现有视频结算限制。

[千问官方模型资料](https://platform.qianwenai.com/docs/models/wan3-0-video) · [任务结果字段](https://platform.qianwenai.com/docs/api-reference/video-generation/wan30-video/query-result)
